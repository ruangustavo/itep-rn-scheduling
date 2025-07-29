package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"os"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Location         string `yaml:"location"`
	AppointmentCount int    `yaml:"appointment_count"`
	ScheduleTime     string `yaml:"schedule_time"`
	BaseURL          string `yaml:"base_url"`
}

type Order struct {
	ID         int      `json:"id"`
	DataInicio string   `json:"data_inicio"`
	DataFim    string   `json:"data_termino"`
	VagasCount string   `json:"vagasCount"`
	Location   Location `json:"localizacao"`
}

type Location struct {
	ID   int    `json:"id"`
	Nome string `json:"nome"`
}

type BookingRequest struct {
	IDOrdem int    `json:"id_ordem"`
	Data    string `json:"data"`
	Hora    string `json:"hora"`
}

type BookingResponse struct {
	CodigoVaga string `json:"codigo_vaga"`
	Agendou    int    `json:"agendou"`
}

type TimeSlot struct {
	OrderID int
	Date    string
	Time    string
}

type Scheduler struct {
	config     Config
	httpClient *http.Client
}

func main() {
	scheduler, err := NewScheduler("config.yaml")
	if err != nil {
		log.Fatalf("Failed to initialize scheduler: %v", err)
	}

	scheduler.waitUntilScheduledTime()

	if err := scheduler.runScheduling(); err != nil {
		log.Fatalf("Scheduling failed: %v", err)
	}
}

func NewScheduler(configPath string) (*Scheduler, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return &Scheduler{
		config: config,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func loadConfig(path string) (Config, error) {
	var config Config

	config.Location = "SÃO MIGUEL"
	config.AppointmentCount = 5
	config.ScheduleTime = "08:00"
	config.BaseURL = "https://agendamento.itep.rn.gov.br/api"

	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &config); err != nil {
			return config, fmt.Errorf("failed to parse config file: %w", err)
		}
		log.Printf("Loaded configuration from %s", path)
	} else {
		log.Printf("Config file not found, using defaults")
		if err := createDefaultConfig(path, config); err != nil {
			log.Printf("Warning: could not create default config file: %v", err)
		}
	}

	return config, nil
}

func createDefaultConfig(path string, config Config) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (s *Scheduler) waitUntilScheduledTime() {
	now := time.Now()
	targetTime, err := time.Parse("15:04", s.config.ScheduleTime)
	if err != nil {
		log.Printf("Invalid schedule time format, running immediately")
		return
	}

	target := time.Date(now.Year(), now.Month(), now.Day(),
		targetTime.Hour(), targetTime.Minute(), 0, 0, now.Location())

	if target.Before(now) {
		target = target.Add(24 * time.Hour)
	}

	duration := target.Sub(now)
	log.Printf("Waiting until %s to start scheduling (in %v)",
		target.Format("2006-01-02 15:04:05"), duration)

	time.Sleep(duration)
}

func (s *Scheduler) runScheduling() error {
	log.Printf("Starting scheduling process for location: %s", s.config.Location)
	log.Printf("Target appointments: %d", s.config.AppointmentCount)

	orders, err := s.getAvailableOrders()
	if err != nil {
		return fmt.Errorf("failed to get orders: %w", err)
	}

	var targetOrders []Order
	for _, order := range orders {
		if order.Location.Nome == s.config.Location {
			targetOrders = append(targetOrders, order)
		}
	}

	if len(targetOrders) == 0 {
		log.Printf("ERROR: No orders found for location '%s' on %s", s.config.Location, s.getNextWorkingDay().Format("2006-01-02"))
		return fmt.Errorf("no orders available for specified location on target date")
	}

	log.Printf("Found %d orders for location '%s'", len(targetOrders), s.config.Location)

	allSlots, err := s.collectAllTimeSlots(targetOrders)
	if err != nil {
		return fmt.Errorf("failed to collect time slots: %w", err)
	}

	if len(allSlots) == 0 {
		log.Printf("ERROR: No available time slots found for location '%s' on %s", s.config.Location, s.getNextWorkingDay().Format("2006-01-02"))
		return fmt.Errorf("no time slots available")
	}

	log.Printf("Found %d total time slots", len(allSlots))

	slotsToBook := s.selectUniqueSlots(allSlots)

	if len(slotsToBook) == 0 {
		log.Printf("ERROR: No unique time slots available for booking")
		return fmt.Errorf("no unique slots available")
	}

	actualBookings := len(slotsToBook)
	if actualBookings < s.config.AppointmentCount {
		log.Printf("WARNING: Only %d slots available, booking fewer than requested %d",
			actualBookings, s.config.AppointmentCount)
	}

	bookingResults := s.bookAppointmentsConcurrently(slotsToBook)

	successCount := 0
	var successfulBookings []BookingResponse

	for i, result := range bookingResults {
		if result.err != nil {
			log.Printf("Booking %d failed: %v", i+1, result.err)
		} else {
			successCount++
			successfulBookings = append(successfulBookings, result.response)
			log.Printf("Booking %d successful: Code %s", i+1, result.response.CodigoVaga)
		}
	}

	if successCount == 0 {
		log.Printf("ERROR: All booking attempts failed")
		return fmt.Errorf("all booking attempts failed")
	}

	log.Printf("\n=== SCHEDULING COMPLETED SUCCESSFULLY ===")
	log.Printf("Successfully booked %d out of %d attempted appointments", successCount, len(slotsToBook))
	log.Printf("\nBooking links:")

	for _, booking := range successfulBookings {
		link := fmt.Sprintf("https://agendamento.itep.rn.gov.br/public/agendamento/%s/agendar",
			booking.CodigoVaga)
		fmt.Println(link)
	}

	return nil
}

func (s *Scheduler) getAvailableOrders() ([]Order, error) {
	targetDate := s.getNextWorkingDay()

	url := fmt.Sprintf("%s/ordens/public?data_inicial=%s&data_final=%s",
		s.config.BaseURL,
		targetDate.Format("2006-01-02"),
		targetDate.Format("2006-01-02"))

	log.Printf("Fetching orders for %s only", targetDate.Format("2006-01-02"))

	resp, err := s.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	log.Printf("Orders API Response: %s", string(body))

	var orders []Order
	if err := json.Unmarshal(body, &orders); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return orders, nil
}

func (s *Scheduler) getNextWorkingDay() time.Time {
	tomorrow := time.Now().AddDate(0, 0, 1)

	for tomorrow.Weekday() == time.Saturday || tomorrow.Weekday() == time.Sunday {
		tomorrow = tomorrow.AddDate(0, 0, 1)
	}

	return tomorrow
}

func (s *Scheduler) collectAllTimeSlots(orders []Order) ([]TimeSlot, error) {
	var allSlots []TimeSlot
	targetDate := s.getNextWorkingDay()
	targetDateStr := targetDate.Format("2006-01-02")

	for _, order := range orders {
		dates, err := s.getAvailableDates(order.ID)
		if err != nil {
			log.Printf("Failed to get dates for order %d: %v", order.ID, err)
			continue
		}

		dateFound := false
		for _, dateStr := range dates {
			if dateStr == targetDateStr {
				dateFound = true
				break
			}
		}

		if !dateFound {
			log.Printf("Target date %s not available for order %d", targetDateStr, order.ID)
			continue
		}

		times, err := s.getAvailableTimes(order.ID, targetDateStr)
		if err != nil {
			log.Printf("Failed to get times for order %d, date %s: %v", order.ID, targetDateStr, err)
			continue
		}

		for _, timeStr := range times {
			allSlots = append(allSlots, TimeSlot{
				OrderID: order.ID,
				Date:    targetDateStr,
				Time:    timeStr,
			})
		}
	}

	sort.Slice(allSlots, func(i, j int) bool {
		return allSlots[i].Time < allSlots[j].Time
	})

	return allSlots, nil
}

func (s *Scheduler) getAvailableDates(orderID int) ([]string, error) {
	url := fmt.Sprintf("%s/ordens/public/datas?ordem=%d", s.config.BaseURL, orderID)

	resp, err := s.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	log.Printf("Dates API Response for order %d: %s", orderID, string(body))

	var dates []string
	if err := json.Unmarshal(body, &dates); err != nil {
		return nil, err
	}

	return dates, nil
}

func (s *Scheduler) getAvailableTimes(orderID int, dateStr string) ([]string, error) {
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return nil, err
	}

	formattedDate := date.UTC().Format("Mon, 02 Jan 2006") + " 03:00:00 GMT"

	encodedDate := url.QueryEscape(formattedDate)

	apiURL := fmt.Sprintf("%s/vagas/horas?ordem=%d&data=%s",
		s.config.BaseURL, orderID, encodedDate)

	log.Printf("Requesting times from URL: %s", apiURL)

	resp, err := s.httpClient.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	log.Printf("Times API Response for order %d, date %s: %s", orderID, dateStr, string(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body))
	}

	var times []string
	if err := json.Unmarshal(body, &times); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return times, nil
}

func (s *Scheduler) selectUniqueSlots(allSlots []TimeSlot) []TimeSlot {
	usedTimes := make(map[string]bool)
	var selectedSlots []TimeSlot

	for _, slot := range allSlots {
		if !usedTimes[slot.Time] && len(selectedSlots) < s.config.AppointmentCount {
			usedTimes[slot.Time] = true
			selectedSlots = append(selectedSlots, slot)
		}
	}

	log.Printf("Selected %d unique time slots for booking on %s", len(selectedSlots), s.getNextWorkingDay().Format("2006-01-02"))
	for i, slot := range selectedSlots {
		log.Printf("Slot %d: Order %d, Date %s, Time %s",
			i+1, slot.OrderID, slot.Date, slot.Time)
	}

	return selectedSlots
}

type BookingResult struct {
	response BookingResponse
	err      error
}

func (s *Scheduler) bookAppointmentsConcurrently(slots []TimeSlot) []BookingResult {
	results := make([]BookingResult, len(slots))
	var wg sync.WaitGroup

	semaphore := make(chan struct{}, 5)

	for i, slot := range slots {
		wg.Add(1)
		go func(index int, timeSlot TimeSlot) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			log.Printf("Attempting booking %d: Order %d, Date %s, Time %s",
				index+1, timeSlot.OrderID, timeSlot.Date, timeSlot.Time)

			response, err := s.bookAppointment(timeSlot)
			results[index] = BookingResult{
				response: response,
				err:      err,
			}
		}(i, slot)
	}

	wg.Wait()
	return results
}

func (s *Scheduler) bookAppointment(slot TimeSlot) (BookingResponse, error) {
	date, err := time.Parse("2006-01-02", slot.Date)
	if err != nil {
		return BookingResponse{}, fmt.Errorf("invalid date format: %w", err)
	}

	utcDate := time.Date(date.Year(), date.Month(), date.Day(), 3, 0, 0, 0, time.UTC)

	timeParts := slot.Time[:5]

	request := BookingRequest{
		IDOrdem: slot.OrderID,
		Data:    utcDate.Format("2006-01-02T15:04:05.000Z"),
		Hora:    timeParts,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return BookingResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Printf("Booking request body: %s", string(requestBody))

	url := fmt.Sprintf("%s/vagas", s.config.BaseURL)
	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		return BookingResponse{}, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return BookingResponse{}, fmt.Errorf("failed to read response: %w", err)
	}

	log.Printf("Booking response: %s", string(responseBody))

	if resp.StatusCode != http.StatusOK {
		return BookingResponse{}, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(responseBody))
	}

	var bookingResponse BookingResponse
	if err := json.Unmarshal(responseBody, &bookingResponse); err != nil {
		return BookingResponse{}, fmt.Errorf("failed to parse response: %w", err)
	}

	if bookingResponse.Agendou != 1 {
		return BookingResponse{}, fmt.Errorf("booking was not successful: agendou=%d", bookingResponse.Agendou)
	}

	return bookingResponse, nil
}

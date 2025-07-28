# Agendador ITEP RN

Este programa agenda consultas no ITEP RN automaticamente. Ele roda no horário configurado e tenta agendar o número de consultas especificado.

## Como funciona

O programa:

1. Espera até o horário configurado
2. Busca ordens disponíveis para o local escolhido
3. Coleta horários disponíveis para o próximo dia útil
4. Seleciona horários únicos
5. Tenta agendar as consultas simultaneamente
6. Mostra os links das consultas agendadas

## Configuração

Edite o arquivo `config.yaml`:

```yaml
location: "NATAL" # Local onde agendar
appointment_count: 2 # Número de consultas para agendar
schedule_time: "18:00" # Horário para começar (formato HH:MM)
base_url: "https://agendamento.itep.rn.gov.br/api"
```

## Instalação

1. Instale o Go (versão 1.19 ou superior)
2. Clone este repositório
3. Execute: `go mod tidy`
4. Configure o arquivo `config.yaml`

## Uso

Execute o programa:

```bash
go run main.go
```

O programa vai:

- Esperar até o horário configurado
- Tentar agendar as consultas
- Mostrar os resultados no terminal

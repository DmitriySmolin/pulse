# Pulse: Go-сервисы

Один Go-модуль (`module pulse`), пять бинарей. Полное поведение, переменные окружения, метрики и формат сообщений описаны в [../CONTRACT.md](../CONTRACT.md). Здесь — только то, что нужно, чтобы собрать, запустить и прочитать код.

| Бинарь | Порт | Что делает | Зависимости для `/readyz` |
|---|---|---|---|
| `cmd/scheduler` | 8081 | раз в `SCHEDULER_TICK` берёт просроченные мониторы (`FOR UPDATE SKIP LOCKED`) и публикует `check.requested` | PostgreSQL, RabbitMQ |
| `cmd/checker` | 8082 | выполняет HTTP-проверки, пишет `checks`/инциденты, публикует `status.changed`; при `KAFKA_BROKERS` — outbox и relay в Kafka | PostgreSQL, RabbitMQ (Kafka — нет: outbox буферизует) |
| `cmd/notifier` | 8083 | алерты о смене статуса: Telegram → webhook → лог, дедуп в Valkey | RabbitMQ, Valkey |
| `cmd/aggregator` | 8084 | читает `pulse.check-results` из Kafka, идемпотентно считает `uptime_hourly` | PostgreSQL, Kafka |
| `cmd/target` | 8090 | управляемая «жертва» для проверок и chaos-лаб | — |

## Устройство кода

```
internal/
├── app/          общий старт: конфиг, логи, HTTP-сервер (/healthz /readyz /metrics), трейсинг, graceful shutdown
├── config/       переменные окружения и *_FILE-секреты
├── logging/      JSON-логи (slog), trace_id/span_id из контекста
├── httpserver/   служебный HTTP-сервер и fault-ручки (PULSE_FAULTS_ENABLED=1)
├── telemetry/    OpenTelemetry (включается OTEL_EXPORTER_OTLP_ENDPOINT)
├── postgres/     пул pgx с ретраями на старте
├── amqpx/        RabbitMQ: соединение с переподключением, топология, publisher confirms, consumer pool
├── kafkax/       Kafka (franz-go): конфиг, TLS/SASL, заголовки traceparent, создание топика
├── outbox/       relay: outbox → Kafka (SKIP LOCKED, удаление после ack брокера)
├── message/      форматы сообщений check.requested, status.changed, check.result
├── scheduler/ checker/ notifier/ aggregator/ target/   логика сервисов
└── retry/        backoff
```

С чего читать при отладке:
- **Проверка не выполняется** — `scheduler/scheduler.go` (выборка и публикация), `amqpx/consumer.go` (ack/nack, prefetch), `checker/service.go`.
- **Статус или инцидент неверный** — `checker/decide.go` (чистая функция, покрыта тестами) и `checker/store.go` (транзакция).
- **uptime не считается** — `outbox/relay.go`, затем `aggregator/aggregator.go` и `aggregator/offsets.go` (коммит offset после коммита БД, ребалансировка).

## Сборка и тесты

Локальный Go в WSL старый, поэтому всё запускается в контейнере:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.27 go test ./...
```

```bash
docker run --rm -v "$PWD":/src -w /src -e CGO_ENABLED=0 golang:1.27 go build -trimpath -o /src/bin/ ./cmd/...
```

Бинари статические (`CGO_ENABLED=0`) и запускаются хоть в `scratch`. Как упаковать их в образы — задача модуля 3, она остаётся студенту.

## Минимальный запуск вручную

Нужны PostgreSQL 18 со схемой (`php bin/console migrate` в `api/` или SQL-файлы из `api/migrations/`), RabbitMQ 4 и Valkey. Kafka не обязательна.

```bash
export DATABASE_URL='postgres://pulse:pass@localhost:5432/pulse?sslmode=disable'
export AMQP_URL='amqp://pulse:pass@localhost:5672/'
export VALKEY_URL='redis://localhost:6379/0'
./bin/target & ./bin/scheduler & ./bin/checker & ./bin/notifier &
```

Признак, что всё работает: `curl localhost:8082/readyz` отвечает `{"status":"ready",...}`, а в таблице `checks` появляются строки. Сквозная проверка стенда — `../scripts/smoke.sh`.

-- 0002_kafka: transactional outbox and hourly uptime aggregates (see CONTRACT.md, "Kafka").
-- The tables exist even when Kafka is disabled: they simply stay empty.

-- checker writes check.result here in the same transaction as `checks`;
-- the relay publishes rows to Kafka and deletes them.
CREATE TABLE outbox (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  topic      text        NOT NULL,
  msg_key    text        NOT NULL,
  payload    jsonb       NOT NULL,
  headers    jsonb       NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

-- aggregator upserts one row per monitor and hour; api serves it from /api/v1/monitors/{id}/uptime.
CREATE TABLE uptime_hourly (
  monitor_id     bigint      NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  hour           timestamptz NOT NULL,          -- date_trunc('hour', checked_at) в UTC
  checks         int         NOT NULL DEFAULT 0,
  ok             int         NOT NULL DEFAULT 0,
  latency_sum_ms bigint      NOT NULL DEFAULT 0,
  latency_max_ms int         NOT NULL DEFAULT 0,
  PRIMARY KEY (monitor_id, hour)
);

-- идемпотентность потребителя: Kafka гарантирует at-least-once, дубли неизбежны
CREATE TABLE aggregated_checks (
  check_id      bigint      PRIMARY KEY,
  aggregated_at timestamptz NOT NULL DEFAULT now()
);

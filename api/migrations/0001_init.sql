-- 0001_init: initial Pulse schema (see CONTRACT.md, "Схема БД").
-- Applied by `php bin/console migrate` inside a single transaction.

CREATE TABLE monitors (
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name                 text        NOT NULL,
  url                  text        NOT NULL,
  method               text        NOT NULL DEFAULT 'GET' CHECK (method IN ('GET','HEAD')),
  interval_seconds     int         NOT NULL DEFAULT 60   CHECK (interval_seconds BETWEEN 10 AND 3600),
  timeout_ms           int         NOT NULL DEFAULT 5000 CHECK (timeout_ms BETWEEN 100 AND 30000),
  expected_status      int         NOT NULL DEFAULT 200  CHECK (expected_status BETWEEN 100 AND 599),
  status               text        NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown','up','down')),
  consecutive_failures int         NOT NULL DEFAULT 0,
  paused               boolean     NOT NULL DEFAULT false,
  last_checked_at      timestamptz,
  next_check_at        timestamptz NOT NULL DEFAULT now(),
  created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX monitors_due_idx ON monitors (next_check_at) WHERE NOT paused;

CREATE TABLE checks (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  monitor_id   bigint      NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  checked_at   timestamptz NOT NULL DEFAULT now(),
  ok           boolean     NOT NULL,
  status_code  int,
  latency_ms   int,
  error        text,
  snapshot_key text
);
CREATE INDEX checks_monitor_time_idx ON checks (monitor_id, checked_at DESC);

CREATE TABLE incidents (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  monitor_id  bigint      NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  started_at  timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  cause       text
);
CREATE UNIQUE INDEX incidents_one_open_idx ON incidents (monitor_id) WHERE resolved_at IS NULL;

CREATE TABLE schema_migrations (
  version    text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

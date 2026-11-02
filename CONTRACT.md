# Pulse: контракт

Единый источник правды для кода приложения (пишет ментор), лаб и `check.sh`. Меняется только осознанно, и все зависимые места правятся вместе с ним.

Pulse — uptime-монитор со статус-страницей. Пользователь добавляет URL, сервис проверяет их по расписанию, хранит историю и инциденты, шлёт алерты и показывает статус-страницу.

## Репозиторий

`pulse/` — **отдельный git-репозиторий** (в курсовом репо он в `.gitignore`). Публикуется на GitHub как портфолио, в нём живут CI и GitOps.

```
pulse/
├── api/                 # PHP 8.5: REST API, миграции, console-команды                ← ментор
├── services/            # Go 1.27: scheduler, checker, notifier, aggregator, target   ← ментор
├── status-page/         # статика: index.html, app.js, style.css                      ← ментор
├── scripts/smoke.sh     # e2e-смоук по работающему стенду                             ← ментор
├── CONTRACT.md
│
│   ниже — зона студента, ментор не пишет
├── mise.toml, mise.lock               # версии инструментов, модуль 0
├── api/Dockerfile, services/Dockerfile, web/ (nginx: Dockerfile + конфиг)   # модуль 3
├── compose.yaml                       # модуль 3
├── .github/workflows/, .gitlab-ci.yml # модуль 4
├── loadtest/                          # k6-сценарии, модуль 11
├── tools/
│   ├── backup/                        # backup.sh + test/*.bats, модуль 1
│   ├── certcheck/                     # uv-проект на Python, модуль 1
│   └── slo-report/                    # Python-автоматизация SRE, модуль 11
├── docs/
│   ├── troubleshooting/NN-<slug>.md   # разборы сломанных стендов, модули 1, 5
│   ├── db/slow-query.md, db/retry.md, dr/report.md # модуль 7
│   ├── gameday/                       # results.tsv и журналы game day, модуль 12
│   ├── slo.md, runbooks/<AlertName>.md # модуль 8 (runbook_url алертов ведёт сюда)
│   ├── security/threat-model.md       # модуль 10
│   ├── perf.md, resilience.md, chaos/, postmortems/YYYY-MM-DD-<slug>.md  # модуль 11
│   └── adr/NNNN-<slug>.md             # решения по архитектуре, по ходу курса
└── deploy/
    ├── vps/             # pulse-target.service, nginx/pulse-target.conf (модуль 1), deploy.sh (модуль 4)
    ├── ansible/         # модуль 2: site, patroni, pgbackrest, restore, backup, web, runner; zabbix (модуль 8)
    ├── nexus/           # setup.sh/README: репозитории Nexus как код, модуль 4
    ├── flux/            # clusters/flux, apps/target/{base,overlays}, модуль 6
    ├── zabbix/          # templates/*.yaml (экспорт), README, модуль 8
    ├── tenants/         # шаблон тенанта + tenant-a, tenant-b, модуль 10
    ├── kind/            # cluster.yaml, модуль 5
    ├── k8s/             # Kustomize: base/, overlays/kind/, модуль 5
    ├── kubeadm/         # конфиги и runbook on-prem кластера, модуль 5
    ├── helm/pulse/      # чарт, модуль 6
    ├── argocd/          # Application/ApplicationSet/AppProject, модуль 6
    ├── platform/        # gateway/ (5), операторы и данные (7), мониторинг (8), openbao/ (+ jwt/) external-secrets/ pki/ policies/ runtime/ (10), keda/ chaos/ (11)
    └── tofu/            # OpenTofu → Yandex Cloud, модуль 9
```

## Компоненты

| Компонент | Язык | Порт по умолчанию | Роль |
|---|---|---|---|
| `api` | PHP 8.5-FPM | 9000 (FastCGI) | REST API, миграции, console-команды |
| `web` | nginx | 8080 | статика status-page + прокси `/api`, `/healthz`, `/readyz`, `/metrics` в FPM |
| `scheduler` | Go | 8081 | раз в `SCHEDULER_TICK` выбирает просроченные мониторы и публикует задания |
| `checker` | Go | 8082 | выполняет HTTP-проверки, пишет результаты, открывает/закрывает инциденты |
| `notifier` | Go | 8083 | шлёт алерты о смене статуса (Telegram / webhook / лог) с дедупликацией |
| `aggregator` | Go | 8084 | читает результаты проверок из Kafka и считает почасовой uptime |
| `target` | Go | 8090 | управляемая «жертва» для проверок и chaos-лаб |

Данные: PostgreSQL 18, Valkey 9, RabbitMQ 4, Kafka 4 (KRaft, без ZooKeeper), S3-совместимое хранилище SeaweedFS. MinIO не используем: community-редакция архивирована в 2026 году.

Почему и RabbitMQ, и Kafka. RabbitMQ — **очередь заданий**: каждое задание на проверку нужно выполнить ровно одному воркеру, с ack, ретраями и DLX. Kafka — **журнал событий**: результаты проверок хранятся с ретеншеном, их можно перечитать с любого offset, и несколько независимых потребителей читают один поток (сегодня `aggregator`, завтра аналитика). Это готовый ответ на вопрос «Kafka или RabbitMQ».

Kafka опциональна: без `KAFKA_BROKERS` checker не пишет в outbox, а `aggregator` не нужен. Так стек до модуля 7 работает и без неё.

Поток данных:

```
status-page ─► web(nginx) ─► api(FPM) ─► PostgreSQL
                                   └──► Valkey (кэш /api/v1/status, счётчики метрик)
scheduler ─► PostgreSQL (SELECT ... FOR UPDATE SKIP LOCKED) ─► RabbitMQ pulse.checks
checker   ◄─ pulse.checks.requested ─► HTTP-проверка ─► PostgreSQL ─► S3 (снапшот ответа при сбое)
                                                     └► RabbitMQ pulse.events (status.changed)
notifier  ◄─ pulse.notifications ─► Valkey (дедуп) ─► Telegram / webhook / лог
checker   ─► outbox (в той же транзакции, что и checks) ─► relay ─► Kafka pulse.check-results
aggregator ◄─ Kafka pulse.check-results (group pulse-aggregator) ─► PostgreSQL uptime_hourly
```

`scheduler` можно масштабировать горизонтально: благодаря `SKIP LOCKED` две реплики не берут один монитор.

## Общие правила для всех сервисов

- Конфигурация — только через переменные окружения. Любую секретную переменную можно передать файлом через суффикс `_FILE`, например `DATABASE_URL_FILE=/run/secrets/db_url`. Если заданы обе формы, это ошибка старта.
- Логи — JSON в stdout, одна строка на событие. Поля: `time`, `level`, `msg`, `service`, плюс контекстные (`monitor_id`, `trace_id`, …). `LOG_LEVEL` = `debug|info|warn|error`, по умолчанию `info`.
- `GET /healthz` — liveness: процесс жив, зависимости не трогает. Всегда `200 {"status":"ok"}`.
- `GET /readyz` — readiness: проверяет нужные компоненту зависимости. `200 {"status":"ready","checks":{...}}` или `503` с теми же полями.
- `GET /metrics` — формат Prometheus.
- У Go-бинарей есть подкоманда `<binary> healthcheck [readyz]`: делает GET на `/healthz` (или `/readyz`) уже запущенного экземпляра по `127.0.0.1:<порт из HTTP_ADDR>` и выходит с кодом 0 или 1. Нужна для `HEALTHCHECK` в distroless/scratch-образах, где нет curl.
- SIGTERM: `/readyz` сразу начинает отдавать 503, сервис перестаёт брать новую работу, дожидается текущей и выходит за `SHUTDOWN_TIMEOUT` (по умолчанию `25s`) с кодом 0.
- Без своих зависимостей сервис не падает в цикле: на старте до 30 секунд ретраит с backoff и только потом выходит с кодом 1 и понятным сообщением.
- Fault injection работает только при `PULSE_FAULTS_ENABLED=1`, иначе эти ручки возвращают 404.

## Переменные окружения

| Переменная | Кто читает | По умолчанию | Пример |
|---|---|---|---|
| `DATABASE_URL` | api, scheduler, checker | — (обязательна) | `postgres://pulse:pass@postgres:5432/pulse?sslmode=disable` |
| `VALKEY_URL` | api, notifier | — (обязательна) | `redis://valkey:6379/0` |
| `AMQP_URL` | scheduler, checker, notifier | — (обязательна) | `amqp://pulse:pass@rabbitmq:5672/` |
| `AMQP_DECLARE_TOPOLOGY` | scheduler, checker, notifier | `true` | `false`, если топологию создаёт оператор |
| `AMQP_RETRY_MODE` | scheduler, checker, notifier | `requeue` | `delayed` — ретрай через очередь с задержкой (см. «RabbitMQ») |
| `AMQP_RETRY_DELAY` | те же | `10s` | TTL очереди `pulse.checks.retry` |
| `AMQP_RETRY_MAX` | checker | `5` | сколько неудачных попыток до парковки в `pulse.dead` |
| `S3_ENDPOINT` | checker | пусто = снапшоты выключены | `http://s3:8333` |
| `S3_REGION` | checker | `us-east-1` | |
| `S3_BUCKET` | checker | `pulse-snapshots` | |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | checker | — | |
| `S3_FORCE_PATH_STYLE` | checker | `true` | |
| `HTTP_ADDR` | Go-сервисы | `:8081` / `:8082` / `:8083` / `:8084` / `:8090` | `:8080` |
| `SHUTDOWN_TIMEOUT` | Go-сервисы | `25s` | |
| `LOG_LEVEL` | все | `info` | |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Go-сервисы | пусто = трейсинг выключен | `http://otel-collector:4318` |
| `OTEL_SERVICE_NAME` | Go-сервисы | имя сервиса | |
| `PULSE_API_TOKEN` | api | — (обязательна) | токен для записи |
| `PULSE_STATUS_CACHE_TTL` | api | `10` (сек) | |
| `SCHEDULER_TICK` | scheduler | `5s` | |
| `SCHEDULER_BATCH` | scheduler | `100` | |
| `CHECKER_WORKERS` | checker | `8` | |
| `CHECKER_PREFETCH` | checker | `16` | |
| `CHECKER_FAILURE_THRESHOLD` | checker | `2` | сколько провалов подряд нужно для `down` |
| `CHECKER_USER_AGENT` | checker | `PulseChecker/1.0` | |
| `NOTIFIER_DEDUP_WINDOW` | notifier | `10m` | |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | notifier | пусто | |
| `TELEGRAM_API_URL` | notifier | `https://api.telegram.org` | |
| `WEBHOOK_URL` | notifier | пусто | если Telegram не задан |
| `KAFKA_BROKERS` | checker, aggregator | пусто = Kafka выключена | `kafka:9092` (через запятую) |
| `KAFKA_TOPIC_CHECK_RESULTS` | checker, aggregator | `pulse.check-results` | |
| `KAFKA_CLIENT_ID` | checker, aggregator | имя сервиса | |
| `KAFKA_GROUP` | aggregator | `pulse-aggregator` | |
| `KAFKA_TLS` | checker, aggregator | `false` | `true` — TLS к брокерам (CA из `KAFKA_CA_FILE`) |
| `KAFKA_SASL_MECHANISM` | checker, aggregator | пусто | `SCRAM-SHA-512` (так выдаёт Strimzi) |
| `KAFKA_USERNAME`, `KAFKA_PASSWORD` | checker, aggregator | пусто | пароль поддерживает `_FILE` |
| `KAFKA_AUTO_CREATE_TOPIC` | checker | `true` | `false`, если топик создаёт оператор (KafkaTopic) |
| `OUTBOX_POLL_INTERVAL` | checker | `1s` | |
| `OUTBOX_BATCH` | checker | `500` | |
| `PULSE_FAULTS_ENABLED` | все | `0` | `1` — включить chaos-ручки |
| `PULSE_FAULT_LATENCY_MS`, `PULSE_FAULT_ERROR_RATE` | api | `0` | задержка / доля 500 на `/api/*` |

Приоритет каналов notifier: Telegram, затем webhook, затем только лог.

## API (`api` за `web`)

Чтение публичное. Запись требует `Authorization: Bearer $PULSE_API_TOKEN`, иначе `401`. Ошибки валидации — `422 {"error":"validation","fields":{...}}`.

| Метод и путь | Ответ |
|---|---|
| `GET /healthz` | `200 {"status":"ok"}` |
| `GET /readyz` | проверяет PostgreSQL и Valkey |
| `GET /metrics` | см. «Метрики» |
| `GET /api/v1/monitors` | `200 {"items":[Monitor...]}` |
| `POST /api/v1/monitors` | тело `{name, url, interval_seconds?, timeout_ms?, expected_status?, method?}` → `201 Monitor` |
| `GET /api/v1/monitors/{id}` | `200 Monitor` / `404` |
| `PATCH /api/v1/monitors/{id}` | частичное обновление, в том числе `paused` → `200 Monitor` |
| `DELETE /api/v1/monitors/{id}` | `204` |
| `GET /api/v1/monitors/{id}/checks?limit=50` | `200 {"items":[Check...]}`, `limit` ≤ 500 |
| `GET /api/v1/incidents?open=1` | `200 {"items":[Incident...]}` |
| `GET /api/v1/monitors/{id}/uptime?days=30` | `200 {"items":[{"hour","checks","ok","uptime","latency_avg_ms","latency_max_ms"}]}` из `uptime_hourly`, `days` ≤ 90; пустой список, если Kafka не включена |
| `GET /api/v1/status` | сводка для статус-страницы, кэш в Valkey на `PULSE_STATUS_CACHE_TTL` секунд |
| `GET /debug/slow-query?seconds=N` | только при включённых fault-ручках: `pg_sleep(N)`, N ≤ 60 |

Монитор: `{id, name, url, method, interval_seconds, timeout_ms, expected_status, status, paused, consecutive_failures, last_checked_at, next_check_at, created_at}`.

Ответ `/api/v1/status`: `{"generated_at": "...", "overall": "up|degraded|down|unknown", "monitors": [{"id","name","status","uptime_24h","last_checked_at"}], "open_incidents": [...]}`.

Валидация: `url` — http(s), длина ≤ 2048; `interval_seconds` — 10..3600 (по умолчанию 60); `timeout_ms` — 100..30000 (по умолчанию 5000); `expected_status` — 100..599 (по умолчанию 200); `method` — `GET|HEAD` (по умолчанию `GET`).

## Console-команды api

```
php bin/console migrate            # применить api/migrations/*.sql; идемпотентно, под advisory lock (параллельный запуск безопасен)
php bin/console migrate:status
php bin/console seed               # демо-мониторы на target
php bin/console prune --days=30    # удалить checks старше N дней (для CronJob)
php bin/console wait-db --timeout=60
```

Код возврата 0 — успех, иначе ≠ 0 с сообщением в stderr.

## Схема БД (PostgreSQL 18)

```sql
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
```

Миграции лежат в `api/migrations/NNNN_name.sql`, каждая выполняется в транзакции. Схема выше — это `0001_init.sql`.

`0002_kafka.sql` — outbox и агрегаты:

```sql
CREATE TABLE outbox (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  topic      text        NOT NULL,
  msg_key    text        NOT NULL,
  payload    jsonb       NOT NULL,
  headers    jsonb       NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

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
```

`prune --days=N` чистит также `aggregated_checks` старше N дней.

## RabbitMQ

Топологию объявляет каждый сервис на старте, если `AMQP_DECLARE_TOPOLOGY=true`. Объявление идемпотентно.

| Объект | Тип | Параметры |
|---|---|---|
| `pulse.checks` | exchange, topic, durable | |
| `pulse.events` | exchange, topic, durable | |
| `pulse.dlx` | exchange, topic, durable | dead letter |
| `pulse.checks.requested` | queue, quorum | bind `pulse.checks` / `check.requested`; `x-dead-letter-exchange=pulse.dlx`, `x-delivery-limit=5` |
| `pulse.notifications` | queue, quorum | bind `pulse.events` / `status.changed`; DLX `pulse.dlx` |
| `pulse.dead` | queue, quorum | bind `pulse.dlx` / `#` |

Сообщения — JSON, `content_type=application/json`, `delivery_mode=persistent`. В заголовке `traceparent` (W3C) передаётся контекст трейса. Публикация идёт с publisher confirms.

`check.requested`:

```json
{"request_id":"uuid","monitor_id":1,"url":"http://target:8090/","method":"GET",
 "timeout_ms":5000,"expected_status":200,"scheduled_at":"2026-10-12T10:00:00Z"}
```

`status.changed`:

```json
{"monitor_id":1,"monitor_name":"target","url":"http://target:8090/","from":"up","to":"down",
 "at":"2026-10-12T10:00:05Z","error":"timeout after 5000ms","incident_id":7}
```

Checker подтверждает сообщение (ack) только после коммита в БД. Что происходит при ошибке обработки, зависит от `AMQP_RETRY_MODE`:

- **`requeue`** (по умолчанию): пауза 1 с, затем `nack requeue=true`. После `x-delivery-limit` попыток RabbitMQ сам отправляет сообщение в `pulse.dead`.
- **`delayed`**: сообщение отклоняется (`reject`) и через `x-dead-letter-exchange` основной очереди попадает в `pulse.retry` → `pulse.checks.retry`. Это quorum-очередь с `x-message-ttl` = `AMQP_RETRY_DELAY` и `x-dead-letter-exchange=pulse.checks`: по истечении TTL сообщение возвращается в основную очередь. Попытки считаются по заголовку `x-death`. После `AMQP_RETRY_MAX` неудачных попыток checker сам публикует копию в `pulse.dlx` (с publisher confirm и заголовками `x-pulse-parked-from`, `x-pulse-parked-reason`, `x-pulse-last-error`) и подтверждает оригинал. Непарсящееся сообщение паркуется сразу, без ретраев.

```
pulse.checks ─► pulse.checks.requested ──reject──► pulse.retry ─► pulse.checks.retry
     ▲                       │                                          │ TTL
     └───────────────────────┼──────────── dead-letter ◄────────────────┘
                             └── попыток ≥ AMQP_RETRY_MAX ──► pulse.dlx ─► pulse.dead
```

В режиме `delayed` топология другая: у основной очереди другой DLX и есть дополнительные exchange и очередь. Поэтому `AMQP_RETRY_*` должны совпадать у всех сервисов, которые её объявляют. Переключение режима на существующей очереди даёт `PRECONDITION_FAILED`: очередь нужно пересоздать или управлять топологией снаружи (Topology Operator, `AMQP_DECLARE_TOPOLOGY=false`). Метрика `pulse_amqp_failed_deliveries_total{queue,action}` показывает исход каждой ошибки: `requeue`, `retry` или `park`.

## Kafka

| Объект | Параметры |
|---|---|
| топик `pulse.check-results` | 6 партиций, ключ = `monitor_id` (порядок в пределах монитора), `retention.ms` = 7 дней; в проде `replication.factor=3`, `min.insync.replicas=2` |
| consumer group `pulse-aggregator` | offset коммитится **после** коммита транзакции в PostgreSQL |

Продюсер: `acks=all`, идемпотентный продюсер включён. В заголовке `traceparent` передаётся контекст трейса. Сообщение `check.result`:

```json
{"check_id":123,"monitor_id":1,"checked_at":"2026-10-12T10:00:05Z","ok":false,
 "status_code":503,"latency_ms":41,"error":"unexpected status 503"}
```

**Outbox relay** работает внутри checker, если задан `KAFKA_BROKERS`. Раз в `OUTBOX_POLL_INTERVAL` он берёт до `OUTBOX_BATCH` строк `outbox ORDER BY id FOR UPDATE SKIP LOCKED`, публикует их, дожидается подтверждения брокера и удаляет строки в той же транзакции. Если Kafka недоступна, строки копятся в outbox, проверки продолжают работать. Несколько реплик checker не публикуют одно и то же благодаря `SKIP LOCKED`. Падение между публикацией и коммитом даёт дубль, поэтому потребитель обязан быть идемпотентным.

**aggregator**: в одной транзакции делает `INSERT INTO aggregated_checks ... ON CONFLICT DO NOTHING`. Если строка вставилась, делает upsert в `uptime_hourly`, иначе считает дубль (`pulse_aggregator_duplicates_total`). Затем коммитит offset. `/readyz` проверяет БД и связь с брокером.

## Известные ограничения (материал для модулей 7 и 11)

Это сознательные упрощения. Их находят и обсуждают в лабах по устойчивости и chaos, а исправляет ментор, если студент придёт с обоснованным предложением.

- **Scheduler не знает о проверках «в полёте».** Он публикует монитор каждые `interval_seconds`, даже если предыдущая проверка ещё в очереди. Если checker не успевает, очередь `pulse.checks.requested` растёт без ограничения.
- **В режиме `requeue` (по умолчанию) падение PostgreSQL** расходует `x-delivery-limit` примерно за 5 секунд: перед каждым nack только фиксированная пауза 1 с. Если БД лежит дольше, сообщения уходят в `pulse.dead`. Лечится режимом `AMQP_RETRY_MODE=delayed` — это лаба модуля 7.
- **`status.changed` публикуется после коммита напрямую, без outbox:** если RabbitMQ недоступен на всех трёх попытках, уведомление теряется (сама проверка сохранена).
- **Мигратор выполняет каждую миграцию в транзакции:** `CREATE INDEX CONCURRENTLY` через `migrate` невозможен.
- Checker **не держит** соединение с PostgreSQL во время HTTP-проверки: проба выполняется до транзакции.

## Логика checker

1. Выполнить запрос с `timeout_ms`, без редиректов дальше 5, с заголовком `User-Agent: $CHECKER_USER_AGENT`.
2. `ok = (status == expected_status)`.
3. В одной транзакции: вставить строку в `checks`, обновить `monitors` (`last_checked_at`, `consecutive_failures`, `status`) и, если Kafka включена, положить `check.result` в `outbox`.
4. Статус: при `ok` — `up`; при провале — `down`, если `consecutive_failures >= CHECKER_FAILURE_THRESHOLD`, иначе статус не меняется.
5. Смена `up→down` открывает инцидент, `down→up` закрывает. Если статус сменился, после коммита публикуется `status.changed`.
6. При провале и включённом S3 checker кладёт снапшот (статус, заголовки, первые 64 КБ тела) в `s3://$S3_BUCKET/{monitor_id}/{check_id}.txt` и пишет ключ в `checks.snapshot_key`. Если загрузка не удалась, это warn в логе, проверку он не валит.

## Метрики

Плюс стандартные `go_*` и `process_*` у Go-сервисов.

| Метрика | Тип | Лейблы | Где |
|---|---|---|---|
| `pulse_api_http_requests_total` | counter | `route`, `method`, `code` | api |
| `pulse_api_http_request_duration_seconds` | histogram | `route` | api |
| `pulse_monitors` | gauge | `status` | api |
| `pulse_open_incidents` | gauge | | api |
| `pulse_scheduler_ticks_total` | counter | | scheduler |
| `pulse_scheduler_published_total` | counter | | scheduler |
| `pulse_scheduler_publish_errors_total` | counter | | scheduler |
| `pulse_scheduler_tick_duration_seconds` | histogram | | scheduler |
| `pulse_scheduler_due_monitors` | gauge | | scheduler |
| `pulse_checker_checks_total` | counter | `result` = `ok\|fail` | checker |
| `pulse_checker_target_duration_seconds` | histogram | | checker |
| `pulse_checker_processing_errors_total` | counter | `stage` | checker |
| `pulse_checker_inflight` | gauge | | checker |
| `pulse_checker_status_changes_total` | counter | `to` | checker |
| `pulse_amqp_failed_deliveries_total` | counter | `queue`, `action` = `requeue\|retry\|park` | checker, notifier |
| `pulse_checker_outbox_pending` | gauge | | checker |
| `pulse_checker_outbox_published_total` | counter | | checker |
| `pulse_checker_outbox_publish_errors_total` | counter | | checker |
| `pulse_notifier_notifications_total` | counter | `channel`, `result` | notifier |
| `pulse_notifier_deduplicated_total` | counter | | notifier |
| `pulse_aggregator_consumed_total` | counter | | aggregator |
| `pulse_aggregator_duplicates_total` | counter | | aggregator |
| `pulse_aggregator_processing_errors_total` | counter | | aggregator |

Lag потребителя (`pulse-aggregator`) снимается снаружи: kafka-exporter или метрики Strimzi.

У api роутов ограниченное множество (шаблоны вида `/api/v1/monitors/{id}`), без высокой кардинальности. Счётчики PHP-FPM хранятся в Valkey, потому что FPM — shared-nothing.

## Fault injection (`PULSE_FAULTS_ENABLED=1`)

Go-сервисы (`POST`, на их `HTTP_ADDR`):

- `/debug/fault/leak?mb=N` — выделить и удерживать N МБ (для OOMKilled-лаб);
- `/debug/fault/cpu?seconds=N` — нагрузить ядро на N секунд;
- `/debug/fault/crash` — `os.Exit(2)`;
- `/debug/fault/ready?value=false` — принудительно сломать или вернуть `/readyz`;
- `/debug/pprof/*` — стандартный pprof.

api: `PULSE_FAULT_LATENCY_MS`, `PULSE_FAULT_ERROR_RATE` и `GET /debug/slow-query?seconds=N`.

## target (`services/cmd/target`)

| Путь | Поведение |
|---|---|
| `GET /` | ответ в текущем режиме (по умолчанию `up` → 200 "ok") |
| `GET /status/{code}` | вернуть код |
| `GET /delay/{ms}` | ответить 200 через ms |
| `GET /flaky?rate=0.3` | доля 500 |
| `POST /admin/mode?mode=up\|down\|slow\|flaky` | сменить режим `/` (`slow` = 10 с, `flaky` = 50%) |
| `GET /healthz`, `/metrics` | как у всех |

## Сборка

- Go: один модуль `services/` (`go.mod`, `module pulse`), бинари из `./cmd/{scheduler,checker,notifier,aggregator,target}`. Kafka-клиент — `github.com/twmb/franz-go`. `CGO_ENABLED=0` собирается статически. Тесты: `go test ./...`.
- PHP: `api/composer.json`. Расширения: `pdo_pgsql` (и `opcache` для прода). Зависимости: `predis/predis`. Тесты: `vendor/bin/phpunit`. Корень FPM: `api/public/index.php`.
- status-page: чистая статика без сборки, ходит на `/api/v1/status` относительным путём.

## Соглашения для инфраструктуры (лабы проверяют именно их)

Любое имя ниже можно переопределить переменной окружения у соответствующего `check.sh`. Личные параметры стенда (`VPS_HOST`, `VPS_USER`, `VPS_DOMAIN`, `PULSE_API_TOKEN`, `GITHUB_OWNER` и т. п.) лежат в `~/.config/devops-course.env`, его подхватывает `lib/check-lib.sh`.

**Образы и registry**
- Имена: `pulse-api` (FPM), `pulse-web` (nginx + статика), `pulse-scheduler`, `pulse-checker`, `pulse-notifier`, `pulse-aggregator`, `pulse-target`. Локальный тег — `dev`.
- В CI: `ghcr.io/<owner>/pulse-<component>:<git sha>`, деплой — только по digest.

**VPS (модули 1–4)**
- target как systemd-юнит `pulse-target.service`, слушает `127.0.0.1:8090`, наружу — через nginx хоста с TLS.
- Деплой стека: compose-проект `pulse` в `/opt/pulse`, образы через переменные `PULSE_<COMPONENT>_IMAGE` (по digest), `web` слушает `127.0.0.1:8080` за nginx хоста. Скрипт `deploy/vps/deploy.sh <release.env>`: health-gate и автооткат.
- Ansible: `deploy/ansible/{ansible.cfg, requirements.yml, inventories/{stand,ha,prod}, site.yml, patroni.yml, roles/*/molecule/default}`. Patroni: `scope: pulse`, конфиг `/etc/patroni/patroni.yml`, HAProxy `:5000` → лидер (rw), `:5001` → реплики (ro).

**compose (модуль 3)**
- Сервисы: `web, api, migrate, scheduler, checker, notifier, aggregator, target, postgres, valkey, rabbitmq, kafka, s3`. Наружу торчит только `web` на `localhost:8080`.
- `web/Dockerfile` собирается из контекста корня `pulse/` (ему нужен `status-page/`).
- Локальные секреты — в `pulse/secrets/` (в `.gitignore`), токен API — `secrets/pulse_api_token`. Образ RabbitMQ 4 не понимает `*_FILE`: пароль задаётся смонтированным `rabbitmq.conf`.
- Pull-through кэш registry: `deploy/registry-cache/compose.yaml`, сервис `registry-cache` на порту 5000.

**Kubernetes (модули 5–11)**
- kind-кластер `pulse`, namespace `pulse`, лейблы `app.kubernetes.io/name=<компонент>` и `app.kubernetes.io/part-of=pulse` на всех объектах, включая зависимости. Job миграций — `app.kubernetes.io/name=migrate`, CronJob очистки — `app.kubernetes.io/name=prune`.
- Service называются как сервисы compose: `web, api, target, postgres, valkey, rabbitmq, kafka, s3`. С модуля 7 зависимости переезжают на операторы, имена ниже.
- Вход — Gateway API, хост `pulse.localtest.me` (резолвится в 127.0.0.1), порт 80 на хосте.
- Helm (модуль 6): релиз `pulse`; values-интерфейс, на который опираются проверки: `image.registry`, `image.tag`, `<component>.{enabled,replicaCount,resources}`, `existingSecret`, schema с `additionalProperties: false`.
- Canary (модули 6, 8): `web` — через Gateway API traffic-router plugin Argo Rollouts. `api` за nginx по FastCGI, отдельного HTTP-маршрута у него нет, поэтому его canary идёт по числу реплик, а анализ — по метрикам `web`.
- Данные (модуль 7): CNPG `Cluster` `pulse-pg`, `Pooler` `pulse-pg-pooler-rw`, `ObjectStore` `pulse-backups`, восстановленный кластер `pulse-pg-restore`; `RabbitmqCluster` `pulse-rabbitmq`; Strimzi `Kafka` `pulse-kafka`, `KafkaUser` `pulse-checker` и `pulse-aggregator`. Операторы — в namespace `cnpg-system`, `rabbitmq-system`, `strimzi`.
- `migrate` ходит в PostgreSQL **напрямую или через session-пул**: мигратор держит session-level advisory lock, transaction-пулинг его ломает. С динамическими кредами из OpenBao (модуль 10) объекты схемы должны принадлежать стабильной роли-владельцу (`pulse_owner`, через `SET ROLE`), иначе после истечения временного пользователя таблицы останутся «ничьими».
- Безопасность (модуль 10): Secret с кредами БД — `pulse-db` (ключ `DATABASE_URL`), OpenBao — namespace `openbao`; манифесты в `deploy/platform/{openbao,external-secrets,policies,runtime}/`, правила SOPS — `.sops.yaml` в корне Pulse. Образ `pulse-api` содержит `sh` (FPM-база), Go-образы — distroless без шелла.
- Облако (модуль 9): `deploy/tofu/{bootstrap, modules/{network,kubernetes,registry,dns}, envs/prod}` и `.tflint.hcl`. Модуль `network` принимает `variable "subnets"` (`map(string)`, зона → CIDR) с валидацией CIDR, остальные переменные имеют default. Outputs `envs/prod`: `network_id`, `cluster_id`, `registry_id`. На ресурсах лейбл `project=pulse`. Оценка стоимости — `docs/cloud-cost.md`. Kube-контекст облачного кластера — `yc-pulse`.
- Мониторинг (модуль 8): namespace `monitoring`, `logging`, `tracing`. Аннотация `runbook_url` у алертов ведёт на `docs/runbooks/<AlertName>.md` в репозитории Pulse.
- Логи checker: проваленная проверка пишется на уровне `info` (`"msg":"check recorded"` с `monitor_id`, `check_id`, `trace_id`), успешная — на `debug`.
- Смоук: `PULSE_URL=http://localhost:8080 PULSE_API_TOKEN=... scripts/smoke.sh`. Проверяет `/healthz`, `/readyz` и статус-страницу. Создаёт два монитора: на `$TARGET_URL/` (ждёт `up`) и на `$TARGET_URL/status/503` (ждёт `down` и открытый инцидент), затем удаляет оба. `TARGET_URL` — адрес target, каким его видит checker (по умолчанию `http://target:8090`). Таймаут — `SMOKE_TIMEOUT`, по умолчанию 120 с. При `SMOKE_KAFKA=1` смоук дополнительно ждёт, что у монитора появились строки в `/api/v1/monitors/{id}/uptime`, то есть цепочка outbox → Kafka → aggregator тоже работает. Код возврата 0 означает, что весь конвейер api → scheduler → RabbitMQ → checker → БД (→ Kafka → aggregator) работает.

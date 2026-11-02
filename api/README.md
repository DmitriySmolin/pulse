# pulse/api

REST API Pulse, миграции и console-команды. PHP 8.5 (совместим с 8.4), без фреймворка: PDO pgsql + `predis/predis`. Контракт — `../CONTRACT.md`.

## Требования к рантайму

- Расширения: `pdo_pgsql` (сборка требует `libpq`), для прода — `opcache`.
- Корень FPM — `public/index.php`: на него идут все пути API (`/api/*`, `/healthz`, `/readyz`, `/metrics`, `/debug/*`).
- Конфигурация читается из переменных окружения процесса, поэтому они должны доходить до воркеров FPM.
- Логи — JSON, одна строка на событие, пишутся в `php://stderr` воркера. Console-команды пишут `info` в stdout, `warn`/`error` в stderr.

## Переменные окружения

| Переменная | По умолчанию | |
|---|---|---|
| `DATABASE_URL` | — (обязательна) | `postgres://user:pass@host:5432/db?sslmode=disable`; query-параметры уходят в libpq |
| `VALKEY_URL` | — (обязательна для HTTP) | `redis://[user:pass@]host:6379/0`, `rediss://` — TLS |
| `PULSE_API_TOKEN` | — (обязательна для HTTP) | Bearer-токен для `POST/PATCH/DELETE` |
| `PULSE_STATUS_CACHE_TTL` | `10` | секунды кэша `/api/v1/status` в Valkey; `0` — без кэша |
| `LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `PULSE_FAULTS_ENABLED` | `0` | `1` включает `PULSE_FAULT_*` и `/debug/slow-query` |
| `PULSE_FAULT_LATENCY_MS` | `0` | задержка на каждый `/api/*` |
| `PULSE_FAULT_ERROR_RATE` | `0` | доля `500` на `/api/*`, `0..1` |
| `SEED_TARGET_URL` | `http://target:8090` | куда смотрят демо-мониторы `seed` |

Любую переменную можно передать файлом: `NAME_FILE=/run/secrets/...` (хвостовой перевод строки отрезается). Если заданы и `NAME`, и `NAME_FILE`, это ошибка. Без обязательных переменных `/healthz` отвечает 200, `/readyz` 503, остальные пути 500, причина пишется в лог.

## Console

```sh
php bin/console migrate              # migrations/*.sql по порядку; advisory lock, каждая миграция в своей транзакции
php bin/console migrate:status
php bin/console seed                 # 3 демо-монитора на SEED_TARGET_URL, повторный запуск ничего не дублирует
php bin/console prune --days=30      # checks и aggregated_checks старше N дней, батчами
php bin/console wait-db --timeout=60 # ждать PostgreSQL с backoff
```

Код возврата: `0` — успех, `1` — ошибка выполнения, `2` — неверные аргументы.

## Тесты

```sh
composer install
vendor/bin/phpunit --testsuite unit          # без внешних зависимостей
TEST_DATABASE_URL=postgres://pulse:pass@localhost:5432/pulse_test?sslmode=disable \
TEST_VALKEY_URL=redis://localhost:6379/15 \
vendor/bin/phpunit                           # unit + integration
```

Интеграционные тесты без `TEST_DATABASE_URL`/`TEST_VALKEY_URL` пропускаются (skip). **Перед каждым тестом они удаляют схему `public` тестовой базы**, поэтому имя базы обязано содержать `test`, иначе тесты падают, ничего не тронув. В Valkey удаляются только ключи `pulse:*`, которые использует API.

## Устройство

```
public/index.php           фронт-контроллер FPM
bin/console                console-команды
migrations/NNNN_name.sql   схема (0001_init, 0002_kafka)
src/Http/                  Kernel (жизненный цикл, ошибки, auth, fault injection), Router, Request/Response
src/Controller/            эндпоинты
src/Repository/            SQL
src/Metrics/               Prometheus text format; счётчики в Valkey (FPM — shared-nothing)
src/Status/                сводка для статус-страницы и её кэш
src/Console/               команды и Migrator
```

`/metrics`: `pulse_api_http_requests_total{route,method,code}`, `pulse_api_http_request_duration_seconds{route}` (histogram), `pulse_monitors{status}` (`paused` считается отдельным статусом), `pulse_open_incidents`. Лейбл `route` — шаблон маршрута (`/api/v1/monitors/{id}`), всё неизвестное — `unmatched`.

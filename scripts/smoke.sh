#!/usr/bin/env bash
# Сквозной смоук-тест работающего стенда Pulse (compose, kind, облако).
#
#   PULSE_URL=http://localhost:8080 PULSE_API_TOKEN=... scripts/smoke.sh
#
# Проверяет: /healthz, /readyz, статус-страницу и /api/v1/status; затем создаёт
# два монитора — на $TARGET_URL/ (ждёт up) и на $TARGET_URL/status/503 (ждёт down
# и открытый инцидент). С SMOKE_KAFKA=1 дополнительно ждёт строки uptime,
# то есть работу цепочки outbox → Kafka → aggregator. Мониторы удаляются всегда.
#
# Переменные:
#   PULSE_URL        адрес web (по умолчанию http://localhost:8080)
#   PULSE_API_TOKEN  токен записи (обязателен; можно PULSE_API_TOKEN_FILE)
#   TARGET_URL       target, каким его видит checker (по умолчанию http://target:8090)
#   SMOKE_TIMEOUT    сколько ждать каждый этап, секунд (по умолчанию 120)
#   SMOKE_KAFKA      1 — проверить Kafka-конвейер
#   SMOKE_INSECURE   1 — не проверять TLS-сертификат (curl -k)
set -euo pipefail

PULSE_URL="${PULSE_URL:-http://localhost:8080}"
PULSE_URL="${PULSE_URL%/}"
TARGET_URL="${TARGET_URL:-http://target:8090}"
TARGET_URL="${TARGET_URL%/}"
SMOKE_TIMEOUT="${SMOKE_TIMEOUT:-120}"
SMOKE_KAFKA="${SMOKE_KAFKA:-0}"

if [[ -z "${PULSE_API_TOKEN:-}" && -n "${PULSE_API_TOKEN_FILE:-}" ]]; then
  PULSE_API_TOKEN="$(<"$PULSE_API_TOKEN_FILE")"
fi
: "${PULSE_API_TOKEN:?нужен PULSE_API_TOKEN (или PULSE_API_TOKEN_FILE)}"

for c in curl jq; do
  command -v "$c" >/dev/null || { echo "smoke: нужна команда $c" >&2; exit 2; }
done

CURL=(curl -sS --max-time 10)
[[ "${SMOKE_INSECURE:-0}" == 1 ]] && CURL+=(-k)

started=$SECONDS
step() { printf '[%3ds] %s\n' "$((SECONDS - started))" "$*"; }
fail() { printf '[%3ds] FAIL: %s\n' "$((SECONDS - started))" "$*" >&2; exit 1; }

# get PATH → тело в $BODY, код в $CODE
BODY=''
CODE=''
request() {
  local method="$1" path="$2" data="${3:-}" out
  local args=("${CURL[@]}" -X "$method" -w '\n%{http_code}' -H "Accept: application/json")
  if [[ "$method" != GET ]]; then
    args+=(-H "Authorization: Bearer $PULSE_API_TOKEN")
  fi
  if [[ -n "$data" ]]; then
    args+=(-H "Content-Type: application/json" --data "$data")
  fi
  if ! out="$("${args[@]}" "$PULSE_URL$path" 2>&1)"; then
    CODE=000
    BODY="$out"
    return 0
  fi
  CODE="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
}

expect() {
  local want="$1" what="$2"
  [[ "$CODE" == "$want" ]] || fail "$what: ожидали HTTP $want, получили $CODE. Ответ: ${BODY:0:300}"
}

# wait_until "описание" команда... — повторяет команду раз в 2 с до SMOKE_TIMEOUT.
wait_until() {
  local what="$1"; shift
  local deadline=$((SECONDS + SMOKE_TIMEOUT))
  until "$@"; do
    (( SECONDS >= deadline )) && fail "$what: не дождались за ${SMOKE_TIMEOUT}s. Последний ответ: ${BODY:0:300}"
    sleep 2
  done
  step "OK: $what"
}

created=()
cleanup() {
  local id
  for id in "${created[@]}"; do
    request DELETE "/api/v1/monitors/$id"
    [[ "$CODE" == 204 || "$CODE" == 404 ]] || echo "smoke: не удалось удалить монитор $id (HTTP $CODE)" >&2
  done
}
trap cleanup EXIT

step "PULSE_URL=$PULSE_URL TARGET_URL=$TARGET_URL"

request GET /healthz
expect 200 "/healthz"
step "OK: /healthz"

request GET /readyz
expect 200 "/readyz (зависимости api недоступны?)"
step "OK: /readyz"

page="$("${CURL[@]}" -w '\n%{http_code}' "$PULSE_URL/" 2>&1 || true)"
[[ "${page##*$'\n'}" == 200 ]] || fail "статус-страница /: HTTP ${page##*$'\n'}"
grep -qi '<html' <<<"$page" || fail "статус-страница /: в ответе нет HTML"
step "OK: статус-страница"

request GET /api/v1/status
expect 200 "/api/v1/status"
jq -e '.overall' >/dev/null <<<"$BODY" || fail "/api/v1/status: нет поля overall"
step "OK: /api/v1/status"

suffix="$(date +%s)-$RANDOM"
MONITOR_ID=''
# Без подоболочки: id сразу попадает в created, и trap удалит монитор даже при сбое дальше.
create_monitor() {
  local name="$1" url="$2"
  request POST /api/v1/monitors "$(jq -nc --arg n "$name" --arg u "$url" '{name:$n, url:$u, interval_seconds:10, timeout_ms:3000}')"
  expect 201 "создание монитора $name"
  MONITOR_ID="$(jq -r '.id' <<<"$BODY")"
  [[ "$MONITOR_ID" =~ ^[0-9]+$ ]] || fail "создание монитора $name: в ответе нет id"
  created+=("$MONITOR_ID")
}

create_monitor "smoke-up-$suffix" "$TARGET_URL/"
up_id="$MONITOR_ID"
create_monitor "smoke-down-$suffix" "$TARGET_URL/status/503"
down_id="$MONITOR_ID"
step "созданы мониторы $up_id (ждём up) и $down_id (ждём down)"

monitor_status_is() {
  request GET "/api/v1/monitors/$1"
  [[ "$CODE" == 200 ]] && [[ "$(jq -r '.status' <<<"$BODY")" == "$2" ]]
}
wait_until "монитор $up_id стал up (scheduler → RabbitMQ → checker → БД)" monitor_status_is "$up_id" up
wait_until "монитор $down_id стал down" monitor_status_is "$down_id" down

incident_open() {
  request GET "/api/v1/incidents?open=1"
  [[ "$CODE" == 200 ]] && jq -e --argjson id "$1" '.items | any(.monitor_id == $id)' >/dev/null <<<"$BODY"
}
wait_until "открыт инцидент по монитору $down_id" incident_open "$down_id"

if [[ "$SMOKE_KAFKA" == 1 ]]; then
  uptime_present() {
    request GET "/api/v1/monitors/$1/uptime?days=1"
    [[ "$CODE" == 200 ]] && jq -e '.items | length > 0' >/dev/null <<<"$BODY"
  }
  wait_until "uptime по монитору $up_id посчитан (outbox → Kafka → aggregator)" uptime_present "$up_id"
fi

step "SMOKE PASSED"

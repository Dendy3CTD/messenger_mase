#!/usr/bin/env bash
# Сборка и запуск mase-сервера в одном из двух режимов.
#
#   run.sh dev    разработка: БД ~/mase-data/dev.sqlite, порт 8081, без Cloudflare Tunnel.
#                 MASE_DB, MASE_ADDR и MASE_MEDIA из окружения здесь игнорируются, чтобы
#                 переменные от прод-сессии не подставили прод-БД.
#   run.sh prod   прод: требует явный MASE_DB, порт 8080, запускает Cloudflare Tunnel.
#                 Если файла БД нет, не стартует (обход: MASE_ALLOW_NEW_DB=1).
#                 Защита от случайного запуска: нужен MASE_I_MEAN_PROD=1 в той же команде
#                 (не экспортировать в окружение) и интерактивный терминал (stdin — tty),
#                 поэтому скрипты, тесты и автоматика прод-туннель поднять не могут:
#                   MASE_I_MEAN_PROD=1 MASE_DB=~/mase-data/mase.sqlite ./run.sh prod
#
# Переменные (необязательные):
#   MASE_DATA_DIR      каталог данных, по умолчанию ~/mase-data
#   MASE_DEV_DB, MASE_DEV_MEDIA, MASE_DEV_PORT   настройки dev
#   MASE_ADDR, MASE_MEDIA                        настройки prod (по умолчанию :8080 и <данные>/media)
#   MASE_LOG_LEVEL     debug|info|warn|error (сейчас только проверяется сервером)
#   MASE_DRY_RUN=1     показать итоговую конфигурацию и выйти, ничего не собирая и не запуская
#   CLOUDFLARED_BIN, MASE_GOPATH
set -euo pipefail

log() { printf '[mase-run] %s\n' "$1"; }
err() { printf '[mase-run] ОШИБКА: %s\n' "$1" >&2; }

usage() {
  cat >&2 <<'EOF'
Использование: run.sh dev | prod
  dev   разработка: dev-БД, порт 8081, без туннеля
  prod  прод: нужен явный MASE_DB (файл должен существовать), MASE_I_MEAN_PROD=1
        в той же команде и интерактивный терминал; порт 8080, туннель
EOF
  exit 2
}

[[ $# -eq 1 ]] || usage
MODE="$1"
[[ "$MODE" == dev || "$MODE" == prod ]] || usage

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(dirname "$SCRIPT_DIR")"
DATA_DIR="${MASE_DATA_DIR:-$HOME/mase-data}"
RUN_DIR="$DATA_DIR/run"
BINARY="$SCRIPT_DIR/mase-server-$MODE"     # отдельный бинарь на режим: dev не перезаписывает работающий prod
PIDFILE="$RUN_DIR/$MODE.pid"
CONFIG_FILE="$SCRIPT_DIR/mase-tunnel.yml"
CLOUDFLARED_BIN="${CLOUDFLARED_BIN:-$(command -v cloudflared 2>/dev/null || echo "$HOME/cloudflared-bin")}"
LOG_LEVEL="${MASE_LOG_LEVEL:-info}"

resolve() { realpath -m -- "$1"; }

# ── Разбор режима ────────────────────────────────────────────────────────────
if [[ "$MODE" == dev ]]; then
  PORT="${MASE_DEV_PORT:-8081}"
  ADDR=":$PORT"
  DB_FILE="${MASE_DEV_DB:-$DATA_DIR/dev.sqlite}"
  MEDIA_DIR="${MASE_DEV_MEDIA:-$DATA_DIR/dev-media}"
  USE_TUNNEL=0

  for v in MASE_DB MASE_ADDR MASE_MEDIA; do
    if [[ -n "${!v:-}" ]]; then
      log "предупреждение: $v из окружения игнорируется в режиме dev"
    fi
  done

  # dev никогда не работает на прод-БД
  prod_dbs=("$DATA_DIR/mase.sqlite")
  [[ -n "${MASE_DB:-}" ]] && prod_dbs+=("$MASE_DB")
  for c in "${prod_dbs[@]}"; do
    if [[ "$(resolve "$DB_FILE")" == "$(resolve "$c")" ]]; then
      err "dev-БД ($DB_FILE) совпадает с прод-БД ($c); выберите другой файл (MASE_DEV_DB)"
      exit 1
    fi
  done
else
  if [[ -z "${MASE_DB:-}" ]]; then
    err "режим prod требует явный MASE_DB (путь к прод-БД); молчаливого значения по умолчанию нет"
    exit 1
  fi
  if [[ "${MASE_I_MEAN_PROD:-}" != 1 ]]; then
    err "режим prod поднимает публичный туннель к mase.nemilk.ru. Подтвердите намерение: укажите MASE_I_MEAN_PROD=1 в этой же команде (не экспортируйте переменную в окружение)"
    exit 1
  fi
  DB_FILE="$MASE_DB"
  if [[ ! -f "$DB_FILE" && "${MASE_ALLOW_NEW_DB:-}" != 1 ]]; then
    err "файл БД не найден: $DB_FILE. Сервер создал бы новую пустую БД. Если это нужно осознанно — MASE_ALLOW_NEW_DB=1"
    exit 1
  fi
  ADDR="${MASE_ADDR:-:8080}"
  PORT="${ADDR##*:}"
  MEDIA_DIR="${MASE_MEDIA:-$DATA_DIR/media}"
  USE_TUNNEL=1
  if [[ ! -f "$CONFIG_FILE" ]]; then
    err "Tunnel не настроен. Сначала выполни: bash deploy/setup-cf-tunnel.sh"
    exit 1
  fi
  if [[ ! -x "$CLOUDFLARED_BIN" ]]; then
    err "cloudflared не найден: $CLOUDFLARED_BIN (укажи CLOUDFLARED_BIN)"
    exit 1
  fi
fi

if ! [[ "$PORT" =~ ^[0-9]+$ ]]; then
  err "не удалось определить порт из адреса '$ADDR'"
  exit 1
fi

# ── Сухой запуск ─────────────────────────────────────────────────────────────
if [[ "${MASE_DRY_RUN:-}" == 1 ]]; then
  log "сухой запуск: ничего не собирается и не запускается"
  printf '  режим: %s\n  адрес: %s\n  БД: %s\n  медиа: %s\n  туннель: %s\n  бинарь: %s\n  уровень логов: %s\n' \
    "$MODE" "$ADDR" "$DB_FILE" "$MEDIA_DIR" "$([[ "$USE_TUNNEL" == 1 ]] && echo да || echo нет)" "$BINARY" "$LOG_LEVEL"
  exit 0
fi

# ── Защита prod: только из интерактивного терминала ──────────────────────────
# Не сухой запуск, значит дальше будут сборка, сервер и туннель. Автоматика (скрипты, тесты,
# ассистент) работает без терминала, и переменная окружения ей не поможет.
if [[ "$MODE" == prod && ! -t 0 ]]; then
  err "prod запускается только из интерактивного терминала (stdin не терминал): скрипты, тесты и автоматика не могут поднять прод-туннель"
  exit 1
fi

# ── Окружение сборки ─────────────────────────────────────────────────────────
command -v go >/dev/null 2>&1 || export PATH="$PATH:$HOME/go/bin:/usr/local/go/bin"
export GOPATH="${MASE_GOPATH:-$HOME/go-workspace}"

mkdir -p "$RUN_DIR" "$MEDIA_DIR" "$(dirname "$DB_FILE")"

log "Режим: $MODE. Собираю Go-сервер..."
( cd "$SERVER_DIR" && go build -buildvcs=false -o "$BINARY" ./cmd/server/ )
log "Сборка OK"

# Останавливаем только собственный прошлый запуск этого режима (по PID-файлу), не всё подряд
stop_previous() {
  [[ -f "$PIDFILE" ]] || return 0
  local pid
  pid="$(cat "$PIDFILE" 2>/dev/null || true)"
  if [[ -n "$pid" && -d "/proc/$pid" && "$(readlink -f "/proc/$pid/exe" 2>/dev/null)" == "$(readlink -f "$BINARY")" ]]; then
    log "Останавливаю прошлый запуск ($MODE, pid $pid)"
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 20); do [[ -d "/proc/$pid" ]] || break; sleep 0.25; done
    if [[ -d "/proc/$pid" ]]; then err "процесс $pid не остановился"; exit 1; fi
  fi
  rm -f "$PIDFILE"
}
stop_previous

if ss -ltn "sport = :$PORT" 2>/dev/null | grep -q LISTEN; then
  err "порт $PORT уже занят другим процессом; ничего не запускаю"
  exit 1
fi

# ── Запуск сервера ───────────────────────────────────────────────────────────
log "Запускаю mase-сервер ($MODE) на $ADDR, БД: $DB_FILE"
env MASE_ADDR="$ADDR" MASE_DB="$DB_FILE" MASE_MEDIA="$MEDIA_DIR" MASE_LOG_LEVEL="$LOG_LEVEL" "$BINARY" &
SERVER_PID=$!
echo "$SERVER_PID" > "$PIDFILE"
CF_PID=""

cleanup() {
  log "Завершаю..."
  kill "$SERVER_PID" ${CF_PID:+"$CF_PID"} 2>/dev/null || true
  rm -f "$PIDFILE"
}
trap cleanup EXIT INT TERM

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    log "Сервер готов"
    break
  fi
  sleep 0.5
done

if ! curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
  err "Сервер не ответил за 15 сек"
  exit 1
fi

if [[ "$USE_TUNNEL" == 1 ]]; then
  # принудительно HTTP/2, т.к. QUIC/UDP может блокироваться
  log "Запускаю Cloudflare Tunnel..."
  "$CLOUDFLARED_BIN" tunnel --protocol http2 --config "$CONFIG_FILE" run &
  CF_PID=$!
fi

log ""
log "Mase ($MODE) запущен!"
if [[ "$MODE" == prod ]]; then
  printf '  WebSocket: wss://mase.nemilk.ru/ws\n'
  printf '  Media:     https://mase.nemilk.ru\n'
else
  printf '  WebSocket: ws://127.0.0.1:%d/ws  (эмулятор Android: ws://10.0.2.2:%d/ws)\n' "$PORT" "$PORT"
fi
printf '  Health:    http://127.0.0.1:%d/health\n' "$PORT"
log "Ctrl+C — остановить"
log ""

wait "${CF_PID:-$SERVER_PID}"

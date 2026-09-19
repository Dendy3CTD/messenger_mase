#!/usr/bin/env bash
# Собирает mase Go-сервер и запускает его вместе с Cloudflare Tunnel
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(dirname "$SCRIPT_DIR")"
CLOUDFLARED_BIN="${CLOUDFLARED_BIN:-/home/danil/cloudflared-bin}"
CONFIG_FILE="$SCRIPT_DIR/mase-tunnel.yml"
BINARY="$SCRIPT_DIR/mase-server"
DB_FILE="${MASE_DB:-$SCRIPT_DIR/mase.sqlite}"
MEDIA_DIR="${MASE_MEDIA:-$SCRIPT_DIR/media}"
PORT=8080

export PATH="$PATH:/home/danil/go/bin"
export GOPATH="/home/danil/go-workspace"

log() { printf '[mase-run] %s\n' "$1"; }

if [[ ! -f "$CONFIG_FILE" ]]; then
  log "Tunnel не настроен. Сначала выполни: bash deploy/setup-cf-tunnel.sh"
  exit 1
fi

# Сборка
log "Собираю Go-сервер..."
cd "$SERVER_DIR"
go build -buildvcs=false -o "$BINARY" ./cmd/server/
log "Сборка OK"

mkdir -p "$MEDIA_DIR"

# Завершаем остатки предыдущего запуска (если есть)
pkill -f "$BINARY" 2>/dev/null || true

# Запуск сервера
log "Запускаю mase-сервер на :$PORT"
"$BINARY" -addr ":$PORT" -db "$DB_FILE" -media "$MEDIA_DIR" &
SERVER_PID=$!

# Ждём готовности
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    log "Сервер готов"
    break
  fi
  sleep 0.5
done

if ! curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
  log "Сервер не ответил за 15 сек"
  kill "$SERVER_PID" 2>/dev/null || true
  exit 1
fi

# Запуск tunnel (принудительно HTTP/2, т.к. QUIC/UDP может блокироваться)
log "Запускаю Cloudflare Tunnel..."
"$CLOUDFLARED_BIN" tunnel --protocol http2 --config "$CONFIG_FILE" run &
CF_PID=$!

cleanup() {
  log "Завершаю..."
  kill "$SERVER_PID" "$CF_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

log ""
log "Mase запущен!"
printf '  WebSocket: wss://mase.nemilk.ru/ws\n'
printf '  Media:     https://mase.nemilk.ru\n'
printf '  Health:    http://127.0.0.1:%d/health\n' "$PORT"
log "Ctrl+C — остановить"
log ""

wait "$CF_PID"

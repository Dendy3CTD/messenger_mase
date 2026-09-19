#!/usr/bin/env bash
# Создаёт Cloudflare Tunnel «mase-server» и маршрутизирует mase.nemilk.ru
set -euo pipefail

CLOUDFLARED_BIN="${CLOUDFLARED_BIN:-/home/danil/cloudflared-bin}"
TUNNEL_NAME="mase-server"
DOMAIN="mase.nemilk.ru"
CF_DIR="${HOME}/.cloudflared"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="$SCRIPT_DIR/mase-tunnel.yml"

log() { printf '[setup-cf-tunnel] %s\n' "$1"; }

if [[ ! -x "$CLOUDFLARED_BIN" ]]; then
  log "Не найден cloudflared: $CLOUDFLARED_BIN"
  exit 1
fi

if [[ ! -f "$CF_DIR/cert.pem" ]]; then
  log "Авторизация в Cloudflare..."
  "$CLOUDFLARED_BIN" tunnel login
fi

if ! "$CLOUDFLARED_BIN" tunnel list 2>/dev/null | grep -q "$TUNNEL_NAME"; then
  log "Создаю tunnel $TUNNEL_NAME"
  "$CLOUDFLARED_BIN" tunnel create "$TUNNEL_NAME"
else
  log "Tunnel $TUNNEL_NAME уже существует"
fi

TUNNEL_ID="$("$CLOUDFLARED_BIN" tunnel list 2>/dev/null | awk -v n="$TUNNEL_NAME" '$2==n{print $1;exit}')"

if [[ -z "$TUNNEL_ID" ]]; then
  log "Не удалось получить ID tunnel"
  exit 1
fi

CREDENTIALS_FILE="$CF_DIR/${TUNNEL_ID}.json"

log "Привязываю DNS $DOMAIN → tunnel $TUNNEL_NAME"
"$CLOUDFLARED_BIN" tunnel route dns "$TUNNEL_NAME" "$DOMAIN" || \
  log "DNS-запись уже существует или добавь вручную CNAME mase → ${TUNNEL_ID}.cfargotunnel.com"

cat > "$CONFIG_FILE" <<EOF
tunnel: ${TUNNEL_ID}
credentials-file: ${CREDENTIALS_FILE}

ingress:
  - hostname: ${DOMAIN}
    service: http://127.0.0.1:8080
  - service: http_status:404
EOF

log "Готово"
printf 'Tunnel:  %s (%s)\n' "$TUNNEL_NAME" "$TUNNEL_ID"
printf 'Domain:  https://%s\n' "$DOMAIN"
printf 'Config:  %s\n' "$CONFIG_FILE"
printf '\nЗапусти сервер: bash deploy/run.sh\n'

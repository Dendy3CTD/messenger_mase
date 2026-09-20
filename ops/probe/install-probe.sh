#!/usr/bin/env bash
# Ставит пробный сервер для netprobe за Caddy с TLS. Запускать на сервере от root (sudo) после
# ops/vps-bootstrap.sh. Бинарь собирается на своей машине (см. docs/probe-runbook.md).
#
#   sudo bash install-probe.sh --domain probe.nemilk.ru --bin /tmp/probeserver
#   sudo bash install-probe.sh --ip 203.0.113.10 --bin /tmp/probeserver    # самоподписанный, netprobe -insecure
#   добавьте --tls12, чтобы Caddy принимал только TLS 1.2 (эксперимент из раздела 4.1 карты)
#
# Повторный запуск безопасен: пароль пробного аккаунта не перезаписывается, Caddyfile пересоздаётся.
# DRY_RUN=1 только печатает, что будет записано.
set -euo pipefail

DOMAIN="" ; IP="" ; BIN="" ; TLS12=""
DRY_RUN="${DRY_RUN:-}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
log() { printf '[probe] %s\n' "$*"; }
die() { printf '[probe] ОШИБКА: %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --ip)     IP="${2:-}"; shift 2 ;;
    --bin)    BIN="${2:-}"; shift 2 ;;
    --tls12)  TLS12=1; shift ;;
    *) die "неизвестный аргумент: $1" ;;
  esac
done
[[ -n "$DOMAIN" || -n "$IP" ]] || die "нужен --domain <имя> или --ip <адрес>"
[[ -z "$DOMAIN" || -z "$IP" ]] || die "--domain и --ip вместе не используются"
[[ -z "$DOMAIN" || "$DOMAIN" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}$ ]] || die "странное имя домена: $DOMAIN"
[[ -z "$IP" || "$IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || die "странный IPv4: $IP"
[[ -n "$BIN" ]] || die "нужен --bin <путь к бинарю probeserver>"

caddyfile() {
  local site
  echo '{'
  echo '	servers {'
  echo '		protocols h1 h2'
  echo '	}'
  [[ -n "$IP" ]] && echo "	default_sni $IP"
  echo '}'
  if [[ -n "$DOMAIN" ]]; then site="$DOMAIN"; else site="https://$IP"; fi
  echo "$site {"
  if [[ -n "$IP" && -z "$TLS12" ]]; then
    echo '	tls internal'
  elif [[ -n "$TLS12" ]]; then
    if [[ -n "$IP" ]]; then echo '	tls internal {'; else echo '	tls {'; fi
    echo '		protocols tls1.2 tls1.2'
    echo '	}'
  fi
  echo '	request_body {'
  echo '		max_size 9MB'
  echo '	}'
  echo '	reverse_proxy 127.0.0.1:8081'
  echo '}'
}

if [[ -n "$DRY_RUN" ]]; then
  log "(dry-run) Caddyfile был бы таким:"; caddyfile | sed 's/^/    /'
  exit 0
fi

[[ $EUID -eq 0 ]] || die "запускать от root (sudo)"
[[ -x "$BIN" || -f "$BIN" ]] || die "нет файла $BIN"
command -v caddy >/dev/null || die "caddy не установлен (сначала ops/vps-bootstrap.sh)"
if [[ -n "$TLS12" && -n "$IP" ]]; then log "предупреждение: для IP-режима строку tls internal { protocols … } проверьте командой caddy validate"; fi

log "пользователь mase и бинарь"
id mase >/dev/null 2>&1 || adduser --system --group --no-create-home --home /nonexistent --shell /usr/sbin/nologin mase
install -m 755 "$BIN" /usr/local/bin/mase-probeserver

log "учётные данные пробного аккаунта (/etc/mase-probe/env, не перезаписываются)"
install -d -m 750 -o root -g mase /etc/mase-probe
if [[ ! -f /etc/mase-probe/env ]]; then
  pw="$(head -c 18 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')"
  umask 027
  printf 'PROBE_ADDR=127.0.0.1:8081\nPROBE_PHONE=+70000000099\nPROBE_PASSWORD=%s\n' "$pw" > /etc/mase-probe/env
  chown root:mase /etc/mase-probe/env; chmod 640 /etc/mase-probe/env
fi

log "systemd-юнит"
install -m 644 "$HERE/probeserver.service" /etc/systemd/system/mase-probeserver.service
systemctl daemon-reload
systemctl enable --now mase-probeserver
systemctl restart mase-probeserver

log "Caddy"
tmp="$(mktemp)"; caddyfile > "$tmp"
caddy validate --config "$tmp" --adapter caddyfile >/dev/null || { cat "$tmp"; rm -f "$tmp"; die "caddy validate не прошёл, Caddyfile не менял"; }
[[ -f /etc/caddy/Caddyfile && ! -f /etc/caddy/Caddyfile.bak-mase ]] && cp /etc/caddy/Caddyfile /etc/caddy/Caddyfile.bak-mase
install -m 644 "$tmp" /etc/caddy/Caddyfile; rm -f "$tmp"
systemctl reload caddy || systemctl restart caddy

log "проверка на самом сервере"
for _ in $(seq 1 20); do curl -fsS http://127.0.0.1:8081/health >/dev/null 2>&1 && break; sleep 0.5; done
curl -fsS http://127.0.0.1:8081/health >/dev/null || die "probeserver не отвечает на 127.0.0.1:8081: journalctl -u mase-probeserver"
systemctl is-active caddy mase-probeserver
if [[ -n "$DOMAIN" ]]; then
  log "жду выпуска сертификата до 60 с"
  for _ in $(seq 1 30); do curl -fsS "https://$DOMAIN/health" >/dev/null 2>&1 && { log "https://$DOMAIN/health отвечает"; break; }; sleep 2; done
  curl -fsS "https://$DOMAIN/health" >/dev/null 2>&1 || log "сертификата ещё нет: journalctl -u caddy -n 50 (частая причина — DNS не указывает на этот IP или закрыт порт 80)"
fi
cat <<EOM

Готово. Пароль пробного аккаунта лежит на сервере: sudo cat /etc/mase-probe/env
(телефон +70000000099). В чат и в git его не класть.
EOM

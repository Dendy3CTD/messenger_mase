#!/usr/bin/env bash
# Первичная настройка чистого VPS (Debian 12/13, Ubuntu 22.04/24.04): администратор с ключом,
# SSH только по ключам, ufw (22, 80, 443/tcp), fail2ban, автообновления безопасности, Caddy,
# swap на маленьких машинах, ограничение журнала. Запускать от root на новом сервере.
#
#   export SSH_PUBKEY_FILE=~/.ssh/id_ed25519.pub     # публичный ключ, который положить админу
#   scp ops/vps-bootstrap.sh $SSH_PUBKEY_FILE root@<IP>:/root/
#   ssh root@<IP> 'SSH_PUBKEY_FILE=/root/id_ed25519.pub bash /root/vps-bootstrap.sh'
#
# Переменные (необязательные):
#   ADMIN_USER   имя администратора, по умолчанию admin
#   SWAP_MB      размер swap-файла, по умолчанию 1024; создаётся, только если RAM < 1500 МБ и swap нет
#   CADDY_REPO   distro (по умолчанию: пакет из репозитория системы) или official (репозиторий Caddy)
#   DRY_RUN=1    только показать, что будет сделано; ничего не менять (root не нужен)
#
# Go на сервер НЕ ставится: бинари собираются на своей машине (CGO_ENABLED=0) и копируются.
# HTTP/3 (UDP 443) не открывается: QUIC режется ТСПУ, Caddy настраивается на h1/h2.
#
# Защита от потери доступа: SSH перенастраивается последним, после проверки ключа и `sshd -t`;
# не закрывайте текущую сессию, пока не проверите вход новым окном (`ssh admin@<IP>`).
set -euo pipefail

ADMIN_USER="${ADMIN_USER:-admin}"
SWAP_MB="${SWAP_MB:-1024}"
CADDY_REPO="${CADDY_REPO:-distro}"
DRY_RUN="${DRY_RUN:-}"

log() { printf '[bootstrap] %s\n' "$*"; }
die() { printf '[bootstrap] ОШИБКА: %s\n' "$*" >&2; exit 1; }
run() {
  if [[ -n "$DRY_RUN" ]]; then log "(dry-run) $*"; else "$@"; fi
}
# write_file <путь> <права>: содержимое со stdin
write_file() {
  if [[ -n "$DRY_RUN" ]]; then
    log "(dry-run) записал бы $1 (права $2):"; sed 's/^/    /'; return
  fi
  install -d -m 755 "$(dirname "$1")"
  cat > "$1"; chmod "$2" "$1"
}

[[ "$ADMIN_USER" =~ ^[a-z][a-z0-9_-]{1,30}$ ]] || die "недопустимое имя ADMIN_USER: $ADMIN_USER"
[[ "$ADMIN_USER" != root ]] || die "ADMIN_USER не может быть root"
[[ "$SWAP_MB" =~ ^[0-9]+$ ]] || die "SWAP_MB должно быть числом"
[[ "$CADDY_REPO" == distro || "$CADDY_REPO" == official ]] || die "CADDY_REPO: distro или official"

PUBKEY=""
if [[ -n "${SSH_PUBKEY:-}" ]]; then PUBKEY="$SSH_PUBKEY"
elif [[ -n "${SSH_PUBKEY_FILE:-}" ]]; then
  [[ -r "$SSH_PUBKEY_FILE" ]] || die "не читается SSH_PUBKEY_FILE: $SSH_PUBKEY_FILE"
  PUBKEY="$(head -n1 "$SSH_PUBKEY_FILE")"
fi
[[ -n "$PUBKEY" ]] || die "нужен публичный ключ: SSH_PUBKEY_FILE=путь или SSH_PUBKEY='ssh-ed25519 AAAA…'"
case "$PUBKEY" in ssh-ed25519\ *|ssh-rsa\ *|ecdsa-sha2-*) ;; *) die "это не публичный ключ (ждём ssh-ed25519/ssh-rsa/ecdsa): не подсовывайте приватный" ;; esac
if command -v ssh-keygen >/dev/null 2>&1; then
  printf '%s\n' "$PUBKEY" | ssh-keygen -l -f /dev/stdin >/dev/null 2>&1 || die "ssh-keygen не разобрал публичный ключ"
fi

if [[ -z "$DRY_RUN" ]]; then
  [[ $EUID -eq 0 ]] || die "запускать от root (или с DRY_RUN=1)"
  [[ -r /etc/os-release ]] || die "нет /etc/os-release"
  . /etc/os-release
  case "${ID:-}" in debian|ubuntu) ;; *) die "проверено только на Debian/Ubuntu, здесь: ${ID:-?}" ;; esac
  export DEBIAN_FRONTEND=noninteractive
fi

log "1/8 пакеты"
run apt-get update -y
run apt-get install -y --no-install-recommends ca-certificates curl gnupg sudo ufw fail2ban unattended-upgrades openssh-server

log "2/8 администратор $ADMIN_USER с ключом и sudo"
if [[ -n "$DRY_RUN" ]] || ! id "$ADMIN_USER" >/dev/null 2>&1; then
  run adduser --disabled-password --gecos "" "$ADMIN_USER"
fi
if [[ -z "$DRY_RUN" ]]; then
  home="$(getent passwd "$ADMIN_USER" | cut -d: -f6)"
  install -d -m 700 -o "$ADMIN_USER" -g "$ADMIN_USER" "$home/.ssh"
  touch "$home/.ssh/authorized_keys"
  grep -qxF "$PUBKEY" "$home/.ssh/authorized_keys" || printf '%s\n' "$PUBKEY" >> "$home/.ssh/authorized_keys"
  chown "$ADMIN_USER:$ADMIN_USER" "$home/.ssh/authorized_keys"; chmod 600 "$home/.ssh/authorized_keys"
  [[ -s "$home/.ssh/authorized_keys" ]] || die "ключ не записался в $home/.ssh/authorized_keys"
else
  log "(dry-run) положил бы ключ в ~$ADMIN_USER/.ssh/authorized_keys"
fi
write_file "/etc/sudoers.d/90-$ADMIN_USER" 440 <<SUDO
$ADMIN_USER ALL=(ALL) NOPASSWD:ALL
SUDO
[[ -n "$DRY_RUN" ]] || visudo -cf "/etc/sudoers.d/90-$ADMIN_USER" >/dev/null || { rm -f "/etc/sudoers.d/90-$ADMIN_USER"; die "sudoers не прошёл visudo -c"; }

log "3/8 файрвол ufw: 22, 80, 443/tcp (UDP 443 закрыт: HTTP/3 не используем)"
run ufw default deny incoming
run ufw default allow outgoing
run ufw allow 22/tcp
run ufw allow 80/tcp
run ufw allow 443/tcp
run ufw --force enable

log "4/8 fail2ban (sshd, журнал systemd)"
write_file /etc/fail2ban/jail.d/sshd.local 644 <<'F2B'
[sshd]
enabled = true
backend = systemd
maxretry = 5
findtime = 10m
bantime = 1h
F2B
run systemctl enable --now fail2ban
run systemctl restart fail2ban

log "5/8 автоматические обновления безопасности"
write_file /etc/apt/apt.conf.d/20auto-upgrades 644 <<'AU'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
AU

log "6/8 журнал не больше 200 МБ, swap на маленьких машинах"
write_file /etc/systemd/journald.conf.d/mase.conf 644 <<'JD'
[Journal]
SystemMaxUse=200M
JD
run systemctl restart systemd-journald
mem_mb=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
if [[ "$mem_mb" -lt 1500 ]] && ! swapon --show --noheadings 2>/dev/null | grep -q .; then
  log "RAM ${mem_mb} МБ и swap нет: создаю /swapfile на ${SWAP_MB} МБ"
  run fallocate -l "${SWAP_MB}M" /swapfile
  run chmod 600 /swapfile
  run mkswap /swapfile
  run swapon /swapfile
  if [[ -n "$DRY_RUN" ]]; then log "(dry-run) дописал бы /swapfile в /etc/fstab"
  elif ! grep -q '^/swapfile' /etc/fstab; then echo '/swapfile none swap sw 0 0' >> /etc/fstab; fi
else
  log "RAM ${mem_mb} МБ, swap уже есть или не нужен: пропускаю"
fi

log "7/8 Caddy ($CADDY_REPO)"
if [[ "$CADDY_REPO" == official ]]; then
  run apt-get install -y debian-keyring debian-archive-keyring apt-transport-https
  if [[ -z "$DRY_RUN" ]]; then
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -y
  else log "(dry-run) добавил бы репозиторий Caddy (cloudsmith)"; fi
else
  if [[ -z "$DRY_RUN" ]] && ! apt-cache policy caddy | grep -q 'Candidate: [0-9]'; then
    die "пакета caddy нет в репозитории этой системы; повторите с CADDY_REPO=official"
  fi
fi
run apt-get install -y caddy
run systemctl enable caddy

log "8/8 SSH: только по ключам (последним шагом)"
if [[ -z "$DRY_RUN" ]]; then
  grep -Eq '^\s*Include\s+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config \
    || die "sshd_config не подключает /etc/ssh/sshd_config.d: настройте вручную, доступ не менял"
fi
write_file /etc/ssh/sshd_config.d/00-mase-hardening.conf 644 <<'SSHD'
# Первое значение побеждает: этот файл идёт раньше 50-cloud-init.conf.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
PubkeyAuthentication yes
SSHD
if [[ -z "$DRY_RUN" ]]; then
  if sshd -t; then
    systemctl reload ssh 2>/dev/null || systemctl reload sshd
  else
    rm -f /etc/ssh/sshd_config.d/00-mase-hardening.conf
    die "sshd -t не прошёл, настройку SSH откатил"
  fi
fi

log "готово"
cat <<EOM

СЕЙЧАС, не закрывая эту сессию, откройте второе окно и проверьте:
    ssh $ADMIN_USER@<IP-сервера>
    sudo -n true && echo sudo-ok
Только после этого закрывайте root-сессию. Дальше: ops/probe/install-probe.sh.
EOM

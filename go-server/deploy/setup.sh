#!/usr/bin/env bash
# One-time server setup script for Oracle Cloud Ubuntu ARM
# Run as ubuntu user: bash setup.sh YOUR_DOMAIN
set -euo pipefail

DOMAIN="${1:-mase.duckdns.org}"
echo "=== Mase Server Setup for $DOMAIN ==="

# ── 1. System packages ─────────────────────────────────────────────────────────
sudo apt-get update -q
sudo apt-get install -y nginx certbot python3-certbot-nginx gcc

# ── 2. Go (for building on server) ────────────────────────────────────────────
if ! command -v go &>/dev/null; then
  GO_VERSION="1.23.4"
  ARCH=$(dpkg --print-architecture)
  if [ "$ARCH" = "arm64" ]; then
    GOARCH="arm64"
  else
    GOARCH="amd64"
  fi
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" \
    | sudo tar -C /usr/local -xz
  echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
  export PATH=$PATH:/usr/local/go/bin
  echo "Go $(go version) installed"
fi

# ── 3. Build binary ────────────────────────────────────────────────────────────
sudo mkdir -p /opt/mase/media
cd /opt/mase
if [ -d "go-server" ]; then
  cd go-server
  go build -buildvcs=false -o /opt/mase/mase-server ./cmd/server/
  echo "Binary built"
fi

# ── 4. Nginx ───────────────────────────────────────────────────────────────────
sudo cp /opt/mase/go-server/deploy/nginx.conf /etc/nginx/sites-available/mase
sudo sed -i "s/mase.duckdns.org/$DOMAIN/g" /etc/nginx/sites-available/mase
sudo ln -sf /etc/nginx/sites-available/mase /etc/nginx/sites-enabled/mase
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx

# ── 5. Let's Encrypt ───────────────────────────────────────────────────────────
sudo certbot --nginx -d "$DOMAIN" --non-interactive --agree-tos -m admin@example.com
sudo systemctl reload nginx

# ── 6. Systemd service ────────────────────────────────────────────────────────
sudo cp /opt/mase/go-server/deploy/mase.service /etc/systemd/system/mase.service
sudo sed -i "s/ubuntu/$(whoami)/g" /etc/systemd/system/mase.service
sudo systemctl daemon-reload
sudo systemctl enable mase
sudo systemctl start mase

echo ""
echo "=== Setup complete ==="
echo "Server: https://$DOMAIN/health"
echo "WebSocket: wss://$DOMAIN/ws"
echo "Logs: sudo journalctl -fu mase"

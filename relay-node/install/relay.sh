#!/bin/bash
set -euo pipefail

# Valhalla Relay Node Install Script

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[-]${NC} $1"; exit 1; }

[[ $EUID -ne 0 ]] && error "This script must be run as root"

# Install Go if needed
GO_VERSION="1.22.5"
if ! command -v go &>/dev/null; then
    log "Installing Go ${GO_VERSION}..."
    apt-get update -qq
    apt-get install -y -qq wget
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    export PATH=$PATH:/usr/local/go/bin
fi

PUBLIC_IP=$(curl -s ifconfig.me || curl -s icanhazip.com || echo "")

# Build binary
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
log "Building relay node from ${SCRIPT_DIR}..."
cd "$SCRIPT_DIR"
/usr/local/go/bin/go build -o /usr/local/bin/valhalla-relay .
chmod +x /usr/local/bin/valhalla-relay
log "Binary built: /usr/local/bin/valhalla-relay"

# Prompt for control plane URL
read -p "Control Plane URL [http://localhost:8443]: " CONTROL_PLANE_URL
CONTROL_PLANE_URL=${CONTROL_PLANE_URL:-http://localhost:8443}

read -p "Max relay sessions [1000]: " CAPACITY
CAPACITY=${CAPACITY:-1000}

# Create systemd unit
cat > /etc/systemd/system/valhalla-relay.service << EOF
[Unit]
Description=Valhalla Relay Node
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/valhalla-relay
Restart=always
RestartSec=5

Environment=LISTEN_ADDR=:51821
Environment=TCP_LISTEN_ADDR=:51822
Environment=VLESS_LISTEN_ADDR=:443
Environment=CONTROL_PLANE_URL=${CONTROL_PLANE_URL}
Environment=PUBLIC_ADDRESS=${PUBLIC_IP}
Environment=CAPACITY=${CAPACITY}

StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-relay

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable valhalla-relay
systemctl start valhalla-relay

# Firewall
if command -v ufw &>/dev/null; then
    ufw allow 51821/udp
    ufw allow 51822/tcp
    ufw allow 443/tcp
    log "Firewall: ports 51821/udp, 51822/tcp, 443/tcp allowed"
fi

log "=================================="
log "Valhalla Relay Node installed!"
log "Service: systemctl status valhalla-relay"
log "Logs:    journalctl -u valhalla-relay -f"
log "UDP:     ${PUBLIC_IP}:51821"
log "TCP:     ${PUBLIC_IP}:51822"
log "=================================="

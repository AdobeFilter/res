#!/bin/bash
set -euo pipefail

# Valhalla STUN Server Install Script

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

# Detect public IP
PUBLIC_IP=$(curl -s ifconfig.me || curl -s icanhazip.com || echo "")
if [ -z "$PUBLIC_IP" ]; then
    warn "Could not detect public IP. Set PUBLIC_ADDRESS manually."
fi

# Build binary
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
log "Building STUN server from ${SCRIPT_DIR}..."
cd "$SCRIPT_DIR"
/usr/local/go/bin/go build -o /usr/local/bin/valhalla-stun .
chmod +x /usr/local/bin/valhalla-stun
log "Binary built: /usr/local/bin/valhalla-stun"

# Prompt for control plane URL
read -p "Control Plane URL [http://localhost:8443]: " CONTROL_PLANE_URL
CONTROL_PLANE_URL=${CONTROL_PLANE_URL:-http://localhost:8443}

# Create systemd unit
cat > /etc/systemd/system/valhalla-stun.service << EOF
[Unit]
Description=Valhalla STUN Server
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/valhalla-stun
Restart=always
RestartSec=5

Environment=LISTEN_ADDR=:3478
Environment=ALT_LISTEN_ADDR=:3479
Environment=CONTROL_PLANE_URL=${CONTROL_PLANE_URL}
Environment=PUBLIC_ADDRESS=${PUBLIC_IP}

StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-stun

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable valhalla-stun
systemctl start valhalla-stun

# Firewall
if command -v ufw &>/dev/null; then
    ufw allow 3478/udp
    ufw allow 3479/udp
    log "Firewall: ports 3478-3479/udp allowed"
fi

log "=================================="
log "Valhalla STUN Server installed!"
log "Service: systemctl status valhalla-stun"
log "Logs:    journalctl -u valhalla-stun -f"
log "Public:  ${PUBLIC_IP}:3478"
log "=================================="

#!/bin/bash
set -euo pipefail

# Valhalla Exit Node Install Script

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[-]${NC} $1"; exit 1; }

[[ $EUID -ne 0 ]] && error "This script must be run as root"

# Install dependencies
log "Installing dependencies..."
apt-get update -qq
apt-get install -y -qq wget wireguard-tools iptables

# Install Go if needed
GO_VERSION="1.22.5"
if ! command -v go &>/dev/null; then
    log "Installing Go ${GO_VERSION}..."
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    export PATH=$PATH:/usr/local/go/bin
fi

# Build binary
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
log "Building exit node from ${SCRIPT_DIR}..."
cd "$SCRIPT_DIR"
/usr/local/go/bin/go build -o /usr/local/bin/valhalla-node .
chmod +x /usr/local/bin/valhalla-node
log "Binary built: /usr/local/bin/valhalla-node"

# Create config directory
mkdir -p /etc/valhalla

# Prompt for control plane URL
read -p "Control Plane URL [http://localhost:8443]: " CONTROL_PLANE_URL
CONTROL_PLANE_URL=${CONTROL_PLANE_URL:-http://localhost:8443}

# Enable IP forwarding
log "Enabling IP forwarding..."
sysctl -w net.ipv4.ip_forward=1
echo "net.ipv4.ip_forward=1" >> /etc/sysctl.d/99-valhalla.conf

# Create systemd unit
cat > /etc/systemd/system/valhalla-node.service << EOF
[Unit]
Description=Valhalla Exit Node
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/valhalla-node
Restart=always
RestartSec=5

Environment=CONTROL_PLANE_URL=${CONTROL_PLANE_URL}
Environment=WG_IFACE=wg0
Environment=TOKEN_FILE=/etc/valhalla/token
Environment=CONFIG_FILE=/etc/valhalla/node.yaml

StandardInput=tty-force
StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-node

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable valhalla-node

# Firewall
if command -v ufw &>/dev/null; then
    ufw allow 51820/udp
    log "Firewall: port 51820/udp allowed"
fi

log "=================================="
log "Valhalla Exit Node installed!"
log ""
log "First run (interactive login):"
log "  /usr/local/bin/valhalla-node"
log ""
log "After login, start as service:"
log "  systemctl start valhalla-node"
log ""
log "Service: systemctl status valhalla-node"
log "Logs:    journalctl -u valhalla-node -f"
log "=================================="

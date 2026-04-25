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

# Install Xray (needed for VLESS+Reality subprocess)
if ! command -v xray &>/dev/null; then
    log "Installing Xray..."
    apt-get install -y -qq curl unzip
    bash -c "$(curl -L https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
    log "Xray installed: $(xray version | head -1)"
    # The installer also creates a systemd unit that we don't want — our
    # relay-node spawns xray with its own config, not the system one.
    systemctl disable --now xray 2>/dev/null || true
fi

# Force IPv4 — clients behind IPv4-only NAT (typical RU home ISPs) can't
# reach an IPv6 address, so a v4 PUBLIC_ADDRESS is what we want regardless
# of whether the relay also has an IPv6 interface.
PUBLIC_IP=$(curl -s -4 ifconfig.me || curl -s -4 icanhazip.com || curl -s -4 api.ipify.org || echo "")
if [[ -z "$PUBLIC_IP" ]]; then
    warn "Could not auto-detect IPv4 public address — relay will register without one."
    warn "Set PUBLIC_ADDRESS in /etc/systemd/system/valhalla-relay.service manually."
fi

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
Environment=XRAY_BINARY=/usr/local/bin/xray
Environment=CONTROL_PLANE_URL=${CONTROL_PLANE_URL}
Environment=PUBLIC_ADDRESS=${PUBLIC_IP}
Environment=CAPACITY=${CAPACITY}

# Let the relay bind to privileged port 443 without running the whole
# process as root. Requires CAP_NET_BIND_SERVICE on the binary; we give
# it below via setcap.

StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-relay

[Install]
WantedBy=multi-user.target
EOF

# Give the binaries privilege to bind 443 without full root.
setcap 'cap_net_bind_service=+ep' /usr/local/bin/valhalla-relay || true
setcap 'cap_net_bind_service=+ep' /usr/local/bin/xray             || true

systemctl daemon-reload
systemctl enable valhalla-relay
systemctl restart valhalla-relay

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

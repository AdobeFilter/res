#!/bin/bash
set -euo pipefail

# Valhalla Relay Node Install Script
# Installs everything under /opt/relay, sets up systemd, removes source tree on completion.
# Expected workflow:
#   mkdir -p /opt/relay && cd /opt/relay
#   git clone https://github.com/AdobeFilter/res.git
#   cd res/relay-node/install
#   bash relay.sh

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[-]${NC} $1"; exit 1; }

RELAY_ROOT=/opt/relay
RELAY_BIN="${RELAY_ROOT}/bin"
RELAY_ETC="${RELAY_ROOT}/etc"

[[ $EUID -ne 0 ]] && error "This script must be run as root"

if ! grep -qi 'debian\|ubuntu' /etc/os-release 2>/dev/null; then
    warn "This script is designed for Debian/Ubuntu. Proceeding anyway..."
fi

# Locate Go source root (script lives in <repo>/relay-node/install/)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
[[ -f "${SRC_DIR}/main.go" ]] || error "main.go not found in ${SRC_DIR}"

log "Installing dependencies..."
apt-get update -qq
apt-get install -y -qq curl wget git build-essential

# Go 1.22+
GO_VERSION="1.22.5"
if ! command -v go &>/dev/null || [[ $(go version 2>/dev/null | grep -oP 'go\K[0-9]+\.[0-9]+') < "1.22" ]]; then
    log "Installing Go ${GO_VERSION}..."
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/golang.sh
    export PATH=$PATH:/usr/local/go/bin
    log "Go installed: $(/usr/local/go/bin/go version)"
else
    log "Go already installed: $(go version)"
fi

# No Xray: the relay forwards WG ciphertext only (DERP-style). Clients reach
# the mesh dispatcher directly through their exit-node, so the relay needs no
# VLESS/Reality and no xray subprocess.

# System user + directories
if ! id relay &>/dev/null; then
    useradd -r -s /bin/false relay
    log "System user 'relay' created"
fi
mkdir -p "${RELAY_BIN}" "${RELAY_ETC}"
chown relay:relay "${RELAY_ROOT}" "${RELAY_BIN}" "${RELAY_ETC}"
chmod 750 "${RELAY_ETC}"

# Force IPv4 — clients behind IPv4-only NAT (typical RU home ISPs) can't
# reach an IPv6 address, so a v4 PUBLIC_ADDRESS is what we want regardless
# of whether the relay also has an IPv6 interface.
PUBLIC_IP=$(curl -s -4 ifconfig.me || curl -s -4 icanhazip.com || curl -s -4 api.ipify.org || echo "")
if [[ -z "$PUBLIC_IP" ]]; then
    warn "Could not auto-detect IPv4 public address — relay will register without one."
    warn "Set PUBLIC_ADDRESS in ${RELAY_ETC}/relay.env manually."
fi

# Env file (preserved across re-installs, same pattern as control_plane.sh)
ENV_FILE="${RELAY_ETC}/relay.env"
if [[ ! -f "${ENV_FILE}" ]]; then
    echo
    log "Configuring relay (will be saved to ${ENV_FILE})"

    read -p "Control Plane URL [http://localhost:8443]: " CONTROL_PLANE_URL
    CONTROL_PLANE_URL=${CONTROL_PLANE_URL:-http://localhost:8443}

    read -p "Max relay sessions [1000]: " CAPACITY
    CAPACITY=${CAPACITY:-1000}

    read -p "Mesh dispatcher listen port [9999]: " MESH_PORT
    MESH_PORT=${MESH_PORT:-9999}

    read -p "Mesh auth key (shared with control-plane; empty = no token enforcement): " MESH_AUTH_KEY

    umask 077
    cat > "${ENV_FILE}" <<ENV
LISTEN_ADDR=:51821
TCP_LISTEN_ADDR=:51822
MESH_LISTEN_ADDR=:${MESH_PORT}
MESH_AUTH_KEY=${MESH_AUTH_KEY}
CONTROL_PLANE_URL=${CONTROL_PLANE_URL}
PUBLIC_ADDRESS=${PUBLIC_IP}
CAPACITY=${CAPACITY}
ENV
    chown relay:relay "${ENV_FILE}"
    chmod 600 "${ENV_FILE}"
    log "Env file written"
else
    log "Existing ${ENV_FILE} kept"
fi

# Re-read mesh port from env so the firewall rule matches whatever is in use.
MESH_PORT=$(grep -oP 'MESH_LISTEN_ADDR=:\K[0-9]+' "${ENV_FILE}" || echo "9999")

# Build binary
log "Building relay node from ${SRC_DIR}..."
cd "${SRC_DIR}"
/usr/local/go/bin/go build -buildvcs=false -o "${RELAY_BIN}/valhalla-relay" .
chmod 755 "${RELAY_BIN}/valhalla-relay"
chown relay:relay "${RELAY_BIN}/valhalla-relay"
log "Binary: ${RELAY_BIN}/valhalla-relay"

# Allow binding privileged ports without running as root.
setcap 'cap_net_bind_service=+ep' "${RELAY_BIN}/valhalla-relay" || true

# systemd unit
cat > /etc/systemd/system/valhalla-relay.service << EOF
[Unit]
Description=Valhalla Relay Node
After=network.target

[Service]
Type=simple
User=relay
Group=relay
EnvironmentFile=${ENV_FILE}
ExecStart=${RELAY_BIN}/valhalla-relay
Restart=always
RestartSec=5

# Allow the non-root user to bind privileged ports.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-relay

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable valhalla-relay
systemctl restart valhalla-relay

# Firewall
if command -v ufw &>/dev/null; then
    ufw allow 51821/udp >/dev/null
    ufw allow 51822/tcp >/dev/null
    ufw allow "${MESH_PORT}/tcp" >/dev/null
    log "Firewall: 51821/udp, 51822/tcp, ${MESH_PORT}/tcp allowed"
fi

# Clean up source tree (the user clones into /opt/relay/res)
if [[ -d /opt/relay/res && "${SRC_DIR}" == /opt/relay/res/* ]]; then
    log "Removing source tree /opt/relay/res"
    cd /
    rm -rf /opt/relay/res
fi

log "=================================="
log "Valhalla Relay Node installed!"
log "Service:  systemctl status valhalla-relay"
log "Logs:     journalctl -u valhalla-relay -f"
log "Env:      ${ENV_FILE}"
log "UDP:      ${PUBLIC_IP}:51821"
log "TCP:      ${PUBLIC_IP}:51822"
log "Mesh:     ${PUBLIC_IP}:${MESH_PORT}"
log "=================================="

#!/bin/bash
set -euo pipefail

# Valhalla Control Plane Install Script
# Installs Go, PostgreSQL, builds the control-plane binary, configures systemd

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[-]${NC} $1"; exit 1; }

# Check root
[[ $EUID -ne 0 ]] && error "This script must be run as root"

# Detect OS
if ! grep -qi 'debian\|ubuntu' /etc/os-release 2>/dev/null; then
    warn "This script is designed for Debian/Ubuntu. Proceeding anyway..."
fi

log "Installing dependencies..."
apt-get update -qq
apt-get install -y -qq curl wget git build-essential

# Install Go 1.22+
GO_VERSION="1.22.5"
if ! command -v go &>/dev/null || [[ $(go version | grep -oP 'go\K[0-9]+\.[0-9]+') < "1.22" ]]; then
    log "Installing Go ${GO_VERSION}..."
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/golang.sh
    export PATH=$PATH:/usr/local/go/bin
    log "Go $(go version) installed"
else
    log "Go already installed: $(go version)"
fi

# Install PostgreSQL 16
if ! command -v psql &>/dev/null; then
    log "Installing PostgreSQL 16..."
    apt-get install -y -qq postgresql postgresql-contrib
    systemctl enable postgresql
    systemctl start postgresql
    log "PostgreSQL installed and started"
else
    log "PostgreSQL already installed"
fi

# Create database and user
log "Setting up database..."
sudo -u postgres psql -c "CREATE USER valhalla WITH PASSWORD 'valhalla';" 2>/dev/null || true
sudo -u postgres psql -c "CREATE DATABASE valhalla OWNER valhalla;" 2>/dev/null || true
sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE valhalla TO valhalla;" 2>/dev/null || true
log "Database 'valhalla' ready"

# Create system user
if ! id valhalla &>/dev/null; then
    useradd -r -s /bin/false valhalla
    log "System user 'valhalla' created"
fi

# Create directories
mkdir -p /etc/valhalla /var/log/valhalla
chown valhalla:valhalla /var/log/valhalla

# Build binary
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
log "Building control-plane from ${SCRIPT_DIR}..."
cd "$SCRIPT_DIR"
/usr/local/go/bin/go build -o /usr/local/bin/valhalla-control .
chmod +x /usr/local/bin/valhalla-control
log "Binary built: /usr/local/bin/valhalla-control"

# Generate JWT secret if not exists
if [ ! -f /etc/valhalla/jwt-secret ]; then
    openssl rand -hex 32 > /etc/valhalla/jwt-secret
    chmod 600 /etc/valhalla/jwt-secret
    chown valhalla:valhalla /etc/valhalla/jwt-secret
    log "JWT secret generated"
fi

JWT_SECRET=$(cat /etc/valhalla/jwt-secret)

# deSEC DNS for auto-domain on exit nodes (optional). Saved to a separate
# env file so secrets stay out of the unit (which is world-readable) and
# can be edited without re-running the installer.
read -p "deSEC DNS API token (leave blank to skip auto-domain): " DNS_API_TOKEN
DNS_DOMAIN=""
if [ -n "$DNS_API_TOKEN" ]; then
    read -p "deSEC DNS domain (e.g. yourname.dedyn.io): " DNS_DOMAIN
fi
cat > /etc/valhalla/control-plane.env <<ENV
DNS_API_TOKEN=${DNS_API_TOKEN}
DNS_DOMAIN=${DNS_DOMAIN}
ENV
chown valhalla:valhalla /etc/valhalla/control-plane.env
chmod 600 /etc/valhalla/control-plane.env

# Create systemd unit
cat > /etc/systemd/system/valhalla-control.service << EOF
[Unit]
Description=Valhalla Control Plane
After=network.target postgresql.service
Requires=postgresql.service

[Service]
Type=simple
User=valhalla
Group=valhalla
ExecStart=/usr/local/bin/valhalla-control
Restart=always
RestartSec=5

EnvironmentFile=/etc/valhalla/control-plane.env
Environment=LISTEN_ADDR=:8443
Environment=DATABASE_URL=postgres://valhalla:valhalla@localhost:5432/valhalla?sslmode=disable
Environment=JWT_SECRET=${JWT_SECRET}
Environment=MESH_CIDR=10.100.0.0/16

StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-control

[Install]
WantedBy=multi-user.target
EOF

# Enable and start
systemctl daemon-reload
systemctl enable valhalla-control
systemctl start valhalla-control

# Firewall
if command -v ufw &>/dev/null; then
    ufw allow 8443/tcp
    log "Firewall: port 8443/tcp allowed"
fi

log "=================================="
log "Valhalla Control Plane installed!"
log "Service: systemctl status valhalla-control"
log "Logs:    journalctl -u valhalla-control -f"
log "API:     http://$(hostname -I | awk '{print $1}'):8443"
log "=================================="

#!/bin/bash
set -euo pipefail

# Valhalla Control Plane Install Script
# Installs everything under /opt/valhalla, sets up systemd, removes source tree on completion.
# Expected workflow:
#   mkdir -p /opt/valhalla && cd /opt/valhalla
#   git clone https://github.com/AdobeFilter/res.git
#   cd res/control-plane/install
#   bash control_plane.sh

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[-]${NC} $1"; exit 1; }

VALHALLA_ROOT=/opt/valhalla
VALHALLA_BIN="${VALHALLA_ROOT}/bin"
VALHALLA_ETC="${VALHALLA_ROOT}/etc"
VALHALLA_BACKUPS="${VALHALLA_ROOT}/backups"

[[ $EUID -ne 0 ]] && error "This script must be run as root"

if ! grep -qi 'debian\|ubuntu' /etc/os-release 2>/dev/null; then
    warn "This script is designed for Debian/Ubuntu. Proceeding anyway..."
fi

# Locate Go source root (script lives in <repo>/control-plane/install/)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
[[ -f "${SRC_DIR}/main.go" ]] || error "main.go not found in ${SRC_DIR}"

log "Installing dependencies..."
apt-get update -qq
apt-get install -y -qq curl wget git build-essential gnupg openssl

# Go 1.22+
GO_VERSION="1.22.5"
if ! command -v go &>/dev/null || [[ $(go version | grep -oP 'go\K[0-9]+\.[0-9]+') < "1.22" ]]; then
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

# PostgreSQL 16
if ! command -v psql &>/dev/null; then
    log "Installing PostgreSQL..."
    apt-get install -y -qq postgresql postgresql-contrib
    systemctl enable postgresql
    systemctl start postgresql
fi

# System user + directories
if ! id valhalla &>/dev/null; then
    useradd -r -s /bin/false valhalla
    log "System user 'valhalla' created"
fi
mkdir -p "${VALHALLA_BIN}" "${VALHALLA_ETC}" "${VALHALLA_BACKUPS}"
chown valhalla:valhalla "${VALHALLA_ROOT}" "${VALHALLA_BIN}" "${VALHALLA_ETC}" "${VALHALLA_BACKUPS}"
chmod 750 "${VALHALLA_ETC}" "${VALHALLA_BACKUPS}"

# JWT secret (preserved across re-installs)
JWT_FILE="${VALHALLA_ETC}/jwt-secret"
if [[ ! -f "${JWT_FILE}" ]]; then
    openssl rand -hex 32 > "${JWT_FILE}"
    chmod 600 "${JWT_FILE}"
    chown valhalla:valhalla "${JWT_FILE}"
    log "JWT secret generated"
else
    log "Existing JWT secret kept"
fi
JWT_SECRET=$(cat "${JWT_FILE}")

# Env file with secrets (preserved across re-installs)
ENV_FILE="${VALHALLA_ETC}/control-plane.env"
if [[ ! -f "${ENV_FILE}" ]]; then
    echo
    log "Configuring secrets (will be saved to ${ENV_FILE})"

    # DB password: prompt; empty -> random 32-hex
    read -s -p "PostgreSQL password for user 'valhalla' (Enter to auto-generate): " DB_PW
    echo
    if [[ -z "${DB_PW}" ]]; then
        DB_PW=$(openssl rand -hex 16)
        warn "Generated DB password: ${DB_PW}"
        warn "WRITE IT DOWN — it will not be shown again."
    fi

    # Apply password to PG (create or alter)
    if sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='valhalla'" | grep -q 1; then
        sudo -u postgres psql -c "ALTER USER valhalla WITH PASSWORD '${DB_PW}';" >/dev/null
    else
        sudo -u postgres psql -c "CREATE USER valhalla WITH PASSWORD '${DB_PW}';" >/dev/null
    fi
    sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='valhalla'" | grep -q 1 \
        || sudo -u postgres psql -c "CREATE DATABASE valhalla OWNER valhalla;" >/dev/null
    sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE valhalla TO valhalla;" >/dev/null

    # Backup encryption passphrase
    echo
    log "Backups are encrypted with a passphrase you choose."
    log "WRITE IT DOWN — without it, backups cannot be restored on another server."
    read -s -p "Backup encryption passphrase (Enter to auto-generate): " BACKUP_PASS
    echo
    if [[ -z "${BACKUP_PASS}" ]]; then
        BACKUP_PASS=$(openssl rand -base64 24)
        warn "Generated backup passphrase: ${BACKUP_PASS}"
        warn "WRITE IT DOWN — it will not be shown again."
    fi
    echo -n "${BACKUP_PASS}" > "${VALHALLA_ETC}/backup-passphrase"
    chmod 600 "${VALHALLA_ETC}/backup-passphrase"
    chown valhalla:valhalla "${VALHALLA_ETC}/backup-passphrase"

    umask 077
    cat > "${ENV_FILE}" <<ENV
LISTEN_ADDR=:8443
DATABASE_URL=postgres://valhalla:${DB_PW}@localhost:5432/valhalla?sslmode=disable
JWT_SECRET=${JWT_SECRET}
MESH_CIDR=10.100.0.0/16
# Antifraud: when true, a device_id linked to another account is rejected.
# Disabled by default so dogfooding doesn't lock the dev's own phone out
# while switching test accounts. Flip to true (and apply migration 010)
# before public launch.
ANTIFRAUD_ENABLED=false
# Relay allowlist: path to a newline-separated list of IPs allowed to
# self-register via POST /api/v1/internal/relay/register. Empty disables
# the check. Empty file = no relay may register (safe default).
ALLOWED_RELAYS_FILE=${VALHALLA_ETC}/allowed-relays.txt
ENV
    chown valhalla:valhalla "${ENV_FILE}"
    chmod 600 "${ENV_FILE}"
    log "Env file written"
else
    log "Existing ${ENV_FILE} kept — DB password and backup passphrase preserved"
fi

# Relay allowlist file — operator-managed. Preserved across re-installs so
# manual additions are not wiped. The control-plane treats a missing file as
# empty (no relay may register), so the placeholder we drop here is safe.
ALLOWLIST_FILE="${VALHALLA_ETC}/allowed-relays.txt"
if [[ ! -f "${ALLOWLIST_FILE}" ]]; then
    cat > "${ALLOWLIST_FILE}" <<'ALLOW'
# Valhalla relay allowlist
# One IPv4 per line. Lines starting with # and blank lines are ignored.
# Must match BOTH the address declared by the relay AND its TCP source IP.
# Empty file = no relay may register.
#
# Example:
# 203.0.113.42
# 198.51.100.7
ALLOW
    chown valhalla:valhalla "${ALLOWLIST_FILE}"
    chmod 600 "${ALLOWLIST_FILE}"
    log "Relay allowlist created at ${ALLOWLIST_FILE} (empty — add IPs before relays come online)"
else
    log "Existing relay allowlist kept at ${ALLOWLIST_FILE}"
fi

# Build binary
log "Building control-plane from ${SRC_DIR}..."
cd "${SRC_DIR}"
/usr/local/go/bin/go build -buildvcs=false -o "${VALHALLA_BIN}/valhalla-control" .
chmod 755 "${VALHALLA_BIN}/valhalla-control"
chown valhalla:valhalla "${VALHALLA_BIN}/valhalla-control"
log "Binary: ${VALHALLA_BIN}/valhalla-control"

# Install backup/restore scripts
install -m 750 -o valhalla -g valhalla "${SCRIPT_DIR}/backup.sh" "${VALHALLA_BIN}/backup.sh"
install -m 750 -o root -g root "${SCRIPT_DIR}/restore.sh" "${VALHALLA_BIN}/restore.sh"
log "backup.sh and restore.sh installed in ${VALHALLA_BIN}"

# systemd unit for control-plane
cat > /etc/systemd/system/valhalla-control.service <<EOF
[Unit]
Description=Valhalla Control Plane
After=network.target postgresql.service
Requires=postgresql.service

[Service]
Type=simple
User=valhalla
Group=valhalla
EnvironmentFile=${ENV_FILE}
ExecStart=${VALHALLA_BIN}/valhalla-control
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-control

[Install]
WantedBy=multi-user.target
EOF

# systemd unit + timer for daily backup
cat > /etc/systemd/system/valhalla-backup.service <<EOF
[Unit]
Description=Valhalla Control Plane Backup
After=postgresql.service valhalla-control.service

[Service]
Type=oneshot
User=valhalla
Group=valhalla
ExecStart=${VALHALLA_BIN}/backup.sh
EOF

cat > /etc/systemd/system/valhalla-backup.timer <<EOF
[Unit]
Description=Daily Valhalla Backup

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=15min

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
systemctl enable valhalla-control valhalla-backup.timer
systemctl restart valhalla-control
systemctl start valhalla-backup.timer

# Firewall
if command -v ufw &>/dev/null; then
    ufw allow 8443/tcp >/dev/null
    log "Firewall: 8443/tcp allowed"
fi

# Clean up source tree (the user clones into /opt/valhalla/res)
if [[ -d /opt/valhalla/res && "${SRC_DIR}" == /opt/valhalla/res/* ]]; then
    log "Removing source tree /opt/valhalla/res"
    cd /
    rm -rf /opt/valhalla/res
fi

log "=================================="
log "Valhalla Control Plane installed!"
log "Service:  systemctl status valhalla-control"
log "Logs:     journalctl -u valhalla-control -f"
log "Backups:  ${VALHALLA_BACKUPS} (daily timer enabled)"
log "Manual:   ${VALHALLA_BIN}/backup.sh"
log "Restore:  sudo ${VALHALLA_BIN}/restore.sh <backup.tar.gz.gpg>"
log "API:      http://$(hostname -I | awk '{print $1}'):8443"
log "=================================="

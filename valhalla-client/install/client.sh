#!/bin/bash
set -euo pipefail

# Valhalla Client Install Script
# Installs Go + xray, clones the repo to /opt/valhalla, builds the
# valhalla-client binary into /usr/local/bin. Idempotent.
#
# One-liner:
#   curl -fsSL https://raw.githubusercontent.com/AdobeFilter/res/main/valhalla-client/install/client.sh | sudo bash

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

log()   { echo -e "${GREEN}[+]${NC} $1"; }
warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[-]${NC} $1"; exit 1; }

[[ $EUID -ne 0 ]] && error "must run as root: curl -fsSL <url> | sudo bash"

GO_VERSION="1.22.5"
REPO_URL="https://github.com/AdobeFilter/res.git"
SRC_DIR="/opt/valhalla"

log "installing prerequisites"
apt-get update -qq
apt-get install -y -qq curl wget git ca-certificates

# Go — pin to 1.22.5 because the system go in Ubuntu 22.04 is 1.18 and our
# deps need atomic.Bool (Go 1.19+).
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go${GO_VERSION}"; then
    log "installing Go ${GO_VERSION}"
    rm -rf /usr/local/go
    wget -qO /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/golang.sh
fi
export PATH="$PATH:/usr/local/go/bin"
log "$(/usr/local/go/bin/go version)"

# Xray — used as a subprocess by valhalla-client (VLESS+Reality outbound).
# We disable the systemd unit the installer creates: the client manages its
# own xray with a per-session config in /tmp.
if ! command -v xray &>/dev/null; then
    log "installing xray"
    bash -c "$(curl -L https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
fi
systemctl disable --now xray 2>/dev/null || true
log "$(xray version 2>/dev/null | head -1)"

# Source
if [ -d "$SRC_DIR/.git" ]; then
    log "updating source at $SRC_DIR"
    git -C "$SRC_DIR" fetch --quiet
    git -C "$SRC_DIR" reset --hard origin/main --quiet
else
    log "cloning source to $SRC_DIR"
    rm -rf "$SRC_DIR"
    git clone --quiet "$REPO_URL" "$SRC_DIR"
fi

# Build
log "building valhalla-client"
cd "$SRC_DIR/valhalla-client"
/usr/local/go/bin/go mod tidy
/usr/local/go/bin/go build -o /usr/local/bin/valhalla-client .
chmod +x /usr/local/bin/valhalla-client

log "================================="
log "valhalla-client installed"
log "  binary:  /usr/local/bin/valhalla-client"
log "  source:  $SRC_DIR"
log "  host IP: $(hostname -I | awk '{print $1}')"
log ""
log "Update later:"
log "  cd $SRC_DIR && git pull && cd valhalla-client && \\"
log "    /usr/local/go/bin/go build -o /usr/local/bin/valhalla-client ."
log "================================="

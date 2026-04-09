#!/bin/bash
set -euo pipefail

# ============================================================
#  Valhalla Exit Node — Xray VLESS+Reality deploy script
#  Usage: curl -sL <url> | bash
#     or: bash xray-exit.sh
#
#  Supports: Ubuntu 20+, Debian 11+, CentOS 8+, Fedora 36+
# ============================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[-]${NC} $1"; exit 1; }
info() { echo -e "${CYAN}[i]${NC} $1"; }

XRAY_VERSION="1.8.24"
XRAY_DIR="/usr/local/share/xray"
XRAY_BIN="/usr/local/bin/xray"
CONFIG_DIR="/etc/valhalla"
XRAY_CONFIG="${CONFIG_DIR}/xray.json"
WG_PORT=51820
XRAY_PORT=443

# ── checks ──────────────────────────────────────────────────

[[ $EUID -ne 0 ]] && err "Run as root: sudo bash $0"

ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  XRAY_ARCH="Xray-linux-64" ;;
    aarch64) XRAY_ARCH="Xray-linux-arm64-v8a" ;;
    armv7l)  XRAY_ARCH="Xray-linux-arm32-v7a" ;;
    *)       err "Unsupported arch: $ARCH" ;;
esac

# Detect default network interface
DEFAULT_IFACE=$(ip route show default | awk '/default/ {print $5}' | head -1)
[[ -z "$DEFAULT_IFACE" ]] && DEFAULT_IFACE="eth0"
log "Default interface: ${DEFAULT_IFACE}"

# ── install deps ────────────────────────────────────────────

install_deps() {
    if command -v apt-get &>/dev/null; then
        apt-get update -qq
        apt-get install -y -qq wget unzip jq wireguard-tools iptables openssl qrencode
    elif command -v dnf &>/dev/null; then
        dnf install -y -q wget unzip jq wireguard-tools iptables openssl qrencode
    elif command -v yum &>/dev/null; then
        yum install -y -q wget unzip jq wireguard-tools iptables openssl qrencode
    else
        err "No supported package manager found"
    fi
}

log "Installing dependencies..."
install_deps

# ── install xray ────────────────────────────────────────────

install_xray() {
    if [[ -f "$XRAY_BIN" ]]; then
        local cur_ver
        cur_ver=$($XRAY_BIN version 2>/dev/null | head -1 | awk '{print $2}' || echo "0")
        if [[ "$cur_ver" == "$XRAY_VERSION" ]]; then
            log "Xray ${XRAY_VERSION} already installed"
            return
        fi
    fi

    log "Installing Xray ${XRAY_VERSION}..."
    local url="https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/${XRAY_ARCH}.zip"
    wget -q "$url" -O /tmp/xray.zip || err "Failed to download Xray"
    mkdir -p "$XRAY_DIR"
    unzip -qo /tmp/xray.zip -d "$XRAY_DIR"
    cp "${XRAY_DIR}/xray" "$XRAY_BIN"
    chmod +x "$XRAY_BIN"
    rm /tmp/xray.zip
    log "Xray installed: $XRAY_BIN"
}

install_xray

# ── generate keys ───────────────────────────────────────────

mkdir -p "$CONFIG_DIR"

generate_reality_keys() {
    local output
    output=$($XRAY_BIN x25519)
    REALITY_PRIVATE_KEY=$(echo "$output" | grep "Private" | awk '{print $NF}')
    REALITY_PUBLIC_KEY=$(echo "$output" | grep "Public" | awk '{print $NF}')
}

# Check for existing config
if [[ -f "$XRAY_CONFIG" ]]; then
    warn "Existing config found: ${XRAY_CONFIG}"
    read -p "Overwrite? [y/N]: " OVERWRITE
    if [[ "${OVERWRITE,,}" != "y" ]]; then
        info "Keeping existing config"
        SKIP_CONFIG=true
    fi
fi

if [[ "${SKIP_CONFIG:-}" != "true" ]]; then
    # Generate UUID
    UUID=$(cat /proc/sys/kernel/random/uuid)
    log "Generated UUID: ${UUID}"

    # Generate Reality keypair
    generate_reality_keys
    log "Reality public key: ${REALITY_PUBLIC_KEY}"

    # Generate short ID (8 hex chars)
    SHORT_ID=$(openssl rand -hex 4)

    # Ask for SNI domain
    read -p "SNI domain [microsoft.com]: " SNI_DOMAIN
    SNI_DOMAIN=${SNI_DOMAIN:-microsoft.com}

    # Ask for control plane URL (optional — for Valhalla integration)
    read -p "Control Plane URL (leave empty for standalone): " CONTROL_PLANE_URL

    # ── write xray config ───────────────────────────────────

    cat > "$XRAY_CONFIG" <<XEOF
{
  "log": {
    "loglevel": "warning"
  },
  "inbounds": [
    {
      "listen": "0.0.0.0",
      "port": ${XRAY_PORT},
      "protocol": "vless",
      "settings": {
        "clients": [
          {
            "id": "${UUID}",
            "flow": "xtls-rprx-vision"
          }
        ],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "dest": "${SNI_DOMAIN}:443",
          "xver": 0,
          "serverNames": [
            "${SNI_DOMAIN}",
            "www.${SNI_DOMAIN}"
          ],
          "privateKey": "${REALITY_PRIVATE_KEY}",
          "shortIds": [
            "${SHORT_ID}"
          ]
        }
      },
      "sniffing": {
        "enabled": true,
        "destOverride": [
          "http",
          "tls",
          "quic"
        ]
      }
    }
  ],
  "outbounds": [
    {
      "protocol": "freedom",
      "tag": "direct"
    },
    {
      "protocol": "blackhole",
      "tag": "block"
    }
  ]
}
XEOF

    log "Xray config written: ${XRAY_CONFIG}"

    # Save credentials for reference
    cat > "${CONFIG_DIR}/credentials.txt" <<CEOF
# Valhalla Exit Node Credentials
# Generated: $(date -u +"%Y-%m-%d %H:%M:%S UTC")

UUID=${UUID}
REALITY_PUBLIC_KEY=${REALITY_PUBLIC_KEY}
REALITY_PRIVATE_KEY=${REALITY_PRIVATE_KEY}
SHORT_ID=${SHORT_ID}
SNI_DOMAIN=${SNI_DOMAIN}
PORT=${XRAY_PORT}
SERVER_IP=$(curl -s4 ifconfig.me || echo "UNKNOWN")

# VLESS share link:
# vless://${UUID}@SERVER_IP:${XRAY_PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${SNI_DOMAIN}&fp=chrome&pbk=${REALITY_PUBLIC_KEY}&sid=${SHORT_ID}&type=tcp#Valhalla-Exit
CEOF
    chmod 600 "${CONFIG_DIR}/credentials.txt"
fi

# ── IP forwarding ───────────────────────────────────────────

log "Enabling IP forwarding..."
sysctl -w net.ipv4.ip_forward=1 >/dev/null
if ! grep -q "net.ipv4.ip_forward=1" /etc/sysctl.conf 2>/dev/null; then
    echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf
fi

# ── iptables NAT ────────────────────────────────────────────

setup_nat() {
    # Clean up existing rules first (ignore errors)
    iptables -t nat -D POSTROUTING -s 10.100.0.0/16 -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
    iptables -D FORWARD -i wg0 -j ACCEPT 2>/dev/null || true
    iptables -D FORWARD -o wg0 -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true

    iptables -t nat -A POSTROUTING -s 10.100.0.0/16 -o "$DEFAULT_IFACE" -j MASQUERADE
    iptables -A FORWARD -i wg0 -j ACCEPT
    iptables -A FORWARD -o wg0 -m state --state RELATED,ESTABLISHED -j ACCEPT
    log "NAT rules configured (interface: ${DEFAULT_IFACE})"
}

setup_nat

# ── systemd service: xray ───────────────────────────────────

cat > /etc/systemd/system/valhalla-xray.service <<SEOF
[Unit]
Description=Valhalla Xray (VLESS+Reality)
After=network.target

[Service]
Type=simple
ExecStart=${XRAY_BIN} run -config ${XRAY_CONFIG}
Restart=always
RestartSec=3
LimitNOFILE=65535

StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-xray

[Install]
WantedBy=multi-user.target
SEOF

systemctl daemon-reload
systemctl enable valhalla-xray
systemctl restart valhalla-xray

log "Xray service started"

# ── systemd service: valhalla node (WireGuard + heartbeat) ──

if [[ -n "${CONTROL_PLANE_URL:-}" ]]; then
    cat > /etc/systemd/system/valhalla-node.service <<NEOF
[Unit]
Description=Valhalla Exit Node (WireGuard)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/valhalla-node
Restart=always
RestartSec=5

Environment=CONTROL_PLANE_URL=${CONTROL_PLANE_URL}
Environment=WG_IFACE=wg0
Environment=TOKEN_FILE=${CONFIG_DIR}/token
Environment=CONFIG_FILE=${CONFIG_DIR}/node.yaml

StandardInput=tty-force
StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-node

[Install]
WantedBy=multi-user.target
NEOF
    systemctl daemon-reload
    systemctl enable valhalla-node
    log "Valhalla node service configured (needs first-run login)"
fi

# ── firewall ────────────────────────────────────────────────

if command -v ufw &>/dev/null; then
    ufw allow ${XRAY_PORT}/tcp >/dev/null 2>&1 || true
    ufw allow ${WG_PORT}/udp >/dev/null 2>&1 || true
    log "UFW: ports ${XRAY_PORT}/tcp, ${WG_PORT}/udp allowed"
elif command -v firewall-cmd &>/dev/null; then
    firewall-cmd --permanent --add-port=${XRAY_PORT}/tcp >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-port=${WG_PORT}/udp >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    log "Firewalld: ports ${XRAY_PORT}/tcp, ${WG_PORT}/udp allowed"
fi

# ── generate share link & QR ────────────────────────────────

SERVER_IP=$(curl -s4 ifconfig.me 2>/dev/null || curl -s4 icanhazip.com 2>/dev/null || echo "YOUR_SERVER_IP")

if [[ "${SKIP_CONFIG:-}" != "true" ]]; then
    SHARE_LINK="vless://${UUID}@${SERVER_IP}:${XRAY_PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${SNI_DOMAIN}&fp=chrome&pbk=${REALITY_PUBLIC_KEY}&sid=${SHORT_ID}&type=tcp#Valhalla-Exit"

    # Update credentials with actual IP
    sed -i "s|SERVER_IP|${SERVER_IP}|g" "${CONFIG_DIR}/credentials.txt"

    echo ""
    echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  Valhalla Exit Node deployed successfully${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  Server:     ${CYAN}${SERVER_IP}:${XRAY_PORT}${NC}"
    echo -e "  Protocol:   ${CYAN}VLESS + Reality${NC}"
    echo -e "  UUID:       ${CYAN}${UUID}${NC}"
    echo -e "  Public Key: ${CYAN}${REALITY_PUBLIC_KEY}${NC}"
    echo -e "  Short ID:   ${CYAN}${SHORT_ID}${NC}"
    echo -e "  SNI:        ${CYAN}${SNI_DOMAIN}${NC}"
    echo ""
    echo -e "  ${YELLOW}Share link:${NC}"
    echo -e "  ${SHARE_LINK}"
    echo ""

    # QR code
    if command -v qrencode &>/dev/null; then
        echo -e "  ${YELLOW}QR Code (scan in Valhalla app):${NC}"
        echo ""
        qrencode -t ANSIUTF8 "$SHARE_LINK"
        echo ""
    fi

    echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${GREEN}Config:${NC}      ${XRAY_CONFIG}"
    echo -e "  ${GREEN}Credentials:${NC} ${CONFIG_DIR}/credentials.txt"
    echo -e "  ${GREEN}Service:${NC}     systemctl status valhalla-xray"
    echo -e "  ${GREEN}Logs:${NC}        journalctl -u valhalla-xray -f"
    echo ""
    if [[ -n "${CONTROL_PLANE_URL:-}" ]]; then
        echo -e "  ${YELLOW}WireGuard node needs first-run login:${NC}"
        echo -e "    /usr/local/bin/valhalla-node"
        echo -e "    Then: systemctl start valhalla-node"
        echo ""
    fi
    echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
else
    echo ""
    log "Xray service restarted with existing config"
    echo -e "  ${GREEN}Service:${NC} systemctl status valhalla-xray"
    echo -e "  ${GREEN}Logs:${NC}    journalctl -u valhalla-xray -f"
    echo ""
fi

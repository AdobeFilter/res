#!/bin/bash
set -euo pipefail

# ============================================================
#  Valhalla Exit Node — Xray VLESS+Reality auto-deploy
#  Usage: curl -sL <url> | bash
#     or: bash xray-exit.sh
#  Fully unattended — no user input required.
# ============================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[-]${NC} $1"; exit 1; }

XRAY_VERSION="1.8.24"
XRAY_DIR="/usr/local/share/xray"
XRAY_BIN="/usr/local/bin/xray"
CONFIG_DIR="/etc/valhalla"
XRAY_CONFIG="${CONFIG_DIR}/xray.json"
WG_PORT=51820
XRAY_PORT=443
SNI_DOMAIN="microsoft.com"
CONTROL_PLANE_URL="${CONTROL_PLANE_URL:-}"

# ── checks ──────────────────────────────────────────────────

[[ $EUID -ne 0 ]] && err "Run as root: sudo bash $0"

ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  XRAY_ARCH="Xray-linux-64" ;;
    aarch64) XRAY_ARCH="Xray-linux-arm64-v8a" ;;
    armv7l)  XRAY_ARCH="Xray-linux-arm32-v7a" ;;
    *)       err "Unsupported arch: $ARCH" ;;
esac

DEFAULT_IFACE=$(ip route show default | awk '/default/ {print $5}' | head -1)
[[ -z "$DEFAULT_IFACE" ]] && DEFAULT_IFACE="eth0"
log "Interface: ${DEFAULT_IFACE}"

# ── install deps ────────────────────────────────────────────

log "Installing dependencies..."
if command -v apt-get &>/dev/null; then
    apt-get update -qq
    apt-get install -y -qq wget unzip jq wireguard-tools iptables openssl qrencode 2>/dev/null
elif command -v dnf &>/dev/null; then
    dnf install -y -q wget unzip jq wireguard-tools iptables openssl qrencode 2>/dev/null
elif command -v yum &>/dev/null; then
    yum install -y -q wget unzip jq wireguard-tools iptables openssl qrencode 2>/dev/null
else
    err "No supported package manager"
fi

# ── install xray ────────────────────────────────────────────

if [[ -f "$XRAY_BIN" ]]; then
    cur_ver=$($XRAY_BIN version 2>/dev/null | head -1 | awk '{print $2}' || echo "0")
    [[ "$cur_ver" == "$XRAY_VERSION" ]] && log "Xray ${XRAY_VERSION} OK" || {
        log "Upgrading Xray to ${XRAY_VERSION}..."
        wget -q "https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/${XRAY_ARCH}.zip" -O /tmp/xray.zip
        mkdir -p "$XRAY_DIR"
        unzip -qo /tmp/xray.zip -d "$XRAY_DIR"
        cp "${XRAY_DIR}/xray" "$XRAY_BIN"
        chmod +x "$XRAY_BIN"
        rm -f /tmp/xray.zip
    }
else
    log "Installing Xray ${XRAY_VERSION}..."
    wget -q "https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/${XRAY_ARCH}.zip" -O /tmp/xray.zip
    mkdir -p "$XRAY_DIR"
    unzip -qo /tmp/xray.zip -d "$XRAY_DIR"
    cp "${XRAY_DIR}/xray" "$XRAY_BIN"
    chmod +x "$XRAY_BIN"
    rm -f /tmp/xray.zip
fi

# ── generate config ─────────────────────────────────────────

mkdir -p "$CONFIG_DIR"

# Reuse existing credentials if config exists
if [[ -f "${CONFIG_DIR}/credentials.txt" ]]; then
    log "Loading existing credentials..."
    source "${CONFIG_DIR}/credentials.txt" 2>/dev/null || true
fi

# Generate missing values
UUID=${UUID:-$(cat /proc/sys/kernel/random/uuid)}
if [[ -z "${REALITY_PRIVATE_KEY:-}" ]]; then
    key_output=$($XRAY_BIN x25519)
    REALITY_PRIVATE_KEY=$(echo "$key_output" | grep "Private" | awk '{print $NF}')
    REALITY_PUBLIC_KEY=$(echo "$key_output" | grep "Public" | awk '{print $NF}')
fi
SHORT_ID=${SHORT_ID:-$(openssl rand -hex 4)}
SERVER_IP=$(curl -s4 --max-time 5 ifconfig.me 2>/dev/null || curl -s4 --max-time 5 icanhazip.com 2>/dev/null || echo "UNKNOWN")

# Write xray config
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
          "serverNames": ["${SNI_DOMAIN}", "www.${SNI_DOMAIN}"],
          "privateKey": "${REALITY_PRIVATE_KEY}",
          "shortIds": ["${SHORT_ID}"]
        }
      },
      "sniffing": {
        "enabled": true,
        "destOverride": ["http", "tls", "quic"]
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

# Save credentials
SHARE_LINK="vless://${UUID}@${SERVER_IP}:${XRAY_PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${SNI_DOMAIN}&fp=chrome&pbk=${REALITY_PUBLIC_KEY}&sid=${SHORT_ID}&type=tcp#Valhalla-Exit"

cat > "${CONFIG_DIR}/credentials.txt" <<CEOF
UUID=${UUID}
REALITY_PUBLIC_KEY=${REALITY_PUBLIC_KEY}
REALITY_PRIVATE_KEY=${REALITY_PRIVATE_KEY}
SHORT_ID=${SHORT_ID}
SNI_DOMAIN=${SNI_DOMAIN}
PORT=${XRAY_PORT}
SERVER_IP=${SERVER_IP}
SHARE_LINK=${SHARE_LINK}
CEOF
chmod 600 "${CONFIG_DIR}/credentials.txt"

# ── IP forwarding + NAT ────────────────────────────────────

sysctl -w net.ipv4.ip_forward=1 >/dev/null
grep -q "net.ipv4.ip_forward=1" /etc/sysctl.conf 2>/dev/null || echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

iptables -t nat -D POSTROUTING -s 10.100.0.0/16 -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
iptables -D FORWARD -i wg0 -j ACCEPT 2>/dev/null || true
iptables -D FORWARD -o wg0 -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
iptables -t nat -A POSTROUTING -s 10.100.0.0/16 -o "$DEFAULT_IFACE" -j MASQUERADE
iptables -A FORWARD -i wg0 -j ACCEPT
iptables -A FORWARD -o wg0 -m state --state RELATED,ESTABLISHED -j ACCEPT
log "NAT configured"

# ── systemd: xray ──────────────────────────────────────────

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
systemctl enable valhalla-xray >/dev/null 2>&1
systemctl restart valhalla-xray
log "Xray service running"

# ── systemd: valhalla node ─────────────────────────────────

if [[ -n "$CONTROL_PLANE_URL" ]]; then
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
StandardInput=tty-force
StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-node

[Install]
WantedBy=multi-user.target
NEOF
    systemctl daemon-reload
    systemctl enable valhalla-node >/dev/null 2>&1
fi

# ── firewall ────────────────────────────────────────────────

if command -v ufw &>/dev/null; then
    ufw allow ${XRAY_PORT}/tcp >/dev/null 2>&1 || true
    ufw allow ${WG_PORT}/udp >/dev/null 2>&1 || true
fi
if command -v firewall-cmd &>/dev/null; then
    firewall-cmd --permanent --add-port=${XRAY_PORT}/tcp >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-port=${WG_PORT}/udp >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
fi

# ── output ──────────────────────────────────────────────────

echo ""
echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Valhalla Exit Node ready${NC}"
echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
echo ""
echo -e "  ${CYAN}${SERVER_IP}:${XRAY_PORT}${NC}  VLESS+Reality"
echo -e "  UUID:  ${CYAN}${UUID}${NC}"
echo -e "  PBK:   ${CYAN}${REALITY_PUBLIC_KEY}${NC}"
echo -e "  SID:   ${CYAN}${SHORT_ID}${NC}"
echo ""
echo -e "  ${SHARE_LINK}"
echo ""
if command -v qrencode &>/dev/null; then
    qrencode -t ANSIUTF8 "$SHARE_LINK"
    echo ""
fi
echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
echo -e "  systemctl status valhalla-xray"
echo -e "  journalctl -u valhalla-xray -f"
echo -e "${CYAN}══════════════════════════════════════════════════${NC}"

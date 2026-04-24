#!/bin/bash
set -euo pipefail

# ============================================================
#  Valhalla Exit Node — Xray VLESS+Reality auto-deploy
#  Usage: bash xray-exit.sh
#  Fully unattended — no user input required.
# ============================================================

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log() { echo -e "${GREEN}[+]${NC} $1"; }
err() { echo -e "${RED}[-]${NC} $1"; exit 1; }

XRAY_VERSION="26.3.27"
XRAY_BIN="/usr/local/bin/xray"
XRAY_DIR="/usr/local/share/xray"
CONFIG_DIR="/etc/valhalla"
XRAY_CONFIG="${CONFIG_DIR}/xray.json"
XRAY_PORT=8443
SNI_DOMAIN="www.icloud.com"

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
SERVER_IP=$(curl -s4 --max-time 5 ifconfig.me 2>/dev/null || curl -s4 --max-time 5 icanhazip.com 2>/dev/null || hostname -I | awk '{print $1}')
log "Server: ${SERVER_IP}, interface: ${DEFAULT_IFACE}"

# ── deps ────────────────────────────────────────────────────

log "Installing dependencies..."
if command -v apt-get &>/dev/null; then
    apt-get update -qq
    apt-get install -y -qq wget unzip jq iptables openssl qrencode curl 2>/dev/null
elif command -v dnf &>/dev/null; then
    dnf install -y -q wget unzip jq iptables openssl qrencode curl 2>/dev/null
elif command -v yum &>/dev/null; then
    yum install -y -q wget unzip jq iptables openssl qrencode curl 2>/dev/null
fi

# ── install xray ────────────────────────────────────────────

if ! [[ -f "$XRAY_BIN" ]] || [[ "$($XRAY_BIN version 2>/dev/null | awk 'NR==1{print $2}')" != "$XRAY_VERSION" ]]; then
    log "Installing Xray ${XRAY_VERSION}..."
    wget -q "https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/${XRAY_ARCH}.zip" -O /tmp/xray.zip
    mkdir -p "$XRAY_DIR"
    unzip -qo /tmp/xray.zip -d "$XRAY_DIR"
    cp "${XRAY_DIR}/xray" "$XRAY_BIN"
    chmod +x "$XRAY_BIN"
    rm -f /tmp/xray.zip
fi
log "Xray OK"

# ── generate config ─────────────────────────────────────────

mkdir -p "$CONFIG_DIR"

# Reuse existing credentials if present
if [[ -f "${CONFIG_DIR}/credentials.txt" ]]; then
    source "${CONFIG_DIR}/credentials.txt" 2>/dev/null || true
fi

UUID=${UUID:-$(cat /proc/sys/kernel/random/uuid)}
if [[ -z "${REALITY_PRIVATE_KEY:-}" ]]; then
    key_output=$($XRAY_BIN x25519)
    REALITY_PRIVATE_KEY=$(echo "$key_output" | grep "Private" | awk '{print $NF}')
    REALITY_PUBLIC_KEY=$(echo "$key_output" | grep "Public" | awk '{print $NF}')
fi
SHORT_ID=${SHORT_ID:-$(openssl rand -hex 4)}

cat > "$XRAY_CONFIG" <<EOF
{
  "log": {"loglevel": "warning"},
  "inbounds": [{
    "listen": "0.0.0.0",
    "port": ${XRAY_PORT},
    "protocol": "vless",
    "settings": {
      "clients": [{"id": "${UUID}", "flow": "xtls-rprx-vision"}],
      "decryption": "none"
    },
    "streamSettings": {
      "network": "tcp",
      "security": "reality",
      "realitySettings": {
        "show": false,
        "dest": "${SNI_DOMAIN}:443",
        "xver": 0,
        "serverNames": ["${SNI_DOMAIN}"],
        "privateKey": "${REALITY_PRIVATE_KEY}",
        "shortIds": ["${SHORT_ID}"]
      }
    },
    "sniffing": {"enabled": true, "destOverride": ["http", "tls", "quic"]}
  }],
  "outbounds": [
    {"protocol": "freedom", "tag": "direct"},
    {"protocol": "blackhole", "tag": "block"}
  ]
}
EOF

SHARE_LINK="vless://${UUID}@${SERVER_IP}:${XRAY_PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${SNI_DOMAIN}&fp=chrome&pbk=${REALITY_PUBLIC_KEY}&sid=${SHORT_ID}&type=tcp#Valhalla-Exit"

cat > "${CONFIG_DIR}/credentials.txt" <<EOF
UUID=${UUID}
REALITY_PUBLIC_KEY=${REALITY_PUBLIC_KEY}
REALITY_PRIVATE_KEY=${REALITY_PRIVATE_KEY}
SHORT_ID=${SHORT_ID}
SNI_DOMAIN=${SNI_DOMAIN}
PORT=${XRAY_PORT}
SERVER_IP=${SERVER_IP}
SHARE_LINK=${SHARE_LINK}
EOF
chmod 600 "${CONFIG_DIR}/credentials.txt"

# ── IP forwarding ───────────────────────────────────────────

sysctl -w net.ipv4.ip_forward=1 >/dev/null
grep -q "net.ipv4.ip_forward=1" /etc/sysctl.conf 2>/dev/null || echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

# ── systemd ─────────────────────────────────────────────────

cat > /etc/systemd/system/valhalla-xray.service <<EOF
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
EOF

systemctl daemon-reload
systemctl enable valhalla-xray >/dev/null 2>&1
systemctl restart valhalla-xray
log "Xray service running"

# ── firewall ────────────────────────────────────────────────

command -v ufw &>/dev/null && { ufw allow ${XRAY_PORT}/tcp >/dev/null 2>&1; } || true
command -v firewall-cmd &>/dev/null && { firewall-cmd --permanent --add-port=${XRAY_PORT}/tcp >/dev/null 2>&1; firewall-cmd --reload >/dev/null 2>&1; } || true

# ── output ──────────────────────────────────────────────────

echo ""
echo -e "${CYAN}══════════════════════════════════════════${NC}"
echo -e "${GREEN}  Valhalla Exit Node ready${NC}"
echo -e "${CYAN}══════════════════════════════════════════${NC}"
echo -e "  ${CYAN}${SERVER_IP}:${XRAY_PORT}${NC}  VLESS+Reality"
echo -e "  UUID:  ${CYAN}${UUID}${NC}"
echo -e "  PBK:   ${CYAN}${REALITY_PUBLIC_KEY}${NC}"
echo -e "  SID:   ${CYAN}${SHORT_ID}${NC}"
echo ""
echo -e "  ${SHARE_LINK}"
echo ""
command -v qrencode &>/dev/null && qrencode -t ANSIUTF8 "$SHARE_LINK" && echo ""
echo -e "${CYAN}══════════════════════════════════════════${NC}"
echo -e "  systemctl status valhalla-xray"
echo -e "  journalctl -u valhalla-xray -f"
echo -e "${CYAN}══════════════════════════════════════════${NC}"

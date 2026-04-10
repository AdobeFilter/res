#!/bin/bash
set -euo pipefail

# ============================================================
#  Valhalla Exit Node — full auto-deploy
#  Sets up Xray VLESS+Reality AND registers with control plane.
#  All account devices will route traffic through this node.
#
#  Usage:
#    EMAIL=user@example.com PASSWORD=pass CONTROL_PLANE=http://1.2.3.4:8443 bash xray-exit.sh
#
#  Or with positional args:
#    bash xray-exit.sh user@example.com password http://1.2.3.4:8443
# ============================================================

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[-]${NC} $1"; exit 1; }

# ── args ────────────────────────────────────────────────────

EMAIL="${EMAIL:-${1:-}}"
PASSWORD="${PASSWORD:-${2:-}}"
CONTROL_PLANE="${CONTROL_PLANE:-${3:-http://144.48.10.51:8443}}"

[[ -z "$EMAIL" ]] && err "EMAIL required: EMAIL=you@mail.com PASSWORD=... bash $0"
[[ -z "$PASSWORD" ]] && err "PASSWORD required"
[[ $EUID -ne 0 ]] && err "Run as root"

XRAY_VERSION="1.8.24"
XRAY_BIN="/usr/local/bin/xray"
XRAY_DIR="/usr/local/share/xray"
CONFIG_DIR="/etc/valhalla"
XRAY_CONFIG="${CONFIG_DIR}/xray.json"
XRAY_PORT=443
WG_PORT=51820
WG_IFACE="wg0"
SNI_DOMAIN="microsoft.com"

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
    apt-get install -y -qq wget unzip jq wireguard-tools iptables openssl qrencode curl 2>/dev/null
elif command -v dnf &>/dev/null; then
    dnf install -y -q wget unzip jq wireguard-tools iptables openssl qrencode curl 2>/dev/null
elif command -v yum &>/dev/null; then
    yum install -y -q wget unzip jq wireguard-tools iptables openssl qrencode curl 2>/dev/null
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

# ── control plane: login ────────────────────────────────────

log "Authenticating with control plane..."
AUTH_RESP=$(curl -sf -X POST "${CONTROL_PLANE}/api/v1/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"${EMAIL}\",\"password\":\"${PASSWORD}\"}" 2>/dev/null) \
    || err "Login failed — check EMAIL/PASSWORD and CONTROL_PLANE URL"

TOKEN=$(echo "$AUTH_RESP" | jq -r '.token')
ACCOUNT_ID=$(echo "$AUTH_RESP" | jq -r '.account_id')
[[ -z "$TOKEN" || "$TOKEN" == "null" ]] && err "Login returned empty token"

mkdir -p "$CONFIG_DIR"
echo "$TOKEN" > "${CONFIG_DIR}/token"
chmod 600 "${CONFIG_DIR}/token"
log "Logged in (account: ${ACCOUNT_ID})"

# ── generate WireGuard keys ────────────────────────────────

WG_PRIVKEY=$(wg genkey)
WG_PUBKEY=$(echo "$WG_PRIVKEY" | wg pubkey)
log "WireGuard pubkey: ${WG_PUBKEY}"

# ── control plane: register exit node ──────────────────────

HOSTNAME=$(hostname)
log "Registering exit node '${HOSTNAME}'..."
REG_RESP=$(curl -sf -X POST "${CONTROL_PLANE}/api/v1/nodes/register" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${TOKEN}" \
    -d "{\"name\":\"${HOSTNAME}\",\"node_type\":\"exit\",\"os\":\"Linux\",\"public_key\":\"${WG_PUBKEY}\"}" 2>/dev/null) \
    || err "Node registration failed"

NODE_ID=$(echo "$REG_RESP" | jq -r '.node_id')
INTERNAL_IP=$(echo "$REG_RESP" | jq -r '.internal_ip')
PEERS_JSON=$(echo "$REG_RESP" | jq -c '.peers // []')

[[ -z "$NODE_ID" || "$NODE_ID" == "null" ]] && err "Registration returned empty node_id"
log "Registered: node_id=${NODE_ID}, internal_ip=${INTERNAL_IP}"

# ── set this node as account exit node ─────────────────────

curl -sf -X PUT "${CONTROL_PLANE}/api/v1/accounts/${ACCOUNT_ID}/settings" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${TOKEN}" \
    -d "{\"exit_node_id\":\"${NODE_ID}\"}" >/dev/null 2>&1 || warn "Failed to set as account exit node"

log "Set as account exit node"

# ── setup WireGuard ─────────────────────────────────────────

log "Setting up WireGuard..."
# Remove existing interface if any
ip link delete dev "$WG_IFACE" 2>/dev/null || true

ip link add dev "$WG_IFACE" type wireguard
ip address add "${INTERNAL_IP}/16" dev "$WG_IFACE"
echo "$WG_PRIVKEY" | wg set "$WG_IFACE" private-key /dev/stdin listen-port $WG_PORT
ip link set up dev "$WG_IFACE"

# Add peers from registration response
PEER_COUNT=$(echo "$PEERS_JSON" | jq 'length')
for i in $(seq 0 $((PEER_COUNT - 1))); do
    PEER_PUBKEY=$(echo "$PEERS_JSON" | jq -r ".[$i].public_key")
    PEER_ENDPOINT=$(echo "$PEERS_JSON" | jq -r ".[$i].endpoint // empty")
    PEER_IP=$(echo "$PEERS_JSON" | jq -r ".[$i].internal_ip")

    WG_ARGS="$WG_IFACE peer $PEER_PUBKEY allowed-ips ${PEER_IP}/32 persistent-keepalive 25"
    [[ -n "$PEER_ENDPOINT" ]] && WG_ARGS="$WG_ARGS endpoint $PEER_ENDPOINT"
    wg set $WG_ARGS 2>/dev/null || true
done
log "WireGuard up: ${INTERNAL_IP}, ${PEER_COUNT} peers"

# ── IP forwarding + NAT ────────────────────────────────────

sysctl -w net.ipv4.ip_forward=1 >/dev/null
grep -q "net.ipv4.ip_forward=1" /etc/sysctl.conf 2>/dev/null || echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

# Clean + add NAT rules
iptables -t nat -D POSTROUTING -s 10.100.0.0/16 -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
iptables -D FORWARD -i "$WG_IFACE" -j ACCEPT 2>/dev/null || true
iptables -D FORWARD -o "$WG_IFACE" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
iptables -t nat -A POSTROUTING -s 10.100.0.0/16 -o "$DEFAULT_IFACE" -j MASQUERADE
iptables -A FORWARD -i "$WG_IFACE" -j ACCEPT
iptables -A FORWARD -o "$WG_IFACE" -m state --state RELATED,ESTABLISHED -j ACCEPT
log "NAT configured"

# ── xray config ─────────────────────────────────────────────

# Reuse existing keys or generate new
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

cat > "$XRAY_CONFIG" <<XEOF
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
        "serverNames": ["${SNI_DOMAIN}", "www.${SNI_DOMAIN}"],
        "privateKey": "${REALITY_PRIVATE_KEY}",
        "shortIds": ["${SHORT_ID}"]
      }
    },
    "sniffing": {"enabled": true, "destOverride": ["http","tls","quic"]}
  }],
  "outbounds": [
    {"protocol": "freedom", "tag": "direct"},
    {"protocol": "blackhole", "tag": "block"}
  ]
}
XEOF

SHARE_LINK="vless://${UUID}@${SERVER_IP}:${XRAY_PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${SNI_DOMAIN}&fp=chrome&pbk=${REALITY_PUBLIC_KEY}&sid=${SHORT_ID}&type=tcp#Valhalla-Exit"

cat > "${CONFIG_DIR}/credentials.txt" <<CEOF
UUID=${UUID}
REALITY_PUBLIC_KEY=${REALITY_PUBLIC_KEY}
REALITY_PRIVATE_KEY=${REALITY_PRIVATE_KEY}
SHORT_ID=${SHORT_ID}
SNI_DOMAIN=${SNI_DOMAIN}
PORT=${XRAY_PORT}
SERVER_IP=${SERVER_IP}
NODE_ID=${NODE_ID}
INTERNAL_IP=${INTERNAL_IP}
ACCOUNT_ID=${ACCOUNT_ID}
SHARE_LINK=${SHARE_LINK}
CEOF
chmod 600 "${CONFIG_DIR}/credentials.txt"

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

# ── systemd: WireGuard + heartbeat ─────────────────────────

cat > /usr/local/bin/valhalla-heartbeat <<'HEOF'
#!/bin/bash
# Simple heartbeat loop — keeps exit node online in control plane
CONFIG_DIR="/etc/valhalla"
TOKEN=$(cat "${CONFIG_DIR}/token" 2>/dev/null)
source "${CONFIG_DIR}/credentials.txt" 2>/dev/null
CONTROL_PLANE="${CONTROL_PLANE_URL:-http://144.48.10.51:8443}"

while true; do
    ENDPOINT="${SERVER_IP}:51820"
    curl -sf -X POST "${CONTROL_PLANE}/api/v1/nodes/${NODE_ID}/heartbeat" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer ${TOKEN}" \
        -d "{\"node_id\":\"${NODE_ID}\",\"endpoint\":\"${ENDPOINT}\",\"metrics\":{\"rtt_ms\":0,\"bandwidth_mbps\":0,\"cpu_percent\":0,\"active_conns\":0,\"packet_loss\":0}}" \
        >/dev/null 2>&1
    sleep 10
done
HEOF
chmod +x /usr/local/bin/valhalla-heartbeat
# Inject CONTROL_PLANE_URL into heartbeat script
sed -i "s|CONTROL_PLANE_URL:-http://144.48.10.51:8443|CONTROL_PLANE_URL:-${CONTROL_PLANE}|" /usr/local/bin/valhalla-heartbeat

cat > /etc/systemd/system/valhalla-heartbeat.service <<SEOF2
[Unit]
Description=Valhalla Heartbeat
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/valhalla-heartbeat
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=valhalla-heartbeat

[Install]
WantedBy=multi-user.target
SEOF2

# ── systemd: WireGuard interface restore on boot ───────────

cat > /usr/local/bin/valhalla-wg-up <<WEOF
#!/bin/bash
WG_IFACE="${WG_IFACE}"
INTERNAL_IP="${INTERNAL_IP}"
WG_PRIVKEY="${WG_PRIVKEY}"

ip link delete dev "\$WG_IFACE" 2>/dev/null || true
ip link add dev "\$WG_IFACE" type wireguard
ip address add "\${INTERNAL_IP}/16" dev "\$WG_IFACE"
echo "\$WG_PRIVKEY" | wg set "\$WG_IFACE" private-key /dev/stdin listen-port ${WG_PORT}
ip link set up dev "\$WG_IFACE"

DEFAULT_IFACE=\$(ip route show default | awk '/default/ {print \$5}' | head -1)
sysctl -w net.ipv4.ip_forward=1 >/dev/null
iptables -t nat -A POSTROUTING -s 10.100.0.0/16 -o "\$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
iptables -A FORWARD -i "\$WG_IFACE" -j ACCEPT 2>/dev/null || true
iptables -A FORWARD -o "\$WG_IFACE" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
WEOF
chmod +x /usr/local/bin/valhalla-wg-up

cat > /etc/systemd/system/valhalla-wg.service <<SEOF3
[Unit]
Description=Valhalla WireGuard
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/valhalla-wg-up
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
SEOF3

systemctl daemon-reload
systemctl enable valhalla-xray valhalla-heartbeat valhalla-wg >/dev/null 2>&1
systemctl restart valhalla-xray
systemctl restart valhalla-heartbeat

log "All services running"

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
echo -e "  Server:      ${CYAN}${SERVER_IP}${NC}"
echo -e "  WireGuard:   ${CYAN}${INTERNAL_IP}${NC} (mesh)  :${WG_PORT}/udp"
echo -e "  VLESS:       ${CYAN}:${XRAY_PORT}/tcp${NC}  Reality+Vision"
echo -e "  Node ID:     ${CYAN}${NODE_ID}${NC}"
echo ""
echo -e "  All account devices will route through this node."
echo ""
echo -e "  ${YELLOW}VLESS share link:${NC}"
echo -e "  ${SHARE_LINK}"
echo ""
if command -v qrencode &>/dev/null; then
    qrencode -t ANSIUTF8 "$SHARE_LINK"
    echo ""
fi
echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
echo -e "  systemctl status valhalla-xray"
echo -e "  systemctl status valhalla-heartbeat"
echo -e "  journalctl -u valhalla-xray -f"
echo -e "${CYAN}══════════════════════════════════════════════════${NC}"

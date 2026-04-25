#!/bin/bash
set -euo pipefail

# Valhalla Client Node Registration
# Authenticates against the control-plane (login → fallback to register),
# generates a WG keypair, registers this VM as a node, saves env to
# /etc/valhalla/client.env so the test client can `source` it and run.
#
# Run AFTER client.sh (which installs the binary). Idempotent only in the
# sense that re-running registers ANOTHER node on the same account — each
# call yields a new node_id. Don't run twice on the same VM unless you want
# multiple nodes for that VM.
#
# Required env:
#   VALHALLA_EMAIL=you@example.com   # same on both VMs to share account
#   VALHALLA_PASS=<password>
#
# Optional env:
#   VALHALLA_CONTROL=http://144.48.10.51:8443   # default
#   VALHALLA_NODE_NAME=<hostname>               # default = `hostname`
#   VALHALLA_EXIT_LINK=vless://...              # user's exit-node, persisted
#                                               # to client.env so valhalla-client
#                                               # picks it up automatically.
#                                               # Required when the relay's IP
#                                               # is DPI-blocked.
#
# One-liner:
#   curl -fsSL https://raw.githubusercontent.com/AdobeFilter/res/main/valhalla-client/install/register.sh \
#     | sudo VALHALLA_EMAIL=you@example.com VALHALLA_PASS=secret bash

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

log()   { echo -e "${GREEN}[+]${NC} $1"; }
warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[-]${NC} $1"; exit 1; }

[[ $EUID -ne 0 ]] && error "must run as root"

CONTROL="${VALHALLA_CONTROL:-http://144.48.10.51:8443}"
EMAIL="${VALHALLA_EMAIL:-}"
PASSWORD="${VALHALLA_PASS:-}"
NODE_NAME="${VALHALLA_NODE_NAME:-$(hostname)}"

[[ -z "$EMAIL" ]] && error "set VALHALLA_EMAIL (use the SAME email on both VMs to share an account)"
[[ -z "$PASSWORD" ]] && error "set VALHALLA_PASS"

# Tools
need=()
command -v wg   >/dev/null || need+=(wireguard-tools)
command -v jq   >/dev/null || need+=(jq)
command -v curl >/dev/null || need+=(curl)
if [ ${#need[@]} -gt 0 ]; then
    log "installing: ${need[*]}"
    apt-get update -qq
    apt-get install -y -qq "${need[@]}"
fi

# 1. Auth: login first; if that fails, try register. Lets the second VM
#    reuse the account the first VM created without separate flags.
log "trying login as $EMAIL"
auth=$(curl -sS -X POST "$CONTROL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" || true)

if [ -z "$auth" ] || ! echo "$auth" | jq -e '.token' >/dev/null 2>&1; then
    log "login failed, registering new account"
    auth=$(curl -fsS -X POST "$CONTROL/api/v1/auth/register" \
      -H 'Content-Type: application/json' \
      -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}") \
      || error "register failed: $auth"
fi

TOKEN=$(echo "$auth" | jq -r .token)
ACCOUNT=$(echo "$auth" | jq -r .account_id)
[[ "$TOKEN" == "null" || -z "$TOKEN" ]] && error "auth response missing token: $auth"
log "auth OK (account $ACCOUNT)"

# 2. WG keypair (private stays here; only public goes to control-plane)
PRIV=$(wg genkey)
PUB=$(echo -n "$PRIV" | wg pubkey)
log "WG pubkey: $PUB"

# 3. Node register
log "registering node name='$NODE_NAME' type=client"
node=$(curl -fsS -X POST "$CONTROL/api/v1/nodes/register" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"$NODE_NAME\",\"node_type\":\"client\",\"os\":\"linux\",\"public_key\":\"$PUB\"}") \
  || error "node register failed: $node"

NODE_ID=$(echo "$node" | jq -r .node_id)
INTERNAL_IP=$(echo "$node" | jq -r .internal_ip)
[[ "$NODE_ID" == "null" || -z "$NODE_ID" ]] && error "node register response missing node_id: $node"

# 4. Persist env for the test client
mkdir -p /etc/valhalla
cat > /etc/valhalla/client.env <<ENV
VALHALLA_CONTROL="$CONTROL"
VALHALLA_TOKEN="$TOKEN"
VALHALLA_SELF_NODE="$NODE_ID"
VALHALLA_SELF_IP="$INTERNAL_IP"
VALHALLA_WG_KEY="$PRIV"
VALHALLA_EXIT_LINK="${VALHALLA_EXIT_LINK:-}"
ENV
chmod 600 /etc/valhalla/client.env
# When invoked via `sudo ... bash`, hand ownership back to the invoking user
# so they can `source` the env without sudo. Carries the WG private key, so
# we keep mode 600.
if [ -n "${SUDO_USER:-}" ] && id -u "$SUDO_USER" >/dev/null 2>&1; then
    chown "$SUDO_USER:$(id -gn "$SUDO_USER")" /etc/valhalla/client.env
fi

log "================================="
log "Node registered:"
log "  node_id:    $NODE_ID"
log "  mesh IP:    $INTERNAL_IP"
log "  WG pubkey:  $PUB"
log "  env saved:  /etc/valhalla/client.env"
log ""
log "On THIS VM (server side) — paste as ONE line:"
log "  set -a && source /etc/valhalla/client.env && set +a && valhalla-client -mode server"
log ""
log "On the OTHER VM (client side), pass THIS node_id as -target:"
log "  set -a && source /etc/valhalla/client.env && set +a && valhalla-client -mode client -target $NODE_ID"
log "================================="

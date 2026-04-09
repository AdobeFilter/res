#!/bin/bash
set -euo pipefail

# ============================================================
#  Valhalla — Remote Exit Node Deploy via SSH
#  Usage: bash remote-deploy.sh <user> <host> [port]
#
#  Connects to the server, uploads and runs xray-exit.sh
#  Returns the VLESS share link for the app.
# ============================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $1"; }
err()  { echo -e "${RED}[-]${NC} $1"; exit 1; }

[[ $# -lt 2 ]] && err "Usage: $0 <user> <host> [ssh-port]"

SSH_USER="$1"
SSH_HOST="$2"
SSH_PORT="${3:-22}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEPLOY_SCRIPT="${SCRIPT_DIR}/xray-exit.sh"

[[ ! -f "$DEPLOY_SCRIPT" ]] && err "xray-exit.sh not found in ${SCRIPT_DIR}"

log "Uploading deploy script to ${SSH_USER}@${SSH_HOST}..."
scp -P "$SSH_PORT" "$DEPLOY_SCRIPT" "${SSH_USER}@${SSH_HOST}:/tmp/xray-exit.sh"

log "Running deploy on remote server..."
ssh -t -p "$SSH_PORT" "${SSH_USER}@${SSH_HOST}" "sudo bash /tmp/xray-exit.sh && rm /tmp/xray-exit.sh"

log "Done! Check the output above for your VLESS share link."

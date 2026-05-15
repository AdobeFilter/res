#!/bin/bash
set -euo pipefail

# Valhalla Control Plane Backup
# Dumps the database, packs it together with the env file and JWT secret,
# encrypts the archive with GPG using the passphrase from
# /opt/valhalla/etc/backup-passphrase, and writes it to /opt/valhalla/backups/.
# Backups older than RETENTION_DAYS (default 14) are deleted.

VALHALLA_ROOT=/opt/valhalla
VALHALLA_ETC="${VALHALLA_ROOT}/etc"
VALHALLA_BACKUPS="${VALHALLA_ROOT}/backups"
PASSPHRASE_FILE="${VALHALLA_ETC}/backup-passphrase"
RETENTION_DAYS="${RETENTION_DAYS:-14}"

[[ -r "${PASSPHRASE_FILE}" ]] || { echo "missing ${PASSPHRASE_FILE}" >&2; exit 1; }

TS=$(date +%Y%m%d-%H%M%S)
WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT

# 1. dump database (custom format, includes schema + data)
sudo -u postgres pg_dump -Fc valhalla > "${WORK}/valhalla.dump"

# 2. copy secrets (env contains DATABASE_URL with the DB password)
cp "${VALHALLA_ETC}/control-plane.env" "${WORK}/control-plane.env"
cp "${VALHALLA_ETC}/jwt-secret"        "${WORK}/jwt-secret"

# 2a. relay allowlist — operator-managed source of truth for who may
#     self-register a relay. Missing file is OK (open dogfood mode);
#     omitted from the archive in that case so restore mirrors install state.
ALLOWLIST_PRESENT=0
if [[ -f "${VALHALLA_ETC}/allowed-relays.txt" ]]; then
    cp "${VALHALLA_ETC}/allowed-relays.txt" "${WORK}/allowed-relays.txt"
    ALLOWLIST_PRESENT=1
fi

# 3. relay roster snapshot — human-readable CSV alongside the binary
#    pg_dump. Lets an operator inspect "who was registered on what date"
#    by extracting one file instead of running pg_restore.
sudo -u postgres psql valhalla -c \
    "\copy (SELECT id, address, port, vless_port, capacity, active_sessions, last_seen, reality_sni FROM relay_servers ORDER BY last_seen DESC NULLS LAST) TO STDOUT WITH CSV HEADER" \
    > "${WORK}/relays.csv"

# 4. metadata for restore sanity-check
{
    echo "backup_ts=${TS}"
    echo "hostname=$(hostname)"
    echo "pg_version=$(sudo -u postgres psql -tAc 'SHOW server_version;')"
    echo "relay_count=$(($(wc -l < "${WORK}/relays.csv") - 1))"
    echo "allowlist_present=${ALLOWLIST_PRESENT}"
    if [[ ${ALLOWLIST_PRESENT} -eq 1 ]]; then
        echo "allowlist_entries=$(grep -cvE '^\s*(#|$)' "${WORK}/allowed-relays.txt" || true)"
    fi
} > "${WORK}/meta"

# 5. tar + gpg symmetric encrypt
TAR="${WORK}/cp-${TS}.tar.gz"
TAR_FILES=(valhalla.dump control-plane.env jwt-secret relays.csv meta)
[[ ${ALLOWLIST_PRESENT} -eq 1 ]] && TAR_FILES+=(allowed-relays.txt)
tar czf "${TAR}" -C "${WORK}" "${TAR_FILES[@]}"

OUT="${VALHALLA_BACKUPS}/cp-${TS}.tar.gz.gpg"
gpg --batch --yes --quiet \
    --passphrase-file "${PASSPHRASE_FILE}" \
    --symmetric --cipher-algo AES256 \
    --output "${OUT}" \
    "${TAR}"

chmod 600 "${OUT}"

# 6. retention
find "${VALHALLA_BACKUPS}" -name 'cp-*.tar.gz.gpg' -mtime "+${RETENTION_DAYS}" -delete

echo "backup ok: ${OUT} ($(du -h "${OUT}" | cut -f1))"

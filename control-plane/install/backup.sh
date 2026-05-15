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

# 3. metadata for restore sanity-check
{
    echo "backup_ts=${TS}"
    echo "hostname=$(hostname)"
    echo "pg_version=$(sudo -u postgres psql -tAc 'SHOW server_version;')"
} > "${WORK}/meta"

# 4. tar + gpg symmetric encrypt
TAR="${WORK}/cp-${TS}.tar.gz"
tar czf "${TAR}" -C "${WORK}" valhalla.dump control-plane.env jwt-secret meta

OUT="${VALHALLA_BACKUPS}/cp-${TS}.tar.gz.gpg"
gpg --batch --yes --quiet \
    --passphrase-file "${PASSPHRASE_FILE}" \
    --symmetric --cipher-algo AES256 \
    --output "${OUT}" \
    "${TAR}"

chmod 600 "${OUT}"

# 5. retention
find "${VALHALLA_BACKUPS}" -name 'cp-*.tar.gz.gpg' -mtime "+${RETENTION_DAYS}" -delete

echo "backup ok: ${OUT} ($(du -h "${OUT}" | cut -f1))"

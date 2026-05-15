#!/bin/bash
set -euo pipefail

# Valhalla Control Plane Restore
# Usage: sudo restore.sh <backup.tar.gz.gpg>
#
# Decrypts the backup, replaces /opt/valhalla/etc/{control-plane.env,jwt-secret},
# resets the postgres 'valhalla' user password to match the one inside the
# restored env file, drops & recreates the database, and loads the dump.
#
# Passphrase: read from /opt/valhalla/etc/backup-passphrase if present
# (same server), otherwise prompted on stdin (migration to a new server).

VALHALLA_ROOT=/opt/valhalla
VALHALLA_ETC="${VALHALLA_ROOT}/etc"
PASSPHRASE_FILE="${VALHALLA_ETC}/backup-passphrase"

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
[[ $# -eq 1 ]]   || { echo "usage: $0 <backup.tar.gz.gpg>" >&2; exit 1; }
BACKUP="$1"
[[ -r "${BACKUP}" ]] || { echo "cannot read ${BACKUP}" >&2; exit 1; }

# Resolve passphrase
if [[ -r "${PASSPHRASE_FILE}" ]]; then
    GPG_PASS_ARGS=(--passphrase-file "${PASSPHRASE_FILE}")
else
    read -s -p "Backup passphrase: " PASS
    echo
    GPG_PASS_ARGS=(--passphrase "${PASS}")
fi

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT

# 1. decrypt + extract
gpg --batch --quiet "${GPG_PASS_ARGS[@]}" --decrypt "${BACKUP}" > "${WORK}/cp.tar.gz"
tar xzf "${WORK}/cp.tar.gz" -C "${WORK}"

for f in valhalla.dump control-plane.env jwt-secret; do
    [[ -f "${WORK}/${f}" ]] || { echo "backup is missing ${f}" >&2; exit 1; }
done

# 2. extract DB password from restored env (DATABASE_URL=postgres://valhalla:PASSWORD@...)
DB_PW=$(grep -E '^DATABASE_URL=' "${WORK}/control-plane.env" \
        | sed -E 's|^DATABASE_URL=postgres://valhalla:([^@]+)@.*|\1|')
[[ -n "${DB_PW}" ]] || { echo "could not parse DB password from restored env" >&2; exit 1; }

# 3. stop service before touching DB / secrets
systemctl stop valhalla-control || true

# 4. ensure PG role + db, set password to match restored env
if sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='valhalla'" | grep -q 1; then
    sudo -u postgres psql -c "ALTER USER valhalla WITH PASSWORD '${DB_PW}';" >/dev/null
else
    sudo -u postgres psql -c "CREATE USER valhalla WITH PASSWORD '${DB_PW}';" >/dev/null
fi

# Disconnect everyone and recreate DB cleanly
sudo -u postgres psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='valhalla' AND pid<>pg_backend_pid();" >/dev/null || true
sudo -u postgres psql -c "DROP DATABASE IF EXISTS valhalla;" >/dev/null
sudo -u postgres psql -c "CREATE DATABASE valhalla OWNER valhalla;" >/dev/null
sudo -u postgres pg_restore -d valhalla --no-owner --role=valhalla "${WORK}/valhalla.dump"

# 5. replace secrets
install -m 600 -o valhalla -g valhalla "${WORK}/control-plane.env" "${VALHALLA_ETC}/control-plane.env"
install -m 600 -o valhalla -g valhalla "${WORK}/jwt-secret"        "${VALHALLA_ETC}/jwt-secret"

# 6. start service
systemctl start valhalla-control

echo "restore ok"
echo "service status:"
systemctl --no-pager --lines=5 status valhalla-control || true

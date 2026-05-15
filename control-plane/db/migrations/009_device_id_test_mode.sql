-- Temporarily relax device_id uniqueness for testing. The service-level
-- antifraud check is gated by the ANTIFRAUD_ENABLED env var; this migration
-- removes the DB-level enforcement so the same phone can register multiple
-- accounts during dogfooding without hitting a constraint violation.
--
-- To re-enable antifraud later, ship a migration 010 that:
--   1. Resolves any duplicate device_ids that accumulated during testing
--      (keep oldest, null-out newer), and
--   2. Re-creates the global unique index from migration 008.
-- Then set ANTIFRAUD_ENABLED=true in /opt/valhalla/etc/control-plane.env
-- and restart the service.

DROP INDEX IF EXISTS idx_nodes_device_id_global;

-- Keep a non-unique index for fast lookups (used by GetByDeviceID and the
-- per-account device count). Without this, both paths would seq-scan.
CREATE INDEX IF NOT EXISTS idx_nodes_device_id
    ON nodes(device_id)
    WHERE device_id IS NOT NULL;

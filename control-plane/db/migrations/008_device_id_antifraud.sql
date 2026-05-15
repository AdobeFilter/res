-- Move device_id uniqueness from per-account to global. Prevents one device
-- from being linked to multiple accounts (registering N free accounts on the
-- same phone for free-tier multiplication).

-- 1. Resolve duplicates if any pre-existed: keep oldest node per device_id,
--    null-out device_id on newer duplicates. Without this the unique index
--    creation would fail.
WITH ranked AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY device_id ORDER BY created_at) AS rn
    FROM nodes
    WHERE device_id IS NOT NULL
)
UPDATE nodes SET device_id = NULL
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

-- 2. Replace per-account uniqueness with global uniqueness.
DROP INDEX IF EXISTS idx_nodes_device_id;
CREATE UNIQUE INDEX idx_nodes_device_id_global
    ON nodes(device_id)
    WHERE device_id IS NOT NULL;

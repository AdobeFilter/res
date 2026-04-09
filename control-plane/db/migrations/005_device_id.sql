ALTER TABLE nodes ADD COLUMN IF NOT EXISTS device_id VARCHAR(64);
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_device_id ON nodes(account_id, device_id) WHERE device_id IS NOT NULL;

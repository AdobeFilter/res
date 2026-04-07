-- Sort order for nodes per account
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS sort_order INTEGER NOT NULL DEFAULT 0;

-- Shared folder path per node (for file sharing)
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS shared_folder VARCHAR(512);

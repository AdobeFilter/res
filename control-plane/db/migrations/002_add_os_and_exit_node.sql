-- Add OS field to nodes
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS os VARCHAR(50);

-- Add exit_node_id to account_settings
ALTER TABLE account_settings ADD COLUMN IF NOT EXISTS exit_node_id UUID REFERENCES nodes(id) ON DELETE SET NULL;

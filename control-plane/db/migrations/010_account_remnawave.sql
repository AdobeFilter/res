-- Add subscription / quota linkage to accounts. Each account becomes a
-- Remnawave user on register (handled in handler/auth.Register); the UUID
-- and subscription URL are stored here so we don't have to look them up by
-- email on every request.

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS tier             VARCHAR(20) NOT NULL DEFAULT 'free';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS remnawave_uuid   UUID;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS subscription_url TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_remnawave_uuid
    ON accounts(remnawave_uuid)
    WHERE remnawave_uuid IS NOT NULL;

-- Extends account_settings so the server holds the full per-account
-- configuration the client used to keep only in SharedPreferences.
-- exit_nodes: ordered list of VLESS/VMess/Trojan/SS endpoints the user added
-- routing_rules: full xray routing JSON the client builds (freeform text)
-- fragment_enabled / block_ads_enabled: feature flags shown in Settings

ALTER TABLE account_settings
    ADD COLUMN exit_nodes        JSONB   NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN routing_rules     TEXT    NOT NULL DEFAULT '',
    ADD COLUMN fragment_enabled  BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN block_ads_enabled BOOLEAN NOT NULL DEFAULT FALSE;

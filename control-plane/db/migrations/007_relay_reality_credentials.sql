-- Each relay-node owns a VLESS+Reality endpoint that clients connect to for
-- mesh traffic when they're in Xray mode. The Reality keypair is generated
-- once when the relay first registers and persisted here — rotating it would
-- invalidate every device's stored mesh-endpoint config.
--
-- vless_port is stored separately from `port` (which is the UDP WG-relay
-- port) because they're different services on the same host.

ALTER TABLE relay_servers
    ADD COLUMN vless_port           INTEGER      NOT NULL DEFAULT 443,
    ADD COLUMN reality_private_key  TEXT         NOT NULL DEFAULT '',
    ADD COLUMN reality_public_key   TEXT         NOT NULL DEFAULT '',
    ADD COLUMN reality_short_ids    TEXT         NOT NULL DEFAULT '',
    ADD COLUMN reality_sni          VARCHAR(255) NOT NULL DEFAULT 'www.microsoft.com',
    ADD COLUMN vless_uuid           UUID;

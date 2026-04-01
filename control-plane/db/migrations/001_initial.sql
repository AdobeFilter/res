CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Accounts
CREATE TABLE accounts (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    email           VARCHAR(255) UNIQUE NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Account-level settings (synced across all devices)
CREATE TABLE account_settings (
    account_id      UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    vless_enabled   BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Nodes in the mesh network
CREATE TABLE nodes (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_id      UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name            VARCHAR(255) NOT NULL,
    node_type       VARCHAR(20) NOT NULL CHECK (node_type IN ('client', 'relay', 'exit')),
    public_key      VARCHAR(64) NOT NULL UNIQUE,
    endpoint        VARCHAR(255),
    nat_type        VARCHAR(30),
    internal_ip     INET,
    status          VARCHAR(20) NOT NULL DEFAULT 'offline' CHECK (status IN ('online', 'offline', 'degraded')),
    last_seen       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_nodes_account ON nodes(account_id);
CREATE INDEX idx_nodes_status ON nodes(status);
CREATE INDEX idx_nodes_type ON nodes(node_type);

-- Time-series node metrics
CREATE TABLE node_metrics (
    id              BIGSERIAL PRIMARY KEY,
    node_id         UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    rtt_ms          REAL,
    bandwidth_mbps  REAL,
    cpu_percent     REAL,
    active_conns    INTEGER,
    packet_loss     REAL,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_metrics_node_time ON node_metrics(node_id, recorded_at DESC);

-- Calculated routes
CREATE TABLE routes (
    id              BIGSERIAL PRIMARY KEY,
    src_node_id     UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    dst_node_id     UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    path            UUID[] NOT NULL,
    cost            REAL NOT NULL,
    connection_type VARCHAR(20) NOT NULL CHECK (connection_type IN ('direct', 'stun', 'relay')),
    relay_node_id   UUID REFERENCES nodes(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_routes_src_dst ON routes(src_node_id, dst_node_id);

-- Auth sessions
CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_id      UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash      VARCHAR(255) NOT NULL,
    device_info     VARCHAR(255),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_sessions_account ON sessions(account_id);

-- Registered STUN servers
CREATE TABLE stun_servers (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    address         VARCHAR(255) NOT NULL,
    port            INTEGER NOT NULL,
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(address, port)
);

-- Registered relay servers
CREATE TABLE relay_servers (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    address         VARCHAR(255) NOT NULL,
    port            INTEGER NOT NULL,
    capacity        INTEGER NOT NULL DEFAULT 1000,
    active_sessions INTEGER NOT NULL DEFAULT 0,
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(address, port)
);

-- IP allocation tracker
CREATE TABLE ip_allocations (
    ip              INET PRIMARY KEY,
    node_id         UUID UNIQUE REFERENCES nodes(id) ON DELETE SET NULL,
    allocated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

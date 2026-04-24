package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"valhalla/common/api"
	"valhalla/common/crypto"
)

type pgMetricsRepo struct {
	pool *pgxpool.Pool
}

func NewMetricsRepository(pool *pgxpool.Pool) MetricsRepository {
	return &pgMetricsRepo{pool: pool}
}

func (r *pgMetricsRepo) Insert(ctx context.Context, m *api.Metrics) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO node_metrics (node_id, rtt_ms, bandwidth_mbps, cpu_percent, active_conns, packet_loss)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		m.NodeID, m.RTTMs, m.BandwidthMbps, m.CPUPercent, m.ActiveConns, m.PacketLoss,
	)
	if err != nil {
		return fmt.Errorf("insert metrics: %w", err)
	}
	return nil
}

func (r *pgMetricsRepo) GetLatest(ctx context.Context, nodeID string) (*api.Metrics, error) {
	var m api.Metrics
	err := r.pool.QueryRow(ctx,
		`SELECT node_id, rtt_ms, bandwidth_mbps, cpu_percent, active_conns, packet_loss, recorded_at
		 FROM node_metrics WHERE node_id=$1 ORDER BY recorded_at DESC LIMIT 1`,
		nodeID,
	).Scan(&m.NodeID, &m.RTTMs, &m.BandwidthMbps, &m.CPUPercent, &m.ActiveConns, &m.PacketLoss, &m.RecordedAt)
	if err == pgx.ErrNoRows {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get latest metrics: %w", err)
	}
	return &m, nil
}

func (r *pgMetricsRepo) GetAllLatest(ctx context.Context) (map[string]*api.Metrics, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT ON (node_id) node_id, rtt_ms, bandwidth_mbps, cpu_percent, active_conns, packet_loss, recorded_at
		 FROM node_metrics
		 WHERE recorded_at > $1
		 ORDER BY node_id, recorded_at DESC`,
		time.Now().Add(-5*time.Minute),
	)
	if err != nil {
		return nil, fmt.Errorf("get all latest metrics: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*api.Metrics)
	for rows.Next() {
		var m api.Metrics
		if err := rows.Scan(&m.NodeID, &m.RTTMs, &m.BandwidthMbps, &m.CPUPercent, &m.ActiveConns, &m.PacketLoss, &m.RecordedAt); err != nil {
			return nil, err
		}
		result[m.NodeID] = &m
	}
	return result, rows.Err()
}

// --- IP Allocator ---

type pgIPAllocator struct {
	pool    *pgxpool.Pool
	meshNet string // e.g., "10.100.0.0/16"
}

func NewIPAllocator(pool *pgxpool.Pool, meshCIDR string) IPAllocator {
	return &pgIPAllocator{pool: pool, meshNet: meshCIDR}
}

func (a *pgIPAllocator) FindAvailable(ctx context.Context) (string, error) {
	var ip string
	err := a.pool.QueryRow(ctx,
		`SELECT host(candidate)::text
		FROM generate_series(1, (1 << (32 - masklen($1::cidr))) - 2) AS s(off),
		     LATERAL (SELECT set_masklen(($1::inet + s.off)::inet, 32) AS candidate) sub
		LEFT JOIN ip_allocations ia ON host(ia.ip) = host(sub.candidate)
		LEFT JOIN nodes n ON host(n.internal_ip) = host(sub.candidate)
		WHERE ia.ip IS NULL AND n.internal_ip IS NULL
		LIMIT 1`,
		a.meshNet,
	).Scan(&ip)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("no available IPs in %s", a.meshNet)
	}
	if err != nil {
		return "", fmt.Errorf("find available IP: %w", err)
	}
	return ip, nil
}

func (a *pgIPAllocator) Allocate(ctx context.Context, nodeID string) (string, error) {
	ip, err := a.FindAvailable(ctx)
	if err != nil {
		return "", err
	}
	if err := a.AssignIP(ctx, ip, nodeID); err != nil {
		return "", err
	}
	return ip, nil
}

func (a *pgIPAllocator) AssignIP(ctx context.Context, ip string, nodeID string) error {
	_, err := a.pool.Exec(ctx,
		`INSERT INTO ip_allocations (ip, node_id) VALUES ($1::inet, $2::uuid)
		 ON CONFLICT (ip) DO UPDATE SET node_id=$2::uuid, allocated_at=NOW()`,
		ip, nodeID,
	)
	if err != nil {
		return fmt.Errorf("assign IP: %w", err)
	}
	return nil
}

func (a *pgIPAllocator) Release(ctx context.Context, nodeID string) error {
	_, err := a.pool.Exec(ctx,
		`DELETE FROM ip_allocations WHERE node_id=$1`, nodeID)
	return err
}

// --- STUN/Relay Server Repos ---

type pgSTUNRepo struct {
	pool *pgxpool.Pool
}

func NewSTUNServerRepository(pool *pgxpool.Pool) STUNServerRepository {
	return &pgSTUNRepo{pool: pool}
}

func (r *pgSTUNRepo) Upsert(ctx context.Context, address string, port int) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO stun_servers (address, port, last_seen)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (address, port) DO UPDATE SET last_seen=NOW()`,
		address, port,
	)
	return err
}

func (r *pgSTUNRepo) GetAll(ctx context.Context) ([]api.STUNServer, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT address, port FROM stun_servers WHERE last_seen > NOW() - INTERVAL '5 minutes'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []api.STUNServer
	for rows.Next() {
		var s api.STUNServer
		if err := rows.Scan(&s.Address, &s.Port); err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	return servers, rows.Err()
}

type pgRelayRepo struct {
	pool *pgxpool.Pool
}

func NewRelayServerRepository(pool *pgxpool.Pool) RelayServerRepository {
	return &pgRelayRepo{pool: pool}
}

func (r *pgRelayRepo) UpsertWithCredentials(
	ctx context.Context,
	id, address string,
	port, vlessPort, capacity int,
) (*RelayCredentials, error) {
	// Try to read existing credentials first — same (address, port) means
	// same relay, keep its keys stable across restarts.
	var existing RelayCredentials
	var uuidStr *string
	err := r.pool.QueryRow(ctx,
		`SELECT vless_uuid, reality_private_key, reality_public_key,
		        reality_short_ids, reality_sni
		 FROM relay_servers WHERE address=$1 AND port=$2`,
		address, port,
	).Scan(&uuidStr, &existing.RealityPrivateKey, &existing.RealityPublicKey,
		&existing.RealityShortIDs, &existing.RealitySNI)

	if err == nil && uuidStr != nil && *uuidStr != "" && existing.RealityPrivateKey != "" {
		existing.VLESSUUID = *uuidStr
		// Still refresh last_seen and capacity/port changes.
		_, upErr := r.pool.Exec(ctx,
			`UPDATE relay_servers SET capacity=$3, vless_port=$4, last_seen=NOW()
			 WHERE address=$1 AND port=$2`,
			address, port, capacity, vlessPort,
		)
		if upErr != nil {
			return nil, fmt.Errorf("refresh relay heartbeat: %w", upErr)
		}
		return &existing, nil
	}
	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("read relay credentials: %w", err)
	}

	// Either first registration for this relay or credentials never got
	// generated (nulls from the migration default). Generate a fresh set.
	kp, err := crypto.GenerateRealityKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate reality keypair: %w", err)
	}
	shortID, err := crypto.GenerateRealityShortID(8)
	if err != nil {
		return nil, fmt.Errorf("generate short id: %w", err)
	}
	// Postgres' uuid_generate_v4() is available (uuid-ossp extension is
	// installed in migration 001), so we delegate UUID minting to the DB.
	sni := "www.microsoft.com"

	_, err = r.pool.Exec(ctx,
		`INSERT INTO relay_servers
		   (id, address, port, vless_port, capacity, last_seen,
		    vless_uuid, reality_private_key, reality_public_key,
		    reality_short_ids, reality_sni)
		 VALUES ($1, $2, $3, $4, $5, NOW(), uuid_generate_v4(), $6, $7, $8, $9)
		 ON CONFLICT (address, port) DO UPDATE SET
		   capacity   = EXCLUDED.capacity,
		   vless_port = EXCLUDED.vless_port,
		   last_seen  = NOW(),
		   vless_uuid = COALESCE(relay_servers.vless_uuid, uuid_generate_v4()),
		   reality_private_key = CASE WHEN relay_servers.reality_private_key = '' THEN EXCLUDED.reality_private_key ELSE relay_servers.reality_private_key END,
		   reality_public_key  = CASE WHEN relay_servers.reality_public_key  = '' THEN EXCLUDED.reality_public_key  ELSE relay_servers.reality_public_key  END,
		   reality_short_ids   = CASE WHEN relay_servers.reality_short_ids   = '' THEN EXCLUDED.reality_short_ids   ELSE relay_servers.reality_short_ids   END,
		   reality_sni         = CASE WHEN relay_servers.reality_sni         = '' THEN EXCLUDED.reality_sni         ELSE relay_servers.reality_sni         END`,
		id, address, port, vlessPort, capacity,
		kp.PrivateKey, kp.PublicKey, shortID, sni,
	)
	if err != nil {
		return nil, fmt.Errorf("insert relay: %w", err)
	}

	// Read-back — in case the ON CONFLICT branch kept existing values.
	var outUUID string
	out := &RelayCredentials{}
	err = r.pool.QueryRow(ctx,
		`SELECT vless_uuid::text, reality_private_key, reality_public_key,
		        reality_short_ids, reality_sni
		 FROM relay_servers WHERE address=$1 AND port=$2`,
		address, port,
	).Scan(&outUUID, &out.RealityPrivateKey, &out.RealityPublicKey,
		&out.RealityShortIDs, &out.RealitySNI)
	if err != nil {
		return nil, fmt.Errorf("read-back relay credentials: %w", err)
	}
	out.VLESSUUID = outUUID
	return out, nil
}

func (r *pgRelayRepo) GetAll(ctx context.Context) ([]api.RelayServer, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, address, port, vless_port, capacity,
		        reality_public_key, reality_short_ids, reality_sni,
		        COALESCE(vless_uuid::text, '')
		 FROM relay_servers WHERE last_seen > NOW() - INTERVAL '5 minutes'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []api.RelayServer
	for rows.Next() {
		var s api.RelayServer
		if err := rows.Scan(&s.ID, &s.Address, &s.Port, &s.VLESSPort, &s.Capacity,
			&s.RealityPublicKey, &s.RealityShortIDs, &s.RealitySNI, &s.VLESSUUID); err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	return servers, rows.Err()
}

func (r *pgRelayRepo) GetBestAvailable(ctx context.Context) (*api.RelayServer, error) {
	var s api.RelayServer
	err := r.pool.QueryRow(ctx,
		`SELECT id, address, port, vless_port, capacity,
		        reality_public_key, reality_short_ids, reality_sni,
		        COALESCE(vless_uuid::text, '')
		 FROM relay_servers
		 WHERE last_seen > NOW() - INTERVAL '5 minutes'
		   AND active_sessions < capacity
		 ORDER BY (capacity - active_sessions) DESC
		 LIMIT 1`,
	).Scan(&s.ID, &s.Address, &s.Port, &s.VLESSPort, &s.Capacity,
		&s.RealityPublicKey, &s.RealityShortIDs, &s.RealitySNI, &s.VLESSUUID)
	if err == pgx.ErrNoRows {
		return nil, api.ErrRelayUnavailable
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"valhalla/common/api"
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

func (a *pgIPAllocator) Allocate(ctx context.Context, nodeID string) (string, error) {
	// Find next available IP in the mesh CIDR range
	var ip string
	err := a.pool.QueryRow(ctx,
		`WITH mesh AS (
			SELECT host(ip)::text AS ip
			FROM generate_series(
				($1::inet + 1)::inet,
				(broadcast($1::inet) - 1)::inet,
				1
			) AS ip
		)
		SELECT mesh.ip FROM mesh
		LEFT JOIN ip_allocations ia ON ia.ip = mesh.ip::inet
		WHERE ia.ip IS NULL
		LIMIT 1`,
		a.meshNet,
	).Scan(&ip)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("no available IPs in %s", a.meshNet)
	}
	if err != nil {
		return "", fmt.Errorf("find available IP: %w", err)
	}

	_, err = a.pool.Exec(ctx,
		`INSERT INTO ip_allocations (ip, node_id) VALUES ($1::inet, $2)
		 ON CONFLICT (ip) DO UPDATE SET node_id=$2, allocated_at=NOW()`,
		ip, nodeID,
	)
	if err != nil {
		return "", fmt.Errorf("allocate IP: %w", err)
	}

	return ip, nil
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

func (r *pgRelayRepo) Upsert(ctx context.Context, id, address string, port, capacity int) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO relay_servers (id, address, port, capacity, last_seen)
		 VALUES ($1, $2, $3, $4, NOW())
		 ON CONFLICT (address, port) DO UPDATE SET capacity=$4, last_seen=NOW()`,
		id, address, port, capacity,
	)
	return err
}

func (r *pgRelayRepo) GetAll(ctx context.Context) ([]api.RelayServer, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, address, port, capacity FROM relay_servers WHERE last_seen > NOW() - INTERVAL '5 minutes'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []api.RelayServer
	for rows.Next() {
		var s api.RelayServer
		if err := rows.Scan(&s.ID, &s.Address, &s.Port, &s.Capacity); err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	return servers, rows.Err()
}

func (r *pgRelayRepo) GetBestAvailable(ctx context.Context) (*api.RelayServer, error) {
	var s api.RelayServer
	err := r.pool.QueryRow(ctx,
		`SELECT id, address, port, capacity FROM relay_servers
		 WHERE last_seen > NOW() - INTERVAL '5 minutes'
		   AND active_sessions < capacity
		 ORDER BY (capacity - active_sessions) DESC
		 LIMIT 1`,
	).Scan(&s.ID, &s.Address, &s.Port, &s.Capacity)
	if err == pgx.ErrNoRows {
		return nil, api.ErrRelayUnavailable
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Server
	ListenAddr string
	TLSCert    string
	TLSKey     string

	// Database
	DatabaseURL string

	// JWT
	JWTSecret     string
	TokenExpiry   time.Duration

	// Mesh network
	MeshCIDR      string // e.g. "10.100.0.0/16"

	// STUN
	STUNAddr    string
	STUNAltAddr string

	// Scheduler
	RouteRecalcInterval    time.Duration
	StaleNodeTimeout       time.Duration
	HeartbeatExpectedInterval time.Duration
	// OfflineNodeRetention is how long a node may stay offline before the
	// control-plane deletes it from the database (default 30 days).
	OfflineNodeRetention   time.Duration
	// OfflineNodeSweepInterval is how often the deleter checks (default 24h).
	OfflineNodeSweepInterval time.Duration

	// AntifraudEnabled gates the global device_id check: when true, a device
	// already linked to another account is rejected with 409 on register.
	// Defaults to false so dogfooding on the developer's own phone doesn't
	// lock them out when switching test accounts. Flipping to true also
	// requires running migration 010 to restore the global unique index.
	AntifraudEnabled bool

	// AllowedRelaysFile points at an operator-managed text file listing the
	// IPs that may self-register a relay. Empty disables the check (open
	// dogfood mode); set in production via the install script.
	AllowedRelaysFile string

	// Remnawave panel that hands out subscriptions and tracks per-user
	// traffic. Empty RemnawaveURL disables provisioning (e.g. when running
	// the control-plane standalone for tests).
	RemnawaveURL       string
	RemnawaveToken     string
	RemnawaveSquadUUID string // default internal-squad UUID to attach new users to
}

func Load() *Config {
	return &Config{
		ListenAddr:              getEnv("LISTEN_ADDR", ":8443"),
		TLSCert:                 getEnv("TLS_CERT", ""),
		TLSKey:                  getEnv("TLS_KEY", ""),
		DatabaseURL:             getEnv("DATABASE_URL", "postgres://valhalla:valhalla@localhost:5432/valhalla?sslmode=disable"),
		JWTSecret:               getEnv("JWT_SECRET", "change-me-in-production"),
		TokenExpiry:             getDurationEnv("TOKEN_EXPIRY", 24*time.Hour),
		MeshCIDR:                getEnv("MESH_CIDR", "10.100.0.0/16"),
		STUNAddr:                getEnv("STUN_ADDR", ":3478"),
		STUNAltAddr:             getEnv("STUN_ALT_ADDR", ":3479"),
		RouteRecalcInterval:       getDurationEnv("ROUTE_RECALC_INTERVAL", 30*time.Second),
		StaleNodeTimeout:          getDurationEnv("STALE_NODE_TIMEOUT", 90*time.Second),
		HeartbeatExpectedInterval: getDurationEnv("HEARTBEAT_INTERVAL", 15*time.Second),
		OfflineNodeRetention:      getDurationEnv("OFFLINE_NODE_RETENTION", 30*24*time.Hour),
		OfflineNodeSweepInterval:  getDurationEnv("OFFLINE_NODE_SWEEP_INTERVAL", 24*time.Hour),
		AntifraudEnabled:          getBoolEnv("ANTIFRAUD_ENABLED", false),
		AllowedRelaysFile:         getEnv("ALLOWED_RELAYS_FILE", ""),
		RemnawaveURL:              getEnv("REMNAWAVE_URL", ""),
		RemnawaveToken:            getEnv("REMNAWAVE_TOKEN", ""),
		RemnawaveSquadUUID:        getEnv("REMNAWAVE_SQUAD_UUID", ""),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if secs, err := strconv.Atoi(val); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	switch val {
	case "1", "true", "TRUE", "True", "yes":
		return true
	case "0", "false", "FALSE", "False", "no":
		return false
	}
	return fallback
}

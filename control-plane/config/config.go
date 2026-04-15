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

	// deSEC DNS (optional — for auto-domain on exit nodes)
	// Register free at desec.io, create a dedyn.io domain
	DNSApiToken string // deSEC API token
	DNSDomain   string // e.g. "valhalla.dedyn.io"
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
		RouteRecalcInterval:     getDurationEnv("ROUTE_RECALC_INTERVAL", 30*time.Second),
		StaleNodeTimeout:        getDurationEnv("STALE_NODE_TIMEOUT", 90*time.Second),
		HeartbeatExpectedInterval: getDurationEnv("HEARTBEAT_INTERVAL", 15*time.Second),
		DNSApiToken:              getEnv("DNS_API_TOKEN", ""),
		DNSDomain:                getEnv("DNS_DOMAIN", ""),
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

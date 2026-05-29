package config

import (
	"os"
	"strconv"
)

type Config struct {
	ControlPlaneURL string
	ListenAddr      string // UDP relay listen address
	TCPListenAddr   string // TCP fallback listen address
	Capacity        int    // max concurrent relay sessions
	PublicAddress   string // public IP for registration
	MeshListenAddr  string // addr the mesh dispatcher binds; ":9999" exposes it publicly so clients reach it directly through their exit-node
	// Mesh auth key is NOT a relay config — it arrives from the control-plane
	// in the register response (one secret, one place: CP's MESH_AUTH_KEY env).
}

func Load() *Config {
	return &Config{
		ControlPlaneURL: getEnv("CONTROL_PLANE_URL", "http://localhost:8443"),
		ListenAddr:      getEnv("LISTEN_ADDR", ":51821"),
		TCPListenAddr:   getEnv("TCP_LISTEN_ADDR", ":51822"),
		Capacity:        getIntEnv("CAPACITY", 1000),
		PublicAddress:   getEnv("PUBLIC_ADDRESS", ""),
		MeshListenAddr:  getEnv("MESH_LISTEN_ADDR", ":9999"),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getIntEnv(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

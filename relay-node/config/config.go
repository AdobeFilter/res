package config

import (
	"os"
	"strconv"
)

type Config struct {
	ControlPlaneURL string
	ListenAddr      string // UDP relay listen address
	TCPListenAddr   string // TCP fallback listen address
	VLESSListenAddr string // VLESS+Reality listen address
	Capacity        int    // max concurrent relay sessions
	PublicAddress   string // public IP for registration
}

func Load() *Config {
	return &Config{
		ControlPlaneURL: getEnv("CONTROL_PLANE_URL", "http://localhost:8443"),
		ListenAddr:      getEnv("LISTEN_ADDR", ":51821"),
		TCPListenAddr:   getEnv("TCP_LISTEN_ADDR", ":51822"),
		VLESSListenAddr: getEnv("VLESS_LISTEN_ADDR", ":443"),
		Capacity:        getIntEnv("CAPACITY", 1000),
		PublicAddress:   getEnv("PUBLIC_ADDRESS", ""),
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

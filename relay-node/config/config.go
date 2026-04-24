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
	VLESSPort       int    // parsed from VLESSListenAddr for registration
	XrayBinary      string // path to xray binary (default: "xray" in PATH)
	Capacity        int    // max concurrent relay sessions
	PublicAddress   string // public IP for registration
}

func Load() *Config {
	c := &Config{
		ControlPlaneURL: getEnv("CONTROL_PLANE_URL", "http://localhost:8443"),
		ListenAddr:      getEnv("LISTEN_ADDR", ":51821"),
		TCPListenAddr:   getEnv("TCP_LISTEN_ADDR", ":51822"),
		VLESSListenAddr: getEnv("VLESS_LISTEN_ADDR", ":443"),
		XrayBinary:      getEnv("XRAY_BINARY", "xray"),
		Capacity:        getIntEnv("CAPACITY", 1000),
		PublicAddress:   getEnv("PUBLIC_ADDRESS", ""),
	}
	// Extract numeric port from VLESSListenAddr (":443" -> 443).
	if parts := splitHostPort(c.VLESSListenAddr); parts != "" {
		if p, err := strconv.Atoi(parts); err == nil {
			c.VLESSPort = p
		}
	}
	if c.VLESSPort == 0 {
		c.VLESSPort = 443
	}
	return c
}

func splitHostPort(addr string) string {
	// Accepts ":443", "0.0.0.0:443", "[::]:443" — return the port part.
	if addr == "" {
		return ""
	}
	// Last colon separates port (good enough for ":N" and "HOST:N").
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return ""
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

package config

import "os"

type Config struct {
	ListenAddr      string // primary STUN port (UDP)
	AltListenAddr   string // alternate port (UDP)
	ControlPlaneURL string // control plane URL for self-registration
	PublicAddress   string // our public IP for registration
}

func Load() *Config {
	return &Config{
		ListenAddr:      getEnv("LISTEN_ADDR", ":3478"),
		AltListenAddr:   getEnv("ALT_LISTEN_ADDR", ":3479"),
		ControlPlaneURL: getEnv("CONTROL_PLANE_URL", "http://localhost:8443"),
		PublicAddress:   getEnv("PUBLIC_ADDRESS", ""),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

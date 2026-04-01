package config

import "os"

type Config struct {
	ControlPlaneURL string
	WireGuardPort   int
	WireGuardIface  string
	TokenFile       string
	ConfigFile      string
}

func Load() *Config {
	return &Config{
		ControlPlaneURL: getEnv("CONTROL_PLANE_URL", "http://localhost:8443"),
		WireGuardPort:   51820,
		WireGuardIface:  getEnv("WG_IFACE", "wg0"),
		TokenFile:       getEnv("TOKEN_FILE", "/etc/valhalla/token"),
		ConfigFile:      getEnv("CONFIG_FILE", "/etc/valhalla/node.yaml"),
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

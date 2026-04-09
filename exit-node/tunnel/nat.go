package tunnel

import (
	"fmt"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// NATManager handles iptables MASQUERADE configuration for exit traffic.
type NATManager struct {
	wgIface  string
	extIface string
	logger   *zap.Logger
}

func NewNATManager(wgIface string, logger *zap.Logger) *NATManager {
	extIface := detectDefaultInterface()
	if extIface == "" {
		extIface = "eth0"
	}
	return &NATManager{wgIface: wgIface, extIface: extIface, logger: logger}
}

// detectDefaultInterface finds the main outbound network interface.
func detectDefaultInterface() string {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// Enable sets up iptables rules for NAT and IP forwarding.
func (n *NATManager) Enable() error {
	// Enable IP forwarding
	cmds := [][]string{
		{"sysctl", "-w", "net.ipv4.ip_forward=1"},
		{"iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.100.0.0/16", "-o", n.extIface, "-j", "MASQUERADE"},
		{"iptables", "-A", "FORWARD", "-i", n.wgIface, "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-o", n.wgIface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cmd %v failed: %s: %w", args, string(out), err)
		}
	}

	n.logger.Info("NAT rules enabled for exit traffic")
	return nil
}

// Disable removes the iptables NAT rules.
func (n *NATManager) Disable() error {
	cmds := [][]string{
		{"iptables", "-t", "nat", "-D", "POSTROUTING", "-s", "10.100.0.0/16", "-o", n.extIface, "-j", "MASQUERADE"},
		{"iptables", "-D", "FORWARD", "-i", n.wgIface, "-j", "ACCEPT"},
		{"iptables", "-D", "FORWARD", "-o", n.wgIface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Run() // best-effort cleanup
	}

	n.logger.Info("NAT rules disabled")
	return nil
}

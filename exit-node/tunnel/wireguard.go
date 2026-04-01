package tunnel

import (
	"fmt"
	"os/exec"
	"strings"

	"go.uber.org/zap"
	"valhalla/common/api"
	vcrypto "valhalla/common/crypto"
)

// WireGuardManager handles WireGuard interface configuration on Linux.
type WireGuardManager struct {
	iface  string
	port   int
	logger *zap.Logger
}

func NewWireGuardManager(iface string, port int, logger *zap.Logger) *WireGuardManager {
	return &WireGuardManager{iface: iface, port: port, logger: logger}
}

// Setup creates and configures the WireGuard interface.
func (wg *WireGuardManager) Setup(internalIP, privateKey string) error {
	cmds := [][]string{
		{"ip", "link", "add", "dev", wg.iface, "type", "wireguard"},
		{"ip", "address", "add", internalIP + "/16", "dev", wg.iface},
		{"wg", "set", wg.iface, "private-key", "/dev/stdin", "listen-port", fmt.Sprintf("%d", wg.port)},
		{"ip", "link", "set", "up", "dev", wg.iface},
	}

	for i, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if i == 2 {
			// Pass private key via stdin
			cmd.Stdin = strings.NewReader(privateKey)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cmd %v failed: %s: %w", args, string(out), err)
		}
	}

	wg.logger.Info("WireGuard interface configured",
		zap.String("iface", wg.iface),
		zap.String("ip", internalIP),
		zap.Int("port", wg.port))

	return nil
}

// AddPeer adds a WireGuard peer to the interface.
func (wg *WireGuardManager) AddPeer(peer api.PeerInfo) error {
	args := []string{"set", wg.iface, "peer", peer.PublicKey}

	if peer.Endpoint != "" {
		args = append(args, "endpoint", peer.Endpoint)
	}

	if peer.InternalIP != "" {
		args = append(args, "allowed-ips", peer.InternalIP+"/32")
	}

	args = append(args, "persistent-keepalive", "25")

	cmd := exec.Command("wg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("add peer failed: %s: %w", string(out), err)
	}

	wg.logger.Debug("peer added", zap.String("pubkey", peer.PublicKey[:8]+"..."))
	return nil
}

// UpdatePeers replaces all peers with the given list.
func (wg *WireGuardManager) UpdatePeers(peers []api.PeerInfo) error {
	// Flush existing peers
	cmd := exec.Command("wg", "set", wg.iface)
	cmd.Run()

	for _, peer := range peers {
		if err := wg.AddPeer(peer); err != nil {
			wg.logger.Warn("failed to add peer", zap.Error(err))
		}
	}
	return nil
}

// Teardown removes the WireGuard interface.
func (wg *WireGuardManager) Teardown() error {
	cmd := exec.Command("ip", "link", "delete", "dev", wg.iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("teardown failed: %s: %w", string(out), err)
	}
	return nil
}

// GenerateKeys generates a new WireGuard keypair for this node.
func GenerateKeys() (*vcrypto.WireGuardKeyPair, error) {
	return vcrypto.GenerateKeyPair()
}

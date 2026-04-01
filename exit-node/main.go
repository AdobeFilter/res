package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/common/protocol"
	"valhalla/exit-node/auth"
	"valhalla/exit-node/config"
	"valhalla/exit-node/metrics"
	"valhalla/exit-node/registration"
	"valhalla/exit-node/tunnel"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Authenticate
	token, err := auth.LoadToken(cfg.TokenFile)
	if err != nil {
		logger.Info("no saved token, interactive login required")
		authResp, err := auth.InteractiveLogin(cfg.ControlPlaneURL)
		if err != nil {
			logger.Fatal("login failed", zap.Error(err))
		}
		token = authResp.Token
		if err := auth.SaveToken(cfg.TokenFile, token); err != nil {
			logger.Warn("failed to save token", zap.Error(err))
		}
		logger.Info("login successful", zap.String("account_id", authResp.AccountID))
	}

	// Generate WireGuard keypair
	keys, err := tunnel.GenerateKeys()
	if err != nil {
		logger.Fatal("key generation failed", zap.Error(err))
	}

	// Register with control plane
	client := registration.NewClient(cfg.ControlPlaneURL, token, logger)
	hostname, _ := os.Hostname()
	regResp, err := client.RegisterNode(ctx, hostname, keys.PublicKey)
	if err != nil {
		logger.Fatal("node registration failed", zap.Error(err))
	}

	logger.Info("node registered",
		zap.String("node_id", regResp.NodeID),
		zap.String("internal_ip", regResp.InternalIP),
		zap.Int("peers", len(regResp.Peers)))

	// Setup WireGuard interface
	wgManager := tunnel.NewWireGuardManager(cfg.WireGuardIface, cfg.WireGuardPort, logger)
	if err := wgManager.Setup(regResp.InternalIP, keys.PrivateKey); err != nil {
		logger.Fatal("WireGuard setup failed", zap.Error(err))
	}
	defer wgManager.Teardown()

	// Add peers
	for _, peer := range regResp.Peers {
		if err := wgManager.AddPeer(peer); err != nil {
			logger.Warn("failed to add peer", zap.Error(err))
		}
	}

	// Setup NAT for exit traffic
	natManager := tunnel.NewNATManager(cfg.WireGuardIface, logger)
	if err := natManager.Enable(); err != nil {
		logger.Fatal("NAT setup failed", zap.Error(err))
	}
	defer natManager.Disable()

	// VLESS+Reality (conditionally started based on account settings)
	vlessManager := tunnel.NewVLESSManager(443, logger)

	// Heartbeat loop
	go client.StartHeartbeatLoop(ctx, regResp.NodeID,
		func() api.Metrics {
			return metrics.Collect()
		},
		func(resp *protocol.HeartbeatResponse) {
			// Update peers
			if resp.Peers != nil {
				wgManager.UpdatePeers(resp.Peers)
			}

			// Handle VLESS toggle
			if resp.Settings != nil {
				if resp.Settings.VLESSEnabled && !vlessManager.IsEnabled() {
					logger.Info("enabling VLESS+Reality transport")
					vlessManager.Start(ctx, "", "", nil)
				} else if !resp.Settings.VLESSEnabled && vlessManager.IsEnabled() {
					logger.Info("disabling VLESS+Reality transport")
					vlessManager.Stop()
				}
			}
		},
	)

	fmt.Println("Exit node is running. Press Ctrl+C to stop.")

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("exit node shutting down...")
	cancel()
}

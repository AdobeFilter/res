package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"go.uber.org/zap"
	"valhalla/relay-node/config"
	"valhalla/relay-node/registration"
	"valhalla/relay-node/relay"
	"valhalla/relay-node/transport"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Session table
	sessions := relay.NewSessionTable(cfg.Capacity)

	// UDP Forwarder (primary)
	forwarder := relay.NewForwarder(sessions, logger)
	go func() {
		if err := forwarder.ListenAndServe(ctx, cfg.ListenAddr); err != nil {
			logger.Fatal("UDP relay failed", zap.Error(err))
		}
	}()

	// TCP Relay (fallback for UDP-blocked networks)
	tcpRelay := transport.NewTCPRelay(logger)
	go func() {
		if err := tcpRelay.ListenAndServe(ctx, cfg.TCPListenAddr); err != nil {
			logger.Fatal("TCP relay failed", zap.Error(err))
		}
	}()

	// VLESS+Reality Relay (for censored networks)
	vlessRelay := transport.NewVLESSRelay(443, logger)
	_ = vlessRelay // Will be started when VLESS config is provided

	// Register with control plane
	port := 51821
	if parts := strings.Split(cfg.ListenAddr, ":"); len(parts) == 2 {
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}
	go registration.RegisterWithControlPlane(ctx, cfg.ControlPlaneURL, cfg.PublicAddress, port, cfg.Capacity, logger)

	logger.Info("relay node started",
		zap.String("udp", cfg.ListenAddr),
		zap.String("tcp", cfg.TCPListenAddr),
		zap.Int("capacity", cfg.Capacity))

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("relay node shutting down...")
	cancel()
}

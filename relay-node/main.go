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
	"valhalla/relay-node/mesh"
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

	// UDP WG hole-punch relay (existing).
	sessions := relay.NewSessionTable(cfg.Capacity)
	forwarder := relay.NewForwarder(sessions, logger)
	go func() {
		if err := forwarder.ListenAndServe(ctx, cfg.ListenAddr); err != nil {
			logger.Fatal("UDP relay failed", zap.Error(err))
		}
	}()

	// TCP fallback for UDP-blocked networks (existing).
	tcpRelay := transport.NewTCPRelay(logger)
	go func() {
		if err := tcpRelay.ListenAndServe(ctx, cfg.TCPListenAddr); err != nil {
			logger.Fatal("TCP relay failed", zap.Error(err))
		}
	}()

	// Derive numeric UDP port for registration.
	udpPort := 51821
	if parts := strings.Split(cfg.ListenAddr, ":"); len(parts) == 2 {
		if p, err := strconv.Atoi(parts[1]); err == nil {
			udpPort = p
		}
	}

	// Registrar: heartbeats the relay to the control-plane so it stays in the
	// pool, and receives the shared mesh-auth key in every register response.
	// The dispatcher reads that key via registrar.MeshAuthKey on each HELLO —
	// no env var on the relay, the secret lives in CP's env only.
	registrar := registration.New(
		cfg.ControlPlaneURL,
		cfg.PublicAddress,
		udpPort,
		cfg.Capacity,
		logger,
	)
	go registrar.Run(ctx)

	// Mesh dispatcher: pubkey-keyed forwarding of WG ciphertext between peers
	// (never decrypts). Binds MeshListenAddr (":9999" = public); clients reach
	// it directly through their exit-node. Token enforcement turns on as soon
	// as the registrar has a non-empty key from control-plane.
	dispatcher := mesh.New(cfg.MeshListenAddr, registrar.MeshAuthKey, logger)
	go func() {
		if err := dispatcher.ListenAndServe(ctx); err != nil {
			logger.Fatal("mesh dispatcher failed", zap.Error(err))
		}
	}()

	logger.Info("relay node started",
		zap.String("udp", cfg.ListenAddr),
		zap.String("tcp", cfg.TCPListenAddr),
		zap.String("mesh", cfg.MeshListenAddr),
		zap.Int("capacity", cfg.Capacity))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("relay node shutting down...")
	cancel()
}

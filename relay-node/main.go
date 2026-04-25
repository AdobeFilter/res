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

	// Mesh dispatcher: terminates the VLESS streams bridged here by xray
	// and performs pubkey-keyed forwarding between peers. Runs on loopback
	// — xray routing rules pin VLESS clients to this destination only.
	dispatcher := mesh.New(cfg.MeshDispatchAddr, logger)
	go func() {
		if err := dispatcher.ListenAndServe(ctx); err != nil {
			logger.Fatal("mesh dispatcher failed", zap.Error(err))
		}
	}()

	// Derive numeric UDP port for registration.
	udpPort := 51821
	if parts := strings.Split(cfg.ListenAddr, ":"); len(parts) == 2 {
		if p, err := strconv.Atoi(parts[1]); err == nil {
			udpPort = p
		}
	}

	// Registrar: heartbeats control-plane, publishes Reality credentials
	// once on first successful registration.
	registrar := registration.New(
		cfg.ControlPlaneURL,
		cfg.PublicAddress,
		udpPort,
		cfg.VLESSPort,
		cfg.Capacity,
		logger,
	)
	go registrar.Run(ctx)

	// VLESS+Reality — xray subprocess, started once credentials arrive.
	vlessRelay := transport.NewVLESSRelay(cfg.VLESSPort, cfg.XrayBinary, logger)
	defer vlessRelay.Stop()

	go func() {
		select {
		case <-ctx.Done():
			return
		case creds, ok := <-registrar.Credentials():
			if !ok {
				return
			}
			if err := vlessRelay.Start(ctx,
				creds.VLESSUUID,
				creds.RealityPrivateKey,
				creds.RealityPublicKey,
				creds.RealityShortIDs,
				creds.RealitySNI,
				cfg.MeshDispatchAddr,
			); err != nil {
				logger.Error("failed to start VLESS relay", zap.Error(err))
			}
		}
	}()

	logger.Info("relay node started",
		zap.String("udp", cfg.ListenAddr),
		zap.String("tcp", cfg.TCPListenAddr),
		zap.String("vless", cfg.VLESSListenAddr),
		zap.Int("capacity", cfg.Capacity))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("relay node shutting down...")
	cancel()
}

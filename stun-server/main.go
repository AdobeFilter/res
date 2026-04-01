package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"valhalla/stun-server/config"
	"valhalla/stun-server/server"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stunServer := server.New(logger)

	// Register with control plane in background
	go server.RegisterWithControlPlane(ctx, cfg.ControlPlaneURL, cfg.PublicAddress, cfg.ListenAddr, logger)

	// Start alternate port listener
	go func() {
		if err := stunServer.ListenAndServe(cfg.AltListenAddr); err != nil {
			logger.Error("alt STUN listener failed", zap.Error(err))
		}
	}()

	// Start primary listener in background
	go func() {
		if err := stunServer.ListenAndServe(cfg.ListenAddr); err != nil {
			logger.Fatal("primary STUN listener failed", zap.Error(err))
		}
	}()

	logger.Info("STUN server started",
		zap.String("primary", cfg.ListenAddr),
		zap.String("alternate", cfg.AltListenAddr))

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("STUN server shutting down...")
	cancel()
}

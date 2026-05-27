package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"valhalla/common/crypto"
	"valhalla/control-plane/config"
	"valhalla/control-plane/db"
	"valhalla/control-plane/events"
	"valhalla/control-plane/handler"
	"valhalla/control-plane/middleware"
	"valhalla/control-plane/remnawave"
	"valhalla/control-plane/scheduler"
	"valhalla/control-plane/service"
	"valhalla/control-plane/stun"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database
	pool, err := db.Connect(ctx, cfg.DatabaseURL, logger)
	if err != nil {
		logger.Fatal("database connection failed", zap.Error(err))
	}
	defer pool.Close()

	if err := db.RunMigrations(ctx, pool, logger); err != nil {
		logger.Fatal("migrations failed", zap.Error(err))
	}

	// Repositories
	accountRepo := db.NewAccountRepository(pool)
	settingsRepo := db.NewAccountSettingsRepository(pool)
	nodeRepo := db.NewNodeRepository(pool)
	metricsRepo := db.NewMetricsRepository(pool)
	routeRepo := db.NewRouteRepository(pool)
	ipAlloc := db.NewIPAllocator(pool, cfg.MeshCIDR)
	stunRepo := db.NewSTUNServerRepository(pool)
	relayRepo := db.NewRelayServerRepository(pool)

	// Token manager
	tokenMgr := crypto.NewTokenManager(cfg.JWTSecret, cfg.TokenExpiry)

	// Remnawave panel client (provisioning + quota lookups). Methods are
	// no-ops on Enabled() == false, so callers don't need to gate manually.
	rwClient := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken, cfg.RemnawaveSquadUUID)
	if rwClient.Enabled() {
		logger.Info("Remnawave provisioning enabled", zap.String("url", cfg.RemnawaveURL))
	} else {
		logger.Warn("Remnawave not configured — accounts will be created without subscriptions")
	}

	// In-memory pub/sub keyed by account_id — wakes long-poll heartbeats
	// when settings change, a peer joins/leaves, or another device's
	// heartbeat refreshes shared state. Process-local: an HA deployment
	// would need to replace this with Redis pub/sub or similar.
	broker := events.NewBroker()

	// Services
	nodeService := service.NewNodeService(nodeRepo, metricsRepo, settingsRepo, stunRepo, ipAlloc, routeRepo, cfg.AntifraudEnabled, logger)
	routeService := service.NewRouteService(nodeRepo, metricsRepo, routeRepo, relayRepo, cfg.MeshAuthKey, cfg.MeshDispatchPort, logger)

	// Handlers
	authHandler := handler.NewAuthHandler(accountRepo, tokenMgr, rwClient, logger)
	nodeHandler := handler.NewNodeHandler(nodeService, nodeRepo, broker, logger)
	routeHandler := handler.NewRouteHandler(routeService, stunRepo, logger)
	settingsHandler := handler.NewSettingsHandler(settingsRepo, nodeRepo, broker, logger)
	internalHandler := handler.NewInternalHandler(stunRepo, relayRepo, cfg.AllowedRelaysFile, logger)
	sshProxyHandler := handler.NewSSHProxyHandler(logger)
	connLogHandler := handler.NewConnectionLogHandler("/var/log/valhalla", logger)
	deviceHandler := handler.NewDeviceHandler(nodeRepo, accountRepo, rwClient, logger)

	// Router
	mux := http.NewServeMux()

	// Auth (public)
	mux.HandleFunc("POST /api/v1/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
	mux.HandleFunc("POST /api/v1/auth/refresh", authHandler.Refresh)

	// Device hint (public, pre-login) — returns the email linked to a device_id
	mux.HandleFunc("GET /api/v1/devices/account-hint", deviceHandler.AccountHint)

	// Internal (STUN/relay self-registration)
	mux.HandleFunc("POST /api/v1/internal/stun/register", internalHandler.RegisterSTUN)
	mux.HandleFunc("POST /api/v1/internal/relay/register", internalHandler.RegisterRelay)

	// Protected routes
	authMw := middleware.Auth(tokenMgr)

	mux.Handle("POST /api/v1/nodes/register", authMw(http.HandlerFunc(nodeHandler.Register)))
	mux.Handle("GET /api/v1/nodes", authMw(http.HandlerFunc(nodeHandler.List)))
	mux.Handle("POST /api/v1/nodes/reorder", authMw(http.HandlerFunc(nodeHandler.Reorder)))
	mux.Handle("PUT /api/v1/nodes/{id}", authMw(http.HandlerFunc(nodeHandler.Update)))
	mux.Handle("DELETE /api/v1/nodes/{id}", authMw(http.HandlerFunc(nodeHandler.Delete)))
	mux.Handle("POST /api/v1/nodes/{id}/heartbeat", authMw(http.HandlerFunc(nodeHandler.Heartbeat)))
	mux.Handle("GET /api/v1/routes/optimal", authMw(http.HandlerFunc(routeHandler.GetOptimal)))
	mux.Handle("GET /api/v1/routes/relay", authMw(http.HandlerFunc(routeHandler.GetRelay)))
	mux.Handle("GET /api/v1/routes/stun-servers", authMw(http.HandlerFunc(routeHandler.GetSTUNServers)))
	mux.Handle("GET /api/v1/accounts/{id}/settings", authMw(http.HandlerFunc(settingsHandler.GetSettings)))
	mux.Handle("PUT /api/v1/accounts/{id}/settings", authMw(http.HandlerFunc(settingsHandler.UpdateSettings)))
	mux.Handle("GET /api/v1/accounts/{id}/devices", authMw(http.HandlerFunc(settingsHandler.GetDevices)))
	mux.Handle("GET /api/v1/accounts/me/quota", authMw(http.HandlerFunc(deviceHandler.Quota)))
	mux.Handle("POST /api/v1/accounts/me/reprovision", authMw(http.HandlerFunc(deviceHandler.Reprovision)))
	mux.Handle("POST /api/v1/ssh/setup", authMw(http.HandlerFunc(sshProxyHandler.Setup)))
	mux.Handle("POST /api/v1/logs/connection", authMw(http.HandlerFunc(connLogHandler.Append)))

	// Apply logging middleware
	logMw := middleware.Logging(logger)
	server := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      logMw(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Embedded STUN server
	stunServer := stun.NewServer(logger)
	go func() {
		if err := stunServer.ListenAndServe(cfg.STUNAddr); err != nil {
			logger.Error("STUN primary listener failed", zap.Error(err))
		}
	}()
	go func() {
		if err := stunServer.ListenAndServe(cfg.STUNAltAddr); err != nil {
			logger.Error("STUN alt listener failed", zap.Error(err))
		}
	}()

	// Start schedulers
	routeRecalc := scheduler.NewRouteRecalculator(routeService, cfg.RouteRecalcInterval, logger)
	go routeRecalc.Start(ctx)

	staleCleaner := scheduler.NewStaleNodeCleaner(nodeRepo, broker, cfg.StaleNodeTimeout, cfg.HeartbeatExpectedInterval, logger)
	go staleCleaner.Start(ctx)

	offlineDeleter := scheduler.NewOfflineNodeDeleter(
		nodeRepo, cfg.OfflineNodeRetention, cfg.OfflineNodeSweepInterval, logger,
	)
	go offlineDeleter.Start(ctx)

	// Start server
	go func() {
		logger.Info("control plane starting", zap.String("addr", cfg.ListenAddr))
		var err error
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			err = server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			logger.Warn("TLS not configured, running without encryption")
			err = server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)
}

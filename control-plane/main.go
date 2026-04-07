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
	"valhalla/control-plane/handler"
	"valhalla/control-plane/middleware"
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

	// Services
	nodeService := service.NewNodeService(nodeRepo, metricsRepo, settingsRepo, stunRepo, ipAlloc, routeRepo, logger)
	routeService := service.NewRouteService(nodeRepo, metricsRepo, routeRepo, relayRepo, logger)

	// Handlers
	authHandler := handler.NewAuthHandler(accountRepo, tokenMgr, logger)
	nodeHandler := handler.NewNodeHandler(nodeService, nodeRepo, logger)
	routeHandler := handler.NewRouteHandler(routeService, stunRepo, logger)
	settingsHandler := handler.NewSettingsHandler(settingsRepo, nodeRepo, logger)
	internalHandler := handler.NewInternalHandler(stunRepo, relayRepo, logger)

	// Router
	mux := http.NewServeMux()

	// Auth (public)
	mux.HandleFunc("POST /api/v1/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
	mux.HandleFunc("POST /api/v1/auth/refresh", authHandler.Refresh)

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
	mux.Handle("GET /api/v1/routes/stun-servers", authMw(http.HandlerFunc(routeHandler.GetSTUNServers)))
	mux.Handle("GET /api/v1/accounts/{id}/settings", authMw(http.HandlerFunc(settingsHandler.GetSettings)))
	mux.Handle("PUT /api/v1/accounts/{id}/settings", authMw(http.HandlerFunc(settingsHandler.UpdateSettings)))
	mux.Handle("GET /api/v1/accounts/{id}/devices", authMw(http.HandlerFunc(settingsHandler.GetDevices)))

	// Apply logging middleware
	logMw := middleware.Logging(logger)
	server := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      logMw(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
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

	staleCleaner := scheduler.NewStaleNodeCleaner(nodeRepo, cfg.StaleNodeTimeout, cfg.HeartbeatExpectedInterval, logger)
	go staleCleaner.Start(ctx)

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

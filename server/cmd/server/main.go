package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

func main() {
	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	logger.Info("starting OZX APM server",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
	)

	// Initialize ClickHouse client
	chClient, err := storage.NewClickHouseClient(&cfg.ClickHouse, logger)
	if err != nil {
		logger.Fatal("failed to connect to ClickHouse", zap.Error(err))
	}
	defer chClient.Close()

	// Run migrations
	ctx := context.Background()
	if err := chClient.Migrate(ctx); err != nil {
		logger.Fatal("failed to run migrations", zap.Error(err))
	}
	logger.Info("database migrations completed")

	// Initialize repository
	repo := storage.NewRepository(chClient, logger)

	// Create SDK ingestion router
	sdkRouter := api.NewRouter(cfg, repo, logger)

	// Create SDK ingestion server
	sdkAddr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	sdkServer := &http.Server{
		Addr:         sdkAddr,
		Handler:      sdkRouter,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Start SDK server in goroutine
	go func() {
		logger.Info("SDK ingestion server listening", zap.String("addr", sdkAddr))
		if err := sdkServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("SDK server error", zap.Error(err))
		}
	}()

	// Start Admin server if enabled
	var adminServer *http.Server
	if cfg.AdminServer.Enabled {
		adminRouter := api.NewAdminRouter(cfg, repo, logger)
		adminAddr := fmt.Sprintf("%s:%d", cfg.AdminServer.Host, cfg.AdminServer.Port)
		adminServer = &http.Server{
			Addr:         adminAddr,
			Handler:      adminRouter,
			ReadTimeout:  cfg.AdminServer.ReadTimeout,
			WriteTimeout: cfg.AdminServer.WriteTimeout,
		}

		go func() {
			logger.Info("Admin server listening", zap.String("addr", adminAddr))
			if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Fatal("Admin server error", zap.Error(err))
			}
		}()
	}

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down servers...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := sdkServer.Shutdown(ctx); err != nil {
		logger.Error("SDK server shutdown error", zap.Error(err))
	}

	if adminServer != nil {
		if err := adminServer.Shutdown(ctx); err != nil {
			logger.Error("Admin server shutdown error", zap.Error(err))
		}
	}

	logger.Info("servers stopped")
}

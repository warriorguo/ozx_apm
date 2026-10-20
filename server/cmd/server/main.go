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

	"github.com/warriorguo/ozx_apm/server/internal/alert"
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

	// Check if at least one server is enabled
	if !cfg.Server.Enabled && !cfg.AdminServer.Enabled {
		logger.Fatal("at least one of server or admin_server must be enabled")
	}

	logger.Info("starting OZX APM",
		zap.Bool("sdk_server_enabled", cfg.Server.Enabled),
		zap.Bool("admin_server_enabled", cfg.AdminServer.Enabled),
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

	// Alert evaluator (APM-8): off unless OZX_ALERT_ENABLED; it reads
	// ClickHouse and never touches the ingest path.
	alertCtx, stopAlerts := context.WithCancel(context.Background())
	defer stopAlerts()
	startAlerting(alertCtx, cfg.Alert, repo, logger)

	// Session files of development builds (APM-13): one service for both
	// routers and the expiry cleaner; off without the object store.
	sessionFiles := api.NewSessionFileService(cfg, repo, logger)
	if sessionFiles != nil {
		logger.Info("session files enabled",
			zap.String("s3_endpoint", cfg.S3.Endpoint), zap.String("s3_bucket", cfg.S3.Bucket),
			zap.String("s3_prefix", cfg.S3.Prefix), zap.Int("retention_days", cfg.SessionFiles.RetentionDays),
			zap.Bool("upload_key_set", cfg.SessionFiles.UploadKey != ""),
			zap.Bool("admin_token_set", cfg.SessionFiles.AdminToken != ""),
			zap.Duration("cleaner_interval", cfg.SessionFiles.CleanerInterval))
		go sessionFiles.RunCleaner(alertCtx, cfg.SessionFiles.CleanerInterval)
	} else {
		logger.Info("session files disabled (OZX_S3_ENDPOINT, OZX_S3_BUCKET, OZX_S3_ACCESS_KEY, OZX_S3_SECRET_KEY not all set; RELEASE.md APM-13)")
	}
	routerOpts := []api.RouterOption{api.WithSessionFiles(sessionFiles)}

	// Start SDK ingestion server if enabled
	var sdkServer *http.Server
	if cfg.Server.Enabled {
		sdkRouter := api.NewRouter(cfg, repo, logger, routerOpts...)
		sdkAddr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		sdkServer = &http.Server{
			Addr:         sdkAddr,
			Handler:      sdkRouter,
			ReadTimeout:  cfg.Server.ReadTimeout,
			WriteTimeout: cfg.Server.WriteTimeout,
		}

		go func() {
			logger.Info("SDK ingestion server listening", zap.String("addr", sdkAddr))
			if err := sdkServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Fatal("SDK server error", zap.Error(err))
			}
		}()
	}

	// Start Admin server if enabled
	var adminServer *http.Server
	if cfg.AdminServer.Enabled {
		adminRouter := api.NewAdminRouter(cfg, repo, logger, routerOpts...)
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
	stopAlerts()

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if sdkServer != nil {
		if err := sdkServer.Shutdown(ctx); err != nil {
			logger.Error("SDK server shutdown error", zap.Error(err))
		}
	}

	if adminServer != nil {
		if err := adminServer.Shutdown(ctx); err != nil {
			logger.Error("Admin server shutdown error", zap.Error(err))
		}
	}

	logger.Info("servers stopped")
}

// startAlerting starts the alert evaluator when enabled and configured; it
// logs one line saying whether it runs. The webhook URL is a secret: only
// its host is logged.
func startAlerting(ctx context.Context, c config.AlertConfig, repo *storage.Repository, logger *zap.Logger) {
	if !c.Enabled {
		logger.Info("alerting disabled (OZX_ALERT_ENABLED is not true; enable it on one deployment, RELEASE.md APM-8)")
		return
	}
	if c.WebhookURL == "" {
		logger.Error("alerting not started: OZX_ALERT_ENABLED is true but OZX_ALERT_WEBHOOK_URL is empty")
		return
	}
	notifier, err := alert.NewNotifier(c.WebhookURL, c.WebhookFormat, c.AdminBaseURL)
	if err != nil {
		logger.Error("alerting not started", zap.Error(err))
		return
	}
	cfg := alert.ConfigFrom(c)
	evaluator := alert.NewEvaluator(repo, notifier, cfg, logger)
	logger.Info("alerting enabled",
		zap.String("webhook_host", notifier.Host()),
		zap.String("webhook_format", notifier.Format()),
		zap.Bool("admin_links", c.AdminBaseURL != ""),
		zap.Duration("interval", cfg.Interval),
		zap.Bool("include_dev_builds", cfg.IncludeDevBuilds))
	go evaluator.Run(ctx)
}

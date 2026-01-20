package api

import (
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"go.uber.org/zap"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/api/handlers"
	"github.com/warriorguo/ozx_apm/server/internal/api/middleware"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

func NewRouter(cfg *config.Config, repo *storage.Repository, logger *zap.Logger) *chi.Mux {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(middleware.Logger(logger))
	r.Use(chiMiddleware.Recoverer)
	r.Use(middleware.Decompress)

	// Rate limiting
	if cfg.RateLimit.Enabled {
		r.Use(httprate.LimitByIP(cfg.RateLimit.RequestsPerMin, time.Minute))
	}

	// Health check (no auth required)
	healthHandler := handlers.NewHealthHandler(repo)
	r.Get("/health", healthHandler.Health)

	// API v1 routes
	r.Route("/v1", func(r chi.Router) {
		// Auth middleware for v1 routes
		if cfg.Auth.Enabled {
			r.Use(middleware.Auth(cfg.Auth.AppKeys))
		}

		// Ingest handler
		ingestHandler := handlers.NewIngestHandler(repo, logger)
		r.Post("/events", ingestHandler.IngestEvents)

		// Query handlers
		queryHandler := handlers.NewQueryHandler(repo, logger)
		r.Get("/metrics/fps", queryHandler.GetFPSMetrics)
		r.Get("/metrics/startup", queryHandler.GetStartupMetrics)
		r.Get("/metrics/jank", queryHandler.GetJankMetrics)
		r.Get("/exceptions", queryHandler.GetExceptions)
		r.Get("/crashes", queryHandler.GetCrashes)
	})

	return r
}

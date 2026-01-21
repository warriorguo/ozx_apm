package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api/handlers/admin"
	"github.com/warriorguo/ozx_apm/server/internal/api/middleware"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// NewAdminRouter creates the admin API router (separate from SDK ingestion API)
func NewAdminRouter(cfg *config.Config, repo *storage.Repository, logger *zap.Logger) *chi.Mux {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(middleware.CORS(cfg.AdminServer.AllowedOrigins))
	r.Use(middleware.Logger(logger))
	r.Use(chiMiddleware.Recoverer)

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"admin"}`))
	})

	// Admin API routes
	r.Route("/api", func(r chi.Router) {
		// Dashboard handlers
		dashboardHandler := admin.NewDashboardHandler(repo, logger)
		r.Get("/summary", dashboardHandler.GetSummary)
		r.Get("/timeseries", dashboardHandler.GetTimeSeries)
		r.Get("/distribution", dashboardHandler.GetDistribution)
		r.Get("/versions", dashboardHandler.GetAppVersions)
		r.Get("/scenes", dashboardHandler.GetScenes)

		// Crash handlers
		crashHandler := admin.NewCrashHandler(repo, logger)
		r.Get("/crashes", crashHandler.ListCrashes)
		r.Get("/crashes/detail", crashHandler.GetCrashDetail)

		// Exception handlers
		exceptionHandler := admin.NewExceptionHandler(repo, logger)
		r.Get("/exceptions", exceptionHandler.ListExceptions)
	})

	// Serve static files for admin UI (if exists)
	fileServer := http.FileServer(http.Dir("./admin/dist"))
	r.Handle("/*", fileServer)

	return r
}

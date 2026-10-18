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
		r.Get("/exceptions/detail", exceptionHandler.GetExceptionDetail)
		// APM-11: the detail page
		r.Get("/exceptions/occurrences", exceptionHandler.GetOccurrences)
		r.Get("/exceptions/distribution", exceptionHandler.GetDistribution)

		// Session time line (APM-15)
		sessionHandler := admin.NewSessionHandler(repo, logger)
		r.Get("/sessions/timeline", sessionHandler.Timeline)
		r.Get("/sessions", sessionHandler.DeviceSessions)

		// Game context (APM-5)
		contextHandler := admin.NewContextHandler(repo, logger)
		r.Get("/context/keys", contextHandler.ListKeys)
		r.Get("/context/values", contextHandler.ListValues)
		r.Get("/context/distribution", contextHandler.Distribution)

		// Custom events and metrics (APM-6)
		customHandler := admin.NewCustomHandler(repo, logger)
		r.Get("/custom/events", customHandler.ListEvents)
		r.Get("/custom/events/distribution", customHandler.EventDistribution)
		r.Get("/custom/metrics", customHandler.ListMetrics)
		r.Get("/custom/metrics/distribution", customHandler.MetricDistribution)

		// Privacy deletion (APM-14). The rest of the admin API has no
		// authentication (internal network only, RELEASE.md); these two
		// destructive endpoints need a bearer token and are disabled (403)
		// without one. Deleting also needs the X-Apm-Confirm-Delete header.
		var privacyStore admin.PrivacyStore
		if repo != nil {
			privacyStore = repo
		}
		privacyHandler := admin.NewPrivacyHandler(privacyStore, logger, cfg.AdminServer.PrivacyToken)
		if !privacyHandler.Enabled() {
			logger.Info("privacy API disabled: no token configured")
		}
		r.Post("/privacy/delete", privacyHandler.Delete)
		r.Post("/privacy/verify", privacyHandler.Verify)

		// Issue state and alerts (APM-8). Changing a state needs the
		// X-Apm-Admin header (not sendable cross-origin); queueing a test
		// alert, which makes the evaluator post to the webhook, needs a
		// bearer token and is disabled (403) without one.
		issueHandler := admin.NewIssueHandler(repo, logger, cfg.Alert.IncludeDevBuilds)
		r.Get("/issues/state", issueHandler.Get)
		r.Post("/issues/state", issueHandler.Set)
		alertHandler := admin.NewAlertHandler(repo, logger, cfg.AdminServer.AlertToken)
		if !alertHandler.TestEnabled() {
			logger.Info("alert test API disabled: no token configured")
		}
		r.Get("/alerts", alertHandler.List)
		r.Post("/alerts/test", alertHandler.Test)

		// Symbol upload (APM-7, cmd/apmsym): the POSTs need the bearer
		// token OZX_SYMBOLS_UPLOAD_TOKEN and are disabled (403) without one.
		var symbolStore admin.SymbolStore
		if repo != nil {
			symbolStore = repo
		}
		symbolHandler := admin.NewSymbolHandler(symbolStore, logger, cfg.AdminServer.SymbolsToken)
		if !symbolHandler.Enabled() {
			logger.Info("symbol upload disabled: no token configured")
		}
		r.Post("/symbols/part", symbolHandler.Part)
		r.Post("/symbols/commit", symbolHandler.Commit)
		r.Get("/symbols", symbolHandler.List)
	})

	// Serve static files for admin UI (if exists)
	fileServer := http.FileServer(http.Dir("./admin/dist"))
	r.Handle("/*", fileServer)

	return r
}

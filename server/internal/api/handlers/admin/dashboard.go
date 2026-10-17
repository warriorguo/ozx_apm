package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type DashboardHandler struct {
	repo   *storage.Repository
	logger *zap.Logger
}

func NewDashboardHandler(repo *storage.Repository, logger *zap.Logger) *DashboardHandler {
	return &DashboardHandler{
		repo:   repo,
		logger: logger,
	}
}

func (h *DashboardHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	f, err := parseAdminFilter(q, 24*time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	summary, err := h.repo.GetDashboardSummary(ctx, f)
	if err != nil {
		h.logger.Error("failed to get dashboard summary", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	summary.TimeRange = models.TimeRange{Start: f.StartTime, End: f.EndTime}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

func (h *DashboardHandler) GetTimeSeries(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	metric := q.Get("metric")
	if metric == "" {
		http.Error(w, "metric parameter required", http.StatusBadRequest)
		return
	}

	f, err := parseAdminFilter(q, 24*time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Determine interval based on time range
	duration := f.EndTime.Sub(f.StartTime)
	interval := "1 HOUR"
	if duration > 7*24*time.Hour {
		interval = "1 DAY"
	} else if duration < 6*time.Hour {
		interval = "5 MINUTE"
	}

	data, err := h.repo.GetTimeSeries(ctx, metric, interval, f)
	if err != nil {
		h.logger.Error("failed to get time series", zap.Error(err), zap.String("metric", metric))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := models.TimeSeriesResponse{
		Metric: metric,
		Data:   data,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *DashboardHandler) GetDistribution(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	metric := q.Get("metric")
	if metric == "" {
		http.Error(w, "metric parameter required", http.StatusBadRequest)
		return
	}

	f, err := parseAdminFilter(q, 24*time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dist, err := h.repo.GetDistribution(ctx, metric, q.Get("scene"), f)
	if err != nil {
		h.logger.Error("failed to get distribution", zap.Error(err), zap.String("metric", metric))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dist)
}

// GetAppVersions returns list of app versions
func (h *DashboardHandler) GetAppVersions(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	versions, err := h.repo.GetAppVersions(ctx)
	if err != nil {
		h.logger.Error("failed to get app versions", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"versions": versions,
	})
}

// GetScenes returns list of scenes
func (h *DashboardHandler) GetScenes(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	appVersion := r.URL.Query().Get("app_version")

	scenes, err := h.repo.GetScenes(ctx, appVersion)
	if err != nil {
		h.logger.Error("failed to get scenes", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"scenes": scenes,
	})
}

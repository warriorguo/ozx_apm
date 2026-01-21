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

	// Parse time range (default: last 24 hours)
	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	if start := q.Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			startTime = t
		}
	}
	if end := q.Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			endTime = t
		}
	}

	appVersion := q.Get("app_version")
	platform := q.Get("platform")

	summary, err := h.repo.GetDashboardSummary(ctx, startTime, endTime, appVersion, platform)
	if err != nil {
		h.logger.Error("failed to get dashboard summary", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	summary.TimeRange = models.TimeRange{Start: startTime, End: endTime}

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

	// Parse time range
	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	if start := q.Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			startTime = t
		}
	}
	if end := q.Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			endTime = t
		}
	}

	appVersion := q.Get("app_version")
	platform := q.Get("platform")

	// Determine interval based on time range
	duration := endTime.Sub(startTime)
	interval := "1 HOUR"
	if duration > 7*24*time.Hour {
		interval = "1 DAY"
	} else if duration < 6*time.Hour {
		interval = "5 MINUTE"
	}

	data, err := h.repo.GetTimeSeries(ctx, metric, startTime, endTime, interval, appVersion, platform)
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

	// Parse time range
	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	if start := q.Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			startTime = t
		}
	}
	if end := q.Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			endTime = t
		}
	}

	appVersion := q.Get("app_version")
	platform := q.Get("platform")
	scene := q.Get("scene")

	dist, err := h.repo.GetDistribution(ctx, metric, startTime, endTime, appVersion, platform, scene)
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

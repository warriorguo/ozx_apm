package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type QueryHandler struct {
	repo   *storage.Repository
	logger *zap.Logger
}

func NewQueryHandler(repo *storage.Repository, logger *zap.Logger) *QueryHandler {
	return &QueryHandler{
		repo:   repo,
		logger: logger,
	}
}

// parseQueryFilter extracts filter parameters from request
func parseQueryFilter(r *http.Request) models.QueryFilter {
	q := r.URL.Query()

	filter := models.QueryFilter{
		AppVersion:  q.Get("app_version"),
		Platform:    q.Get("platform"),
		DeviceModel: q.Get("device_model"),
		OSVersion:   q.Get("os_version"),
		Scene:       q.Get("scene"),
		Country:     q.Get("country"),
		NetType:     q.Get("net_type"),
	}

	// Parse time range (default: last 24 hours)
	if startStr := q.Get("start_time"); startStr != "" {
		if t, err := time.Parse(time.RFC3339, startStr); err == nil {
			filter.StartTime = t
		}
	}
	if filter.StartTime.IsZero() {
		filter.StartTime = time.Now().Add(-24 * time.Hour)
	}

	if endStr := q.Get("end_time"); endStr != "" {
		if t, err := time.Parse(time.RFC3339, endStr); err == nil {
			filter.EndTime = t
		}
	}
	if filter.EndTime.IsZero() {
		filter.EndTime = time.Now()
	}

	// Parse pagination
	if limitStr := q.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			filter.Limit = limit
		}
	}
	if filter.Limit == 0 {
		filter.Limit = 100
	}

	if offsetStr := q.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			filter.Offset = offset
		}
	}

	return filter
}

// GetFPSMetrics returns FPS distribution metrics
func (h *QueryHandler) GetFPSMetrics(w http.ResponseWriter, r *http.Request) {
	filter := parseQueryFilter(r)

	metrics, err := h.repo.QueryFPSMetrics(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to query FPS metrics", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.MetricsResponse{
		Data: metrics,
	})
}

// GetStartupMetrics returns startup time percentiles
func (h *QueryHandler) GetStartupMetrics(w http.ResponseWriter, r *http.Request) {
	filter := parseQueryFilter(r)

	metrics, err := h.repo.QueryStartupMetrics(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to query startup metrics", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.MetricsResponse{
		Data: metrics,
	})
}

// GetJankMetrics returns jank statistics
func (h *QueryHandler) GetJankMetrics(w http.ResponseWriter, r *http.Request) {
	filter := parseQueryFilter(r)

	metrics, err := h.repo.QueryJankMetrics(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to query jank metrics", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.MetricsResponse{
		Data: metrics,
	})
}

// GetExceptions returns exception list with counts
func (h *QueryHandler) GetExceptions(w http.ResponseWriter, r *http.Request) {
	filter := parseQueryFilter(r)

	exceptions, err := h.repo.QueryExceptions(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to query exceptions", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.MetricsResponse{
		Data: exceptions,
	})
}

// GetCrashes returns crash list with counts
func (h *QueryHandler) GetCrashes(w http.ResponseWriter, r *http.Request) {
	filter := parseQueryFilter(r)

	crashes, err := h.repo.QueryCrashes(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to query crashes", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.MetricsResponse{
		Data: crashes,
	})
}

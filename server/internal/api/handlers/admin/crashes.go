package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type CrashHandler struct {
	repo   *storage.Repository
	logger *zap.Logger
}

func NewCrashHandler(repo *storage.Repository, logger *zap.Logger) *CrashHandler {
	return &CrashHandler{
		repo:   repo,
		logger: logger,
	}
}

func (h *CrashHandler) ListCrashes(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	// Parse time range
	endTime := time.Now()
	startTime := endTime.Add(-7 * 24 * time.Hour)

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

	page := 1
	if p := q.Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}

	pageSize := 20
	if ps := q.Get("page_size"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 && v <= 100 {
			pageSize = v
		}
	}

	crashes, totalCount, err := h.repo.GetCrashGroups(ctx, startTime, endTime, appVersion, platform, page, pageSize)
	if err != nil {
		h.logger.Error("failed to get crash groups", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := models.CrashListResponse{
		Crashes:    crashes,
		TotalCount: totalCount,
		Page:       page,
		PageSize:   pageSize,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *CrashHandler) GetCrashDetail(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	fingerprint := q.Get("fingerprint")
	if fingerprint == "" {
		http.Error(w, "fingerprint parameter required", http.StatusBadRequest)
		return
	}

	// Parse time range
	endTime := time.Now()
	startTime := endTime.Add(-30 * 24 * time.Hour)

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

	detail, err := h.repo.GetCrashDetail(ctx, fingerprint, startTime, endTime)
	if err != nil {
		h.logger.Error("failed to get crash detail", zap.Error(err), zap.String("fingerprint", fingerprint))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if detail == nil {
		http.Error(w, "crash not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(detail)
}

// ExceptionHandler handles exception-related requests
type ExceptionHandler struct {
	repo   *storage.Repository
	logger *zap.Logger
}

func NewExceptionHandler(repo *storage.Repository, logger *zap.Logger) *ExceptionHandler {
	return &ExceptionHandler{
		repo:   repo,
		logger: logger,
	}
}

func (h *ExceptionHandler) ListExceptions(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	// Parse time range
	endTime := time.Now()
	startTime := endTime.Add(-7 * 24 * time.Hour)

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

	page := 1
	if p := q.Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}

	pageSize := 20
	if ps := q.Get("page_size"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 && v <= 100 {
			pageSize = v
		}
	}

	exceptions, totalCount, err := h.repo.GetExceptionGroups(ctx, startTime, endTime, appVersion, platform, page, pageSize)
	if err != nil {
		h.logger.Error("failed to get exception groups", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := models.ExceptionListResponse{
		Exceptions: exceptions,
		TotalCount: totalCount,
		Page:       page,
		PageSize:   pageSize,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

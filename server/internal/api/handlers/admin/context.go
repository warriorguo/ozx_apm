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

// ContextHandler serves the game context endpoints (APM-5). All of them take
// the common filter (parseAdminFilter); default time range: 7 days.
type ContextHandler struct {
	repo   *storage.Repository
	logger *zap.Logger
}

func NewContextHandler(repo *storage.Repository, logger *zap.Logger) *ContextHandler {
	return &ContextHandler{repo: repo, logger: logger}
}

const contextDefaultRange = 7 * 24 * time.Hour

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// filterAndSource parses the filter and the "source" parameter (perf, janks,
// startups, scene_loads, exceptions, crashes, custom_events, custom_metrics,
// all); false after writing a 400.
func (h *ContextHandler) filterAndSource(w http.ResponseWriter, r *http.Request) (models.AdminFilter, string, bool) {
	q := r.URL.Query()
	f, err := parseAdminFilter(q, contextDefaultRange)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return f, "", false
	}
	source := q.Get("source")
	if !storage.ValidContextSource(source) {
		http.Error(w, "source must be one of perf, janks, startups, scene_loads, exceptions, crashes, custom_events, custom_metrics, all", http.StatusBadRequest)
		return f, "", false
	}
	return f, source, true
}

// requireKey reads the "key" parameter; false after writing a 400.
func requireKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.URL.Query().Get("key")
	if !models.ValidContextKey(key) {
		http.Error(w, "key parameter required: [A-Za-z0-9_.-]{1,64}", http.StatusBadRequest)
		return "", false
	}
	return key, true
}

// ListKeys: GET /api/context/keys -> the keys seen, most frequent first.
func (h *ContextHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	f, source, ok := h.filterAndSource(w, r)
	if !ok {
		return
	}
	keys, err := h.repo.GetContextKeys(r.Context(), f, source)
	if err != nil {
		h.logger.Error("failed to get context keys", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, models.ContextKeysResponse{Keys: keys})
}

// ListValues: GET /api/context/values?key=K[&limit=N] -> the most frequent
// values of K (default 50, at most 500).
func (h *ContextHandler) ListValues(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	f, source, ok := h.filterAndSource(w, r)
	if !ok {
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 500 {
			limit = v
		}
	}
	values, err := h.repo.GetContextValues(r.Context(), f, source, key, limit)
	if err != nil {
		h.logger.Error("failed to get context values", zap.Error(err), zap.String("key", key))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, models.ContextValuesResponse{Key: key, Values: values})
}

// Distribution: GET /api/context/distribution?key=K&metric=M[&fingerprint=F]
// -> the metric grouped by the value of K. M is fps, frame_time, janks,
// startup, scene_load, exceptions or crashes.
func (h *ContextHandler) Distribution(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	metric := q.Get("metric")
	if !storage.ValidContextMetric(metric) {
		http.Error(w, "metric must be one of fps, frame_time, janks, startup, scene_load, exceptions, crashes", http.StatusBadRequest)
		return
	}
	f, err := parseAdminFilter(q, contextDefaultRange)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dist, err := h.repo.GetContextDistribution(r.Context(), f, metric, key, q.Get("fingerprint"))
	if err != nil {
		h.logger.Error("failed to get context distribution", zap.Error(err), zap.String("key", key), zap.String("metric", metric))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, dist)
}

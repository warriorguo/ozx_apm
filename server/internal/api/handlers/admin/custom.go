package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// CustomStore is the part of the repository the custom endpoints read.
type CustomStore interface {
	GetCustomEvents(ctx context.Context, f models.AdminFilter) (*models.CustomEventsResponse, error)
	GetCustomMetrics(ctx context.Context, f models.AdminFilter) (*models.CustomMetricsResponse, error)
	GetCustomDistribution(ctx context.Context, f models.AdminFilter, metric bool, name string, g storage.CustomGroup) (*models.CustomDistribution, error)
}

// CustomHandler serves custom events and metrics (APM-6). Every endpoint
// takes the common filter (parseAdminFilter: time range, app_version,
// platform, build_type, ctx=key:value); default range 7 days.
type CustomHandler struct {
	repo   CustomStore
	logger *zap.Logger
}

func NewCustomHandler(repo *storage.Repository, logger *zap.Logger) *CustomHandler {
	h := &CustomHandler{logger: logger}
	if repo != nil {
		h.repo = repo
	}
	return h
}

func (h *CustomHandler) filter(w http.ResponseWriter, r *http.Request) (models.AdminFilter, bool) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return models.AdminFilter{}, false
	}
	f, err := parseAdminFilter(r.URL.Query(), contextDefaultRange)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return f, false
	}
	return f, true
}

// ListEvents: GET /api/custom/events -> event names with calls, sessions,
// devices and last time, most calls first (at most 200).
func (h *CustomHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	f, ok := h.filter(w, r)
	if !ok {
		return
	}
	resp, err := h.repo.GetCustomEvents(r.Context(), f)
	if err != nil {
		h.logger.Error("failed to get custom events", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

// ListMetrics: GET /api/custom/metrics -> metric names with samples,
// sessions, avg, min, max, p50, p90, p95, p99 over the filter.
func (h *CustomHandler) ListMetrics(w http.ResponseWriter, r *http.Request) {
	f, ok := h.filter(w, r)
	if !ok {
		return
	}
	resp, err := h.repo.GetCustomMetrics(r.Context(), f)
	if err != nil {
		h.logger.Error("failed to get custom metrics", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

// EventDistribution: GET /api/custom/events/distribution?name=N&<group> ->
// the calls of event N per value of the group (see parseGroup).
func (h *CustomHandler) EventDistribution(w http.ResponseWriter, r *http.Request) {
	h.distribution(w, r, false)
}

// MetricDistribution: GET /api/custom/metrics/distribution?name=N&<group> ->
// samples and percentiles of metric N per value of the group, e.g.
// name=room_switch_ms&key=roomId for the room switch time per room.
func (h *CustomHandler) MetricDistribution(w http.ResponseWriter, r *http.Request) {
	h.distribution(w, r, true)
}

func (h *CustomHandler) distribution(w http.ResponseWriter, r *http.Request, metric bool) {
	f, ok := h.filter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	name := q.Get("name")
	if !models.ValidContextKey(name) {
		http.Error(w, "name parameter required: [A-Za-z0-9_.-]{1,64}", http.StatusBadRequest)
		return
	}
	g, err := parseGroup(q, metric)
	if err != "" {
		http.Error(w, err, http.StatusBadRequest)
		return
	}
	dist, derr := h.repo.GetCustomDistribution(r.Context(), f, metric, name, g)
	if derr != nil {
		h.logger.Error("failed to get custom distribution", zap.Error(derr), zap.String("name", name), zap.Bool("metric", metric))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, dist)
}

// parseGroup reads exactly one of: key=<context key>, prop=<property key>
// (events only), by=app_version|platform|device_model|os_version|build_type.
// None: by=app_version. Returns an error message for a 400.
func parseGroup(q url.Values, metric bool) (storage.CustomGroup, string) {
	key, prop, by := q.Get("key"), q.Get("prop"), q.Get("by")
	n := 0
	for _, v := range []string{key, prop, by} {
		if v != "" {
			n++
		}
	}
	if n > 1 {
		return storage.CustomGroup{}, "give one of key, prop, by"
	}
	switch {
	case key != "":
		if !models.ValidContextKey(key) {
			return storage.CustomGroup{}, "key: [A-Za-z0-9_.-]{1,64}"
		}
		return storage.CustomGroup{Kind: "ctx", Key: key}, ""
	case prop != "":
		if metric {
			return storage.CustomGroup{}, "metrics have no properties: use key or by"
		}
		if !models.ValidContextKey(prop) {
			return storage.CustomGroup{}, "prop: [A-Za-z0-9_.-]{1,64}"
		}
		return storage.CustomGroup{Kind: "prop", Key: prop}, ""
	case by != "":
		if !storage.ValidCustomColumn(by) {
			return storage.CustomGroup{}, "by must be one of " + strings.Join([]string{"app_version", "platform", "device_model", "os_version", "build_type"}, ", ")
		}
		return storage.CustomGroup{Kind: "column", Key: by}, ""
	}
	return storage.CustomGroup{Kind: "column", Key: "app_version"}, ""
}

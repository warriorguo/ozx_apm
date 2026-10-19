package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/processor"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// APM-9: memory breakdown and run peaks, session health (crash-free
// sessions and users, ANR and exception rates) and funnels over custom
// events. Every endpoint takes the common filter (parseAdminFilter); the
// time range defaults to 7 days and may not exceed 31 days (they read every
// row of the range). Their queries run one after the other, at most two
// such requests at once (storage.analysisSlots), each bounded by
// analysisTimeout.

// AnalysisStore is the part of the repository the APM-9 endpoints read.
type AnalysisStore interface {
	GetMemoryBreakdown(ctx context.Context, f models.AdminFilter, g storage.CustomGroup) (*models.MemoryBreakdownResponse, error)
	GetRunPeaks(ctx context.Context, f models.AdminFilter, metric string, g storage.CustomGroup) (*models.RunPeaksResponse, error)
	GetSessionHealth(ctx context.Context, f models.AdminFilter, g storage.CustomGroup, crashTypes []string) (*models.SessionHealthResponse, error)
	GetFunnel(ctx context.Context, f models.AdminFilter, steps []models.FunnelStep, windowSec int64, by string) (*models.FunnelResponse, error)
}

type AnalysisHandler struct {
	repo   AnalysisStore
	logger *zap.Logger
}

func NewAnalysisHandler(repo *storage.Repository, logger *zap.Logger) *AnalysisHandler {
	h := &AnalysisHandler{logger: logger}
	if repo != nil {
		h.repo = repo
	}
	return h
}

// analysisTimeout bounds one request's queries (below the admin server's
// 30 s write timeout). Sequential queries only: a deadline is safe here
// (the clickhouse-go settings race of APM-15 needs concurrent queries).
const analysisTimeout = 25 * time.Second

// maxFunnelParam bounds the steps parameter (bytes).
const maxFunnelParam = 8 << 10

// filter parses the common filter and checks the range; false after a 400.
func (h *AnalysisHandler) filter(w http.ResponseWriter, q url.Values) (models.AdminFilter, bool) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return models.AdminFilter{}, false
	}
	f, err := parseAdminFilter(q, contextDefaultRange)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return f, false
	}
	if !f.EndTime.After(f.StartTime) {
		http.Error(w, "end_time must be after start_time", http.StatusBadRequest)
		return f, false
	}
	if f.EndTime.Sub(f.StartTime) > storage.MaxAnalysisRange {
		http.Error(w, "time range longer than 31 days: narrow it", http.StatusBadRequest)
		return f, false
	}
	return f, true
}

// group reads key=<context key> or by=<column> (default app_version).
func (h *AnalysisHandler) group(w http.ResponseWriter, q url.Values) (storage.CustomGroup, bool) {
	if q.Get("prop") != "" {
		http.Error(w, "prop is not supported here: use key or by", http.StatusBadRequest)
		return storage.CustomGroup{}, false
	}
	g, msg := parseGroup(q, true)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return g, false
	}
	return g, true
}

// fail answers a storage error: 503 when the request ran out of time or
// waited too long for a slot, else 500.
func (h *AnalysisHandler) fail(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		h.logger.Warn(what+": timed out", zap.Error(err))
		http.Error(w, "the query took too long: narrow the time range or the filter", http.StatusServiceUnavailable)
		return
	}
	h.logger.Error(what, zap.Error(err))
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// MemoryBreakdown: GET /api/memory/breakdown[?by=..|key=..] -> per group
// the perf samples' memory figures (p50/p95/max of the samples that have
// each figure).
func (h *AnalysisHandler) MemoryBreakdown(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, ok := h.filter(w, q)
	if !ok {
		return
	}
	g, ok := h.group(w, q)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), analysisTimeout)
	defer cancel()
	resp, err := h.repo.GetMemoryBreakdown(ctx, f, g)
	if err != nil {
		h.fail(w, "memory breakdown", err)
		return
	}
	writeJSON(w, resp)
}

// RunPeaks: GET /api/memory/runs?metric=peak_rss|peak_alloc|peak_reserved|peak_gfx|peak_mem|peak_mono_heap[&by=..|key=..]
// -> per group the distribution of the runs' peak (default peak_rss) and
// how many runs crashed, were killed for low memory or otherwise; plus a
// histogram of the peak.
func (h *AnalysisHandler) RunPeaks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := q.Get("metric")
	if metric == "" {
		metric = "peak_rss"
	}
	if !storage.ValidRunPeakMetric(metric) {
		http.Error(w, "metric must be one of peak_rss, peak_alloc, peak_reserved, peak_gfx, peak_mem, peak_mono_heap", http.StatusBadRequest)
		return
	}
	f, ok := h.filter(w, q)
	if !ok {
		return
	}
	g, ok := h.group(w, q)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), analysisTimeout)
	defer cancel()
	resp, err := h.repo.GetRunPeaks(ctx, f, metric, g)
	if err != nil {
		h.fail(w, "run peaks", err)
		return
	}
	writeJSON(w, resp)
}

// Health: GET /api/health/sessions[?by=..|key=..][&crash_types=native,java,oom,killed]
// -> per group and build type: sessions, users, crash-free sessions and
// users, ANR rate, exception rates and crashes per type. crash_types: which
// crash types make a session crashed (default storage.DefaultHealthCrashTypes).
func (h *AnalysisHandler) Health(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, ok := h.filter(w, q)
	if !ok {
		return
	}
	g, ok := h.group(w, q)
	if !ok {
		return
	}
	var types []string
	if ct := q.Get("crash_types"); ct != "" {
		seen := map[string]bool{}
		for _, t := range strings.Split(ct, ",") {
			t = strings.TrimSpace(t)
			if !processor.CrashTypes[t] {
				http.Error(w, "crash_types: comma-separated native, java, oom, killed, anr, unknown", http.StatusBadRequest)
				return
			}
			if !seen[t] {
				seen[t] = true
				types = append(types, t)
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), analysisTimeout)
	defer cancel()
	resp, err := h.repo.GetSessionHealth(ctx, f, g, types)
	if err != nil {
		h.fail(w, "session health", err)
		return
	}
	writeJSON(w, resp)
}

// parseFunnelSteps reads the steps parameter: a JSON array of
// {"name": "...", "props": {"key": "value"}} (1 to 8 steps, at most 4
// properties each; names and keys [A-Za-z0-9_.-]{1,64}, values up to 256
// characters). Returns a message for a 400.
func parseFunnelSteps(raw string) ([]models.FunnelStep, string) {
	if raw == "" {
		return nil, "steps parameter required: a JSON array of {\"name\":\"...\",\"props\":{...}}"
	}
	if len(raw) > maxFunnelParam {
		return nil, "steps parameter too long"
	}
	var steps []models.FunnelStep
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&steps); err != nil {
		return nil, "steps: not a JSON array of {\"name\",\"props\"}"
	}
	if len(steps) == 0 || len(steps) > storage.MaxFunnelSteps {
		return nil, "steps: 1 to " + strconv.Itoa(storage.MaxFunnelSteps) + " steps"
	}
	for i, s := range steps {
		if !models.ValidContextKey(s.Name) {
			return nil, "step " + strconv.Itoa(i+1) + ": name must be [A-Za-z0-9_.-]{1,64}"
		}
		if len(s.Props) > storage.MaxFunnelPropsPerStep {
			return nil, "step " + strconv.Itoa(i+1) + ": at most " + strconv.Itoa(storage.MaxFunnelPropsPerStep) + " properties"
		}
		for k, v := range s.Props {
			if !models.ValidContextKey(k) {
				return nil, "step " + strconv.Itoa(i+1) + ": property keys must be [A-Za-z0-9_.-]{1,64}"
			}
			if utf8.RuneCountInString(v) > models.MaxContextValueLength {
				return nil, "step " + strconv.Itoa(i+1) + ": property values up to 256 characters"
			}
		}
		if len(s.Props) == 0 {
			steps[i].Props = nil
		}
	}
	return steps, ""
}

// Funnel: GET /api/funnel?steps=<JSON>[&window=<seconds>][&by=app_version|build_type|platform]
// -> the sessions that reached each step in order (custom events of the
// session within window seconds of the first step, default 2 h, at most
// 24 h), the conversion from the first and the previous step, overall or
// per value of by (top 20).
func (h *AnalysisHandler) Funnel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	steps, msg := parseFunnelSteps(q.Get("steps"))
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	window := int64(storage.DefaultFunnelWindowSec)
	if ws := q.Get("window"); ws != "" {
		v, err := strconv.ParseInt(ws, 10, 64)
		if err != nil || v <= 0 || v > storage.MaxFunnelWindowSec {
			http.Error(w, "window: seconds, 1 to 86400", http.StatusBadRequest)
			return
		}
		window = v
	}
	by := q.Get("by")
	if !storage.ValidFunnelGroup(by) {
		http.Error(w, "by must be app_version, build_type or platform", http.StatusBadRequest)
		return
	}
	f, ok := h.filter(w, q)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), analysisTimeout)
	defer cancel()
	resp, err := h.repo.GetFunnel(ctx, f, steps, window, by)
	if err != nil {
		h.fail(w, "funnel", err)
		return
	}
	writeJSON(w, resp)
}

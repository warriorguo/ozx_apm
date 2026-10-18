package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

type CrashHandler struct {
	repo   *storage.Repository
	logger *zap.Logger
	// symbols names native frames from the uploaded symbols when a crash is
	// read (APM-7), so symbols uploaded after a crash apply to it.
	symbols *symbols.Symbolizer
}

// crashSymbolCacheSize: resolved addresses kept by the admin symbolizer.
const crashSymbolCacheSize = 50000

// crashSymbolTimeout bounds the symbol lookups of one request; on timeout
// or error the frames are shown as sent (with a warning in the log).
const crashSymbolTimeout = 5 * time.Second

func NewCrashHandler(repo *storage.Repository, logger *zap.Logger) *CrashHandler {
	h := &CrashHandler{
		repo:   repo,
		logger: logger,
	}
	if repo != nil {
		h.symbols = symbols.NewSymbolizer(repo, crashSymbolCacheSize)
	}
	return h
}

func (h *CrashHandler) ListCrashes(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	f, err := parseAdminFilter(q, 7*24*time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

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

	crashes, totalCount, err := h.repo.GetCrashGroups(ctx, f, page, pageSize)
	if err != nil {
		h.logger.Error("failed to get crash groups", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// APM-8: each group's issue state.
	fps := make([]string, len(crashes))
	for i := range crashes {
		fps[i] = crashes[i].Fingerprint
	}
	if st := issueStatuses(ctx, h.repo, models.IssueKindCrash, fps, h.logger); st != nil {
		for i := range crashes {
			crashes[i].IssueStatusInfo = st[crashes[i].Fingerprint]
		}
	}
	h.addTopFrames(ctx, crashes)

	resp := models.CrashListResponse{
		Crashes:    crashes,
		TotalCount: totalCount,
		Page:       page,
		PageSize:   pageSize,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// addTopFrames symbolicates the top in-app frame of each group's latest crash.
func (h *CrashHandler) addTopFrames(ctx context.Context, groups []models.CrashGroup) {
	sctx, cancel := context.WithTimeout(ctx, crashSymbolTimeout)
	defer cancel()
	s := h.symbols
	for i := range groups {
		g := &groups[i]
		if g.SampleStack == "" {
			continue
		}
		top, sym, err := resolveTopFrame(sctx, s, g.SampleStack, g.CrashType, symbols.Fallback{AppVersion: g.SampleVersion, ABI: g.SampleABI})
		if err != nil {
			// Once: the remaining groups show addresses.
			h.logger.Warn("crash list: symbol lookup failed; showing addresses", zap.Error(err))
			s = nil
		}
		g.TopFrame, g.Symbolicated = top, sym
	}
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

	f, err := parseAdminFilter(q, 30*24*time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	detail, err := h.repo.GetCrashDetail(ctx, fingerprint, f)
	if err != nil {
		h.logger.Error("failed to get crash detail", zap.Error(err), zap.String("fingerprint", fingerprint))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if detail == nil {
		http.Error(w, "crash not found", http.StatusNotFound)
		return
	}
	if st := issueStatuses(ctx, h.repo, models.IssueKindCrash, []string{fingerprint}, h.logger); st != nil {
		detail.IssueStatusInfo = st[fingerprint]
	}
	detail.Symbols = []models.CrashSymbolStatus{}
	if s := detail.Sample; s != nil {
		sctx, cancel := context.WithTimeout(ctx, crashSymbolTimeout)
		threads, status, err := symbolicateStack(sctx, h.symbols, s.Stack, symbols.Fallback{AppVersion: s.AppVersion, ABI: s.ABI})
		cancel()
		if err != nil {
			h.logger.Warn("crash detail: symbol lookup failed; showing addresses", zap.Error(err), zap.String("fingerprint", fingerprint))
			threads, status, _ = symbolicateStack(ctx, nil, s.Stack, symbols.Fallback{})
		}
		s.Threads = threads
		detail.Symbols = status
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(detail)
}

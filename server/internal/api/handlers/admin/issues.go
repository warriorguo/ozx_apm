package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/alert"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// Issue state (APM-8): an exception or crash group is unresolved until an
// admin marks it resolved; the alert evaluator reopens it on a regression.

// AdminActionHeader must be sent with state changes ("1"). It is not in the
// CORS allow-list, so a page of another origin cannot send it (the admin API
// has no login; RELEASE.md, APM-14 "open risk").
const AdminActionHeader = "X-Apm-Admin"

// IssueStateReader reads issue states (*storage.Repository).
type IssueStateReader interface {
	IssueStates(ctx context.Context, kind string, fingerprints []string) (map[string]models.IssueState, error)
}

// IssueStore is what the state endpoints need (*storage.Repository).
type IssueStore interface {
	IssueStateReader
	InsertIssueState(ctx context.Context, s models.IssueState) error
	IssueVersions(ctx context.Context, kind, fingerprint string, includeDev bool) ([]string, bool, error)
	VersionFirstSeen(ctx context.Context, includeDev bool) (map[string]time.Time, error)
}

// issueStatuses returns the state summary per fingerprint (unresolved when
// an issue has no state row), or nil when the states cannot be read (logged;
// the list is still served, without states).
func issueStatuses(ctx context.Context, r IssueStateReader, kind string, fps []string, logger *zap.Logger) map[string]models.IssueStatusInfo {
	if r == nil || len(fps) == 0 {
		return nil
	}
	states, err := r.IssueStates(ctx, kind, fps)
	if err != nil {
		logger.Warn("could not read issue states", zap.String("kind", kind), zap.Error(err))
		return nil
	}
	out := make(map[string]models.IssueStatusInfo, len(fps))
	for _, fp := range fps {
		if s, ok := states[fp]; ok {
			out[fp] = s.StatusInfo()
		} else {
			out[fp] = models.IssueStatusInfo{Status: models.IssueUnresolved}
		}
	}
	return out
}

// IssueHandler serves GET and POST /api/issues/state.
type IssueHandler struct {
	store      IssueStore
	logger     *zap.Logger
	includeDev bool // the alerting's build filter (config alert.include_dev_builds)
	now        func() time.Time
}

func NewIssueHandler(repo *storage.Repository, logger *zap.Logger, includeDev bool) *IssueHandler {
	h := &IssueHandler{logger: logger, includeDev: includeDev, now: time.Now}
	if repo != nil {
		h.store = repo
	}
	return h
}

func defaultState(kind, fp string) models.IssueState {
	return models.IssueState{Kind: kind, Fingerprint: fp, Status: models.IssueUnresolved}
}

// Get: GET /api/issues/state?kind=exception|crash&fingerprint=F -> the
// state (unresolved with no other field when it was never changed).
func (h *IssueHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	kind, fp := q.Get("kind"), q.Get("fingerprint")
	if !models.ValidIssueKind(kind) {
		http.Error(w, "kind must be exception or crash", http.StatusBadRequest)
		return
	}
	if fp == "" || len(fp) > models.MaxFingerprintLength {
		http.Error(w, "fingerprint parameter required", http.StatusBadRequest)
		return
	}
	states, err := h.store.IssueStates(r.Context(), kind, []string{fp})
	if err != nil {
		h.logger.Error("failed to read issue state", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	st, ok := states[fp]
	if !ok {
		st = defaultState(kind, fp)
	}
	writeJSON(w, st)
}

// Set: POST /api/issues/state with header X-Apm-Admin: 1 and
// {"kind","fingerprint","status":"resolved"|"unresolved",
// "resolved_in_version" (optional: the version with the fix), "comment"}.
// Resolving records the newest version that had the issue
// (last_seen_version) and the time; regressions are occurrences after it in
// a newer version. 404 when the issue has no row in the retained data.
func (h *IssueHandler) Set(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	if r.Header.Get(AdminActionHeader) != "1" {
		http.Error(w, "missing header "+AdminActionHeader+": 1", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		http.Error(w, "could not read the body", http.StatusBadRequest)
		return
	}
	var req models.IssueStateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `body must be JSON {"kind","fingerprint","status","resolved_in_version","comment"}`, http.StatusBadRequest)
		return
	}
	req.ResolvedInVersion = strings.TrimSpace(req.ResolvedInVersion)
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	versions, found, err := h.store.IssueVersions(ctx, req.Kind, req.Fingerprint, h.includeDev)
	if err != nil {
		h.logger.Error("failed to read issue versions", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no "+req.Kind+" with this fingerprint in the retained data", http.StatusNotFound)
		return
	}
	prev, err := h.store.IssueStates(ctx, req.Kind, []string{req.Fingerprint})
	if err != nil {
		h.logger.Error("failed to read issue state", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	old, existed := prev[req.Fingerprint]
	now := h.now().UTC().Truncate(time.Millisecond)
	st := models.IssueState{
		Kind: req.Kind, Fingerprint: req.Fingerprint, Status: req.Status,
		UpdatedAt: now, UpdatedBy: "admin", Comment: req.Comment,
		Version: alert.NextStateVersion(now, old.Version),
	}
	if req.Status == models.IssueResolved {
		order := alert.VersionOrder{FirstSeen: map[string]time.Time{}}
		if needsFirstSeen(versions) {
			if order.FirstSeen, err = h.store.VersionFirstSeen(ctx, h.includeDev); err != nil {
				h.logger.Error("failed to read version first-seen times", zap.Error(err))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		st.ResolvedInVersion = req.ResolvedInVersion
		st.LastSeenVersion = order.Newest(versions, now)
		st.ResolvedAt = &now
	} else if existed {
		// Reopened by hand: keep the record of the last resolution.
		st.ResolvedInVersion, st.LastSeenVersion, st.ResolvedAt = old.ResolvedInVersion, old.LastSeenVersion, old.ResolvedAt
		st.RegressedInVersion, st.RegressedAt = old.RegressedInVersion, old.RegressedAt
	}
	if err := h.store.InsertIssueState(ctx, st); err != nil {
		h.logger.Error("failed to write issue state", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.logger.Info("issue state changed", zap.String("kind", st.Kind), zap.String("fingerprint", st.Fingerprint),
		zap.String("status", st.Status), zap.String("remote", r.RemoteAddr))
	writeJSON(w, st)
}

// needsFirstSeen reports whether some pair of versions cannot be ordered by
// name (alert.CompareVersions).
func needsFirstSeen(versions []string) bool {
	for i := 1; i < len(versions); i++ {
		if _, ok := alert.CompareVersions(versions[0], versions[i]); !ok {
			return true
		}
	}
	return false
}

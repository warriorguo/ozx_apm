package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/alert"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// AlertStore is what the alert endpoints need (*storage.Repository).
type AlertStore interface {
	ListAlerts(ctx context.Context, limit int) ([]storage.AlertSummaryRow, error)
	InsertAlertRecords(ctx context.Context, recs []models.AlertRecord) error
}

// AlertHandler serves the alert outbox (APM-8):
//
//	GET  /api/alerts[?limit=N]  the most recent alerts and their delivery
//	                            status (read-only, like the rest of the API)
//	POST /api/alerts/test       queue a test alert; "Authorization: Bearer
//	                            <token>" (admin_server.alert_token,
//	                            OZX_ADMIN_ALERT_TOKEN), disabled (403) without
//	                            one, at most one per TestInterval
//
// The test alert is written to the outbox like any alert, and the alert
// evaluator (wherever it runs: one process with OZX_ALERT_ENABLED) posts it
// to its webhook at its next evaluation. This endpoint never makes an
// outbound request itself.
type AlertHandler struct {
	store  AlertStore
	logger *zap.Logger
	token  []byte

	// TestInterval: the minimum time between two test alerts (per process).
	TestInterval time.Duration
	mu           sync.Mutex
	lastTest     time.Time
	now          func() time.Time
}

func NewAlertHandler(repo *storage.Repository, logger *zap.Logger, token string) *AlertHandler {
	h := &AlertHandler{logger: logger, token: []byte(token), TestInterval: 10 * time.Second, now: time.Now}
	if repo != nil {
		h.store = repo
	}
	return h
}

// TestEnabled reports whether a token is configured.
func (h *AlertHandler) TestEnabled() bool { return len(h.token) > 0 }

// List: GET /api/alerts[?limit=N] (default 50, at most 200).
func (h *AlertHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 || v > 200 {
			http.Error(w, "limit must be 1-200", http.StatusBadRequest)
			return
		}
		limit = v
	}
	rows, err := h.store.ListAlerts(r.Context(), limit)
	if err != nil {
		h.logger.Error("failed to list alerts", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]models.AlertSummary, 0, len(rows))
	for _, row := range rows {
		s := row.AlertSummary
		var a alert.Alert
		if json.Unmarshal([]byte(row.Payload), &a) == nil {
			s.Title = a.Title
		}
		out = append(out, s)
	}
	writeJSON(w, map[string]interface{}{"alerts": out})
}

// authorizeBearer checks "Authorization: Bearer <token>" in constant time:
// 403 while no token is configured, 401 for a missing or wrong one (logged
// with the remote address, never with the token).
func authorizeBearer(w http.ResponseWriter, r *http.Request, token []byte, api, envVar string, logger *zap.Logger) bool {
	if len(token) == 0 {
		writePrivacyError(w, http.StatusForbidden, api+" disabled: no token configured (set "+envVar+")")
		return false
	}
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	reason := ""
	switch {
	case header == "":
		reason = "missing Authorization header"
	case !strings.HasPrefix(header, prefix):
		reason = "Authorization is not a bearer token"
	case subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), token) != 1:
		reason = "wrong token"
	}
	if reason == "" {
		return true
	}
	logger.Warn(api+": unauthorized request", zap.String("path", r.URL.Path),
		zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
	w.Header().Set("WWW-Authenticate", `Bearer realm="ozx-apm"`)
	writePrivacyError(w, http.StatusUnauthorized, "unauthorized: "+reason)
	return false
}

// Test: POST /api/alerts/test -> 202 {"alert_key", "note"}.
func (h *AlertHandler) Test(w http.ResponseWriter, r *http.Request) {
	if !authorizeBearer(w, r, h.token, "alert test API", "OZX_ADMIN_ALERT_TOKEN", h.logger) {
		return
	}
	if h.store == nil {
		writePrivacyError(w, http.StatusInternalServerError, "repository not configured")
		return
	}
	now := h.now()
	h.mu.Lock()
	if !h.lastTest.IsZero() && now.Sub(h.lastTest) < h.TestInterval {
		h.mu.Unlock()
		writePrivacyError(w, http.StatusTooManyRequests, "one test alert per "+h.TestInterval.String())
		return
	}
	h.lastTest = now
	h.mu.Unlock()

	rec, err := alert.NewTestAlertRecord(r.RemoteAddr, now)
	if err != nil {
		writePrivacyError(w, http.StatusInternalServerError, "could not create the test alert")
		return
	}
	if err := h.store.InsertAlertRecords(r.Context(), []models.AlertRecord{rec}); err != nil {
		h.logger.Error("failed to queue a test alert", zap.Error(err))
		writePrivacyError(w, http.StatusInternalServerError, "could not queue the test alert")
		return
	}
	h.logger.Info("test alert queued", zap.String("key", rec.AlertKey), zap.String("remote", r.RemoteAddr))
	writePrivacyJSON(w, http.StatusAccepted, map[string]string{
		"alert_key": rec.AlertKey,
		"note":      "queued; the alert evaluator posts it at its next evaluation (GET /api/alerts shows its status)",
	})
}

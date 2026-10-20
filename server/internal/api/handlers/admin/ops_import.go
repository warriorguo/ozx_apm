package admin

import (
	"bytes"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/opsimport"
)

// OpsImportHandler imports operations reference data (APM-12):
//
//	POST /api/ops/import/fx-rates   CSV date,currency,usd_per_unit       (?source=&dry_run=1)
//	POST /api/ops/import/ad-report  an ad network's daily report CSV     (?network=&currency=&map=&source=&dry_run=1)
//
// Both need "Authorization: Bearer <token>" (config admin_server.ops_token,
// OZX_ADMIN_OPS_TOKEN) and are disabled (403) without a configured token.
// The body is the CSV file (gzip allowed), at most opsimport.MaxFileBytes.
// A file is validated as a whole before anything is stored (400 with the
// line number otherwise); re-importing it replaces its rows. The answer is
// the import's summary (rows, date range, currencies or networks), never
// the data. cmd/opsimport does the same from a shell.
type OpsImportHandler struct {
	store  opsimport.Store
	logger *zap.Logger
	token  []byte
	now    func() time.Time
}

// NewOpsImportHandler: token "" disables the endpoints.
func NewOpsImportHandler(store opsimport.Store, logger *zap.Logger, token string) *OpsImportHandler {
	return &OpsImportHandler{store: store, logger: logger, token: []byte(token), now: time.Now}
}

// Enabled reports whether a token is configured.
func (h *OpsImportHandler) Enabled() bool { return len(h.token) > 0 }

func (h *OpsImportHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if !h.Enabled() {
		writePrivacyError(w, http.StatusForbidden,
			"ops import disabled: no token configured (set OZX_ADMIN_OPS_TOKEN, RELEASE.md APM-12)")
		return false
	}
	if reason := bearerReason(r.Header.Get("Authorization"), h.token); reason != "" {
		h.logger.Warn("ops import: unauthorized request", zap.String("path", r.URL.Path),
			zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
		w.Header().Set("WWW-Authenticate", `Bearer realm="ozx-apm-ops"`)
		writePrivacyError(w, http.StatusUnauthorized, "unauthorized: "+reason)
		return false
	}
	return true
}

func (h *OpsImportHandler) body(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if h.store == nil {
		writePrivacyError(w, http.StatusInternalServerError, "repository not configured")
		return nil, false
	}
	b, err := readBody(r, opsimport.MaxFileBytes, opsimport.MaxFileBytes)
	if errors.Is(err, errTooLarge) {
		writePrivacyError(w, http.StatusRequestEntityTooLarge, "file too large: split it")
		return nil, false
	}
	if err != nil {
		writePrivacyError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return nil, false
	}
	return b, true
}

func importSource(r *http.Request, kind string) string {
	s := strings.TrimSpace(r.URL.Query().Get("source"))
	if s == "" {
		return "api:" + kind
	}
	s = path.Base(s)
	if len(s) > 128 {
		s = s[:128]
	}
	return strings.ToValidUTF8(s, "")
}

func dryRun(r *http.Request) bool {
	v := r.URL.Query().Get("dry_run")
	return v == "1" || strings.EqualFold(v, "true")
}

// FXRates handles POST /api/ops/import/fx-rates.
func (h *OpsImportHandler) FXRates(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	b, ok := h.body(w, r)
	if !ok {
		return
	}
	res, err := opsimport.ImportFXRates(r.Context(), h.store, bytes.NewReader(b), importSource(r, "fx-rates"), h.now(), dryRun(r))
	h.done(w, res, err)
}

// AdReport handles POST /api/ops/import/ad-report.
func (h *OpsImportHandler) AdReport(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	q := r.URL.Query()
	mapping, err := opsimport.ParseMapping(q.Get("map"))
	if err != nil {
		writePrivacyError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, ok := h.body(w, r)
	if !ok {
		return
	}
	opt := opsimport.AdReportOptions{Network: q.Get("network"), Currency: q.Get("currency"), Mapping: mapping, Source: importSource(r, "ad-report")}
	res, err := opsimport.ImportAdReport(r.Context(), h.store, bytes.NewReader(b), opt, h.now(), dryRun(r))
	h.done(w, res, err)
}

func (h *OpsImportHandler) done(w http.ResponseWriter, res opsimport.Result, err error) {
	if err != nil {
		if res.Rows > 0 {
			// Parsed, not stored.
			h.logger.Error("ops import failed to store", zap.String("kind", res.Kind), zap.Int("rows", res.Rows), zap.Error(err))
			writePrivacyError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writePrivacyError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Info("ops import", zap.String("kind", res.Kind), zap.Int("rows", res.Rows), zap.String("from", res.From),
		zap.String("to", res.To), zap.Strings("keys", res.Keys), zap.Bool("dry_run", res.DryRun))
	writePrivacyJSON(w, http.StatusOK, res)
}

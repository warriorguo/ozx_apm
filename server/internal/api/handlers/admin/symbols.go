package admin

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

// SymbolStore is what the symbol endpoints need from storage (*storage.Repository).
type SymbolStore interface {
	InsertSymbolRows(ctx context.Context, buildID, uploadID string, funcs []symbols.Func, lines []symbols.Line) error
	CountSymbolRows(ctx context.Context, buildID, uploadID string) (funcs, lines uint64, err error)
	InsertSymbolUpload(ctx context.Context, c symbols.Commit) error
	ListSymbolUploads(ctx context.Context, f storage.SymbolUploadFilter) ([]symbols.UploadRef, error)
}

// SymbolHandler serves the symbol upload of cmd/apmsym (APM-7):
//
//	POST /api/symbols/part    a part of an upload (gzip JSON, symbols.Part)
//	POST /api/symbols/commit  the end of an upload (JSON, symbols.Commit)
//	GET  /api/symbols         committed uploads (?build_id=&app_version=&module=)
//
// The two POSTs need "Authorization: Bearer <token>" (config
// admin_server.symbols_token, OZX_SYMBOLS_UPLOAD_TOKEN) and are disabled
// (403) without a configured token. The GET is read-only like the rest of
// the admin API. Bodies are bounded (symbols.MaxPartBodyBytes compressed,
// symbols.MaxPartDecodedBytes once decompressed, MaxPartRows rows), every
// text is cleaned and cut, and at most two parts are inserted at once (the
// ClickHouse connections are shared with ingest).
type SymbolHandler struct {
	store  SymbolStore
	logger *zap.Logger
	token  []byte
	slots  chan struct{}
}

// NewSymbolHandler: token "" disables the upload endpoints.
func NewSymbolHandler(store SymbolStore, logger *zap.Logger, token string) *SymbolHandler {
	return &SymbolHandler{store: store, logger: logger, token: []byte(token), slots: make(chan struct{}, 2)}
}

// Enabled reports whether a token is configured.
func (h *SymbolHandler) Enabled() bool { return len(h.token) > 0 }

func (h *SymbolHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if !h.Enabled() {
		writePrivacyError(w, http.StatusForbidden,
			"symbol upload disabled: no token configured (set OZX_SYMBOLS_UPLOAD_TOKEN, RELEASE.md APM-7)")
		return false
	}
	if reason := bearerReason(r.Header.Get("Authorization"), h.token); reason != "" {
		h.logger.Warn("symbol upload: unauthorized request", zap.String("path", r.URL.Path),
			zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
		w.Header().Set("WWW-Authenticate", `Bearer realm="ozx-apm-symbols"`)
		writePrivacyError(w, http.StatusUnauthorized, "unauthorized: "+reason)
		return false
	}
	return true
}

// bearerReason checks "Bearer <token>" in constant time; "" when it matches.
func bearerReason(header string, token []byte) string {
	const prefix = "Bearer "
	switch {
	case header == "":
		return "missing Authorization header"
	case !strings.HasPrefix(header, prefix):
		return "Authorization is not a bearer token"
	case subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), token) != 1:
		return "wrong token"
	}
	return ""
}

var errTooLarge = errors.New("too large")

// readBody reads at most max bytes of the body, decompressing gzip (also
// bounded: a small body cannot expand without limit).
func readBody(r *http.Request, maxCompressed, maxDecoded int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxCompressed+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxCompressed {
		return nil, errTooLarge
	}
	if !strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		if int64(len(raw)) > maxDecoded {
			return nil, errTooLarge
		}
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, maxDecoded+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxDecoded {
		return nil, errTooLarge
	}
	return out, nil
}

func (h *SymbolHandler) badBody(w http.ResponseWriter, err error) {
	if errors.Is(err, errTooLarge) {
		writePrivacyError(w, http.StatusRequestEntityTooLarge, "body too large: send smaller parts (apmsym -part-bytes)")
		return
	}
	writePrivacyError(w, http.StatusBadRequest, "invalid body: "+err.Error())
}

// Part stores one part of an upload.
func (h *SymbolHandler) Part(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if h.store == nil {
		writePrivacyError(w, http.StatusInternalServerError, "repository not configured")
		return
	}
	body, err := readBody(r, symbols.MaxPartBodyBytes, symbols.MaxPartDecodedBytes)
	if err != nil {
		h.badBody(w, err)
		return
	}
	var p symbols.Part
	if err := json.Unmarshal(body, &p); err != nil {
		h.badBody(w, err)
		return
	}
	funcs, lines, err := p.Decode()
	if err != nil {
		writePrivacyError(w, http.StatusBadRequest, err.Error())
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-r.Context().Done():
		writePrivacyError(w, http.StatusServiceUnavailable, "busy")
		return
	}
	if err := h.store.InsertSymbolRows(r.Context(), p.BuildID, p.UploadID, funcs, lines); err != nil {
		h.logger.Error("symbol upload: insert failed", zap.String("build_id", p.BuildID),
			zap.String("upload_id", p.UploadID), zap.Int("part", p.Part), zap.Error(err))
		writePrivacyError(w, http.StatusServiceUnavailable, "storage error, retry")
		return
	}
	writePrivacyJSON(w, http.StatusOK, map[string]interface{}{"part": p.Part, "functions": len(funcs), "lines": len(lines)})
}

// Commit ends an upload once every part is stored.
func (h *SymbolHandler) Commit(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if h.store == nil {
		writePrivacyError(w, http.StatusInternalServerError, "repository not configured")
		return
	}
	body, err := readBody(r, 16*1024, 16*1024)
	if err != nil {
		h.badBody(w, err)
		return
	}
	var c symbols.Commit
	if err := json.Unmarshal(body, &c); err != nil {
		h.badBody(w, err)
		return
	}
	if err := c.Validate(); err != nil {
		writePrivacyError(w, http.StatusBadRequest, err.Error())
		return
	}
	funcs, lines, err := h.store.CountSymbolRows(r.Context(), c.BuildID, c.UploadID)
	if err != nil {
		h.logger.Error("symbol upload: count failed", zap.String("build_id", c.BuildID), zap.Error(err))
		writePrivacyError(w, http.StatusServiceUnavailable, "storage error, retry")
		return
	}
	if funcs != c.Functions || lines != c.Lines {
		writePrivacyJSON(w, http.StatusConflict, map[string]interface{}{
			"error":            "incomplete upload: the stored rows do not match the commit (upload again)",
			"functions":        c.Functions,
			"functions_stored": funcs,
			"lines":            c.Lines,
			"lines_stored":     lines,
		})
		return
	}
	if err := h.store.InsertSymbolUpload(r.Context(), c); err != nil {
		h.logger.Error("symbol upload: commit failed", zap.String("build_id", c.BuildID), zap.Error(err))
		writePrivacyError(w, http.StatusServiceUnavailable, "storage error, retry")
		return
	}
	h.logger.Info("symbols uploaded", zap.String("build_id", c.BuildID), zap.String("module", c.Module),
		zap.String("arch", c.Arch), zap.String("app_version", c.AppVersion), zap.Uint64("functions", c.Functions),
		zap.Uint64("lines", c.Lines), zap.String("upload_id", c.UploadID))
	writePrivacyJSON(w, http.StatusOK, map[string]interface{}{"status": "committed", "build_id": c.BuildID, "upload_id": c.UploadID})
}

// List returns committed uploads, newest first (at most 200).
func (h *SymbolHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writePrivacyError(w, http.StatusInternalServerError, "repository not configured")
		return
	}
	q := r.URL.Query()
	f := storage.SymbolUploadFilter{Limit: 200}
	if id := q.Get("build_id"); id != "" {
		norm, err := symbols.NormalizeBuildID(id)
		if err != nil {
			writePrivacyError(w, http.StatusBadRequest, err.Error())
			return
		}
		f.BuildID = norm
	}
	f.AppVersion = symbols.Clean(q.Get("app_version"), symbols.MaxVersionLength)
	f.Module = symbols.Clean(q.Get("module"), symbols.MaxModuleLength)
	ups, err := h.store.ListSymbolUploads(r.Context(), f)
	if err != nil {
		h.logger.Error("list symbol uploads", zap.Error(err))
		writePrivacyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ups == nil {
		ups = []symbols.UploadRef{}
	}
	writePrivacyJSON(w, http.StatusOK, map[string]interface{}{"uploads": ups})
}

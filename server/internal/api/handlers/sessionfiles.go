package handlers

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/sessionfiles"
)

// Session file upload (APM-13), on the ingest server:
//
//	POST /v1/session-files                   create or resume (JSON sessionfiles.CreateRequest)
//	PUT  /v1/session-files/{id}/parts/{n}    part n (raw bytes; X-Apm-Part-Sha256, X-Apm-Device-Id)
//	POST /v1/session-files/{id}/commit       join and verify (JSON sessionfiles.CommitRequest)
//
// Authentication: the ingest app key when auth is enabled (middleware.Auth,
// as for /v1/events) and always the upload key, X-Apm-Upload-Key, compared
// in constant time with session_files.upload_key
// (OZX_SESSION_FILES_UPLOAD_KEY), which only development builds carry
// (ApmConfig.SessionFilesUploadKey). The server also refuses any build type
// but dev and editor.
//
// Answers: 200 with the upload's state; errors as JSON {"error",
// "reject_code", "missing", "retry"} with Retry-After where a retry makes
// sense. A reject_code means "never retry this upload" (the SDK drops it);
// everything else is retried later (the SDK keeps the recording).

// Headers of the protocol.
const (
	UploadKeyHeader  = "X-Apm-Upload-Key"
	DeviceIDHeader   = "X-Apm-Device-Id"
	PartSHA256Header = "X-Apm-Part-Sha256"
)

// SessionFileHandler serves the upload. svc nil: the object store is not
// configured on this server.
type SessionFileHandler struct {
	svc       *sessionfiles.Service
	uploadKey []byte
	logger    *zap.Logger
	// PartReadTimeout: how long a part's body may take to arrive (longer than
	// the ingest server's read timeout: a phone on a slow network).
	PartReadTimeout time.Duration
	// CommitTimeout: how long a commit (assembly) may take.
	CommitTimeout time.Duration
}

// NewSessionFileHandler returns the handler.
func NewSessionFileHandler(svc *sessionfiles.Service, uploadKey string, logger *zap.Logger) *SessionFileHandler {
	return &SessionFileHandler{svc: svc, uploadKey: []byte(uploadKey), logger: logger,
		PartReadTimeout: 2 * time.Minute, CommitTimeout: 5 * time.Minute}
}

// Enabled reports whether uploads are accepted.
func (h *SessionFileHandler) Enabled() bool { return h.svc != nil && len(h.uploadKey) > 0 }

type sessionFileError struct {
	Error      string `json:"error"`
	RejectCode string `json:"reject_code,omitempty"`
	Missing    []int  `json:"missing,omitempty"`
	Retry      bool   `json:"retry,omitempty"`
}

func writeSessionFileJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (h *SessionFileHandler) writeError(w http.ResponseWriter, err error) {
	var e *sessionfiles.Error
	if !errors.As(err, &e) {
		h.logger.Error("session files: unexpected error", zap.Error(err))
		e = &sessionfiles.Error{Status: http.StatusInternalServerError, Message: "internal error", RetryAfter: 60}
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	writeSessionFileJSON(w, e.Status, sessionFileError{Error: e.Message, RejectCode: e.RejectCode, Missing: e.Missing,
		Retry: e.RejectCode == "" && e.Status >= 500})
}

// authorize: 503 (kept on the device, retried in an hour) while the feature
// is not configured here; 401 for a missing or wrong upload key (retried
// with backoff: a key rotation must not make dev builds drop recordings).
func (h *SessionFileHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if !h.Enabled() {
		w.Header().Set("Retry-After", "3600")
		writeSessionFileJSON(w, http.StatusServiceUnavailable, sessionFileError{
			Error: "session file upload is not configured on this server (OZX_S3_* and OZX_SESSION_FILES_UPLOAD_KEY, RELEASE.md APM-13)", Retry: true})
		return false
	}
	got := sha256.Sum256([]byte(r.Header.Get(UploadKeyHeader)))
	want := sha256.Sum256(h.uploadKey)
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		reason := "wrong upload key"
		if r.Header.Get(UploadKeyHeader) == "" {
			reason = "missing upload key"
		}
		h.logger.Warn("session files: unauthorized upload", zap.String("path", r.URL.Path),
			zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
		w.Header().Set("Retry-After", "3600")
		writeSessionFileJSON(w, http.StatusUnauthorized, sessionFileError{Error: "unauthorized: " + reason})
		return false
	}
	return true
}

func readJSONBody(r *http.Request, max int64, v interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return &sessionfiles.Error{Status: http.StatusBadRequest, Message: "reading the body failed", RetryAfter: 5}
	}
	if int64(len(body)) > max {
		return &sessionfiles.Error{Status: http.StatusRequestEntityTooLarge, RejectCode: "invalid_request", Message: "body too large"}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &sessionfiles.Error{Status: http.StatusBadRequest, RejectCode: "invalid_request", Message: "invalid JSON: " + err.Error()}
	}
	return nil
}

// Create handles POST /v1/session-files.
func (h *SessionFileHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var req sessionfiles.CreateRequest
	if err := readJSONBody(r, sessionfiles.MaxCreateBody, &req); err != nil {
		h.writeError(w, err)
		return
	}
	state, err := h.svc.Create(r.Context(), req)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeSessionFileJSON(w, http.StatusOK, state)
}

// Part handles PUT /v1/session-files/{id}/parts/{n}.
func (h *SessionFileHandler) Part(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n < 0 || n >= sessionfiles.MaxParts {
		h.writeError(w, &sessionfiles.Error{Status: http.StatusBadRequest, RejectCode: "invalid_request", Message: "invalid part number"})
		return
	}
	if r.ContentLength > sessionfiles.MaxPartBytes {
		h.writeError(w, &sessionfiles.Error{Status: http.StatusRequestEntityTooLarge, RejectCode: "invalid_request", Message: "part too large"})
		return
	}
	// A part may take longer than the server's read timeout on a slow network.
	// The write deadline too: the server's WriteTimeout counts from the
	// request's headers, so a slow body would leave no time for the answer.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(h.PartReadTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(h.PartReadTimeout + 30*time.Second))
	if err := h.svc.PutPart(r.Context(), chi.URLParam(r, "id"), n, r.Header.Get(DeviceIDHeader), r.Header.Get(PartSHA256Header), r.Body); err != nil {
		h.writeError(w, err)
		return
	}
	writeSessionFileJSON(w, http.StatusOK, map[string]interface{}{"part": n, "received": true})
}

// Commit handles POST /v1/session-files/{id}/commit.
func (h *SessionFileHandler) Commit(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var req sessionfiles.CommitRequest
	if err := readJSONBody(r, 4096, &req); err != nil {
		h.writeError(w, err)
		return
	}
	// Joining a large archive takes longer than the write timeout.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(h.CommitTimeout))
	state, err := h.svc.Commit(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeSessionFileJSON(w, http.StatusOK, state)
}

package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/sessionfiles"
)

// SessionFileService is what the admin endpoints need (*sessionfiles.Service).
type SessionFileService interface {
	List(ctx context.Context, f models.SessionFileFilter) ([]models.SessionFile, error)
	Availability(ctx context.Context, sessionIDs []string) ([]models.SessionFileAvailability, error)
	Get(ctx context.Context, uploadID string) (*models.SessionFile, error)
	Open(ctx context.Context, f models.SessionFile) (io.ReadCloser, int64, error)
}

// SessionFilesHandler serves the recordings of development builds (APM-13)
// on the admin server:
//
//	GET  /api/session-files            list (token): ?git_commit=&app_version=&build=&build_type=
//	                                   &device_id=&session_id=&recording=&status=&start_time=&end_time=&limit=
//	GET  /api/session-files/detail     one upload with its session.json (token): ?upload_id=
//	POST /api/session-files/link       a download link valid LinkTTL (token): {"upload_id":"..."}
//	GET  /api/session-files/download   the zip: ?upload_id= with the token, or the link's &expires=&sig=
//	GET  /api/session-files/available  which of the sessions have a recording (no token):
//	                                   ?session_id=a&session_id=b (ids, sizes, times only)
//
// The token is session_files.admin_token (OZX_ADMIN_SESSIONFILES_TOKEN,
// "Authorization: Bearer"); without it every endpoint but "available"
// answers 403. The admin UI has no login: it asks for the token, keeps it in
// sessionStorage (this browser tab), sends it as a header, and for a
// download asks for a link: a URL signed with a key derived from the token,
// valid for LinkTTL and for one upload, which the browser opens without a
// header. Recordings are never served without one of the two.
type SessionFilesHandler struct {
	svc     SessionFileService
	logger  *zap.Logger
	token   []byte
	linkKey []byte
	now     func() time.Time
	// LinkTTL is how long a download link works.
	LinkTTL time.Duration
	// DownloadTimeout bounds one download (above the admin server's write timeout).
	DownloadTimeout time.Duration
}

// NewSessionFilesHandler: svc nil when the object store is not configured
// (every endpoint answers 503 then); token "" disables the protected ones.
func NewSessionFilesHandler(svc SessionFileService, logger *zap.Logger, token string) *SessionFilesHandler {
	h := &SessionFilesHandler{svc: svc, logger: logger, token: []byte(token), now: time.Now,
		LinkTTL: 5 * time.Minute, DownloadTimeout: 30 * time.Minute}
	if token != "" {
		m := hmac.New(sha256.New, []byte(token))
		m.Write([]byte("ozx-apm session-files download link v1"))
		h.linkKey = m.Sum(nil)
	}
	return h
}

// Enabled reports whether a token is configured.
func (h *SessionFilesHandler) Enabled() bool { return len(h.token) > 0 }

func (h *SessionFilesHandler) available(w http.ResponseWriter) bool {
	if h.svc == nil {
		writePrivacyError(w, http.StatusServiceUnavailable,
			"session files are not configured on this server (OZX_S3_*, RELEASE.md APM-13)")
		return false
	}
	return true
}

func (h *SessionFilesHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if !h.Enabled() {
		writePrivacyError(w, http.StatusForbidden,
			"session files API disabled: no token configured (set OZX_ADMIN_SESSIONFILES_TOKEN, RELEASE.md APM-13)")
		return false
	}
	if reason := bearerReason(r.Header.Get("Authorization"), h.token); reason != "" {
		h.logger.Warn("session files API: unauthorized request", zap.String("path", r.URL.Path),
			zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
		w.Header().Set("WWW-Authenticate", `Bearer realm="ozx-apm-session-files"`)
		writePrivacyError(w, http.StatusUnauthorized, "unauthorized: "+reason)
		return false
	}
	return h.available(w)
}

func sessionFileFilter(q url.Values) (models.SessionFileFilter, string) {
	f := models.SessionFileFilter{
		GitCommit:     strings.TrimSpace(q.Get("git_commit")),
		AppVersion:    q.Get("app_version"),
		AppBuild:      q.Get("build"),
		BuildType:     q.Get("build_type"),
		DeviceID:      q.Get("device_id"),
		RecordingName: q.Get("recording"),
		Status:        q.Get("status"),
	}
	if f.GitCommit != "" {
		if len(f.GitCommit) < 4 || len(f.GitCommit) > 80 {
			return f, "git_commit: at least 4 characters of the commit"
		}
	}
	switch f.Status {
	case "", "all", models.SessionFileComplete, models.SessionFileUploading, models.SessionFileFailed:
	default:
		return f, "status must be complete, uploading, failed or all"
	}
	for _, s := range q["session_id"] {
		for _, id := range strings.Split(s, ",") {
			if id = strings.TrimSpace(id); id != "" {
				f.SessionIDs = append(f.SessionIDs, id)
			}
		}
	}
	if len(f.SessionIDs) > 200 {
		return f, "at most 200 session ids"
	}
	if v := q.Get("start_time"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, "start_time must be RFC 3339"
		}
		f.StartTime = t
	}
	if v := q.Get("end_time"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, "end_time must be RFC 3339"
		}
		f.EndTime = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return f, "limit must be 1-1000"
		}
		f.Limit = n
	}
	return f, ""
}

// List handles GET /api/session-files.
func (h *SessionFilesHandler) List(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	f, msg := sessionFileFilter(r.URL.Query())
	if msg != "" {
		writePrivacyError(w, http.StatusBadRequest, msg)
		return
	}
	files, err := h.svc.List(r.Context(), f)
	if err != nil {
		h.logger.Error("session files: list", zap.Error(err))
		writePrivacyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for i := range files {
		files[i].SessionJSON = "" // in the detail only
	}
	w.Header().Set("Cache-Control", "no-store")
	writePrivacyJSON(w, http.StatusOK, map[string]interface{}{"session_files": files, "count": len(files)})
}

// Detail handles GET /api/session-files/detail.
func (h *SessionFilesHandler) Detail(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	f, ok := h.lookup(w, r, r.URL.Query().Get("upload_id"), false)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writePrivacyJSON(w, http.StatusOK, f)
}

func (h *SessionFilesHandler) lookup(w http.ResponseWriter, r *http.Request, uploadID string, complete bool) (*models.SessionFile, bool) {
	if !sessionfiles.ValidUploadID(uploadID) {
		writePrivacyError(w, http.StatusBadRequest, "upload_id must be 32 lower-case hex digits")
		return nil, false
	}
	f, err := h.svc.Get(r.Context(), uploadID)
	if err != nil {
		h.logger.Error("session files: get", zap.String("upload_id", uploadID), zap.Error(err))
		writePrivacyError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	if f == nil {
		writePrivacyError(w, http.StatusNotFound, "no such upload")
		return nil, false
	}
	if complete {
		if f.Status != models.SessionFileComplete {
			writePrivacyError(w, http.StatusConflict, "the upload is "+f.Status+", not complete")
			return nil, false
		}
		if !f.ExpiresAt.After(h.now()) {
			writePrivacyError(w, http.StatusGone, "the recording has expired")
			return nil, false
		}
	}
	return f, true
}

// Available handles GET /api/session-files/available (no token: which
// sessions have a recording, never its content or device).
func (h *SessionFilesHandler) Available(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writePrivacyJSON(w, http.StatusOK, map[string]interface{}{"configured": false, "recordings": []models.SessionFileAvailability{}})
		return
	}
	f, msg := sessionFileFilter(url.Values{"session_id": r.URL.Query()["session_id"]})
	if msg != "" {
		writePrivacyError(w, http.StatusBadRequest, msg)
		return
	}
	out, err := h.svc.Availability(r.Context(), f.SessionIDs)
	if err != nil {
		h.logger.Error("session files: availability", zap.Error(err))
		writePrivacyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writePrivacyJSON(w, http.StatusOK, map[string]interface{}{"configured": true, "recordings": out})
}

func (h *SessionFilesHandler) sign(uploadID string, expires int64) string {
	m := hmac.New(sha256.New, h.linkKey)
	m.Write([]byte(uploadID + "\n" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// Link handles POST /api/session-files/link.
func (h *SessionFilesHandler) Link(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var req struct {
		UploadID string `json:"upload_id"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err := json.Unmarshal(body, &req); err != nil {
		writePrivacyError(w, http.StatusBadRequest, `body must be {"upload_id":"..."}`)
		return
	}
	f, ok := h.lookup(w, r, req.UploadID, true)
	if !ok {
		return
	}
	expires := h.now().Add(h.LinkTTL).Unix()
	link := "/api/session-files/download?upload_id=" + f.UploadID + "&expires=" + strconv.FormatInt(expires, 10) +
		"&sig=" + h.sign(f.UploadID, expires)
	w.Header().Set("Cache-Control", "no-store")
	writePrivacyJSON(w, http.StatusOK, map[string]interface{}{"url": link, "expires_at": time.Unix(expires, 0).UTC(),
		"filename": f.RecordingName + ".zip"})
}

// linkReason checks a signed link; "" when valid.
func (h *SessionFilesHandler) linkReason(q url.Values) string {
	if !h.Enabled() {
		return "disabled"
	}
	expires, err := strconv.ParseInt(q.Get("expires"), 10, 64)
	if err != nil {
		return "malformed link"
	}
	now := h.now().Unix()
	if expires < now {
		return "link expired"
	}
	if expires > now+int64(h.LinkTTL/time.Second)+60 {
		return "link valid for too long"
	}
	want := h.sign(q.Get("upload_id"), expires)
	if !hmac.Equal([]byte(want), []byte(q.Get("sig"))) {
		return "bad signature"
	}
	return ""
}

// Download handles GET /api/session-files/download.
func (h *SessionFilesHandler) Download(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("sig") != "" {
		if !h.Enabled() {
			writePrivacyError(w, http.StatusForbidden, "session files API disabled: no token configured")
			return
		}
		if reason := h.linkReason(q); reason != "" {
			h.logger.Warn("session files API: refused download link", zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
			writePrivacyError(w, http.StatusForbidden, reason+": ask for a new link")
			return
		}
		if !h.available(w) {
			return
		}
	} else if !h.authorize(w, r) {
		return
	}
	f, ok := h.lookup(w, r, q.Get("upload_id"), true)
	if !ok {
		return
	}
	// A large archive over a slow link takes longer than the write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(h.now().Add(h.DownloadTimeout))
	rc, size, err := h.svc.Open(r.Context(), *f)
	if err != nil {
		h.logger.Error("session files: open", zap.String("upload_id", f.UploadID), zap.Error(err))
		writePrivacyError(w, http.StatusServiceUnavailable, "object store error, retry")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+f.RecordingName+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Apm-Sha256", f.SHA256)
	w.Header().Set("X-Apm-Recording", f.RecordingName)
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	n, err := io.Copy(w, rc)
	if err != nil {
		h.logger.Warn("session files: download interrupted", zap.String("upload_id", f.UploadID), zap.Int64("bytes", n), zap.Error(err))
		return
	}
	h.logger.Info("session files: downloaded", zap.String("upload_id", f.UploadID), zap.Int64("bytes", n),
		zap.Bool("signed_link", q.Get("sig") != ""), zap.String("remote", r.RemoteAddr))
}

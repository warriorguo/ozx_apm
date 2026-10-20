package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// ConfirmDeleteHeader must repeat the id of a deletion request. It guards
// against typos and against cross-site requests from a browser: it is not in
// the admin CORS allow-list, so no other origin can send it.
const ConfirmDeleteHeader = "X-Apm-Confirm-Delete"

// PrivacyStore is what the privacy endpoints need from storage
// (*storage.Repository).
type PrivacyStore interface {
	PrivacyTables(ctx context.Context, column string) ([]string, error)
	CountPrivacyRows(ctx context.Context, table, column, id string) (uint64, error)
	DeletePrivacyRows(ctx context.Context, table, column, id string) error
	InsertPrivacyAudit(ctx context.Context, rec models.PrivacyAuditRecord) error
	PrivacyAuditHistory(ctx context.Context, idType, idHash string, limit int) ([]models.PrivacyAuditRecord, error)
}

const aggregatesNote = "apm_*_daily aggregates are not changed: they hold no device or player id"

// PrivacyHandler serves the APM-14 deletion endpoints. Both need
// "Authorization: Bearer <token>" (NewPrivacyHandler) and are disabled
// without a configured token:
//
//	POST /api/privacy/delete  {"id_type":"device_id","id":"..."}
//	                          header X-Apm-Confirm-Delete: <the same id>
//	POST /api/privacy/verify  {"id_type":"device_id","id":"..."}
//
// A deletion runs in the background, one at a time, with its own deadline
// (DeleteTimeout), so it is not cut off by the admin server's write timeout.
// The request waits up to SyncWait for it: 200 when nothing of the subject is
// left, 202 while it is still running or when rows are left (run it again
// later, it is idempotent), 503 when the ClickHouse account may not delete
// (RELEASE.md has the GRANT), 500 on other errors. Every request is recorded
// in the audit table before anything is deleted; without the record nothing
// is deleted.
type PrivacyHandler struct {
	store  PrivacyStore
	logger *zap.Logger
	token  []byte // empty: the endpoints are disabled

	// SyncWait: how long POST /delete waits for the result before answering 202.
	SyncWait time.Duration
	// DeleteTimeout bounds one deletion request (all tables).
	DeleteTimeout time.Duration

	mu sync.Mutex // one deletion at a time

	// sessionFiles deletes the subject's recordings (APM-13) before the
	// tables; nil when this server has no object store configured.
	sessionFiles SessionFilePurger
}

// SessionFilePurger deletes a subject's session file recordings: objects
// first, then their index rows (*sessionfiles.Service).
type SessionFilePurger interface {
	Purge(ctx context.Context, idType, id string) models.PrivacySessionFiles
	Count(ctx context.Context, idType, id string) (int, error)
}

// SetSessionFiles makes deletions remove the subject's recordings too.
func (h *PrivacyHandler) SetSessionFiles(p SessionFilePurger) { h.sessionFiles = p }

const sessionFilesNotConfigured = "this server has no object store configured (OZX_S3_*): the subject's session file recordings, if any, " +
	"are not deleted here; run the deletion on a deployment with the object store (RELEASE.md APM-13)"

// NewPrivacyHandler: token is the bearer token both endpoints require
// (config admin_server.privacy_token, OZX_ADMIN_PRIVACY_TOKEN). With an empty
// token they answer 403 and do nothing.
func NewPrivacyHandler(store PrivacyStore, logger *zap.Logger, token string) *PrivacyHandler {
	return &PrivacyHandler{
		store:         store,
		logger:        logger,
		token:         []byte(token),
		SyncWait:      20 * time.Second,
		DeleteTimeout: 30 * time.Minute,
	}
}

func writePrivacyJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writePrivacyError(w http.ResponseWriter, status int, msg string) {
	writePrivacyJSON(w, status, map[string]string{"error": msg})
}

// Enabled reports whether a token is configured.
func (h *PrivacyHandler) Enabled() bool {
	return len(h.token) > 0
}

// authorize checks "Authorization: Bearer <token>" in constant time. It
// answers 403 while the API is disabled and 401 for a missing or wrong token
// (logged with the remote address, never with the token).
func (h *PrivacyHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if !h.Enabled() {
		writePrivacyError(w, http.StatusForbidden,
			"privacy API disabled: no token configured (set OZX_ADMIN_PRIVACY_TOKEN, RELEASE.md APM-14)")
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
	case subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), h.token) != 1:
		reason = "wrong token"
	}
	if reason == "" {
		return true
	}
	h.logger.Warn("privacy API: unauthorized request", zap.String("path", r.URL.Path),
		zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
	w.Header().Set("WWW-Authenticate", `Bearer realm="ozx-apm-privacy"`)
	writePrivacyError(w, http.StatusUnauthorized, "unauthorized: "+reason)
	return false
}

// parsePrivacyRequest reads and validates the body. Returns the column the id lives in.
func parsePrivacyRequest(r *http.Request) (models.PrivacyRequest, string, error) {
	var req models.PrivacyRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return req, "", fmt.Errorf("read body: %w", err)
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, "", fmt.Errorf(`body must be JSON {"id_type":"device_id","id":"..."}: %v`, err)
	}
	if req.IDType == "" {
		req.IDType = "device_id"
	}
	column, ok := models.PrivacyIDTypes[req.IDType]
	if !ok {
		return req, "", fmt.Errorf("unknown id_type %q (supported: device_id, player_id)", req.IDType)
	}
	if strings.TrimSpace(req.ID) == "" {
		return req, "", errors.New("id is required")
	}
	if len(req.ID) > models.MaxPrivacyIDLength {
		return req, "", fmt.Errorf("id is longer than %d bytes", models.MaxPrivacyIDLength)
	}
	for _, c := range req.ID {
		if unicode.IsControl(c) {
			return req, "", errors.New("id contains control characters")
		}
	}
	return req, column, nil
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Delete handles POST /api/privacy/delete.
func (h *PrivacyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if h.store == nil {
		writePrivacyError(w, http.StatusInternalServerError, "repository not configured")
		return
	}
	req, column, err := parsePrivacyRequest(r)
	if err != nil {
		writePrivacyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Header.Get(ConfirmDeleteHeader) != req.ID {
		writePrivacyError(w, http.StatusBadRequest,
			"deleting needs the header "+ConfirmDeleteHeader+" set to the id being deleted")
		return
	}

	job := &models.PrivacyDeletion{
		RequestID: newRequestID(),
		IDType:    req.IDType,
		IDHash:    models.PrivacyIDHash(req.ID),
		Status:    models.PrivacyStatusRunning,
		StartedAt: time.Now().UTC(),
		Tables:    []models.PrivacyTableResult{},
		Note:      aggregatesNote,
	}
	requestedBy := r.RemoteAddr
	done := make(chan *models.PrivacyDeletion, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), h.DeleteTimeout)
		defer cancel()
		done <- h.runDeletion(ctx, job, column, req.ID, requestedBy)
	}()

	select {
	case result := <-done:
		writePrivacyJSON(w, deletionStatusCode(result), result)
	case <-time.After(h.SyncWait):
		snapshot := *job
		snapshot.Tables = nil
		snapshot.Status = models.PrivacyStatusRunning
		snapshot.Note = "still running; POST /api/privacy/verify shows the outcome. " + aggregatesNote
		writePrivacyJSON(w, http.StatusAccepted, snapshot)
	}
}

func deletionStatusCode(d *models.PrivacyDeletion) int {
	switch d.Status {
	case models.PrivacyStatusDone:
		return http.StatusOK
	case models.PrivacyStatusIncomplete:
		return http.StatusAccepted
	}
	if strings.Contains(d.Error, storage.ErrPrivacyDeleteNotPermitted.Error()) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// runDeletion deletes the subject from every table that has the column and
// records the request. It owns job; the caller only reads the returned copy.
func (h *PrivacyHandler) runDeletion(ctx context.Context, job *models.PrivacyDeletion, column, id, requestedBy string) *models.PrivacyDeletion {
	h.mu.Lock()
	defer h.mu.Unlock()

	result := *job
	result.Tables = []models.PrivacyTableResult{}
	audit := models.PrivacyAuditRecord{
		RequestID:   result.RequestID,
		At:          time.Now().UTC(),
		IDType:      result.IDType,
		IDHash:      result.IDHash,
		Status:      models.PrivacyStatusRunning,
		RequestedBy: requestedBy,
	}
	log := h.logger.With(zap.String("request_id", result.RequestID), zap.String("id_type", result.IDType),
		zap.String("id_hash", result.IDHash))

	fail := func(err error) *models.PrivacyDeletion {
		now := time.Now().UTC()
		result.Status = models.PrivacyStatusFailed
		result.Error = err.Error()
		result.FinishedAt = &now
		return &result
	}

	if err := h.store.InsertPrivacyAudit(ctx, audit); err != nil {
		log.Error("privacy deletion not started: cannot write the audit record", zap.Error(err))
		return fail(fmt.Errorf("audit record could not be written, nothing was deleted: %w", err))
	}

	// Session file recordings (APM-13) first: their index rows say where
	// the objects are, so the rows go only after the objects.
	keepSessionFileRows := true
	if h.sessionFiles != nil {
		sf := h.sessionFiles.Purge(ctx, result.IDType, id)
		result.SessionFiles = &sf
		keepSessionFileRows = sf.Error != ""
	} else {
		result.Note += "; " + sessionFilesNotConfigured
	}

	tables, err := h.store.PrivacyTables(ctx, column)
	if err == nil && len(tables) == 0 {
		err = fmt.Errorf("no table has a %s column", column)
	}

	var denied error
	if err == nil {
		for _, table := range tables {
			if table == storage.SessionFilesTable && keepSessionFileRows {
				// The objects were not deleted: keep the rows that point to them.
				tr := models.PrivacyTableResult{Table: table}
				n, cerr := h.store.CountPrivacyRows(ctx, table, column, id)
				tr.RowsBefore, tr.RowsAfter = n, n
				if cerr != nil {
					tr.Error = cerr.Error()
				} else if n > 0 {
					tr.Error = "kept: the recordings' objects could not be deleted"
					if h.sessionFiles == nil {
						tr.Error = "kept: " + sessionFilesNotConfigured
					}
				}
				result.Tables = append(result.Tables, tr)
				result.RowsRemaining += tr.RowsAfter
				continue
			}
			tr, terr := h.deleteFromTable(ctx, table, column, id)
			result.Tables = append(result.Tables, tr)
			result.RowsRemaining += tr.RowsAfter
			if tr.RowsBefore > tr.RowsAfter {
				result.RowsDeleted += tr.RowsBefore - tr.RowsAfter
			}
			if errors.Is(terr, storage.ErrPrivacyDeleteNotPermitted) {
				denied = terr
				break // the same grant is missing for every table
			}
		}
	}

	now := time.Now().UTC()
	result.FinishedAt = &now
	var tableErrors []string
	for _, tr := range result.Tables {
		if tr.Error != "" {
			tableErrors = append(tableErrors, tr.Table+": "+tr.Error)
		}
	}
	if sf := result.SessionFiles; sf != nil {
		if sf.Error != "" {
			tableErrors = append(tableErrors, "session files: "+sf.Error)
		} else if sf.UploadsAfter > 0 && result.IDType != "device_id" {
			// (by device id the rows are counted in apm_session_files)
			result.RowsRemaining += uint64(sf.UploadsAfter)
		}
	}
	switch {
	case err != nil:
		result.Status = models.PrivacyStatusFailed
		result.Error = err.Error()
	case denied != nil:
		result.Status = models.PrivacyStatusFailed
		result.Error = denied.Error()
	case len(tableErrors) > 0:
		result.Status = models.PrivacyStatusFailed
		result.Error = strings.Join(tableErrors, "; ")
	case result.RowsRemaining > 0:
		result.Status = models.PrivacyStatusIncomplete
		result.Error = "rows were inserted while deleting, or a mutation has not finished: verify and run the deletion again"
	default:
		result.Status = models.PrivacyStatusDone
	}

	audit.At = now
	audit.Status = result.Status
	audit.Error = result.Error
	audit.RowsBefore = result.RowsDeleted + result.RowsRemaining
	audit.RowsAfter = result.RowsRemaining
	for _, tr := range result.Tables {
		audit.Tables = append(audit.Tables, tr.Table)
	}
	if aerr := h.store.InsertPrivacyAudit(ctx, audit); aerr != nil {
		log.Error("privacy deletion finished but its final audit record could not be written", zap.Error(aerr))
	}

	fields := []zap.Field{zap.String("status", result.Status), zap.Uint64("rows_deleted", result.RowsDeleted),
		zap.Uint64("rows_remaining", result.RowsRemaining), zap.Int("tables", len(result.Tables))}
	switch {
	case denied != nil:
		log.Error("privacy deletion refused by ClickHouse: the service account lacks ALTER DELETE; an admin must add "+
			storage.PrivacyDeleteGrant+" to its grants (RELEASE.md, APM-14)", append(fields, zap.Error(denied))...)
	case result.Status == models.PrivacyStatusFailed:
		log.Error("privacy deletion failed", append(fields, zap.String("error", result.Error))...)
	default:
		log.Info("privacy deletion", fields...)
	}
	return &result
}

// deleteFromTable deletes the subject's rows from one table. RowsAfter is
// what is left (RowsBefore when the delete was refused or the count failed).
func (h *PrivacyHandler) deleteFromTable(ctx context.Context, table, column, id string) (models.PrivacyTableResult, error) {
	tr := models.PrivacyTableResult{Table: table}
	before, err := h.store.CountPrivacyRows(ctx, table, column, id)
	if err != nil {
		tr.Error = err.Error()
		return tr, err
	}
	tr.RowsBefore, tr.RowsAfter = before, before
	if before == 0 {
		return tr, nil // nothing to delete: no mutation (and no privilege needed)
	}
	derr := h.store.DeletePrivacyRows(ctx, table, column, id)
	if errors.Is(derr, storage.ErrPrivacyDeleteNotPermitted) {
		tr.Error = derr.Error()
		return tr, derr
	}
	// Count even after another error: a mutation that timed out may still finish.
	after, err := h.store.CountPrivacyRows(ctx, table, column, id)
	if err == nil {
		tr.RowsAfter = after
	}
	if derr != nil {
		tr.Error = derr.Error()
		return tr, derr
	}
	if err != nil {
		tr.Error = err.Error()
	}
	return tr, err
}

// Verify handles POST /api/privacy/verify: the rows of the subject stored
// now, per table, and its deletion requests.
func (h *PrivacyHandler) Verify(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if h.store == nil {
		writePrivacyError(w, http.StatusInternalServerError, "repository not configured")
		return
	}
	req, column, err := parsePrivacyRequest(r)
	if err != nil {
		writePrivacyError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	out := models.PrivacyVerification{
		IDType: req.IDType,
		IDHash: models.PrivacyIDHash(req.ID),
		Tables: []models.PrivacyTableResult{},
		Note:   aggregatesNote,
	}
	tables, err := h.store.PrivacyTables(ctx, column)
	if err != nil {
		h.logger.Error("privacy verify: list tables", zap.Error(err))
		writePrivacyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for _, table := range tables {
		n, err := h.store.CountPrivacyRows(ctx, table, column, req.ID)
		if err != nil {
			h.logger.Error("privacy verify: count", zap.String("table", table), zap.Error(err))
			writePrivacyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out.Tables = append(out.Tables, models.PrivacyTableResult{Table: table, RowsAfter: n})
		out.RowsRemaining += n
	}
	if h.sessionFiles != nil {
		n, err := h.sessionFiles.Count(ctx, req.IDType, req.ID)
		sf := &models.PrivacySessionFiles{UploadsBefore: n, UploadsAfter: n}
		if err != nil {
			h.logger.Error("privacy verify: session files", zap.Error(err))
			sf.Error = "could not count the session file recordings"
		}
		out.SessionFiles = sf
		if req.IDType != "device_id" {
			out.RowsRemaining += uint64(n) // by device id they are counted in apm_session_files already
		}
	} else {
		out.Note += "; " + sessionFilesNotConfigured
	}
	out.Deletions, err = h.store.PrivacyAuditHistory(ctx, req.IDType, out.IDHash, 50)
	if err != nil {
		h.logger.Error("privacy verify: audit history", zap.Error(err))
		writePrivacyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writePrivacyJSON(w, http.StatusOK, out)
}

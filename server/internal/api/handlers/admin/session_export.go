package admin

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/sessionexport"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// SessionExportStore reads a session for the export (APM-10).
type SessionExportStore interface {
	ExportSession(ctx context.Context, q storage.SessionQuery) (*sessionexport.Input, bool, error)
}

// SessionExportHandler serves the session export.
type SessionExportHandler struct {
	repo   SessionExportStore
	logger *zap.Logger
	now    func() time.Time
}

func NewSessionExportHandler(repo *storage.Repository, logger *zap.Logger) *SessionExportHandler {
	h := &SessionExportHandler{logger: logger, now: time.Now}
	if repo != nil {
		h.repo = repo
	}
	return h
}

// Export: GET /api/sessions/export?session_id=S[&at=T][&start_time&end_time]
// -> a zip of the session in OZX's AiTelemetry session directory format
// (sessionexport): <dir>/session.json, a JSONL file per channel, custom.jsonl
// and apm.jsonl. The range is the time line's (at +- 24 h, else the last 7
// days, at most 31 days). 404 when the session has no row in the range. The
// X-Apm-Export-Truncated header lists what was cut (also in session.json).
func (h *SessionExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	sq := storage.SessionQuery{SessionID: q.Get("session_id")}
	if !validID(sq.SessionID) {
		http.Error(w, "session_id parameter required (at most 128 printable characters)", http.StatusBadRequest)
		return
	}
	if s := q.Get("at"); s != "" {
		at, err := parseTime(s)
		if err != nil {
			http.Error(w, "at: RFC 3339", http.StatusBadRequest)
			return
		}
		sq.At = &at
	}
	var err error
	if sq.Start, sq.End, err = sessionRange(q, sq.At); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sq.At = nil // the export keeps the earliest rows, whatever it was opened from
	in, found, err := h.repo.ExportSession(r.Context(), sq)
	if err != nil {
		if r.Context().Err() != nil {
			return // the client went away
		}
		h.logger.Error("failed to export session", zap.Error(err), zap.String("session_id", sq.SessionID))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "session not found in the range", http.StatusNotFound)
		return
	}
	res := sessionexport.Build(*in, h.now())
	var buf bytes.Buffer
	if err := sessionexport.WriteZip(&buf, res); err != nil {
		h.logger.Error("failed to write session export", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	// The name is [0-9A-Za-z_-] only (sessionexport.DirName).
	w.Header().Set("Content-Disposition", `attachment; filename="`+res.Dir+`.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "no-store")
	if len(res.Truncated) > 0 {
		t := ""
		for i, s := range res.Truncated {
			if i > 0 {
				t += ","
			}
			t += s
		}
		w.Header().Set("X-Apm-Export-Truncated", t)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

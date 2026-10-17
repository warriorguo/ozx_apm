package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// SessionStore is the part of the repository the session endpoints read.
type SessionStore interface {
	GetSessionTimeline(ctx context.Context, q storage.SessionQuery) (*models.SessionTimeline, error)
	GetDeviceSessions(ctx context.Context, deviceID string, start, end time.Time) (*models.DeviceSessionsResponse, error)
}

// SessionHandler serves the session time line (APM-15).
type SessionHandler struct {
	repo   SessionStore
	logger *zap.Logger
}

func NewSessionHandler(repo *storage.Repository, logger *zap.Logger) *SessionHandler {
	h := &SessionHandler{logger: logger}
	if repo != nil {
		h.repo = repo
	}
	return h
}

const (
	sessionDefaultRange = 7 * 24 * time.Hour
	// sessionAroundAt: the range around at when no range is given.
	sessionAroundAt = 24 * time.Hour
	maxIDLength     = 128
)

// validID accepts the ids the SDK generates and anything printable up to
// maxIDLength bytes (ids are bound arguments, never SQL text).
func validID(id string) bool {
	if id == "" || len(id) > maxIDLength {
		return false
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// parseTime accepts RFC 3339 with or without fractional seconds.
func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// sessionRange reads start_time/end_time (RFC 3339). Without them: around at
// (24 h each side) if given, else the last 7 days. The range must not be
// inverted nor longer than storage.SessionRangeMax.
func sessionRange(q url.Values, at *time.Time) (time.Time, time.Time, error) {
	end := time.Now()
	start := end.Add(-sessionDefaultRange)
	if at != nil {
		start, end = at.Add(-sessionAroundAt), at.Add(sessionAroundAt)
	}
	if s := q.Get("start_time"); s != "" {
		t, err := parseTime(s)
		if err != nil {
			return start, end, fmt.Errorf("start_time: RFC 3339")
		}
		start = t
	}
	if s := q.Get("end_time"); s != "" {
		t, err := parseTime(s)
		if err != nil {
			return start, end, fmt.Errorf("end_time: RFC 3339")
		}
		end = t
	}
	if end.Before(start) {
		return start, end, fmt.Errorf("end_time before start_time")
	}
	if end.Sub(start) > storage.SessionRangeMax {
		return start, end, fmt.Errorf("range longer than %d days", int(storage.SessionRangeMax/(24*time.Hour)))
	}
	return start, end, nil
}

// Timeline: GET /api/sessions/timeline?session_id=S[&at=T][&start_time&end_time][&perf=1]
// -> everything stored for the session in the range, merged by time. at
// (RFC 3339, e.g. an exception occurrence): when a type has more rows than
// returned, the ones nearest to it are kept; without a range, at +- 24 h.
func (h *SessionHandler) Timeline(w http.ResponseWriter, r *http.Request) {
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
	switch p := q.Get("perf"); p {
	case "", "0", "false":
	case "1", "true":
		sq.Perf = true
	default:
		http.Error(w, "perf must be 1, true, 0 or false", http.StatusBadRequest)
		return
	}
	var err error
	if sq.Start, sq.End, err = sessionRange(q, sq.At); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tl, err := h.repo.GetSessionTimeline(r.Context(), sq)
	if err != nil {
		h.logger.Error("failed to get session timeline", zap.Error(err), zap.String("session_id", sq.SessionID))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, tl)
}

// DeviceSessions: GET /api/sessions?device_id=D[&start_time&end_time] -> the
// device's sessions in the range (default 7 days), most recent first.
func (h *SessionHandler) DeviceSessions(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "repository not configured", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	deviceID := q.Get("device_id")
	if !validID(deviceID) {
		http.Error(w, "device_id parameter required (at most 128 printable characters)", http.StatusBadRequest)
		return
	}
	start, end, err := sessionRange(q, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := h.repo.GetDeviceSessions(r.Context(), deviceID, start, end)
	if err != nil {
		h.logger.Error("failed to get device sessions", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

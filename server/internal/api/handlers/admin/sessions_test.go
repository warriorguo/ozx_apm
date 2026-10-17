package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type fakeSessionStore struct {
	q        storage.SessionQuery
	device   string
	start    time.Time
	end      time.Time
	err      error
	timeline *models.SessionTimeline
}

func (f *fakeSessionStore) GetSessionTimeline(_ context.Context, q storage.SessionQuery) (*models.SessionTimeline, error) {
	f.q = q
	if f.timeline != nil {
		return f.timeline, f.err
	}
	return &models.SessionTimeline{SessionID: q.SessionID, Items: []models.TimelineItem{}}, f.err
}

func (f *fakeSessionStore) GetDeviceSessions(_ context.Context, device string, start, end time.Time) (*models.DeviceSessionsResponse, error) {
	f.device, f.start, f.end = device, start, end
	return &models.DeviceSessionsResponse{DeviceID: device, Sessions: []models.SessionSummary{}}, f.err
}

func TestSessionHandler_Timeline(t *testing.T) {
	store := &fakeSessionStore{}
	h := &SessionHandler{repo: store, logger: zap.NewNop()}

	w := serve(h.Timeline, "/api/sessions/timeline?session_id=1729_abcd&at=2026-10-18T10:00:05.123Z&perf=1")
	if w.Code != 200 || store.q.SessionID != "1729_abcd" || !store.q.Perf || store.q.At == nil || store.q.At.UnixMilli()%1000 != 123 {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store.q)
	}
	// Without a range: at +- 24 h.
	if store.q.End.Sub(store.q.Start) != 48*time.Hour || !store.q.At.Add(-24*time.Hour).Equal(store.q.Start) {
		t.Errorf("range %v - %v", store.q.Start, store.q.End)
	}
	// Without at: the last 7 days.
	serve(h.Timeline, "/api/sessions/timeline?session_id=s")
	if d := store.q.End.Sub(store.q.Start); d != 7*24*time.Hour || store.q.At != nil || store.q.Perf || time.Since(store.q.End) > time.Minute {
		t.Errorf("default range %v, %+v", d, store.q)
	}
	// An explicit range wins.
	serve(h.Timeline, "/api/sessions/timeline?session_id=s&at=2026-10-18T10:00:00Z&start_time=2026-10-18T09:00:00Z&end_time=2026-10-18T11:00:00Z")
	if store.q.End.Sub(store.q.Start) != 2*time.Hour {
		t.Errorf("range %v - %v", store.q.Start, store.q.End)
	}

	for _, target := range []string{
		"/api/sessions/timeline",
		"/api/sessions/timeline?session_id=" + strings.Repeat("x", 129),
		"/api/sessions/timeline?session_id=a%00b",
		"/api/sessions/timeline?session_id=s&at=yesterday",
		"/api/sessions/timeline?session_id=s&perf=yes",
		"/api/sessions/timeline?session_id=s&start_time=2026-10-18T11:00:00Z&end_time=2026-10-18T09:00:00Z",
		"/api/sessions/timeline?session_id=s&start_time=2026-08-01T00:00:00Z&end_time=2026-10-18T09:00:00Z",
		"/api/sessions/timeline?session_id=s&start_time=bad",
	} {
		if w := serve(h.Timeline, target); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", target, w.Code)
		}
	}

	store.err = errors.New("boom")
	if w := serve(h.Timeline, "/api/sessions/timeline?session_id=s"); w.Code != 500 || strings.Contains(w.Body.String(), "boom") {
		t.Errorf("error: %d %s", w.Code, w.Body)
	}
}

func TestSessionHandler_DeviceSessions(t *testing.T) {
	store := &fakeSessionStore{}
	h := &SessionHandler{repo: store, logger: zap.NewNop()}
	q := url.Values{"device_id": {"d' OR 1=1"}, "start_time": {"2026-10-01T00:00:00Z"}, "end_time": {"2026-10-18T00:00:00Z"}}
	w := serve(h.DeviceSessions, "/api/sessions?"+q.Encode())
	if w.Code != 200 || store.device != "d' OR 1=1" || store.end.Sub(store.start) != 17*24*time.Hour {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var resp models.DeviceSessionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Sessions == nil {
		t.Errorf("%s %v", w.Body, err)
	}
	if w := serve(h.DeviceSessions, "/api/sessions"); w.Code != http.StatusBadRequest {
		t.Errorf("no device: %d", w.Code)
	}
}

func TestSessionHandler_NilRepo(t *testing.T) {
	h := NewSessionHandler(nil, zap.NewNop())
	if w := serve(h.Timeline, "/api/sessions/timeline?session_id=s"); w.Code != 500 {
		t.Errorf("%d", w.Code)
	}
	if w := serve(h.DeviceSessions, "/api/sessions?device_id=d"); w.Code != 500 {
		t.Errorf("%d", w.Code)
	}
}

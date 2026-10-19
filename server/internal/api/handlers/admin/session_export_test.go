package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/sessionexport"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type fakeExportStore struct {
	q     storage.SessionQuery
	in    *sessionexport.Input
	found bool
	err   error
}

func (f *fakeExportStore) ExportSession(_ context.Context, q storage.SessionQuery) (*sessionexport.Input, bool, error) {
	f.q = q
	return f.in, f.found, f.err
}

func TestSessionExportHandler(t *testing.T) {
	first := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	gt, frame := 12.5, int64(700)
	store := &fakeExportStore{found: true, in: &sessionexport.Input{
		Session: sessionexport.Session{SessionID: "1729_abcd", FirstSeen: first, LastSeen: first.Add(time.Minute), Meta: map[string]string{}},
		Rows: []sessionexport.Row{{Source: sessionexport.SourceCustomEvent, Time: first, GameT: &gt, Frame: &frame, RunID: "r-1",
			Name: "playermovement.room_enter", Count: 1, Props: map[string]string{"roomId": "r1"}}},
		Truncated: []string{"perf_sample"},
	}}
	h := &SessionExportHandler{repo: store, logger: zap.NewNop(), now: func() time.Time { return first.Add(time.Hour) }}

	w := serve(h.Export, "/api/sessions/export?session_id=1729_abcd&at=2026-10-18T10:00:05Z")
	if w.Code != 200 || store.q.SessionID != "1729_abcd" || store.q.At != nil || store.q.End.Sub(store.q.Start) != 48*time.Hour {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store.q)
	}
	if w.Header().Get("Content-Type") != "application/zip" ||
		w.Header().Get("Content-Disposition") != `attachment; filename="2026-10-18_100000_1729_abcd.zip"` ||
		w.Header().Get("X-Apm-Export-Truncated") != "perf_sample" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", w.Header())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, f := range z.File {
		entries = append(entries, f.Name)
		if f.Name == "2026-10-18_100000_1729_abcd/playermovement.jsonl" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if string(b) != `{"t":12.5000,"frame":700,"run":1,"ev":"room_enter","roomId":"r1"}`+"\n" {
				t.Errorf("line %s", b)
			}
		}
	}
	if strings.Join(entries, ",") != "2026-10-18_100000_1729_abcd/session.json,2026-10-18_100000_1729_abcd/playermovement.jsonl" {
		t.Errorf("entries %v", entries)
	}

	// Without at: the last 7 days.
	serve(h.Export, "/api/sessions/export?session_id=s")
	if d := store.q.End.Sub(store.q.Start); d != 7*24*time.Hour {
		t.Errorf("default range %v", d)
	}

	for _, target := range []string{
		"/api/sessions/export",
		"/api/sessions/export?session_id=" + strings.Repeat("x", 129),
		"/api/sessions/export?session_id=a%00b",
		"/api/sessions/export?session_id=s&at=yesterday",
		"/api/sessions/export?session_id=s&start_time=2026-08-01T00:00:00Z&end_time=2026-10-18T09:00:00Z",
	} {
		if w := serve(h.Export, target); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", target, w.Code)
		}
	}

	store.found = false
	if w := serve(h.Export, "/api/sessions/export?session_id=s"); w.Code != http.StatusNotFound {
		t.Errorf("not found: %d", w.Code)
	}
	store.err = errors.New("boom")
	if w := serve(h.Export, "/api/sessions/export?session_id=s"); w.Code != 500 || strings.Contains(w.Body.String(), "boom") {
		t.Errorf("error: %d %s", w.Code, w.Body)
	}
	if w := serve(NewSessionExportHandler(nil, zap.NewNop()).Export, "/api/sessions/export?session_id=s"); w.Code != 500 {
		t.Errorf("nil repo: %d", w.Code)
	}
}

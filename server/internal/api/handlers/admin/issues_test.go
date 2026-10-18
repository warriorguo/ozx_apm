package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type fakeIssueStore struct {
	states   map[string]models.IssueState
	inserted []models.IssueState
	versions map[string][]string // fingerprint -> versions; absent: no rows
	first    map[string]time.Time
	err      error
}

func (f *fakeIssueStore) IssueStates(_ context.Context, kind string, fps []string) (map[string]models.IssueState, error) {
	out := map[string]models.IssueState{}
	for _, fp := range fps {
		if s, ok := f.states[kind+"/"+fp]; ok {
			out[fp] = s
		}
	}
	return out, f.err
}

func (f *fakeIssueStore) InsertIssueState(_ context.Context, s models.IssueState) error {
	f.inserted = append(f.inserted, s)
	if f.states == nil {
		f.states = map[string]models.IssueState{}
	}
	f.states[s.Kind+"/"+s.Fingerprint] = s
	return f.err
}

func (f *fakeIssueStore) IssueVersions(_ context.Context, kind, fp string, includeDev bool) ([]string, bool, error) {
	v, ok := f.versions[fp]
	return v, ok, f.err
}

func (f *fakeIssueStore) VersionFirstSeen(context.Context, bool) (map[string]time.Time, error) {
	return f.first, f.err
}

var issueNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func newIssueHandler(store IssueStore) *IssueHandler {
	return &IssueHandler{store: store, logger: zap.NewNop(), now: func() time.Time { return issueNow }}
}

func postState(h *IssueHandler, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/issues/state", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(AdminActionHeader, "1")
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.Set(w, r)
	return w
}

func TestIssueHandler_Resolve(t *testing.T) {
	store := &fakeIssueStore{versions: map[string][]string{"fp1": {"1.4.2", "1.10.0", "1.9.0"}}}
	h := newIssueHandler(store)
	w := postState(h, `{"kind":"exception","fingerprint":"fp1","status":"resolved","resolved_in_version":" 1.11.0 ","comment":"fixed in RoomBuilder"}`, nil)
	if w.Code != 200 || len(store.inserted) != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	st := store.inserted[0]
	if st.Status != "resolved" || st.ResolvedInVersion != "1.11.0" || st.LastSeenVersion != "1.10.0" ||
		st.ResolvedAt == nil || !st.ResolvedAt.Equal(issueNow) || st.UpdatedBy != "admin" || st.Version != uint64(issueNow.UnixMilli()) {
		t.Fatalf("state %+v", st)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "resolved" || resp["last_seen_version"] != "1.10.0" {
		t.Errorf("response %v", resp)
	}

	// Reopen by hand: the resolution stays on record; the version increases
	// even within the same millisecond.
	w = postState(h, `{"kind":"exception","fingerprint":"fp1","status":"unresolved"}`, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	st2 := store.inserted[1]
	if st2.Status != "unresolved" || st2.ResolvedInVersion != "1.11.0" || st2.ResolvedAt == nil || st2.Version <= st.Version {
		t.Errorf("reopened %+v", st2)
	}

	// GET returns it; an unknown issue is unresolved.
	g := httptest.NewRecorder()
	h.Get(g, httptest.NewRequest(http.MethodGet, "/api/issues/state?kind=exception&fingerprint=fp1", nil))
	if g.Code != 200 || !strings.Contains(g.Body.String(), `"status":"unresolved"`) || !strings.Contains(g.Body.String(), `"resolved_in_version":"1.11.0"`) {
		t.Errorf("get %d %s", g.Code, g.Body)
	}
	g = httptest.NewRecorder()
	h.Get(g, httptest.NewRequest(http.MethodGet, "/api/issues/state?kind=crash&fingerprint=other", nil))
	if g.Code != 200 || !strings.Contains(g.Body.String(), `"status":"unresolved"`) {
		t.Errorf("get unknown %d %s", g.Code, g.Body)
	}
}

func TestIssueHandler_ResolveWithIncomparableVersions(t *testing.T) {
	store := &fakeIssueStore{
		versions: map[string][]string{"c": {"apmtest-2.0.0", "1.9.0"}},
		first:    map[string]time.Time{"apmtest-2.0.0": issueNow.Add(-time.Hour), "1.9.0": issueNow.Add(-48 * time.Hour)},
	}
	h := newIssueHandler(store)
	if w := postState(h, `{"kind":"crash","fingerprint":"c","status":"resolved"}`, nil); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := store.inserted[0].LastSeenVersion; got != "apmtest-2.0.0" {
		t.Errorf("last seen %q (first seen later must win)", got)
	}
}

func TestIssueHandler_Rejects(t *testing.T) {
	store := &fakeIssueStore{versions: map[string][]string{"fp": {"1.0"}}}
	h := newIssueHandler(store)
	cases := []struct {
		body    string
		headers map[string]string
		status  int
	}{
		{`{"kind":"exception","fingerprint":"fp","status":"resolved"}`, map[string]string{AdminActionHeader: ""}, 400},
		{`{"kind":"exception","fingerprint":"fp","status":"resolved"}`, map[string]string{"Content-Type": "text/plain"}, 415},
		{`not json`, nil, 400},
		{`{"kind":"anr","fingerprint":"fp","status":"resolved"}`, nil, 400},
		{`{"kind":"exception","fingerprint":"","status":"resolved"}`, nil, 400},
		{`{"kind":"exception","fingerprint":"` + strings.Repeat("x", 300) + `","status":"resolved"}`, nil, 400},
		{`{"kind":"exception","fingerprint":"fp","status":"ignored"}`, nil, 400},
		{`{"kind":"exception","fingerprint":"fp","status":"unresolved","resolved_in_version":"2.0"}`, nil, 400},
		{`{"kind":"exception","fingerprint":"fp","status":"resolved","resolved_in_version":"` + strings.Repeat("9", 200) + `"}`, nil, 400},
		{`{"kind":"exception","fingerprint":"fp","status":"resolved","comment":"` + strings.Repeat("c", 501) + `"}`, nil, 400},
		{`{"kind":"exception","fingerprint":"nope","status":"resolved"}`, nil, 404},
	}
	for i, c := range cases {
		if w := postState(h, c.body, c.headers); w.Code != c.status {
			t.Errorf("case %d: %d %s, want %d", i, w.Code, w.Body, c.status)
		}
	}
	if len(store.inserted) != 0 {
		t.Errorf("a rejected request wrote a state")
	}
	store.err = errors.New("ch down")
	if w := postState(h, `{"kind":"exception","fingerprint":"fp","status":"resolved"}`, nil); w.Code != 500 || strings.Contains(w.Body.String(), "ch down") {
		t.Errorf("store error: %d %s", w.Code, w.Body)
	}
	for _, target := range []string{"/api/issues/state?kind=x&fingerprint=a", "/api/issues/state?kind=crash"} {
		g := httptest.NewRecorder()
		h.Get(g, httptest.NewRequest(http.MethodGet, target, nil))
		if g.Code != 400 {
			t.Errorf("%s: %d", target, g.Code)
		}
	}
}

// Exception groups carry their state; the list's status filter reaches the
// store; a failing state read still serves the list.
type groupsStore struct {
	fakeExceptionStore
	groups []models.ExceptionGroup
}

func (g *groupsStore) GetExceptionGroups(ctx context.Context, f models.AdminFilter, sort string, page, size int) ([]models.ExceptionGroup, int64, error) {
	g.filter = f
	return append([]models.ExceptionGroup{}, g.groups...), int64(len(g.groups)), nil
}

func TestExceptionList_IssueStatus(t *testing.T) {
	at := issueNow
	states := &fakeIssueStore{states: map[string]models.IssueState{
		"exception/a": {Kind: "exception", Fingerprint: "a", Status: "resolved", ResolvedInVersion: "2.0", ResolvedAt: &at},
	}}
	store := &groupsStore{groups: []models.ExceptionGroup{{Fingerprint: "a"}, {Fingerprint: "b"}}}
	h := &ExceptionHandler{repo: store, states: states, logger: zap.NewNop()}
	w := serve(h.ListExceptions, "/api/exceptions?status=unresolved")
	if w.Code != 200 || store.filter.IssueStatus != "unresolved" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store.filter)
	}
	var resp models.ExceptionListResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Exceptions[0].Status != "resolved" || resp.Exceptions[0].ResolvedInVersion != "2.0" || resp.Exceptions[1].Status != "unresolved" {
		t.Errorf("states %+v", resp.Exceptions)
	}
	if w := serve(h.ListExceptions, "/api/exceptions?status=all"); w.Code != 200 || store.filter.IssueStatus != "" {
		t.Errorf("all: %d %+v", w.Code, store.filter)
	}
	if w := serve(h.ListExceptions, "/api/exceptions?status=ignored"); w.Code != 400 {
		t.Errorf("bad status: %d", w.Code)
	}
	states.err = errors.New("no table")
	w = serve(h.ListExceptions, "/api/exceptions")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"status"`) {
		t.Errorf("without states: %d %s", w.Code, w.Body)
	}
}

type fakeAlertStore struct {
	rows     []storage.AlertSummaryRow
	inserted []models.AlertRecord
	err      error
}

func (f *fakeAlertStore) ListAlerts(context.Context, int) ([]storage.AlertSummaryRow, error) {
	return f.rows, f.err
}

func (f *fakeAlertStore) InsertAlertRecords(_ context.Context, recs []models.AlertRecord) error {
	f.inserted = append(f.inserted, recs...)
	return f.err
}

func TestAlertHandler_TestEndpoint(t *testing.T) {
	store := &fakeAlertStore{}
	now := issueNow
	post := func(h *AlertHandler, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/alerts/test", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.Test(w, r)
		return w
	}
	disabled := &AlertHandler{store: store, logger: zap.NewNop(), TestInterval: 10 * time.Second, now: func() time.Time { return now }}
	if w := post(disabled, "Bearer x"); w.Code != 403 {
		t.Errorf("without a token configured: %d", w.Code)
	}
	h := &AlertHandler{store: store, logger: zap.NewNop(), token: []byte("tok-123"), TestInterval: 10 * time.Second,
		now: func() time.Time { return now }}
	for _, auth := range []string{"", "tok-123", "Bearer wrong", "bearer tok-123"} {
		if w := post(h, auth); w.Code != 401 || strings.Contains(w.Body.String(), "tok-123") {
			t.Errorf("auth %q: %d %s", auth, w.Code, w.Body)
		}
	}
	w := post(h, "Bearer tok-123")
	if w.Code != 202 || len(store.inserted) != 1 || store.inserted[0].Status != models.AlertPending || store.inserted[0].Type != "test" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store.inserted)
	}
	if !strings.Contains(w.Body.String(), store.inserted[0].AlertKey) {
		t.Errorf("key not returned: %s", w.Body)
	}
	if w := post(h, "Bearer tok-123"); w.Code != 429 {
		t.Errorf("second within the interval: %d", w.Code)
	}
	now = now.Add(11 * time.Second)
	if w := post(h, "Bearer tok-123"); w.Code != 202 || len(store.inserted) != 2 {
		t.Errorf("after the interval: %d", w.Code)
	}
}

func TestAlertHandler_List(t *testing.T) {
	store := &fakeAlertStore{rows: []storage.AlertSummaryRow{{
		AlertSummary: models.AlertSummary{AlertKey: "k", Type: "new_issue", Status: "sent"},
		Payload:      `{"title":"New exception: E"}`,
	}, {AlertSummary: models.AlertSummary{AlertKey: "j"}, Payload: "garbage"}}}
	h := &AlertHandler{store: store, logger: zap.NewNop()}
	w := serve(h.List, "/api/alerts?limit=10")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"title":"New exception: E"`) || strings.Contains(w.Body.String(), "payload") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"0", "201", "x"} {
		if w := serve(h.List, "/api/alerts?limit="+bad); w.Code != 400 {
			t.Errorf("limit %s: %d", bad, w.Code)
		}
	}
}

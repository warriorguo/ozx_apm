package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// fakePrivacyStore keeps rows per table and id in memory.
type fakePrivacyStore struct {
	mu        sync.Mutex
	rows      map[string]map[string]uint64 // table -> id -> rows
	audit     []models.PrivacyAuditRecord
	deleteErr error
	auditErr  error
	deletes   int
	block     chan struct{} // DeletePrivacyRows waits on it when set
	resurrect uint64        // rows that reappear after a delete (concurrent insert)
}

func newFakeStore() *fakePrivacyStore {
	return &fakePrivacyStore{rows: map[string]map[string]uint64{
		"apm_crashes":      {"dev-a": 2, "dev-b": 1},
		"apm_perf_samples": {"dev-a": 5, "dev-b": 3},
		"apm_janks":        {"dev-b": 1},
	}}
}

func (f *fakePrivacyStore) PrivacyTables(ctx context.Context, column string) ([]string, error) {
	switch column {
	case "device_id":
		return []string{"apm_crashes", "apm_janks", "apm_perf_samples"}, nil
	case "player_id": // APM-12
		return []string{"ops_events", "ops_player_days", "ops_players"}, nil
	}
	return nil, fmt.Errorf("column %s", column)
}

// APM-12: id_type player_id deletes from the tables with a player_id column.
func TestPrivacyDelete_PlayerID(t *testing.T) {
	store := newFakeStore()
	store.rows["ops_events"] = map[string]uint64{"p-1": 7, "p-2": 1}
	store.rows["ops_player_days"] = map[string]uint64{"p-1": 2}
	store.rows["ops_players"] = map[string]uint64{"p-1": 1}
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	w := postPrivacy(h.Delete, `{"id_type":"player_id","id":"p-1"}`, "p-1")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	d := decodeDeletion(t, w)
	if d.Status != "done" || d.IDType != "player_id" || d.RowsDeleted != 10 || len(d.Tables) != 3 || d.Tables[0].Table != "ops_events" {
		t.Errorf("%+v", d)
	}
	if store.rows["ops_events"]["p-2"] != 1 || store.rows["apm_crashes"]["dev-a"] != 2 {
		t.Error("other players and the technical tables must be untouched")
	}
	if strings.Contains(w.Body.String(), "p-1") || store.audit[0].IDType != "player_id" {
		t.Errorf("the id must not be echoed; audit %+v", store.audit)
	}
}

func (f *fakePrivacyStore) CountPrivacyRows(ctx context.Context, table, column, id string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[table][id], nil
}

func (f *fakePrivacyStore) DeletePrivacyRows(ctx context.Context, table, column, id string) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.rows[table][id] = f.resurrect
	return nil
}

func (f *fakePrivacyStore) InsertPrivacyAudit(ctx context.Context, rec models.PrivacyAuditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audit = append(f.audit, rec)
	return nil
}

func (f *fakePrivacyStore) PrivacyAuditHistory(ctx context.Context, idType, idHash string, limit int) ([]models.PrivacyAuditRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []models.PrivacyAuditRecord{}
	for i := len(f.audit) - 1; i >= 0; i-- {
		if f.audit[i].IDHash == idHash && f.audit[i].IDType == idType {
			out = append(out, f.audit[i])
		}
	}
	return out, nil
}

const testToken = "test-token-0123456789"

func postPrivacy(h http.HandlerFunc, body, confirm string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/privacy/delete", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	if confirm != "" {
		req.Header.Set(ConfirmDeleteHeader, confirm)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func decodeDeletion(t *testing.T, w *httptest.ResponseRecorder) models.PrivacyDeletion {
	t.Helper()
	var d models.PrivacyDeletion
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return d
}

func TestPrivacyDelete_DeletesOnlyTheDevice_AndAudits(t *testing.T) {
	store := newFakeStore()
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)

	w := postPrivacy(h.Delete, `{"id_type":"device_id","id":"dev-a"}`, "dev-a")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	d := decodeDeletion(t, w)
	if d.Status != models.PrivacyStatusDone || d.RowsDeleted != 7 || d.RowsRemaining != 0 || len(d.Tables) != 3 {
		t.Fatalf("%+v", d)
	}
	if d.IDHash != models.PrivacyIDHash("dev-a") || strings.Contains(w.Body.String(), `"dev-a"`) {
		t.Errorf("the response must carry the hash, not the id: %s", w.Body.String())
	}
	if store.rows["apm_perf_samples"]["dev-b"] != 3 || store.rows["apm_crashes"]["dev-b"] != 1 {
		t.Errorf("other device touched: %v", store.rows)
	}
	if store.deletes != 2 {
		t.Errorf("%d deletes; a table without rows of the device needs none", store.deletes)
	}
	if len(store.audit) != 2 || store.audit[0].Status != "running" || store.audit[1].Status != "done" ||
		store.audit[1].RowsBefore != 7 || store.audit[1].RowsAfter != 0 || len(store.audit[1].Tables) != 3 {
		t.Errorf("audit %+v", store.audit)
	}

	// Idempotent: again, nothing to delete, still done, no mutation.
	w = postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	if d := decodeDeletion(t, w); w.Code != http.StatusOK || d.Status != "done" || d.RowsDeleted != 0 || d.IDType != "device_id" {
		t.Fatalf("second run %d %+v", w.Code, d)
	}
	if store.deletes != 2 {
		t.Errorf("second run issued deletes")
	}
}

func TestPrivacyDelete_RequiresConfirmation(t *testing.T) {
	store := newFakeStore()
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	for _, confirm := range []string{"", "dev-b", "yes"} {
		w := postPrivacy(h.Delete, `{"id":"dev-a"}`, confirm)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), ConfirmDeleteHeader) {
			t.Errorf("confirm %q: %d %s", confirm, w.Code, w.Body.String())
		}
	}
	if store.deletes != 0 || len(store.audit) != 0 {
		t.Errorf("deleted without confirmation")
	}
}

func TestPrivacyDelete_ValidatesTheRequest(t *testing.T) {
	h := NewPrivacyHandler(newFakeStore(), zap.NewNop(), testToken)
	cases := map[string]string{
		`not json`:                                  "JSON",
		`{"id":""}`:                                 "id is required",
		`{"id":"  "}`:                               "id is required",
		`{"id_type":"player_id","id":""}`:           "id is required",
		`{"id_type":"email","id":"x"}`:              "unknown id_type",
		`{"id":"a\u0000b"}`:                         "control",
		`{"id":"` + strings.Repeat("x", 300) + `"}`: "longer",
	}
	for body, want := range cases {
		w := postPrivacy(h.Delete, body, "x")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s (want %q)", body, w.Code, w.Body.String(), want)
		}
	}
}

func TestPrivacyDelete_WithoutPrivilege_Is503WithTheGrant(t *testing.T) {
	store := newFakeStore()
	store.deleteErr = fmt.Errorf("%w (code: 497, Not enough privileges)", storage.ErrPrivacyDeleteNotPermitted)
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)

	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	d := decodeDeletion(t, w)
	if d.Status != "failed" || !strings.Contains(d.Error, storage.PrivacyDeleteGrant) || d.RowsDeleted != 0 {
		t.Errorf("%+v", d)
	}
	if store.deletes != 1 {
		t.Errorf("%d delete attempts; it must stop at the first refusal", store.deletes)
	}
	if len(store.audit) != 2 || store.audit[1].Status != "failed" || !strings.Contains(store.audit[1].Error, "ALTER DELETE") {
		t.Errorf("audit %+v", store.audit)
	}
	if store.rows["apm_crashes"]["dev-a"] != 2 {
		t.Errorf("rows changed")
	}
}

func TestPrivacyDelete_OtherErrorIs500(t *testing.T) {
	store := newFakeStore()
	store.deleteErr = errors.New("code: 159, timeout")
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	if d := decodeDeletion(t, w); w.Code != http.StatusInternalServerError || d.Status != "failed" || len(d.Tables) != 3 {
		t.Fatalf("%d %+v", w.Code, d)
	}
}

func TestPrivacyDelete_NoAuditNoDeletion(t *testing.T) {
	store := newFakeStore()
	store.auditErr = errors.New("table apm_privacy_deletions does not exist")
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	if d := decodeDeletion(t, w); w.Code != http.StatusInternalServerError || !strings.Contains(d.Error, "nothing was deleted") {
		t.Fatalf("%d %+v", w.Code, d)
	}
	if store.deletes != 0 {
		t.Errorf("deleted without an audit record")
	}
}

func TestPrivacyDelete_RowsLeft_IsIncomplete202(t *testing.T) {
	store := newFakeStore()
	store.resurrect = 1 // a row of the device arrived during the mutation
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	d := decodeDeletion(t, w)
	if w.Code != http.StatusAccepted || d.Status != "incomplete" || d.RowsRemaining != 2 {
		t.Fatalf("%d %+v", w.Code, d)
	}
}

func TestPrivacyDelete_SlowDeletionAnswers202_ThenFinishes(t *testing.T) {
	store := newFakeStore()
	store.block = make(chan struct{})
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	h.SyncWait = 20 * time.Millisecond

	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	if d := decodeDeletion(t, w); w.Code != http.StatusAccepted || d.Status != "running" || d.RequestID == "" {
		t.Fatalf("%d %+v", w.Code, d)
	}
	close(store.block)

	verify := func() models.PrivacyVerification {
		req := httptest.NewRequest(http.MethodPost, "/api/privacy/verify", strings.NewReader(`{"id":"dev-a"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.Verify(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("verify %d %s", rec.Code, rec.Body.String())
		}
		var v models.PrivacyVerification
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		v := verify()
		if v.RowsRemaining == 0 && len(v.Deletions) == 2 && v.Deletions[0].Status == "done" {
			if len(v.Tables) != 3 || v.Note == "" {
				t.Errorf("%+v", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not finished: %+v", v)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPrivacyDelete_OneAtATime(t *testing.T) {
	store := newFakeStore()
	store.block = make(chan struct{})
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	h.SyncWait = 20 * time.Millisecond

	postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	postPrivacy(h.Delete, `{"id":"dev-b"}`, "dev-b")
	time.Sleep(50 * time.Millisecond)
	store.mu.Lock()
	running := 0
	for _, a := range store.audit {
		if a.Status == "running" {
			running++
		}
	}
	store.mu.Unlock()
	if running != 1 {
		t.Errorf("%d deletions started at once", running)
	}
	close(store.block)
}

func TestPrivacy_NilStore(t *testing.T) {
	h := NewPrivacyHandler(nil, zap.NewNop(), testToken)
	if w := postPrivacy(h.Delete, `{"id":"a"}`, "a"); w.Code != http.StatusInternalServerError {
		t.Errorf("delete %d", w.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/privacy/verify", strings.NewReader(`{"id":"a"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.Verify(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("verify %d", w.Code)
	}
}

func privacyRequest(h *PrivacyHandler, path, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"id":"dev-a"}`))
	req.Header.Set(ConfirmDeleteHeader, "dev-a")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	if strings.HasSuffix(path, "/delete") {
		h.Delete(w, req)
	} else {
		h.Verify(w, req)
	}
	return w
}

func TestPrivacy_DisabledWithoutToken(t *testing.T) {
	store := newFakeStore()
	h := NewPrivacyHandler(store, zap.NewNop(), "")
	if h.Enabled() {
		t.Fatal("enabled without a token")
	}
	for _, path := range []string{"/api/privacy/delete", "/api/privacy/verify"} {
		for _, auth := range []string{"", "Bearer ", "Bearer anything"} {
			w := privacyRequest(h, path, auth)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "privacy API disabled") {
				t.Errorf("%s auth %q: %d %s", path, auth, w.Code, w.Body.String())
			}
		}
	}
	if store.deletes != 0 || len(store.audit) != 0 {
		t.Errorf("disabled API touched the store")
	}
}

func TestPrivacy_WrongOrMissingToken_Is401_AndTheTokenIsNeverLogged(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	store := newFakeStore()
	h := NewPrivacyHandler(store, zap.New(core), testToken)
	cases := []string{"", testToken, "Basic " + testToken, "Bearer", "Bearer ", "Bearer wrong", "Bearer " + testToken + "x",
		"bearer " + testToken, "Bearer " + testToken[:len(testToken)-1]}
	for _, path := range []string{"/api/privacy/delete", "/api/privacy/verify"} {
		for _, auth := range cases {
			w := privacyRequest(h, path, auth)
			if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("%s auth %q: %d %s", path, auth, w.Code, w.Body.String())
			}
		}
	}
	if store.deletes != 0 || len(store.audit) != 0 {
		t.Errorf("unauthorized requests touched the store")
	}
	if n := logs.FilterMessage("privacy API: unauthorized request").Len(); n != 2*len(cases) {
		t.Errorf("%d unauthorized requests logged, want %d", n, 2*len(cases))
	}
	for _, e := range logs.All() {
		text := e.Message
		for _, f := range e.Context {
			text += " " + f.String
		}
		if strings.Contains(text, testToken[:8]) {
			t.Errorf("log contains the token: %s", text)
		}
	}
}

func TestPrivacy_RightToken_Is200(t *testing.T) {
	store := newFakeStore()
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	if w := privacyRequest(h, "/api/privacy/verify", "Bearer "+testToken); w.Code != http.StatusOK {
		t.Fatalf("verify %d %s", w.Code, w.Body.String())
	}
	if w := privacyRequest(h, "/api/privacy/delete", "Bearer "+testToken); w.Code != http.StatusOK {
		t.Fatalf("delete %d %s", w.Code, w.Body.String())
	}
	if store.rows["apm_crashes"]["dev-a"] != 0 {
		t.Errorf("not deleted")
	}
	// The token does not replace the confirmation header.
	req := httptest.NewRequest(http.MethodPost, "/api/privacy/delete", strings.NewReader(`{"id":"dev-b"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.Delete(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("without confirmation: %d", w.Code)
	}
}

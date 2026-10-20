package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// ------------------------------------------------------------------
// Session files API (APM-13)

type fakeSessionFiles struct {
	files   map[string]models.SessionFile
	content []byte
	openErr error
}

func (f *fakeSessionFiles) List(ctx context.Context, flt models.SessionFileFilter) ([]models.SessionFile, error) {
	out := []models.SessionFile{}
	for _, x := range f.files {
		out = append(out, x)
	}
	return out, nil
}

func (f *fakeSessionFiles) Availability(ctx context.Context, ids []string) ([]models.SessionFileAvailability, error) {
	return []models.SessionFileAvailability{}, nil
}

func (f *fakeSessionFiles) Get(ctx context.Context, id string) (*models.SessionFile, error) {
	x, ok := f.files[id]
	if !ok {
		return nil, nil
	}
	return &x, nil
}

func (f *fakeSessionFiles) Open(ctx context.Context, x models.SessionFile) (io.ReadCloser, int64, error) {
	if f.openErr != nil {
		return nil, 0, f.openErr
	}
	return io.NopCloser(bytes.NewReader(f.content)), int64(len(f.content)), nil
}

const (
	sfToken  = "sf-token-0123456789"
	sfUpload = "00112233445566778899aabbccddeeff"
)

func newSFHandler(now time.Time) (*SessionFilesHandler, *fakeSessionFiles) {
	svc := &fakeSessionFiles{files: map[string]models.SessionFile{
		sfUpload: {UploadID: sfUpload, Status: models.SessionFileComplete, RecordingName: "2026-10-20_153042",
			ExpiresAt: now.Add(24 * time.Hour), SHA256: strings.Repeat("a", 64), SessionJSON: `{"scene":"x"}`},
		"ffeeddccbbaa99887766554433221100": {UploadID: "ffeeddccbbaa99887766554433221100", Status: models.SessionFileUploading,
			ExpiresAt: now.Add(24 * time.Hour)},
		"0000000000000000000000000000000f": {UploadID: "0000000000000000000000000000000f", Status: models.SessionFileComplete,
			ExpiresAt: now.Add(-time.Minute), RecordingName: "old"},
	}, content: []byte("PK zip bytes")}
	h := NewSessionFilesHandler(svc, zap.NewNop(), sfToken)
	h.now = func() time.Time { return now }
	return h, svc
}

func sfRequest(h http.HandlerFunc, method, target, body string, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestSessionFilesAPI_Token(t *testing.T) {
	now := time.Now()
	h, _ := newSFHandler(now)
	for _, c := range []struct {
		token string
		want  int
	}{{"", 401}, {"wrong", 401}, {sfToken, 200}} {
		if w := sfRequest(h.List, "GET", "/api/session-files", "", c.token); w.Code != c.want {
			t.Errorf("token %q: %d", c.token, w.Code)
		}
	}
	disabled := NewSessionFilesHandler(&fakeSessionFiles{}, zap.NewNop(), "")
	for _, fn := range []http.HandlerFunc{disabled.List, disabled.Detail, disabled.Link, disabled.Download} {
		if w := sfRequest(fn, "GET", "/api/session-files/download?upload_id="+sfUpload+"&expires=1&sig=x", "", "anything"); w.Code != 403 {
			t.Errorf("disabled: %d", w.Code)
		}
	}
	// The list leaves session.json to the detail.
	w := sfRequest(h.List, "GET", "/api/session-files", "", sfToken)
	if strings.Contains(w.Body.String(), "session_json") {
		t.Errorf("list carries session.json: %s", w.Body)
	}
	w = sfRequest(h.Detail, "GET", "/api/session-files/detail?upload_id="+sfUpload, "", sfToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "session_json") {
		t.Errorf("detail: %d %s", w.Code, w.Body)
	}
	if w := sfRequest(h.List, "GET", "/api/session-files?status=bogus", "", sfToken); w.Code != 400 {
		t.Errorf("bad status: %d", w.Code)
	}
	if w := sfRequest(h.List, "GET", "/api/session-files?git_commit=ab", "", sfToken); w.Code != 400 {
		t.Errorf("short commit: %d", w.Code)
	}
}

func TestSessionFilesAPI_LinksExpire(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	h, svc := newSFHandler(now)
	w := sfRequest(h.Link, "POST", "/api/session-files/link", `{"upload_id":"`+sfUpload+`"}`, sfToken)
	var link struct {
		URL string `json:"url"`
	}
	json.Unmarshal(w.Body.Bytes(), &link)
	if w.Code != 200 || link.URL == "" {
		t.Fatalf("link %d %s", w.Code, w.Body)
	}
	if w := sfRequest(h.Download, "GET", link.URL, "", ""); w.Code != 200 || w.Body.String() != string(svc.content) {
		t.Fatalf("download %d", w.Code)
	}
	// Five minutes later the link is dead.
	h.now = func() time.Time { return now.Add(6 * time.Minute) }
	if w := sfRequest(h.Download, "GET", link.URL, "", ""); w.Code != 403 || !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("expired link: %d %s", w.Code, w.Body)
	}
	// A link made with another token's key, or valid for too long, is refused.
	h.now = func() time.Time { return now }
	other := NewSessionFilesHandler(svc, zap.NewNop(), "another-token")
	other.now = h.now
	w = sfRequest(other.Link, "POST", "/api/session-files/link", `{"upload_id":"`+sfUpload+`"}`, "another-token")
	json.Unmarshal(w.Body.Bytes(), &link)
	if w := sfRequest(h.Download, "GET", link.URL, "", ""); w.Code != 403 {
		t.Fatalf("foreign link: %d", w.Code)
	}
	far := now.Add(24 * time.Hour).Unix()
	q := url.Values{"upload_id": {sfUpload}, "expires": {jsonInt(far)}, "sig": {h.sign(sfUpload, far)}}
	if w := sfRequest(h.Download, "GET", "/api/session-files/download?"+q.Encode(), "", ""); w.Code != 403 {
		t.Fatalf("long link: %d", w.Code)
	}
	// Not complete / expired / unknown / store error.
	cases := map[string]int{"ffeeddccbbaa99887766554433221100": 409, "0000000000000000000000000000000f": 410,
		"99999999999999999999999999999999": 404, "nope": 400}
	for id, want := range cases {
		if w := sfRequest(h.Download, "GET", "/api/session-files/download?upload_id="+id, "", sfToken); w.Code != want {
			t.Errorf("%s: %d, want %d", id, w.Code, want)
		}
		if w := sfRequest(h.Link, "POST", "/api/session-files/link", `{"upload_id":"`+id+`"}`, sfToken); w.Code != want {
			t.Errorf("link %s: %d, want %d", id, w.Code, want)
		}
	}
	svc.openErr = errors.New("s3 down")
	if w := sfRequest(h.Download, "GET", "/api/session-files/download?upload_id="+sfUpload, "", sfToken); w.Code != 503 {
		t.Errorf("store error: %d", w.Code)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ------------------------------------------------------------------
// Privacy deletion and the recordings (APM-13 + APM-14)

// sfPrivacyStore adds apm_session_files to the device tables.
type sfPrivacyStore struct {
	*fakePrivacyStore
	order *[]string
}

func (s sfPrivacyStore) PrivacyTables(ctx context.Context, column string) ([]string, error) {
	tables, err := s.fakePrivacyStore.PrivacyTables(ctx, column)
	if column == "device_id" {
		tables = append(tables, storage.SessionFilesTable)
	}
	return tables, err
}

func (s sfPrivacyStore) DeletePrivacyRows(ctx context.Context, table, column, id string) error {
	*s.order = append(*s.order, "rows:"+table)
	return s.fakePrivacyStore.DeletePrivacyRows(ctx, table, column, id)
}

type fakePurger struct {
	mu     sync.Mutex
	result models.PrivacySessionFiles
	count  int
	order  *[]string
}

func (p *fakePurger) Purge(ctx context.Context, idType, id string) models.PrivacySessionFiles {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.order = append(*p.order, "purge:"+idType)
	return p.result
}

func (p *fakePurger) Count(ctx context.Context, idType, id string) (int, error) { return p.count, nil }

func sfPrivacyFixture() (sfPrivacyStore, *[]string) {
	order := &[]string{}
	base := newFakeStore()
	base.rows[storage.SessionFilesTable] = map[string]uint64{"dev-a": 2}
	return sfPrivacyStore{fakePrivacyStore: base, order: order}, order
}

func TestPrivacyDelete_SessionFilesFirst(t *testing.T) {
	store, order := sfPrivacyFixture()
	purger := &fakePurger{result: models.PrivacySessionFiles{UploadsBefore: 2}, order: order}
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	h.SetSessionFiles(purger)
	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	d := decodeDeletion(t, w)
	if w.Code != 200 || d.Status != "done" || d.SessionFiles == nil || d.SessionFiles.UploadsBefore != 2 {
		t.Fatalf("%d %+v", w.Code, d)
	}
	if len(*order) == 0 || (*order)[0] != "purge:device_id" {
		t.Fatalf("the objects must go before the rows: %v", *order)
	}
	if store.rows[storage.SessionFilesTable]["dev-a"] != 0 {
		t.Fatal("index rows kept")
	}
}

func TestPrivacyDelete_SessionFileFailureKeepsTheirRows(t *testing.T) {
	store, order := sfPrivacyFixture()
	purger := &fakePurger{result: models.PrivacySessionFiles{UploadsBefore: 2, UploadsAfter: 2, Error: "s3 down"}, order: order}
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	h.SetSessionFiles(purger)
	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	d := decodeDeletion(t, w)
	if w.Code != 500 || d.Status != "failed" || !strings.Contains(d.Error, "s3 down") {
		t.Fatalf("%d %+v", w.Code, d)
	}
	if store.rows[storage.SessionFilesTable]["dev-a"] != 2 || store.rows["apm_crashes"]["dev-a"] != 0 {
		t.Fatalf("rows: session files %d (want kept), crashes %d (want deleted)",
			store.rows[storage.SessionFilesTable]["dev-a"], store.rows["apm_crashes"]["dev-a"])
	}
}

func TestPrivacyDelete_NoObjectStoreKeepsRecordings(t *testing.T) {
	store, _ := sfPrivacyFixture()
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	w := postPrivacy(h.Delete, `{"id":"dev-a"}`, "dev-a")
	d := decodeDeletion(t, w)
	if d.Status != "failed" || !strings.Contains(d.Error, "object store") || d.SessionFiles != nil ||
		store.rows[storage.SessionFilesTable]["dev-a"] != 2 || !strings.Contains(d.Note, "OZX_S3") {
		t.Fatalf("%d %+v", w.Code, d)
	}
	// A device without recordings is deleted as before.
	w = postPrivacy(h.Delete, `{"id":"dev-b"}`, "dev-b")
	if d := decodeDeletion(t, w); w.Code != 200 || d.Status != "done" {
		t.Fatalf("%d %+v", w.Code, d)
	}
}

func TestPrivacy_PlayerRecordingsAndVerify(t *testing.T) {
	store, order := sfPrivacyFixture()
	store.rows["ops_events"] = map[string]uint64{"p-1": 1}
	purger := &fakePurger{result: models.PrivacySessionFiles{UploadsBefore: 1, UploadsAfter: 1}, count: 1, order: order}
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	h.SetSessionFiles(purger)
	w := postPrivacy(h.Delete, `{"id_type":"player_id","id":"p-1"}`, "p-1")
	if d := decodeDeletion(t, w); d.Status != "incomplete" || d.RowsRemaining != 1 {
		t.Fatalf("%d %+v", w.Code, d)
	}
	req := httptest.NewRequest("POST", "/api/privacy/verify", strings.NewReader(`{"id_type":"player_id","id":"p-1"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.Verify(rec, req)
	var v models.PrivacyVerification
	json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != 200 || v.SessionFiles == nil || v.SessionFiles.UploadsAfter != 1 || v.RowsRemaining != 1 {
		t.Fatalf("verify %d %s", rec.Code, rec.Body)
	}
}

// By player id the recordings are found through the player's ops events:
// when they cannot be deleted, nothing is (a retry must find them again).
func TestPrivacyDelete_PlayerRecordingsFailure_KeepsEverything(t *testing.T) {
	store, order := sfPrivacyFixture()
	store.rows["ops_events"] = map[string]uint64{"p-1": 3}
	purger := &fakePurger{result: models.PrivacySessionFiles{UploadsBefore: 1, UploadsAfter: 1, Error: "s3 down"}, order: order}
	h := NewPrivacyHandler(store, zap.NewNop(), testToken)
	h.SetSessionFiles(purger)
	w := postPrivacy(h.Delete, `{"id_type":"player_id","id":"p-1"}`, "p-1")
	d := decodeDeletion(t, w)
	if d.Status != "failed" || !strings.Contains(d.Error, "run it again") || store.rows["ops_events"]["p-1"] != 3 {
		t.Fatalf("%d %+v, ops rows %d", w.Code, d, store.rows["ops_events"]["p-1"])
	}
}

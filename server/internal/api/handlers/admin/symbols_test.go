package admin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

type fakeSymbolStore struct {
	funcs   map[string]map[uint64]symbols.Func // upload id -> addr
	lines   map[string]map[uint64]symbols.Line
	commits []symbols.Commit
	fail    error
}

func newFakeSymbolStore() *fakeSymbolStore {
	return &fakeSymbolStore{funcs: map[string]map[uint64]symbols.Func{}, lines: map[string]map[uint64]symbols.Line{}}
}

func (f *fakeSymbolStore) InsertSymbolRows(_ context.Context, buildID, uploadID string, fs []symbols.Func, ls []symbols.Line) error {
	if f.fail != nil {
		return f.fail
	}
	if f.funcs[uploadID] == nil {
		f.funcs[uploadID] = map[uint64]symbols.Func{}
		f.lines[uploadID] = map[uint64]symbols.Line{}
	}
	for _, x := range fs {
		f.funcs[uploadID][x.Addr] = x
	}
	for _, x := range ls {
		f.lines[uploadID][x.Addr] = x
	}
	return nil
}

func (f *fakeSymbolStore) CountSymbolRows(_ context.Context, buildID, uploadID string) (uint64, uint64, error) {
	return uint64(len(f.funcs[uploadID])), uint64(len(f.lines[uploadID])), nil
}

func (f *fakeSymbolStore) InsertSymbolUpload(_ context.Context, c symbols.Commit) error {
	f.commits = append(f.commits, c)
	return nil
}

func (f *fakeSymbolStore) ListSymbolUploads(_ context.Context, flt storage.SymbolUploadFilter) ([]symbols.UploadRef, error) {
	var out []symbols.UploadRef
	for _, c := range f.commits {
		if flt.BuildID != "" && c.BuildID != flt.BuildID {
			continue
		}
		out = append(out, symbols.UploadRef{BuildID: c.BuildID, UploadID: c.UploadID, Module: c.Module, Functions: c.Functions, UploadedAt: time.Now()})
	}
	return out, nil
}

const testUploadID = "0123456789abcdef0123456789abcdef"

func gz(t *testing.T, v interface{}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return buf.Bytes()
}

func postSymbols(h http.HandlerFunc, path string, body []byte, token string, gzipped bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if gzipped {
		req.Header.Set("Content-Encoding", "gzip")
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestSymbolUpload_PartsCommitAndList(t *testing.T) {
	store := newFakeSymbolStore()
	h := NewSymbolHandler(store, zap.NewNop(), "tok")
	part := func(n int, funcs []symbols.Func, lines []symbols.Line) *httptest.ResponseRecorder {
		p := symbols.Part{BuildID: "0D65A2D2978F75F0", UploadID: testUploadID, Part: n,
			Funcs: symbols.EncodeFuncs(funcs), Lines: symbols.EncodeLines(lines)}
		return postSymbols(h.Part, "/api/symbols/part", gz(t, p), "tok", true)
	}
	fs := []symbols.Func{{Addr: 0x1000, Size: 0x10, Name: "A.B()", RawName: "A_B_m12345678"}, {Addr: 0x2000, Size: 0x10, Name: "C.D()", RawName: "C_D_m12345678"}}
	ls := []symbols.Line{{Addr: 0x1000, File: "Assets/A.cs", Line: 3}}
	if w := part(0, fs[:1], nil); w.Code != http.StatusOK {
		t.Fatalf("part 0: %d %s", w.Code, w.Body)
	}
	commit := symbols.Commit{BuildID: "0d65a2d2978f75f0", UploadID: testUploadID, Module: "libil2cpp.so", Arch: "arm64", Functions: 2, Lines: 1}
	body, _ := json.Marshal(commit)
	// Not all parts are there yet: 409 with the counts.
	w := postSymbols(h.Commit, "/api/symbols/commit", body, "tok", false)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"functions_stored":1`) {
		t.Fatalf("early commit: %d %s", w.Code, w.Body)
	}
	if w := part(1, fs[1:], ls); w.Code != http.StatusOK {
		t.Fatalf("part 1: %d %s", w.Code, w.Body)
	}
	// A part sent again (timeout on the tool's side) does not count twice.
	if w := part(1, fs[1:], ls); w.Code != http.StatusOK {
		t.Fatalf("part 1 again: %d", w.Code)
	}
	w = postSymbols(h.Commit, "/api/symbols/commit", body, "tok", false)
	if w.Code != http.StatusOK || len(store.commits) != 1 {
		t.Fatalf("commit: %d %s", w.Code, w.Body)
	}

	lw := httptest.NewRecorder()
	h.List(lw, httptest.NewRequest(http.MethodGet, "/api/symbols?build_id=0D65A2D2978F75F0", nil))
	var list struct {
		Uploads []symbols.UploadRef `json:"uploads"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &list); err != nil || len(list.Uploads) != 1 || list.Uploads[0].Functions != 2 {
		t.Fatalf("list: %s", lw.Body)
	}
	lw = httptest.NewRecorder()
	h.List(lw, httptest.NewRequest(http.MethodGet, "/api/symbols?build_id=zz", nil))
	if lw.Code != http.StatusBadRequest {
		t.Fatalf("bad build id: %d", lw.Code)
	}
}

func TestSymbolUpload_Auth(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	store := newFakeSymbolStore()
	h := NewSymbolHandler(store, zap.New(core), "s3cret")
	body := gz(t, symbols.Part{BuildID: "0d65a2d2", UploadID: testUploadID})
	for _, tc := range []struct {
		header string
		code   int
	}{{"", 401}, {"Basic x", 401}, {"Bearer wrong", 401}, {"bearer s3cret", 401}, {"Bearer s3cret", 200}} {
		req := httptest.NewRequest(http.MethodPost, "/api/symbols/part", bytes.NewReader(body))
		req.Header.Set("Content-Encoding", "gzip")
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		w := httptest.NewRecorder()
		h.Part(w, req)
		if w.Code != tc.code {
			t.Errorf("%q: %d %s", tc.header, w.Code, w.Body)
		}
	}
	for _, e := range logs.All() {
		for _, f := range e.Context {
			if strings.Contains(f.String, "s3cret") {
				t.Fatal("token logged")
			}
		}
	}
	disabled := NewSymbolHandler(store, zap.NewNop(), "")
	if w := postSymbols(disabled.Part, "/api/symbols/part", body, "anything", true); w.Code != http.StatusForbidden {
		t.Fatalf("disabled: %d", w.Code)
	}
	if w := postSymbols(disabled.Commit, "/api/symbols/commit", []byte("{}"), "anything", false); w.Code != http.StatusForbidden {
		t.Fatalf("disabled commit: %d", w.Code)
	}
}

func TestSymbolUpload_BadBodies(t *testing.T) {
	store := newFakeSymbolStore()
	h := NewSymbolHandler(store, zap.NewNop(), "tok")
	// A small gzip body that expands beyond the decoded limit.
	var bomb bytes.Buffer
	zw := gzip.NewWriter(&bomb)
	zw.Write([]byte(`{"build_id":"0d65a2d2","upload_id":"` + testUploadID + `","funcs":[` + strings.Repeat(" ", symbols.MaxPartDecodedBytes+10) + `]}`))
	zw.Close()
	if w := postSymbols(h.Part, "/api/symbols/part", bomb.Bytes(), "tok", true); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("gzip bomb: %d", w.Code)
	}
	big := bytes.Repeat([]byte("x"), symbols.MaxPartBodyBytes+1)
	if w := postSymbols(h.Part, "/api/symbols/part", big, "tok", false); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body: %d", w.Code)
	}
	for _, b := range []string{"not json", `{"build_id":"../../etc","upload_id":"` + testUploadID + `"}`,
		`{"build_id":"0d65a2d2","upload_id":"x"}`, `{"build_id":"0d65a2d2","upload_id":"` + testUploadID + `","funcs":[[1,2,3]]}`} {
		if w := postSymbols(h.Part, "/api/symbols/part", []byte(b), "tok", false); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", b, w.Code)
		}
	}
	for _, b := range []string{"{", `{"build_id":"0d65a2d2","upload_id":"` + testUploadID + `","module":"a/b.so","arch":"arm64","functions":1}`} {
		if w := postSymbols(h.Commit, "/api/symbols/commit", []byte(b), "tok", false); w.Code != http.StatusBadRequest {
			t.Errorf("commit %q: %d", b, w.Code)
		}
	}
	store.fail = errors.New("down")
	p := gz(t, symbols.Part{BuildID: "0d65a2d2", UploadID: testUploadID, Funcs: symbols.EncodeFuncs([]symbols.Func{{Addr: 1, Size: 1, Name: "x"}})})
	if w := postSymbols(h.Part, "/api/symbols/part", p, "tok", true); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage error: %d", w.Code)
	}
}

package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/objstore"
	"github.com/warriorguo/ozx_apm/server/internal/objstore/s3test"
	"github.com/warriorguo/ozx_apm/server/internal/sessionfiles"
	"github.com/warriorguo/ozx_apm/server/internal/sessionfiles/sftest"
)

// APM-13 through both routers: the device's upload on the ingest server,
// the list and the download on the admin server, with their keys.

const (
	testUploadKey  = "upload-key-for-tests"
	testAdminToken = "admin-token-for-tests"
	testDeviceID   = "0123456789abcdef0123456789abcdef"
	testRecording  = "2026-10-20_153042"
	testCommit     = "3f2c9e1a7b6d5c4e3f2a1b0c9d8e7f6a5b4c3d2e"
)

type sfEnv struct {
	ingest, admin http.Handler
	s3            *s3test.Server
	store         *sftest.MemStore
}

func newSFEnv(t *testing.T, withService bool) *sfEnv {
	t.Helper()
	cfg := &config.Config{}
	cfg.SessionFiles.UploadKey = testUploadKey
	cfg.SessionFiles.AdminToken = testAdminToken
	e := &sfEnv{s3: s3test.New("ozx-apm-sessions"), store: sftest.NewMemStore()}
	var svc *sessionfiles.Service
	if withService {
		client, err := objstore.New(objstore.Config{Endpoint: e.s3.URL, Bucket: e.s3.Bucket, AccessKey: e.s3.AccessKey, SecretKey: e.s3.SecretKey}, e.s3.HTTPClient())
		if err != nil {
			t.Fatal(err)
		}
		svc = sessionfiles.New(e.store, client, sessionfiles.Config{}, zap.NewNop())
	}
	e.ingest = NewRouter(cfg, nil, zap.NewNop(), WithSessionFiles(svc))
	e.admin = NewAdminRouter(cfg, nil, zap.NewNop(), WithSessionFiles(svc))
	return e
}

func do(t *testing.T, h http.Handler, method, url string, body []byte, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, bytes.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func recordingZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"session.json":         `{"startedAt":"2026-10-20T15:30:42Z","scene":"Gameplay","gitCommit":"` + testCommit + `"}`,
		"playermovement.jsonl": strings.Repeat(`{"t":0.1000,"frame":6,"run":1,"ev":"move"}`+"\n", 5000),
	} {
		w, _ := zw.Create(testRecording + "/" + name)
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func uploadKey() map[string]string {
	return map[string]string{"X-Apm-Upload-Key": testUploadKey, "Content-Type": "application/json"}
}

func createBody(id string, data []byte, buildType string) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"upload_id": id, "device_id": testDeviceID, "session_id": "s-1", "recording_name": testRecording,
		"app_version": "1.4.0", "build": "140", "build_type": buildType, "platform": "Android",
		"size_bytes": len(data), "sha256": sha(data), "part_bytes": sessionfiles.MinPartBytes,
		"session_json": `{"gitCommit":"` + testCommit + `"}`,
	})
	return b
}

func TestSessionFiles_UploadListDownload(t *testing.T) {
	e := newSFEnv(t, true)
	data := recordingZip(t)
	id := "00112233445566778899aabbccddeeff"

	// No upload key, a wrong one: 401, nothing stored.
	if rec := do(t, e.ingest, "POST", "/v1/session-files", createBody(id, data, "dev"), nil); rec.Code != 401 {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, e.ingest, "POST", "/v1/session-files", createBody(id, data, "dev"), map[string]string{"X-Apm-Upload-Key": "nope"}); rec.Code != 401 ||
		strings.Contains(rec.Body.String(), "reject_code") {
		t.Fatalf("wrong key: %d %s", rec.Code, rec.Body)
	}
	// A release build is refused for good.
	rec := do(t, e.ingest, "POST", "/v1/session-files", createBody(id, data, "release"), uploadKey())
	var refusal struct {
		RejectCode string `json:"reject_code"`
	}
	json.Unmarshal(rec.Body.Bytes(), &refusal)
	if rec.Code != 403 || refusal.RejectCode != "release_build" {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, e.ingest, "POST", "/v1/session-files", createBody(id, data, "dev"), uploadKey())
	var state sessionfiles.UploadState
	if err := json.Unmarshal(rec.Body.Bytes(), &state); rec.Code != 200 || err != nil || state.PartCount < 1 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	// Commit before the parts: 409 with the missing parts, no reject_code.
	commit, _ := json.Marshal(map[string]interface{}{"device_id": testDeviceID, "sha256": sha(data), "size_bytes": len(data)})
	rec = do(t, e.ingest, "POST", "/v1/session-files/"+id+"/commit", commit, uploadKey())
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"missing":[0`) || strings.Contains(rec.Body.String(), "reject_code") {
		t.Fatalf("early commit: %d %s", rec.Code, rec.Body)
	}
	for n := 0; n < int(state.PartCount); n++ {
		end := (n + 1) * sessionfiles.MinPartBytes
		if end > len(data) {
			end = len(data)
		}
		part := data[n*sessionfiles.MinPartBytes : end]
		h := uploadKey()
		h["X-Apm-Device-Id"] = testDeviceID
		h["X-Apm-Part-Sha256"] = sha(part)
		h["Content-Type"] = "application/octet-stream"
		if rec := do(t, e.ingest, "PUT", "/v1/session-files/"+id+"/parts/"+strconv.Itoa(n), part, h); rec.Code != 200 {
			t.Fatalf("part %d: %d %s", n, rec.Code, rec.Body)
		}
	}
	rec = do(t, e.ingest, "POST", "/v1/session-files/"+id+"/commit", commit, uploadKey())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"complete"`) {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body)
	}

	// The admin list needs the token.
	if rec := do(t, e.admin, "GET", "/api/session-files?git_commit=3f2c9e1", nil, nil); rec.Code != 401 {
		t.Fatalf("list without token: %d", rec.Code)
	}
	auth := map[string]string{"Authorization": "Bearer " + testAdminToken}
	rec = do(t, e.admin, "GET", "/api/session-files?git_commit=3f2c9e1", nil, auth)
	var list struct {
		Files []map[string]interface{} `json:"session_files"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Files) != 1 || list.Files[0]["upload_id"] != id || list.Files[0]["git_commit"] != testCommit ||
		list.Files[0]["recording_name"] != testRecording {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "object_key") || strings.Contains(rec.Body.String(), "session-files/") {
		t.Fatalf("the list exposes storage keys: %s", rec.Body)
	}
	if rec := do(t, e.admin, "GET", "/api/session-files?git_commit=ffff", nil, auth); !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("other commit: %s", rec.Body)
	}
	// Availability without the token: ids and sizes only.
	rec = do(t, e.admin, "GET", "/api/session-files/available?session_id=s-1&session_id=s-2", nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), id) || strings.Contains(rec.Body.String(), testDeviceID) {
		t.Fatalf("available: %d %s", rec.Code, rec.Body)
	}

	// Download with the bearer token, and through a signed link.
	rec = do(t, e.admin, "GET", "/api/session-files/download?upload_id="+id, nil, auth)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) || rec.Header().Get("Content-Type") != "application/zip" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="`+testRecording+`.zip"` || rec.Header().Get("X-Apm-Sha256") != sha(data) {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	if rec := do(t, e.admin, "GET", "/api/session-files/download?upload_id="+id, nil, nil); rec.Code != 401 {
		t.Fatalf("download without token: %d", rec.Code)
	}
	rec = do(t, e.admin, "POST", "/api/session-files/link", []byte(`{"upload_id":"`+id+`"}`), auth)
	var link struct {
		URL string `json:"url"`
	}
	json.Unmarshal(rec.Body.Bytes(), &link)
	if rec.Code != 200 || !strings.HasPrefix(link.URL, "/api/session-files/download?upload_id="+id+"&expires=") {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, e.admin, "GET", link.URL, nil, nil); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("signed download: %d", rec.Code)
	}
	tampered := strings.Replace(link.URL, "upload_id="+id, "upload_id=ffeeddccbbaa99887766554433221100", 1)
	if rec := do(t, e.admin, "GET", tampered, nil, nil); rec.Code != 403 {
		t.Fatalf("link for another upload: %d", rec.Code)
	}
	if rec := do(t, e.admin, "GET", link.URL[:len(link.URL)-1]+"0", nil, nil); rec.Code != 403 {
		t.Fatalf("bad signature: %d", rec.Code)
	}
}

func TestSessionFiles_NotConfigured(t *testing.T) {
	e := newSFEnv(t, false)
	data := recordingZip(t)
	rec := do(t, e.ingest, "POST", "/v1/session-files", createBody("00112233445566778899aabbccddeeff", data, "dev"), uploadKey())
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "3600" || !strings.Contains(rec.Body.String(), `"retry":true`) ||
		strings.Contains(rec.Body.String(), "reject_code") {
		t.Fatalf("ingest: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec := do(t, e.admin, "GET", "/api/session-files", nil, map[string]string{"Authorization": "Bearer " + testAdminToken}); rec.Code != 503 {
		t.Fatalf("admin: %d", rec.Code)
	}
	rec = do(t, e.admin, "GET", "/api/session-files/available?session_id=s-1", nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("available: %d %s", rec.Code, rec.Body)
	}
}

package sessionfiles

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/objstore"
	"github.com/warriorguo/ozx_apm/server/internal/objstore/s3test"
	"github.com/warriorguo/ozx_apm/server/internal/sessionfiles/sftest"
)

const (
	testRecording = "2026-10-20_153042"
	testDevice    = "0123456789abcdef0123456789abcdef"
	testSession   = "1761000000000_a10a10a1"
	testCommit    = "3f2c9e1a7b6d5c4e3f2a1b0c9d8e7f6a5b4c3d2e"
)

var testSessionJSON = `{"startedAt":"2026-10-20T15:30:42.1230000Z","scene":"Gameplay","unityVersion":"2022.3.62f3","gitCommit":"` + testCommit +
	`","dataSource":"builtin","dataPackVersion":0,"dataPackSha256":"","sampleHz":10,"timeBase":"Time.time","maxRetainedSessions":20}`

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

// sessionZip builds an AiTelemetry session directory archive as the SDK does.
func sessionZip(t *testing.T, name string, extra ...zipEntry) []byte {
	t.Helper()
	entries := []zipEntry{
		{name: name + "/session.json", data: []byte(testSessionJSON)},
		{name: name + "/level.json", data: []byte(`{"dataType":"LevelData"}`)},
		{name: name + "/playermovement.jsonl", data: []byte(`{"t":0.1000,"frame":6,"run":1,"ev":"move","pos":{"x":1.000,"y":2.000}}` + "\n")},
		{name: name + "/room01_f0_room_0_0/blightsaccrawler_7-wave1_event.jsonl", data: []byte(`{"t":1.0000,"frame":60,"run":1,"ev":"spawn"}` + "\n")},
	}
	entries = append(entries, extra...)
	return makeZip(t, entries)
}

func makeZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newUploadID(t *testing.T) string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type env struct {
	svc   *Service
	store *sftest.MemStore
	s3    *s3test.Server
	now   time.Time
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	s3 := s3test.New("ozx-apm-sessions")
	client, err := objstore.New(objstore.Config{Endpoint: s3.URL, Bucket: s3.Bucket, AccessKey: s3.AccessKey, SecretKey: s3.SecretKey}, s3.HTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	store := sftest.NewMemStore()
	e := &env{store: store, s3: s3, now: time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)}
	e.svc = New(store, client, cfg, zap.NewNop())
	e.svc.SetClock(func() time.Time { return e.now })
	s3.Now = func() time.Time { return e.now }
	return e
}

func createReq(id string, data []byte, partBytes int64) CreateRequest {
	return CreateRequest{UploadID: id, DeviceID: testDevice, SessionID: testSession, RecordingName: testRecording,
		AppVersion: "1.4.0", AppBuild: "140", BuildType: "dev", Platform: "Android", DeviceModel: "samsung SM-A336B",
		OSVersion: "Android OS 16", UnityVersion: "2022.3.62f3", SDKVersion: "2.1.0",
		SizeBytes: int64(len(data)), SHA256: shaHex(data), PartBytes: partBytes, FileCount: 4, RawBytes: 1000,
		SessionJSON: testSessionJSON}
}

func (e *env) putParts(t *testing.T, id string, data []byte, partBytes int, only func(int) bool) {
	t.Helper()
	for n := 0; n*partBytes < len(data); n++ {
		if only != nil && !only(n) {
			continue
		}
		end := (n + 1) * partBytes
		if end > len(data) {
			end = len(data)
		}
		part := data[n*partBytes : end]
		if err := e.svc.PutPart(context.Background(), id, n, testDevice, shaHex(part), bytes.NewReader(part)); err != nil {
			t.Fatalf("part %d: %v", n, err)
		}
	}
}

// upload runs the whole protocol.
func (e *env) upload(t *testing.T, data []byte, partBytes int) (string, *UploadState) {
	t.Helper()
	ctx := context.Background()
	id := newUploadID(t)
	if _, err := e.svc.Create(ctx, createReq(id, data, int64(partBytes))); err != nil {
		t.Fatalf("create: %v", err)
	}
	e.putParts(t, id, data, partBytes, nil)
	state, err := e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id, state
}

func wantError(t *testing.T, err error, status int, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want an *Error %d %q, got %v", status, code, err)
	}
	if e.Status != status || e.RejectCode != code {
		t.Fatalf("got %d %q (%s), want %d %q", e.Status, e.RejectCode, e.Message, status, code)
	}
	return e
}

func TestUpload_SmallArchive(t *testing.T) {
	e := newEnv(t, Config{})
	data := sessionZip(t, testRecording)
	id, state := e.upload(t, data, MinPartBytes)
	if state.Status != models.SessionFileComplete || state.FileCount != 4 {
		t.Fatalf("state %+v", state)
	}
	f, _ := e.store.GetSessionFile(context.Background(), id)
	wantKey := "session-files/sessions/2026/10/20/" + id + ".zip"
	if f.Status != models.SessionFileComplete || f.ObjectKey != wantKey || f.GitCommit != testCommit ||
		f.StartedAt != "2026-10-20T15:30:42.1230000Z" || f.Scene != "Gameplay" || f.DataSource != "builtin" || f.DataPackVersion != "0" ||
		f.FileCount != 4 || f.RawBytes == 0 || f.SizeBytes != uint64(len(data)) || f.BuildType != "dev" ||
		!f.ExpiresAt.Equal(e.now.Add(30*24*time.Hour)) {
		t.Fatalf("row %+v", f)
	}
	stored, ok := e.s3.Object(wantKey)
	if !ok || !bytes.Equal(stored, data) {
		t.Fatalf("stored archive differs (%d bytes, ok %v)", len(stored), ok)
	}
	if keys := e.s3.Keys("session-files/tmp/"); len(keys) != 0 {
		t.Fatalf("parts left: %v", keys)
	}
	// The download is the archive.
	rc, n, err := e.svc.Open(context.Background(), *f)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if n != int64(len(data)) || !bytes.Equal(got, data) {
		t.Fatal("download differs")
	}
	// Committing again is a no-op; creating again answers complete.
	if st, err := e.svc.Commit(context.Background(), id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))}); err != nil || st.Status != models.SessionFileComplete {
		t.Fatalf("second commit %+v %v", st, err)
	}
	if st, err := e.svc.Create(context.Background(), createReq(id, data, MinPartBytes)); err != nil || st.Status != models.SessionFileComplete {
		t.Fatalf("create after commit %+v %v", st, err)
	}
	// A part sent late (lost answer) is accepted and not stored.
	if err := e.svc.PutPart(context.Background(), id, 0, testDevice, shaHex(data), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if keys := e.s3.Keys("session-files/tmp/"); len(keys) != 0 {
		t.Fatalf("late part stored: %v", keys)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// Larger than one assembly part: joined with S3 multipart (8 MiB parts, the
// last smaller; the fake refuses parts below 5 MiB as S3 does).
func TestUpload_LargeArchiveUsesMultipart(t *testing.T) {
	e := newEnv(t, Config{})
	blob := randomBytes(19 << 20) // incompressible
	data := sessionZip(t, testRecording, zipEntry{name: testRecording + "/rail.jsonl", data: blob})
	id, state := e.upload(t, data, 1<<20)
	if state.Status != models.SessionFileComplete {
		t.Fatal(state)
	}
	f, _ := e.store.GetSessionFile(context.Background(), id)
	stored, _ := e.s3.Object(f.ObjectKey)
	if !bytes.Equal(stored, data) || e.s3.OpenUploads() != 0 || e.s3.Requests["upload-part"] != 3 {
		t.Fatalf("assembled %d of %d bytes, open %d, %d multipart parts", len(stored), len(data), e.s3.OpenUploads(), e.s3.Requests["upload-part"])
	}
}

func TestUpload_ResumeAndMissingParts(t *testing.T) {
	e := newEnv(t, Config{})
	ctx := context.Background()
	data := sessionZip(t, testRecording, zipEntry{name: testRecording + "/x.jsonl", data: randomBytes(300 << 10)})
	id := newUploadID(t)
	st, err := e.svc.Create(ctx, createReq(id, data, MinPartBytes))
	if err != nil || st.PartCount != int64((len(data)+MinPartBytes-1)/MinPartBytes) || len(st.Received) != 0 {
		t.Fatalf("create %+v %v", st, err)
	}
	e.putParts(t, id, data, MinPartBytes, func(n int) bool { return n%2 == 0 })
	// The app restarts: create again (same id) learns what arrived.
	st, err = e.svc.Create(ctx, createReq(id, data, MinPartBytes))
	if err != nil || fmt.Sprint(st.Received) != "[0 2 4]" {
		t.Fatalf("resume %+v %v", st, err)
	}
	_, err = e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))})
	if ce := wantError(t, err, http.StatusConflict, ""); fmt.Sprint(ce.Missing) != "[1 3]" {
		t.Fatalf("missing %v", ce.Missing)
	}
	e.putParts(t, id, data, MinPartBytes, func(n int) bool { return n%2 == 1 })
	e.putParts(t, id, data, MinPartBytes, func(n int) bool { return n == 0 }) // re-sent: harmless
	if st, err := e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))}); err != nil || st.Status != models.SessionFileComplete {
		t.Fatalf("commit %+v %v", st, err)
	}
}

func TestCreate_Validation(t *testing.T) {
	e := newEnv(t, Config{MaxArchiveBytes: 10 << 20})
	data := sessionZip(t, testRecording)
	ok := createReq(newUploadID(t), data, MinPartBytes)
	cases := []struct {
		name   string
		mut    func(*CreateRequest)
		status int
		code   string
	}{
		{"release build", func(r *CreateRequest) { r.BuildType = "release" }, 403, "release_build"},
		{"no build type", func(r *CreateRequest) { r.BuildType = "" }, 403, "release_build"},
		{"upper-case id", func(r *CreateRequest) { r.UploadID = strings.ToUpper(r.UploadID) }, 400, "invalid_request"},
		{"no device", func(r *CreateRequest) { r.DeviceID = "" }, 400, "invalid_request"},
		{"device with a slash", func(r *CreateRequest) { r.DeviceID = "a/b" }, 400, "invalid_request"},
		{"recording ..", func(r *CreateRequest) { r.RecordingName = ".." }, 400, "invalid_request"},
		{"recording with a slash", func(r *CreateRequest) { r.RecordingName = "a/b" }, 400, "invalid_request"},
		{"recording starting with .", func(r *CreateRequest) { r.RecordingName = ".hidden" }, 400, "invalid_request"},
		{"too large", func(r *CreateRequest) { r.SizeBytes = 11 << 20 }, 413, "too_large"},
		{"too small", func(r *CreateRequest) { r.SizeBytes = 10 }, 400, "invalid_request"},
		{"part too small", func(r *CreateRequest) { r.PartBytes = 1000 }, 400, "invalid_request"},
		{"part too large", func(r *CreateRequest) { r.PartBytes = 8 << 20 }, 400, "invalid_request"},
		{"part count wrong", func(r *CreateRequest) { r.PartCount = 7 }, 400, "invalid_request"},
		{"bad sha", func(r *CreateRequest) { r.SHA256 = "xyz" }, 400, "invalid_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ok
			c.mut(&r)
			_, err := e.svc.Create(context.Background(), r)
			wantError(t, err, c.status, c.code)
		})
	}
	if len(e.store.Rows()) != 0 {
		t.Fatalf("a refused upload was stored: %d rows", len(e.store.Rows()))
	}
	// An oversized session.json is left out of the index, never a refusal.
	big := ok
	big.UploadID = newUploadID(t)
	big.SessionJSON = `{"gitCommit":"abcdef1"}` + strings.Repeat(" ", MaxSessionJSON)
	if _, err := e.svc.Create(context.Background(), big); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.store.GetSessionFile(context.Background(), big.UploadID); f.SessionJSON != "" || f.GitCommit != "" {
		t.Fatalf("oversized session.json kept: %d bytes, commit %q", len(f.SessionJSON), f.GitCommit)
	}
	// The editor build type is accepted; a bad commit from session.json is dropped.
	r := ok
	r.BuildType = "editor"
	r.SessionJSON = `{"gitCommit":"not a commit; rm -rf"}`
	if _, err := e.svc.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.store.GetSessionFile(context.Background(), r.UploadID); f.GitCommit != "" || f.BuildType != "editor" {
		t.Fatalf("row %+v", f)
	}
}

func TestCreate_ConflictsAndLimits(t *testing.T) {
	e := newEnv(t, Config{MaxOpenUploads: 2, MaxUploadsPerDay: 3})
	ctx := context.Background()
	data := sessionZip(t, testRecording)
	req := createReq(newUploadID(t), data, MinPartBytes)
	if _, err := e.svc.Create(ctx, req); err != nil {
		t.Fatal(err)
	}
	other := req
	other.DeviceID = "another-device"
	_, err := e.svc.Create(ctx, other)
	wantError(t, err, http.StatusConflict, "upload_conflict")
	other = req
	other.SHA256 = strings.Repeat("0", 64)
	_, err = e.svc.Create(ctx, other)
	wantError(t, err, http.StatusConflict, "upload_conflict")

	second := createReq(newUploadID(t), data, MinPartBytes)
	if _, err := e.svc.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	third := createReq(newUploadID(t), data, MinPartBytes)
	_, err = e.svc.Create(ctx, third)
	if le := wantError(t, err, http.StatusTooManyRequests, ""); le.RetryAfter == 0 {
		t.Fatal("no Retry-After")
	}
	// Committing one frees a slot; the daily count still applies.
	e.putParts(t, req.UploadID, data, MinPartBytes, nil)
	if _, err := e.svc.Commit(ctx, req.UploadID, CommitRequest{DeviceID: testDevice, SHA256: req.SHA256, SizeBytes: req.SizeBytes}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, third); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Create(ctx, createReq(newUploadID(t), data, MinPartBytes))
	wantError(t, err, http.StatusTooManyRequests, "")
	// A day later the device may upload again (the open ones still count).
	e.now = e.now.Add(25 * time.Hour)
	e.putParts(t, second.UploadID, data, MinPartBytes, nil)
	if _, err := e.svc.Commit(ctx, second.UploadID, CommitRequest{DeviceID: testDevice, SHA256: second.SHA256, SizeBytes: second.SizeBytes}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, createReq(newUploadID(t), data, MinPartBytes)); err != nil {
		t.Fatal(err)
	}
}

func TestPart_Errors(t *testing.T) {
	e := newEnv(t, Config{})
	ctx := context.Background()
	data := sessionZip(t, testRecording, zipEntry{name: testRecording + "/x.jsonl", data: randomBytes(100 << 10)})
	id := newUploadID(t)
	if _, err := e.svc.Create(ctx, createReq(id, data, MinPartBytes)); err != nil {
		t.Fatal(err)
	}
	p0 := data[:MinPartBytes]
	wantError(t, e.svc.PutPart(ctx, id, 0, testDevice, shaHex(p0[1:]), bytes.NewReader(p0)), 400, "") // corrupted: retry
	wantError(t, e.svc.PutPart(ctx, id, 0, testDevice, shaHex(p0[1:]), bytes.NewReader(p0[1:])), 400, "invalid_request")
	wantError(t, e.svc.PutPart(ctx, id, 0, "someone-else", shaHex(p0), bytes.NewReader(p0)), 403, "wrong_device")
	wantError(t, e.svc.PutPart(ctx, id, 0, "", shaHex(p0), bytes.NewReader(p0)), 403, "wrong_device")
	wantError(t, e.svc.PutPart(ctx, id, 9, testDevice, shaHex(p0), bytes.NewReader(p0)), 400, "invalid_request")
	wantError(t, e.svc.PutPart(ctx, newUploadID(t), 0, testDevice, shaHex(p0), bytes.NewReader(p0)), 404, "")
	wantError(t, e.svc.PutPart(ctx, "nope", 0, testDevice, shaHex(p0), bytes.NewReader(p0)), 400, "invalid_request")
	wantError(t, e.svc.PutPart(ctx, id, 0, testDevice, "", bytes.NewReader(p0)), 400, "invalid_request")
	if keys := e.s3.Keys(""); len(keys) != 0 {
		t.Fatalf("stored: %v", keys)
	}
	// Object store down: retry later.
	e.s3.Fail("put", 3)
	wantError(t, e.svc.PutPart(ctx, id, 0, testDevice, shaHex(p0), bytes.NewReader(p0)), 503, "")
	// ClickHouse down (no cached row): retry later.
	e.store.Fail = sftest.ErrStore
	e.svc.forget(id)
	wantError(t, e.svc.PutPart(ctx, id, 0, testDevice, shaHex(p0), bytes.NewReader(p0)), 503, "")
	_, err := e.svc.Create(ctx, createReq(newUploadID(t), data, MinPartBytes))
	wantError(t, err, 503, "")
}

func TestCommit_ChecksumMismatchFailsTheUpload(t *testing.T) {
	e := newEnv(t, Config{})
	ctx := context.Background()
	data := sessionZip(t, testRecording)
	id := newUploadID(t)
	req := createReq(id, data, MinPartBytes)
	req.SHA256 = shaHex([]byte("something else"))
	if _, err := e.svc.Create(ctx, req); err != nil {
		t.Fatal(err)
	}
	e.putParts(t, id, data, MinPartBytes, nil)
	_, err := e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: req.SHA256, SizeBytes: req.SizeBytes})
	wantError(t, err, http.StatusUnprocessableEntity, "checksum_mismatch")
	f, _ := e.store.GetSessionFile(ctx, id)
	if f.Status != models.SessionFileFailed || !strings.Contains(f.Error, "SHA-256") {
		t.Fatalf("row %+v", f)
	}
	if keys := e.s3.Keys(""); len(keys) != 0 {
		t.Fatalf("objects left: %v", keys)
	}
	_, err = e.svc.Create(ctx, req)
	wantError(t, err, http.StatusConflict, "upload_failed")
	wantError(t, e.svc.PutPart(ctx, id, 0, testDevice, shaHex(data), bytes.NewReader(data)), http.StatusConflict, "upload_failed")
	// The commit must repeat the upload's checksum.
	id2 := newUploadID(t)
	if _, err := e.svc.Create(ctx, createReq(id2, data, MinPartBytes)); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Commit(ctx, id2, CommitRequest{DeviceID: testDevice, SHA256: req.SHA256, SizeBytes: int64(len(data))})
	wantError(t, err, http.StatusConflict, "upload_conflict")
}

func TestCommit_InvalidArchivesAreRefused(t *testing.T) {
	r := testRecording
	cases := map[string][]byte{
		"not a zip":            []byte(strings.Repeat("not a zip archive ", 10)),
		"outside the root":     makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: "other/x.jsonl"}}),
		"zip slip":             makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: r + "/../../etc/passwd"}}),
		"absolute":             makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: "/" + r + "/x"}}),
		"backslash":            makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: r + `/a\..\..\x`}}),
		"no session.json":      makeZip(t, []zipEntry{{name: r + "/level.json", data: []byte("{}")}}),
		"session.json deeper":  makeZip(t, []zipEntry{{name: r + "/sub/session.json", data: []byte("{}")}}),
		"symlink":              makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: r + "/link", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0777}}),
		"dot segment":          makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: r + "/./x"}}),
		"duplicate":            makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: r + "/session.json", data: []byte("{}")}}),
		"another root":         makeZip(t, []zipEntry{{name: "2026-01-01_000000/session.json", data: []byte("{}")}}),
		"drive letter (colon)": makeZip(t, []zipEntry{{name: r + "/session.json", data: []byte("{}")}, {name: r + "/c:x"}}),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, Config{})
			ctx := context.Background()
			id := newUploadID(t)
			if _, err := e.svc.Create(ctx, createReq(id, data, MinPartBytes)); err != nil {
				t.Fatal(err)
			}
			e.putParts(t, id, data, MinPartBytes, nil)
			_, err := e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))})
			wantError(t, err, http.StatusUnprocessableEntity, "invalid_archive")
			if keys := e.s3.Keys(""); len(keys) != 0 {
				t.Fatalf("objects left: %v", keys)
			}
		})
	}
}

func TestValidateArchive_Accepts(t *testing.T) {
	data := makeZip(t, []zipEntry{
		{name: testRecording + "/"},
		{name: testRecording + "/session.json", data: []byte("{}")},
		{name: testRecording + "/room01_a/", mode: os.ModeDir | 0755},
		{name: testRecording + "/room01_a/e_1-wave1_event.jsonl", data: []byte("{}\n")},
	})
	files, raw, err := ValidateArchive(bytes.NewReader(data), int64(len(data)), testRecording)
	if err != nil || files != 2 || raw != 5 {
		t.Fatalf("%d %d %v", files, raw, err)
	}
}

func TestCommit_AssemblyFailureIsRetriedAndAborts(t *testing.T) {
	e := newEnv(t, Config{})
	ctx := context.Background()
	data := sessionZip(t, testRecording, zipEntry{name: testRecording + "/big.bin", data: randomBytes(10 << 20)})
	id := newUploadID(t)
	if _, err := e.svc.Create(ctx, createReq(id, data, 1<<20)); err != nil {
		t.Fatal(err)
	}
	e.putParts(t, id, data, 1<<20, nil)
	e.s3.Fail("upload-part", 3)
	_, err := e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))})
	wantError(t, err, http.StatusServiceUnavailable, "")
	if e.s3.OpenUploads() != 0 {
		t.Fatal("the multipart upload was left open")
	}
	if f, _ := e.store.GetSessionFile(ctx, id); f.Status != models.SessionFileUploading {
		t.Fatalf("status %s", f.Status)
	}
	// The retry succeeds with the parts still there.
	if st, err := e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))}); err != nil || st.Status != models.SessionFileComplete {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestExpiry(t *testing.T) {
	e := newEnv(t, Config{Retention: 30 * 24 * time.Hour, Incomplete: 7 * 24 * time.Hour})
	ctx := context.Background()
	data := sessionZip(t, testRecording)
	oldID, _ := e.upload(t, data, MinPartBytes)
	old, _ := e.store.GetSessionFile(ctx, oldID)
	// An abandoned upload with one part.
	abandoned := newUploadID(t)
	if _, err := e.svc.Create(ctx, createReq(abandoned, data, MinPartBytes)); err != nil {
		t.Fatal(err)
	}
	e.putParts(t, abandoned, data, MinPartBytes, func(n int) bool { return n == 0 })
	e.now = e.now.Add(8 * 24 * time.Hour)
	recentID, _ := e.upload(t, data, MinPartBytes)
	recent, _ := e.store.GetSessionFile(ctx, recentID)

	n, err := e.svc.ExpireOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("after 8 days: %d %v (the abandoned upload)", n, err)
	}
	if f, _ := e.store.GetSessionFile(ctx, abandoned); f != nil || len(e.s3.Keys("session-files/tmp/")) != 0 {
		t.Fatalf("abandoned upload left: %+v %v", f, e.s3.Keys("session-files/tmp/"))
	}
	e.now = e.now.Add(23 * 24 * time.Hour) // 31 days after the first
	n, err = e.svc.ExpireOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("after 31 days: %d %v", n, err)
	}
	if _, ok := e.s3.Object(old.ObjectKey); ok {
		t.Fatal("expired archive kept")
	}
	if f, _ := e.store.GetSessionFile(ctx, oldID); f != nil {
		t.Fatal("expired row kept")
	}
	if _, ok := e.s3.Object(recent.ObjectKey); !ok {
		t.Fatal("recent archive deleted")
	}
	// An object store failure keeps the row for the next run.
	e.now = e.now.Add(10 * 24 * time.Hour)
	e.s3.Fail("delete", 3)
	if _, err := e.svc.ExpireOnce(ctx); err == nil {
		t.Fatal("want an error")
	}
	if f, _ := e.store.GetSessionFile(ctx, recentID); f == nil {
		t.Fatal("row deleted although its object was not")
	}
	if n, err := e.svc.ExpireOnce(ctx); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	// A second run (another replica) finds nothing.
	if n, err := e.svc.ExpireOnce(ctx); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
}

func TestSweep(t *testing.T) {
	e := newEnv(t, Config{})
	ctx := context.Background()
	data := sessionZip(t, testRecording)
	keptID, _ := e.upload(t, data, MinPartBytes)
	kept, _ := e.store.GetSessionFile(ctx, keptID)
	orphanArchive := "session-files/sessions/2026/09/01/" + newUploadID(t) + ".zip"
	orphanPart := "session-files/tmp/" + newUploadID(t) + "/000000"
	freshPart := "session-files/tmp/" + newUploadID(t) + "/000000"
	foreign := "other-app/x.zip"
	for _, k := range []string{orphanArchive, orphanPart, freshPart, foreign} {
		e.s3.Put(k, []byte("x"))
	}
	old := e.now.Add(-10 * 24 * time.Hour)
	e.s3.SetModified(orphanArchive, old)
	e.s3.SetModified(orphanPart, old)
	e.s3.SetModified(kept.ObjectKey, old)
	e.s3.SetModified(foreign, old)
	stale := e.s3.OpenMultipart("session-files/sessions/2026/10/01/x.zip", old)
	_ = stale
	e.s3.OpenMultipart("session-files/sessions/2026/10/20/y.zip", e.now)

	n, err := e.svc.SweepOnce(ctx)
	if err != nil || n != 3 {
		t.Fatalf("swept %d %v", n, err)
	}
	if _, ok := e.s3.Object(orphanArchive); ok {
		t.Error("orphan archive kept")
	}
	if _, ok := e.s3.Object(orphanPart); ok {
		t.Error("old part kept")
	}
	for _, k := range []string{freshPart, foreign, kept.ObjectKey} {
		if _, ok := e.s3.Object(k); !ok {
			t.Errorf("%s deleted", k)
		}
	}
	if e.s3.OpenUploads() != 1 {
		t.Errorf("open multipart uploads %d, want the recent one", e.s3.OpenUploads())
	}
}

func TestPurge(t *testing.T) {
	e := newEnv(t, Config{})
	ctx := context.Background()
	data := sessionZip(t, testRecording)
	id1, _ := e.upload(t, data, MinPartBytes)
	open := newUploadID(t)
	if _, err := e.svc.Create(ctx, createReq(open, data, MinPartBytes)); err != nil {
		t.Fatal(err)
	}
	e.putParts(t, open, data, MinPartBytes, func(n int) bool { return n == 0 })
	// Another device's recording, in a session a player also played.
	other := createReq(newUploadID(t), data, MinPartBytes)
	other.DeviceID = "other-device"
	other.SessionID = "other-session"
	if _, err := e.svc.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	e.store.PlayerOps["player-1"] = []string{"other-session"}

	if n, _ := e.svc.Count(ctx, "device_id", testDevice); n != 2 {
		t.Fatalf("count %d", n)
	}
	res := e.svc.Purge(ctx, "device_id", testDevice)
	if res.UploadsBefore != 2 || res.UploadsAfter != 0 || res.Error != "" {
		t.Fatalf("purge %+v", res)
	}
	if f, _ := e.store.GetSessionFile(ctx, id1); f != nil {
		t.Fatal("row kept")
	}
	if keys := e.s3.Keys("session-files/"); len(keys) != 0 {
		t.Fatalf("objects kept: %v", keys)
	}
	if f, _ := e.store.GetSessionFile(ctx, other.UploadID); f == nil {
		t.Fatal("the other device's upload was deleted")
	}
	res = e.svc.Purge(ctx, "player_id", "player-1")
	if res.UploadsBefore != 1 || res.UploadsAfter != 0 || res.Error != "" {
		t.Fatalf("purge by player %+v", res)
	}
	// Idempotent; an object store failure is reported and keeps the rows.
	if res := e.svc.Purge(ctx, "device_id", testDevice); res.UploadsBefore != 0 || res.Error != "" {
		t.Fatalf("again %+v", res)
	}
	id3, _ := e.upload(t, data, MinPartBytes)
	e.s3.Fail("delete", 3)
	res = e.svc.Purge(ctx, "device_id", testDevice)
	if res.Error == "" || res.UploadsAfter != 1 {
		t.Fatalf("failure %+v", res)
	}
	if f, _ := e.store.GetSessionFile(ctx, id3); f == nil {
		t.Fatal("row deleted although its object was not")
	}
}

// The archive the SDK writes (SessionArchive, generated on Unity's Mono by
// SessionArchiveTests.WriteServerFixture): data descriptors, UTF-8 names,
// Deflate. Go reads it, the validator accepts it, the upload completes.
func TestSDKArchiveFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/sdk_session_apm13.zip")
	if err != nil {
		t.Fatal(err)
	}
	shaFile, err := os.ReadFile("testdata/sdk_session_apm13.zip.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if got := shaHex(data); got != strings.TrimSpace(string(shaFile)) {
		t.Fatalf("SHA-256 %s, the SDK computed %s", got, shaFile)
	}
	files, raw, err := ValidateArchive(bytes.NewReader(data), int64(len(data)), testRecording)
	if err != nil || files != 8 || raw == 0 {
		t.Fatalf("validate: %d %d %v", files, raw, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc) // checks the CRC
		rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if f.Method != zip.Deflate || f.Flags&0x8 == 0 || f.Flags&0x800 == 0 {
			t.Errorf("%s: method %d flags %x", f.Name, f.Method, f.Flags)
		}
		names[f.Name] = string(b)
	}
	if !strings.Contains(names[testRecording+"/session.json"], `"gitCommit":"6116e2e4ff38de10eac61ba3c425e8f50a3aacdb"`) ||
		!strings.HasPrefix(names[testRecording+"/room01_f0_room_1_0/café_1-wave1_event.jsonl"], `{"t":3.0000,"frame":180`) ||
		!strings.HasPrefix(names[testRecording+"/runstart.jsonl"], `{"t":0.5000`) {
		t.Fatalf("entries %v", names)
	}
	e := newEnv(t, Config{})
	_, state := e.upload(t, data, MinPartBytes)
	if state.Status != models.SessionFileComplete || state.FileCount != 8 {
		t.Fatalf("%+v", state)
	}

	// OZX's ai_trace.py reads the unzipped directory as it is
	// (OZX_AI_TRACE=<ozx_base>/scripts/ai_trace.py; python3 -I).
	script := os.Getenv("OZX_AI_TRACE")
	if script == "" {
		t.Log("OZX_AI_TRACE not set: ai_trace.py not run")
		return
	}
	root := t.TempDir()
	for name, content := range names {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("python3", "-I", script, filepath.Join(root, testRecording), "--json").Output()
	if err != nil {
		t.Fatalf("ai_trace.py: %v", err)
	}
	var report struct {
		Overview struct {
			PlayerDeaths []map[string]interface{} `json:"playerDeaths"`
		} `json:"overview"`
		Anomalies []interface{} `json:"anomalies"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if d := report.Overview.PlayerDeaths; len(d) != 1 || d[0]["killerId"] != "quick_sand_worm" || d[0]["roomId"] != "f0_room_1_0" || len(report.Anomalies) != 0 {
		t.Fatalf("ai_trace.py: %s", out)
	}
}

func TestSummarize(t *testing.T) {
	s := summarize(testSessionJSON)
	if s.GitCommit != testCommit || s.Scene != "Gameplay" || s.DataPackVersion != "0" || s.UnityVersion != "2022.3.62f3" {
		t.Fatalf("%+v", s)
	}
	if (summarize("not json") != sessionSummary{}) || (summarize(`{"gitCommit":null}`) != sessionSummary{}) {
		t.Fatal("garbage must give an empty summary")
	}
	if s := summarize(`{"scene":"a\u0000b\nc"}`); s.Scene != "abc" {
		t.Fatalf("control characters kept: %q", s.Scene)
	}
}

func TestVersionOrdersStates(t *testing.T) {
	later := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if models.SessionFileVersion(models.SessionFileComplete, earlier) <= models.SessionFileVersion(models.SessionFileUploading, later) {
		t.Fatal("a late uploading row must not replace a completed one")
	}
	if models.SessionFileVersion(models.SessionFileFailed, earlier) <= models.SessionFileVersion(models.SessionFileUploading, later) {
		t.Fatal("failed must win over uploading")
	}
}

// A zip whose local header names another file than its central directory
// entry (a streaming extractor would write ../c.jsonl): refused.
func TestCommit_LocalHeaderNameMismatchIsRefused(t *testing.T) {
	data := makeZip(t, []zipEntry{{name: testRecording + "/session.json", data: []byte("{}")}, {name: testRecording + "/ab/c.jsonl", data: []byte("x")}})
	i := bytes.Index(data, []byte("/ab/c.jsonl"))
	copy(data[i:], "/../c.jsonl")
	if _, _, err := ValidateArchive(bytes.NewReader(data), int64(len(data)), testRecording); err == nil || !strings.Contains(err.Error(), "local header") {
		t.Fatalf("err %v", err)
	}
}

// goneObjects reports every archive as missing after it was written: a
// privacy deletion or the cleaner removed the upload during the commit.
type goneObjects struct{ Objects }

func (g goneObjects) HeadObject(ctx context.Context, key string) (int64, error) {
	return 0, &objstore.Error{Op: "head", Key: key, Status: http.StatusNotFound}
}

func TestCommit_DeletedDuringTheCommitLeavesNoRow(t *testing.T) {
	e := newEnv(t, Config{})
	e.svc.obj = goneObjects{e.svc.obj}
	ctx := context.Background()
	data := sessionZip(t, testRecording)
	id := newUploadID(t)
	if _, err := e.svc.Create(ctx, createReq(id, data, MinPartBytes)); err != nil {
		t.Fatal(err)
	}
	e.putParts(t, id, data, MinPartBytes, nil)
	_, err := e.svc.Commit(ctx, id, CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))})
	wantError(t, err, http.StatusNotFound, "")
	if f, _ := e.store.GetSessionFile(ctx, id); f != nil {
		t.Fatalf("row left: %+v", f)
	}
}

// A commit outlives the request that started it: the client's retry gets
// its result.
func TestCommit_DetachedFromTheRequest(t *testing.T) {
	e := newEnv(t, Config{})
	data := sessionZip(t, testRecording)
	id := newUploadID(t)
	if _, err := e.svc.Create(context.Background(), createReq(id, data, MinPartBytes)); err != nil {
		t.Fatal(err)
	}
	e.putParts(t, id, data, MinPartBytes, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client has gone already
	req := CommitRequest{DeviceID: testDevice, SHA256: shaHex(data), SizeBytes: int64(len(data))}
	if _, err := e.svc.Commit(ctx, id, req); err == nil {
		// the run may have finished first: fine
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := e.svc.Commit(context.Background(), id, req)
		if err == nil && st.Status == models.SessionFileComplete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%+v %v", st, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/objstore"
	"github.com/warriorguo/ozx_apm/server/internal/sessionfiles"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-13 end to end on a real ClickHouse and, where OZX_TEST_S3_ENDPOINT is
// set, a real S3 implementation (the lead's local SeaweedFS 4.48:
// OZX_TEST_S3_ENDPOINT=http://127.0.0.1:19533; any access key works there):
// a recording uploaded in parts across two server "replicas", joined with
// S3 multipart, listed by git commit, downloaded through a signed link,
// unzipped to OZX's SessionFolder/<session>/ (and read by ai_trace.py when
// OZX_AI_TRACE is set); expiry; privacy deletion by device and by player.
// Without OZX_TEST_S3_ENDPOINT only the ClickHouse part runs.
//
// Optional: OZX_TEST_S3_BUCKET (default ozx-apm-test, created if missing),
// OZX_TEST_S3_ACCESS_KEY / OZX_TEST_S3_SECRET_KEY (default "test").

const (
	apm13UploadKey = "apm13-upload-key"
	apm13Token     = "apm13-admin-token"
	apm13Device    = "a13a13a13a13a13a13a13a13a13a13a1"
	apm13Commit    = "6116e2e4ff38de10eac61ba3c425e8f50a3aacdb"
	apm13Recording = "2026-10-20_153042"
)

func apm13S3(t *testing.T) (*objstore.Client, config.S3Config) {
	t.Helper()
	endpoint := os.Getenv("OZX_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("OZX_TEST_S3_ENDPOINT not set (an S3 endpoint, e.g. http://127.0.0.1:19533)")
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	cfg := config.S3Config{Endpoint: endpoint, Bucket: env("OZX_TEST_S3_BUCKET", "ozx-apm-test"), Region: "us-east-1",
		AccessKey: env("OZX_TEST_S3_ACCESS_KEY", "test"), SecretKey: env("OZX_TEST_S3_SECRET_KEY", "test"),
		Prefix: fmt.Sprintf("apm13-it-%d/", time.Now().UnixNano())}
	client, err := objstore.New(objstore.Config{Endpoint: cfg.Endpoint, Bucket: cfg.Bucket, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.CreateBucket(ctx); err != nil {
		t.Logf("create bucket %s: %v (continuing: it may exist)", cfg.Bucket, err)
	}
	t.Cleanup(func() {
		objs, err := client.ListAll(context.Background(), cfg.Prefix, 100000)
		if err != nil {
			t.Logf("cleanup list: %v", err)
		}
		for _, o := range objs {
			client.DeleteObject(context.Background(), o.Key)
		}
	})
	return client, cfg
}

type apm13Env struct {
	db       string
	client   *storage.ClickHouseClient
	repo     *storage.Repository
	s3       *objstore.Client
	cfg      *config.Config
	services []*sessionfiles.Service
	ingest   []http.Handler // one per "replica"
	admin    []http.Handler
}

// setupAPM13 builds two server replicas (separate services and routers)
// over one database and bucket prefix.
func setupAPM13(t *testing.T) *apm13Env {
	t.Helper()
	admin := adminClient(t)
	s3, s3cfg := apm13S3(t)
	e := &apm13Env{db: fmt.Sprintf("ozx_apm_apm13_%d", time.Now().UnixNano()%1e9), s3: s3}
	createDB(t, admin, e.db)
	chCfg := testutil.TestClickHouseConfig()
	chCfg.Database = e.db
	client, err := testutil.SetupTestDatabase(chCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	e.client = client
	e.repo = storage.NewRepository(client, zap.NewNop())
	e.cfg = &config.Config{ClickHouse: *chCfg, S3: s3cfg}
	e.cfg.SessionFiles = config.SessionFilesConfig{UploadKey: apm13UploadKey, AdminToken: apm13Token, RetentionDays: 30,
		IncompleteDays: 7, MaxArchiveBytes: 256 << 20, MaxUploadsPerDevicePerDay: 100, MaxBytesPerDevicePerDay: 4 << 30, MaxOpenUploadsPerDevice: 4}
	e.cfg.AdminServer.PrivacyToken = privacyTestToken
	for i := 0; i < 2; i++ {
		svc := api.NewSessionFileService(e.cfg, e.repo, zap.NewNop())
		if svc == nil {
			t.Fatal("session file service not built")
		}
		e.services = append(e.services, svc)
		e.ingest = append(e.ingest, api.NewRouter(e.cfg, e.repo, zap.NewNop(), api.WithSessionFiles(svc)))
		e.admin = append(e.admin, api.NewAdminRouter(e.cfg, e.repo, zap.NewNop(), api.WithSessionFiles(svc)))
	}
	return e
}

func serve(h http.Handler, method, target string, body []byte, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// apm13Session is an AiTelemetry session directory (relative path -> content):
// the files ai_trace.py reads, plus fill bytes (random hex lines, so the
// archive is about fill bytes large) to make it a multi-part upload.
func apm13Session(fill int) map[string]string {
	files := map[string]string{
		"session.json": `{"startedAt":"2026-10-20T15:30:42.1230000Z","scene":"Gameplay","unityVersion":"2022.3.62f3","gitCommit":"` + apm13Commit +
			`","dataSource":"builtin","dataPackVersion":0,"dataPackSha256":"","sampleHz":10,"timeBase":"Time.time","maxRetainedSessions":20}`,
		"level.json": `{"dataType":"LevelData","generation":{"source":"session","planId":"ch3","masterSeed":42}}`,
		"playermovement.jsonl": `{"t":1.2500,"frame":76,"run":1,"ev":"room_enter","pos":{"x":5.000,"y":0.660},"roomId":"f0_room_1_0"}` + "\n" +
			`{"t":30.0833,"frame":1767,"run":1,"ev":"died","damageType":"Physical","hit0Amount":49.032,"hit0Src":"quick_sand_worm",` +
			`"hit0SrcInstanceId":80,"hit0T":29.6000,"hit0Type":"Physical","hitDirection":{"x":0.000,"y":-1.000},"hitPosition":{"x":1.700,"y":8.500},` +
			`"killerId":"quick_sand_worm","killerInstanceId":80,"killerKind":"EnemyMelee","killerTypeId":"quick_sand_worm","liveEnemies":7,` +
			`"pos":{"x":1.703,"y":8.090},"roomId":"f0_room_1_0"}` + "\n",
		"encounter.jsonl": `{"t":1.2500,"frame":76,"run":1,"ev":"room_ready","folder":"room01_f0_room_1_0","isCave":false,"roomId":"f0_room_1_0"}` + "\n",
		"runstart.jsonl":  `{"t":0.5000,"frame":31,"run":1,"ev":"run"}` + "\n",
		"room01_f0_room_1_0/quicksandworm_80-wave1_event.jsonl": `{"t":2.0000,"frame":120,"run":1,"ev":"spawn","instanceId":80}` + "\n" +
			`{"t":29.6000,"frame":1740,"run":1,"ev":"attack","instanceId":80}` + "\n",
	}
	if fill > 0 {
		var sb strings.Builder
		buf := make([]byte, 32)
		for sb.Len() < fill {
			rand.Read(buf)
			fmt.Fprintf(&sb, `{"t":%d.0000,"frame":%d,"run":1,"ev":"train","id":"%s"}`+"\n", sb.Len()%1000, sb.Len(), hex.EncodeToString(buf))
		}
		files["rail.jsonl"] = sb.String()
	}
	return files
}

func zipSession(t *testing.T, name string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/" + n, Method: zip.Deflate})
		must(t, err)
		w.Write([]byte(files[n]))
	}
	must(t, zw.Close())
	return buf.Bytes()
}

// upload runs the protocol, part n on replica n%2 (commit on the other one
// than create), as a device behind a load balancer would.
func (e *apm13Env) upload(t *testing.T, id, device, session string, data []byte, partBytes int) {
	t.Helper()
	key := map[string]string{"X-Apm-Upload-Key": apm13UploadKey, "Content-Type": "application/json"}
	body, _ := json.Marshal(map[string]interface{}{
		"upload_id": id, "device_id": device, "session_id": session, "recording_name": apm13Recording,
		"app_version": "1.4.0", "build": "140", "build_type": "dev", "platform": "Android", "device_model": "samsung SM-A336B",
		"os_version": "Android OS 16", "unity_version": "2022.3.62f3", "sdk_version": "2.1.0",
		"size_bytes": len(data), "sha256": hexSHA(data), "part_bytes": partBytes,
		"session_json": apm13Session(0)["session.json"],
	})
	w := serve(e.ingest[0], "POST", "/v1/session-files", body, key)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var st sessionfiles.UploadState
	must(t, json.Unmarshal(w.Body.Bytes(), &st))
	for n := 0; n < int(st.PartCount); n++ {
		end := (n + 1) * partBytes
		if end > len(data) {
			end = len(data)
		}
		part := data[n*partBytes : end]
		h := map[string]string{"X-Apm-Upload-Key": apm13UploadKey, "X-Apm-Device-Id": device, "X-Apm-Part-Sha256": hexSHA(part)}
		if w := serve(e.ingest[n%2], "PUT", "/v1/session-files/"+id+"/parts/"+strconv.Itoa(n), part, h); w.Code != 200 {
			t.Fatalf("part %d: %d %s", n, w.Code, w.Body)
		}
	}
	commit, _ := json.Marshal(map[string]interface{}{"device_id": device, "sha256": hexSHA(data), "size_bytes": len(data)})
	if w := serve(e.ingest[1], "POST", "/v1/session-files/"+id+"/commit", commit, key); w.Code != 200 {
		t.Fatalf("commit: %d %s", w.Code, w.Body)
	}
}

func unzipTo(t *testing.T, data []byte, dir string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	must(t, err)
	for _, f := range zr.File {
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			t.Fatalf("entry %s escapes", f.Name)
		}
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		rc, err := f.Open()
		must(t, err)
		b, err := io.ReadAll(rc)
		rc.Close()
		must(t, err)
		must(t, os.WriteFile(p, b, 0o644))
	}
}

func TestAPM13_UploadListDownload(t *testing.T) {
	e := setupAPM13(t)
	files := apm13Session(20 << 20) // > 8 MiB: S3 multipart
	data := zipSession(t, apm13Recording, files)
	id := "13131313131313131313131313131313"
	e.upload(t, id, apm13Device, "s-apm13", data, 1<<20)

	// The row, read back from ClickHouse.
	f, err := e.repo.GetSessionFile(context.Background(), id)
	must(t, err)
	if f == nil || f.Status != models.SessionFileComplete || f.GitCommit != apm13Commit || f.FileCount != uint32(len(files)) ||
		f.SizeBytes != uint64(len(data)) || f.SHA256 != hexSHA(data) || f.Scene != "Gameplay" || f.DataPackVersion != "0" ||
		f.BuildType != "dev" || f.AppBuild != "140" || f.ExpiresAt.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("row %+v", f)
	}
	if n := queryUint(t, e.client, "SELECT count() FROM apm_session_files"); n != 2 {
		t.Errorf("%d rows, want 2 (uploading, complete)", n)
	}
	if n := queryUint(t, e.client, "SELECT count() FROM apm_session_files FINAL WHERE status = 'complete'"); n != 1 {
		t.Errorf("FINAL: %d complete rows", n)
	}
	// Parts gone, one archive.
	objs, err := e.s3.ListAll(context.Background(), e.cfg.S3.Prefix, 1000)
	must(t, err)
	if len(objs) != 1 || !strings.HasSuffix(objs[0].Key, "/"+id+".zip") || objs[0].Size != int64(len(data)) {
		t.Fatalf("objects %+v", objs)
	}

	// List by git commit (prefix) on the other replica.
	auth := map[string]string{"Authorization": "Bearer " + apm13Token}
	w := serve(e.admin[1], "GET", "/api/session-files?git_commit=6116e2e&build_type=dev", nil, auth)
	var list struct {
		Files []models.SessionFile `json:"session_files"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &list))
	if w.Code != 200 || len(list.Files) != 1 || list.Files[0].UploadID != id {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	for _, q := range []string{"git_commit=0000000", "build=141", "device_id=other",
		"start_time=" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339)} {
		w := serve(e.admin[0], "GET", "/api/session-files?"+q, nil, auth)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"count":0`) {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	w = serve(e.admin[0], "GET", "/api/session-files/available?session_id=s-apm13", nil, nil)
	if !strings.Contains(w.Body.String(), id) {
		t.Errorf("available: %s", w.Body)
	}

	// A link from one replica, the download from the other.
	w = serve(e.admin[0], "POST", "/api/session-files/link", []byte(`{"upload_id":"`+id+`"}`), auth)
	var link struct {
		URL string `json:"url"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &link))
	w = serve(e.admin[1], "GET", link.URL, nil, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("download: %d, %d bytes of %d", w.Code, w.Body.Len(), len(data))
	}

	// Unzipped: exactly SessionFolder/<session>/.
	root := t.TempDir()
	unzipTo(t, w.Body.Bytes(), root)
	dir := filepath.Join(root, apm13Recording)
	for name, content := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || string(got) != content {
			t.Errorf("%s: %v", name, err)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("the archive has %d top-level entries", len(entries))
	}
	if script := os.Getenv("OZX_AI_TRACE"); script != "" {
		out, err := exec.Command("python3", "-I", script, dir, "--json").Output()
		if err != nil {
			t.Fatalf("ai_trace.py: %v", err)
		}
		var report struct {
			Overview struct {
				PlayerDeaths []map[string]interface{} `json:"playerDeaths"`
			} `json:"overview"`
			Anomalies []interface{} `json:"anomalies"`
		}
		must(t, json.Unmarshal(out, &report))
		if d := report.Overview.PlayerDeaths; len(d) != 1 || d[0]["killerId"] != "quick_sand_worm" || len(report.Anomalies) != 0 {
			t.Errorf("ai_trace.py: %s", out)
		}
	}
}

func TestAPM13_ResumeExpiryAndPrivacy(t *testing.T) {
	e := setupAPM13(t)
	ctx := context.Background()
	small := zipSession(t, apm13Recording, apm13Session(0))
	key := map[string]string{"X-Apm-Upload-Key": apm13UploadKey, "Content-Type": "application/json"}

	// Resume: half the parts, a "restart" (create again) answers what arrived.
	data := zipSession(t, apm13Recording, apm13Session(400<<10))
	id := "21212121212121212121212121212121"
	body, _ := json.Marshal(map[string]interface{}{"upload_id": id, "device_id": apm13Device, "session_id": "s-1",
		"recording_name": apm13Recording, "build_type": "dev", "size_bytes": len(data), "sha256": hexSHA(data), "part_bytes": 65536})
	must(t, statusErr(serve(e.ingest[0], "POST", "/v1/session-files", body, key)))
	parts := (len(data) + 65535) / 65536
	for n := 0; n < parts; n += 2 {
		end := (n + 1) * 65536
		if end > len(data) {
			end = len(data)
		}
		h := map[string]string{"X-Apm-Upload-Key": apm13UploadKey, "X-Apm-Device-Id": apm13Device, "X-Apm-Part-Sha256": hexSHA(data[n*65536 : end])}
		must(t, statusErr(serve(e.ingest[1], "PUT", "/v1/session-files/"+id+"/parts/"+strconv.Itoa(n), data[n*65536:end], h)))
	}
	w := serve(e.ingest[1], "POST", "/v1/session-files", body, key)
	var st sessionfiles.UploadState
	must(t, json.Unmarshal(w.Body.Bytes(), &st))
	if len(st.Received) != (parts+1)/2 || st.Received[1] != 2 {
		t.Fatalf("resume: %s", w.Body)
	}

	// Recordings of two devices; one in a session a player played (ops_events).
	e.upload(t, "31313131313131313131313131313131", apm13Device, "s-dev", small, 65536)
	e.upload(t, "41414141414141414141414141414141", "other-device", "s-player", small, 65536)
	must(t, e.client.Exec(ctx, "INSERT INTO ops_events (event_id, name, day, event_time, client_time, server_time, player_id, session_id) VALUES ('o1', 'x', today(), now64(3), now64(3), now64(3), 'player-13', 's-player')"))

	// Deletion by device: its objects (the archive, the parts) and rows go.
	pw := serve(e.admin[0], "POST", "/api/privacy/delete", []byte(`{"id_type":"device_id","id":"`+apm13Device+`"}`),
		map[string]string{"Authorization": "Bearer " + privacyTestToken, "X-Apm-Confirm-Delete": apm13Device})
	var d models.PrivacyDeletion
	must(t, json.Unmarshal(pw.Body.Bytes(), &d))
	if pw.Code != 200 || d.Status != "done" || d.SessionFiles == nil || d.SessionFiles.UploadsBefore != 2 || d.SessionFiles.UploadsAfter != 0 {
		t.Fatalf("delete device: %d %s", pw.Code, pw.Body)
	}
	if n := queryUint(t, e.client, "SELECT count() FROM apm_session_files WHERE device_id = ?", apm13Device); n != 0 {
		t.Errorf("%d rows of the device left", n)
	}
	objs, _ := e.s3.ListAll(ctx, e.cfg.S3.Prefix, 1000)
	if len(objs) != 1 || !strings.Contains(objs[0].Key, "41414141414141414141414141414141") {
		t.Fatalf("objects after deleting the device: %+v", objs)
	}
	// Deletion by player: the recording of the player's session.
	pw = serve(e.admin[1], "POST", "/api/privacy/delete", []byte(`{"id_type":"player_id","id":"player-13"}`),
		map[string]string{"Authorization": "Bearer " + privacyTestToken, "X-Apm-Confirm-Delete": "player-13"})
	must(t, json.Unmarshal(pw.Body.Bytes(), &d))
	if pw.Code != 200 || d.Status != "done" || d.SessionFiles == nil || d.SessionFiles.UploadsBefore != 1 {
		t.Fatalf("delete player: %d %s", pw.Code, pw.Body)
	}
	if objs, _ := e.s3.ListAll(ctx, e.cfg.S3.Prefix, 1000); len(objs) != 0 {
		t.Fatalf("objects left: %+v", objs)
	}
	if n := queryUint(t, e.client, "SELECT count() FROM apm_session_files"); n != 0 {
		t.Errorf("%d rows left", n)
	}

	// Expiry: a recording 31 days later, and a run on each replica.
	e.upload(t, "51515151515151515151515151515151", apm13Device, "s-x", small, 65536)
	later := time.Now().Add(31 * 24 * time.Hour)
	for _, svc := range e.services {
		svc.SetClock(func() time.Time { return later })
	}
	n, err := e.services[0].ExpireOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if n, err := e.services[1].ExpireOnce(ctx); err != nil || n != 0 {
		t.Fatalf("second replica: %d %v", n, err)
	}
	if objs, _ := e.s3.ListAll(ctx, e.cfg.S3.Prefix, 1000); len(objs) != 0 {
		t.Fatalf("objects after expiry: %+v", objs)
	}
	if n := queryUint(t, e.client, "SELECT count() FROM apm_session_files"); n != 0 {
		t.Errorf("%d rows after expiry", n)
	}
}

func statusErr(w *httptest.ResponseRecorder) error {
	if w.Code != 200 {
		return fmt.Errorf("HTTP %d: %s", w.Code, w.Body)
	}
	return nil
}

// ClickHouse only (no S3): the index's queries on every version.
func TestAPM13_IndexQueries(t *testing.T) {
	admin := adminClient(t)
	db := fmt.Sprintf("ozx_apm_apm13ix_%d", time.Now().UnixNano()%1e9)
	createDB(t, admin, db)
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = db
	client, err := testutil.SetupTestDatabase(cfg)
	must(t, err)
	defer client.Close()
	repo := storage.NewRepository(client, zap.NewNop())
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := func(id, status, device, session, commit string, created time.Time, writeAt time.Time) models.SessionFile {
		return models.SessionFile{UploadID: id, Status: status, Version: models.SessionFileVersion(status, writeAt),
			CreatedAt: created, UpdatedAt: writeAt, ExpiresAt: created.Add(30 * 24 * time.Hour).Truncate(time.Second),
			DeviceID: device, SessionID: session, RecordingName: "r", BuildType: "dev", AppBuild: "7", GitCommit: commit, SizeBytes: 100}
	}
	a := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	b := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	c := "cccccccccccccccccccccccccccccccc"
	must(t, repo.InsertSessionFile(ctx, row(a, "uploading", "d1", "s1", "ABCDEF12", now.Add(-time.Hour), now.Add(-time.Hour))))
	must(t, repo.InsertSessionFile(ctx, row(a, "complete", "d1", "s1", "ABCDEF12", now.Add(-time.Hour), now.Add(-30*time.Minute))))
	// A late "uploading" row of a replica with a clock ahead must not hide the completion.
	must(t, repo.InsertSessionFile(ctx, row(a, "uploading", "d1", "s1", "ABCDEF12", now.Add(-time.Hour), now.Add(time.Hour))))
	must(t, repo.InsertSessionFile(ctx, row(b, "uploading", "d1", "s2", "abcdef99", now.Add(-10*24*time.Hour), now.Add(-10*24*time.Hour))))
	must(t, repo.InsertSessionFile(ctx, row(c, "complete", "d2", "s3", "1234567", now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour))))

	f, err := repo.GetSessionFile(ctx, a)
	if err != nil || f == nil || f.Status != "complete" {
		t.Fatalf("get: %+v %v", f, err)
	}
	list, err := repo.ListSessionFiles(ctx, models.SessionFileFilter{GitCommit: "abcdef"})
	if err != nil || len(list) != 1 || list[0].UploadID != a {
		t.Fatalf("list by commit (case-insensitive prefix, complete only): %+v %v", list, err)
	}
	list, err = repo.ListSessionFiles(ctx, models.SessionFileFilter{Status: "all", DeviceID: "d1", AppBuild: "7",
		StartTime: now.Add(-11 * 24 * time.Hour), EndTime: now})
	if err != nil || len(list) != 2 || list[0].UploadID != a || list[1].UploadID != b {
		t.Fatalf("list all of d1: %+v %v", list, err)
	}
	list, err = repo.ListSessionFiles(ctx, models.SessionFileFilter{SessionIDs: []string{"s3", "s9"}})
	if err != nil || len(list) != 1 || list[0].UploadID != c {
		t.Fatalf("by session: %+v %v", list, err)
	}
	uploads, bytes, open, err := repo.SessionFileDeviceUsage(ctx, "d1", now.Add(-24*time.Hour))
	if err != nil || uploads != 1 || bytes != 100 || open != 0 {
		t.Fatalf("usage %d %d %d %v", uploads, bytes, open, err)
	}
	exp, err := repo.SessionFilesToExpire(ctx, now, now.Add(-7*24*time.Hour), 10)
	if err != nil || len(exp) != 2 || exp[0].UploadID != c || exp[1].UploadID != b {
		t.Fatalf("to expire: %+v %v", exp, err)
	}
	av, err := repo.SessionFileAvailability(ctx, []string{"s1", "s2"})
	if err != nil || len(av) != 1 || av[0].UploadID != a {
		t.Fatalf("availability %+v %v", av, err)
	}
	must(t, repo.DeleteSessionFileRows(ctx, []string{b, c}))
	if n := queryUint(t, client, "SELECT count() FROM apm_session_files WHERE upload_id IN ('"+b+"','"+c+"')"); n != 0 {
		t.Errorf("deleted rows: %d left", n)
	}
	// The table is found by privacy deletion's column scan.
	tables, err := repo.PrivacyTables(ctx, "device_id")
	must(t, err)
	found := false
	for _, tb := range tables {
		found = found || tb == storage.SessionFilesTable
	}
	if !found {
		t.Errorf("apm_session_files not among %v", tables)
	}
}

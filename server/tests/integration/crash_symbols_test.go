package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/internal/symbols"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-7 end to end: symbols of a library (internal/symbols/testdata, built
// like IL2CPP's output) are uploaded through the admin API in parts, and
// native crashes in the SDK's wire format go through ingest into ClickHouse
// and are read back symbolicated: C# method, file and line, also for a
// crash stored before the upload.

const symbolsToken = "test-symbols-token"

type crashEnv struct {
	*apm4Env
	repo *storage.Repository
	lib  *symbols.Library
	cfg  *config.ClickHouseConfig
}

func setupCrashEnv(t *testing.T, async bool) *crashEnv {
	t.Helper()
	e := setupAPM4(t, async)
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = currentDB(t, e.client)
	cfg.AsyncInsert = async
	repo := storage.NewRepository(e.client, zap.NewNop())
	e.admin = api.NewAdminRouter(&config.Config{AdminServer: config.AdminServerConfig{SymbolsToken: symbolsToken}}, repo, zap.NewNop())
	return &crashEnv{apm4Env: e, repo: repo, lib: readDemoLibrary(t), cfg: cfg}
}

func currentDB(t *testing.T, c *storage.ClickHouseClient) string {
	t.Helper()
	var db string
	must(t, c.Conn().QueryRow(context.Background(), "SELECT currentDatabase()").Scan(&db))
	return db
}

func readDemoLibrary(t *testing.T) *symbols.Library {
	t.Helper()
	const dir = "../../internal/symbols/testdata/"
	mf, err := os.Open(dir + "MethodMap.tsv")
	must(t, err)
	defer mf.Close()
	mm, err := symbols.ReadMethodMap(mf)
	must(t, err)
	lf, err := os.Open(dir + "LineNumberMappings.json")
	must(t, err)
	defer lf.Close()
	lm, err := symbols.ReadLineMap(lf)
	must(t, err)
	f, err := os.Open(dir + "libdemo-arm64.elf")
	must(t, err)
	defer f.Close()
	lib, err := symbols.ReadLibrary("libil2cpp.dbg.so", f, mm, lm)
	must(t, err)
	return lib
}

// lineAddr is the address where the library's C# line l of file starts.
func (e *crashEnv) lineAddr(t *testing.T, file string, l uint32) uint64 {
	t.Helper()
	for _, x := range e.lib.Lines {
		if strings.HasSuffix(x.File, file) && x.Line == l {
			return x.Addr
		}
	}
	t.Fatalf("no line %s:%d", file, l)
	return 0
}

func (e *crashEnv) upload(t *testing.T, uploadID string, partRows int, commit bool) int {
	t.Helper()
	post := func(path string, body []byte, gz bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+symbolsToken)
		if gz {
			req.Header.Set("Content-Encoding", "gzip")
		}
		w := httptest.NewRecorder()
		e.admin.ServeHTTP(w, req)
		return w
	}
	fs, ls := e.lib.Funcs, e.lib.Lines
	for part := 0; len(fs) > 0 || len(ls) > 0; part++ {
		nf := min(partRows, len(fs))
		nl := min(partRows-nf, len(ls))
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		must(t, json.NewEncoder(zw).Encode(symbols.Part{BuildID: e.lib.BuildID, UploadID: uploadID, Part: part,
			Funcs: symbols.EncodeFuncs(fs[:nf]), Lines: symbols.EncodeLines(ls[:nl])}))
		must(t, zw.Close())
		if w := post("/api/symbols/part", buf.Bytes(), true); w.Code != http.StatusOK {
			t.Fatalf("part %d: %d %s", part, w.Code, w.Body)
		}
		fs, ls = fs[nf:], ls[nl:]
	}
	if !commit {
		return 0
	}
	body, _ := json.Marshal(symbols.Commit{BuildID: e.lib.BuildID, UploadID: uploadID, Module: e.lib.Module, Arch: e.lib.Arch,
		AppVersion: "2.1", AppBuild: "42", Functions: uint64(len(e.lib.Funcs)), Lines: uint64(len(e.lib.Lines)),
		LineMap: true, MethodMap: true, Source: "libil2cpp.dbg.so", Tool: "test"})
	return post("/api/symbols/commit", body, false).Code
}

// crash is a native crash of the demo library as the SDK sends it.
func (e *crashEnv) crash(session string, buildID string, pcs []uint64, extra map[string]interface{}) string {
	var stack strings.Builder
	stack.WriteString("Thread \"UnityMain\" tid=4242 crashed\n")
	for i, pc := range pcs {
		bid := ""
		if buildID != "" {
			bid = " (BuildId: " + buildID + ")"
		}
		fmt.Fprintf(&stack, "#%02d pc %016x libil2cpp.so%s\n", i, pc, bid)
	}
	stack.WriteString("#09 pc 00000000000c8d2c /apex/com.android.runtime/lib64/bionic/libc.so (__pthread_start(void*)+208)\n")
	f := map[string]interface{}{
		"type": "crash", "crash_type": "native", "fingerprint": "sdk-fp", "stack": stack.String(),
		"breadcrumbs": []string{"[10:00:00.000] Scene: Dungeon", "[10:00:01.000] Event: room_enter roomId=r-12", "[10:00:01.200] Warning: <img src=x onerror=alert(1)>"},
		"exit_reason": "CRASH_NATIVE", "exit_status": 11, "importance": 100, "pss_kb": 400000,
		"signal": "SIGSEGV", "signal_code": "SEGV_MAPERR", "fault_addr": "0x0000000000000008", "abi": "arm64-v8a",
		"session_ms": 34000, "foreground": true, "details": map[string]string{"fps": "58.9", "detected_by": "exit_info"},
	}
	for k, v := range extra {
		f[k] = v
	}
	return e.event(session, "r-12", f)
}

func TestCrashSymbols_UploadThenCrashesShowCSharpLines(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupCrashEnv(t, async)
			load71 := e.lineAddr(t, "RoomLoader.cs", 71)   // the call into Helper.Twice
			start500 := e.lineAddr(t, "ApmClient.cs", 500) // the lambda calling LoadTemplate
			helper12 := e.lineAddr(t, "Helper.cs", 12)
			// The top frame faulted in Helper.Twice; the callers' pcs are return addresses.
			pcs := []uint64{helper12, load71 + 4, start500 + 4}

			// A crash stored before the symbols exist: grouped by address.
			e.post(t, e.crash("S1", e.lib.BuildID, pcs, nil))

			uploadID := "0123456789abcdef0123456789abcdef"
			// An incomplete upload is refused and never used.
			if code := e.upload(t, "ffffffffffffffffffffffffffffffff", 3, false); code != 0 {
				t.Fatal(code)
			}
			body, _ := json.Marshal(symbols.Commit{BuildID: e.lib.BuildID, UploadID: "ffffffffffffffffffffffffffffffff", Module: "libil2cpp.so",
				Arch: "arm64", Functions: uint64(len(e.lib.Funcs)) + 5, Lines: uint64(len(e.lib.Lines))})
			req := httptest.NewRequest(http.MethodPost, "/api/symbols/commit", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+symbolsToken)
			w := httptest.NewRecorder()
			e.admin.ServeHTTP(w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("incomplete commit: %d %s", w.Code, w.Body)
			}
			// The real upload in small parts, one sent twice.
			if code := e.upload(t, uploadID, 3, true); code != http.StatusOK {
				t.Fatalf("commit: %d", code)
			}
			if code := e.upload(t, uploadID, 2, true); code != http.StatusOK {
				t.Fatalf("commit again: %d", code)
			}
			var list struct {
				Uploads []symbols.UploadRef `json:"uploads"`
			}
			getAdmin(t, e.admin, "/api/symbols", withParams(nil, "build_id", e.lib.BuildID), &list)
			if len(list.Uploads) != 1 || list.Uploads[0].UploadID != uploadID || list.Uploads[0].Functions != uint64(len(e.lib.Funcs)) ||
				!list.Uploads[0].LineMap || list.Uploads[0].AppVersion != "2.1" {
				t.Fatalf("uploads %+v", list.Uploads)
			}

			// The ingest symbolizer remembers for a minute that this build had
			// no symbols (UploadTTL): a new ingest router stands for a server
			// a minute later (or another replica).
			e.ingest = api.NewRouter(&config.Config{ClickHouse: *e.cfg}, e.repo, zap.NewNop())

			// Crashes stored after the upload: grouped by C# method, across a
			// build without symbols for the same code.
			e.post(t, e.crash("S2", e.lib.BuildID, pcs, nil), e.crash("S3", e.lib.BuildID, pcs, nil))

			var crashes models.CrashListResponse
			getAdmin(t, e.admin, "/api/crashes", e.params, &crashes)
			if len(crashes.Crashes) != 2 {
				t.Fatalf("groups %+v", crashes.Crashes)
			}
			var byName, byAddr models.CrashGroup
			for _, g := range crashes.Crashes {
				if strings.Contains(g.CrashSite, "!") {
					byName = g
				} else {
					byAddr = g
				}
			}
			if byName.Count != 2 || byAddr.Count != 1 || byName.CrashSite != "libil2cpp.so!Helper.Twice" {
				t.Fatalf("by name %+v / by address %+v", byName, byAddr)
			}
			// Both show the top frame symbolicated now, with the C# line.
			for _, g := range []models.CrashGroup{byName, byAddr} {
				if g.TopFrame != "Helper.Twice (Assets/Scripts/Util/Helper.cs:12)" || !g.Symbolicated || g.Signal != "SIGSEGV" || g.ExitReason != "CRASH_NATIVE" || g.Foreground != g.Count {
					t.Errorf("group %+v", g)
				}
			}
			if !strings.HasPrefix(byName.SampleMessage, "SIGSEGV (SEGV_MAPERR) at 0x0000000000000008 in Helper.Twice (libil2cpp.so)") {
				t.Errorf("summary %q", byName.SampleMessage)
			}

			var d models.CrashDetail
			getAdmin(t, e.admin, "/api/crashes/detail", withParams(e.params, "fingerprint", byAddr.Fingerprint), &d)
			if d.Sample == nil || len(d.Sample.Threads) != 1 || len(d.Symbols) != 1 || !d.Symbols[0].Found || d.Symbols[0].UploadID != uploadID {
				t.Fatalf("detail %+v", d)
			}
			fr := d.Sample.Threads[0].Frames
			want := []struct {
				fn   string
				file string
				line int
			}{
				{"Helper.Twice", "Assets/Scripts/Util/Helper.cs", 12},
				{"Game.Rooms.RoomLoader.LoadTemplate(System.Object,System.Int32)", "Assets/Scripts/Rooms/RoomLoader.cs", 71},
				{"OzxApm.Core.ApmClient+<>c.<Start>b__5_0()", "Library/PackageCache/com.ozx.apm@2.0.0/Runtime/Core/ApmClient.cs", 500},
			}
			for i, w := range want {
				if fr[i].Function != w.fn || fr[i].File != w.file || fr[i].Line != w.line || !fr[i].Symbolicated || fr[i].SymbolsBy != "build_id" {
					t.Errorf("frame %d: %+v, want %+v", i, fr[i], w)
				}
			}
			if fr[3].Symbolicated || fr[3].InApp || fr[3].Function != "__pthread_start(void*)" {
				t.Errorf("system frame %+v", fr[3])
			}
			s := d.Sample
			if s.SessionID != "S1" || s.DeviceID != "d-S1" || s.Context["roomId"] != "r-12" || s.Details["fps"] != "58.9" ||
				s.PssKB != 400000 || !s.Foreground || s.ABI != "arm64-v8a" || s.SessionMs != 34000 || len(s.Breadcrumbs) != 3 ||
				s.Breadcrumbs[2] != "[10:00:01.200] Warning: <img src=x onerror=alert(1)>" || d.FingerprintVersion != 1 {
				t.Errorf("sample %+v", s)
			}
			if len(d.Occurrences) != 1 || d.Occurrences[0].Stack != "" || d.Occurrences[0].Summary == "" {
				t.Errorf("occurrences %+v", d.Occurrences)
			}

			// The session time line shows the crash's summary.
			tl, err := e.repo.GetSessionTimeline(context.Background(), storage.SessionQuery{SessionID: "S2", Start: time.Now().Add(-2 * time.Hour), End: time.Now()})
			must(t, err)
			found := false
			for _, it := range tl.Items {
				if it.Type == models.TimelineCrash {
					found = true
					if !strings.HasPrefix(it.Message, "SIGSEGV (SEGV_MAPERR)") || it.CrashType != "native" {
						t.Errorf("time line crash %+v", it)
					}
				}
			}
			if !found {
				t.Error("no crash on the time line")
			}

			// Without a build id in the frames: the upload of the version and
			// ABI names them, so the crash joins the group by name.
			e.post(t, e.crash("S4", "", pcs, nil))
			getAdmin(t, e.admin, "/api/crashes", e.params, &crashes)
			if len(crashes.Crashes) != 2 || crashes.Crashes[0].Count != 3 || crashes.Crashes[0].CrashSite != "libil2cpp.so!Helper.Twice" {
				t.Errorf("version fallback: %+v", crashes.Crashes)
			}
			// Another ABI has no upload: unnamed, it joins the group of the
			// crash stored before the upload (unnamed frames are keyed by
			// library, never by address: no new issue per build, APM-8).
			e.post(t, e.crash("S5", "", pcs, map[string]interface{}{"abi": "armeabi-v7a"}))
			getAdmin(t, e.admin, "/api/crashes", e.params, &crashes)
			if len(crashes.Crashes) != 2 || crashes.Crashes[1].Count != 2 || crashes.Crashes[1].Fingerprint != byAddr.Fingerprint {
				t.Errorf("other ABI: %+v", crashes.Crashes)
			}

			// Symbols hold no device data: privacy deletion never touches them.
			tables, err := e.repo.PrivacyTables(context.Background(), "device_id")
			must(t, err)
			for _, tb := range tables {
				if strings.HasPrefix(tb, "apm_symbol") {
					t.Errorf("privacy deletion would touch %s", tb)
				}
			}
		})
	}
}

// Many frames (more than one lookup batch) of a big stack, and addresses
// outside every function, resolve through the real queries.
func TestCrashSymbols_LookupManyAddresses(t *testing.T) {
	e := setupCrashEnv(t, false)
	if code := e.upload(t, "abcdefabcdefabcdefabcdefabcdef12", 1000, true); code != http.StatusOK {
		t.Fatal(code)
	}
	s := symbols.NewSymbolizer(e.repo, 1000)
	var frames []symbols.Frame
	for i := 0; i < 300; i++ {
		f := e.lib.Funcs[i%len(e.lib.Funcs)]
		frames = append(frames, symbols.Frame{Module: "libil2cpp.so", BuildID: e.lib.BuildID, PC: f.Addr + uint64(i%int(f.Size))})
	}
	frames = append(frames, symbols.Frame{Module: "libil2cpp.so", BuildID: e.lib.BuildID, PC: 0x10})
	got, err := s.Resolve(context.Background(), frames, symbols.Fallback{})
	must(t, err)
	if len(got) != 300 {
		t.Fatalf("resolved %d of 300", len(got))
	}
	for i := 0; i < 300; i++ {
		if want := e.lib.Funcs[i%len(e.lib.Funcs)].Name; got[i].Name != want {
			t.Fatalf("frame %d: %q, want %q", i, got[i].Name, want)
		}
	}
}

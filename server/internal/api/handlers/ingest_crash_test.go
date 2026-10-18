package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

// symbolEventStore is the fake event store with uploaded symbols.
type symbolEventStore struct {
	fakeEventStore
	uploads map[string]symbols.UploadRef
	funcs   map[uint64]symbols.Func // by address, of every upload
	fail    error
}

func (s *symbolEventStore) LatestSymbolUpload(_ context.Context, buildID string) (symbols.UploadRef, bool, error) {
	if s.fail != nil {
		return symbols.UploadRef{}, false, s.fail
	}
	r, ok := s.uploads[buildID]
	return r, ok, nil
}

func (s *symbolEventStore) SymbolUploadsForVersion(context.Context, string, string) ([]symbols.UploadRef, error) {
	return nil, nil
}

func (s *symbolEventStore) LookupSymbols(_ context.Context, _ symbols.UploadRef, addrs []uint64) (map[uint64]symbols.Hit, error) {
	out := map[uint64]symbols.Hit{}
	for _, a := range addrs {
		var best symbols.Func
		for _, f := range s.funcs {
			if f.Addr <= a && f.Addr >= best.Addr {
				best = f
			}
		}
		if best.Name != "" {
			out[a] = symbols.Hit{Func: best}
		}
	}
	return out, nil
}

func nativeCrashJSON(eventID string, buildID string, pcs ...string) string {
	var stack strings.Builder
	stack.WriteString("Thread \"UnityMain\" tid=4242 crashed\n")
	for i, pc := range pcs {
		stack.WriteString("#0" + string(rune('0'+i)) + " pc " + pc + " libil2cpp.so (BuildId: " + buildID + ")\n")
	}
	stack.WriteString("#09 pc 00000000000c8d2c /apex/com.android.runtime/lib64/bionic/libc.so (__pthread_start(void*)+208) (BuildId: 9c7a)\n")
	fields := map[string]interface{}{
		"crash_type": "native", "fingerprint": "sdk-" + eventID, "stack": stack.String(),
		"breadcrumbs": []string{"[10:00:00.000] Scene: Dungeon", "[10:00:01.000] Event: room_enter roomId=r-12"},
		"exit_reason": "CRASH_NATIVE", "exit_status": 11, "exit_description": "crash", "importance": 100,
		"pss_kb": 512000, "rss_kb": 600000, "signal": "SIGSEGV", "signal_code": "SEGV_MAPERR",
		"fault_addr": "0x0000000000000008", "abi": "arm64-v8a", "session_ms": 34000, "foreground": true,
		"details": map[string]interface{}{"fps": "58.9", "mem_mb": "812", "bad key!": "x"},
		// Computed by the server; a client cannot set them.
		"client_fingerprint": "forged", "summary": "forged", "crash_site": "forged", "fingerprint_version": 9,
	}
	b, _ := json.Marshal(fields)
	return sdkEventJSON("crash", eventID, 1, ","+string(b[1:len(b)-1]))
}

func TestIngest_NativeCrashFingerprintUsesUploadedSymbols(t *testing.T) {
	store := &symbolEventStore{
		uploads: map[string]symbols.UploadRef{
			"aaaaaaaa": {BuildID: "aaaaaaaa", UploadID: strings.Repeat("1", 32), Module: "libil2cpp.so"},
			"bbbbbbbb": {BuildID: "bbbbbbbb", UploadID: strings.Repeat("2", 32), Module: "libil2cpp.so"},
		},
		funcs: map[uint64]symbols.Func{
			0x1000: {Addr: 0x1000, Size: 0x100, Name: "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)"},
			0x2000: {Addr: 0x2000, Size: 0x100, Name: "Game.Rooms.RoomBuilder.Build()"},
			// The same methods at other addresses in the second build.
			0x5000: {Addr: 0x5000, Size: 0x100, Name: "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)"},
			0x6000: {Addr: 0x6000, Size: 0x100, Name: "Game.Rooms.RoomBuilder.Build()"},
		},
	}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(
		nativeCrashJSON("c1", "AAAAAAAA", "0000000000001010", "0000000000002010"),
		nativeCrashJSON("c2", "bbbbbbbb", "0000000000005010", "0000000000006010"),
		nativeCrashJSON("c3", "cccccccc", "0000000000001010", "0000000000002010"), // no symbols
	), nil)
	if resp.Accepted != 3 || len(store.crashes) != 3 {
		t.Fatalf("response %+v", resp)
	}
	c1, c2, c3 := store.crashes[0], store.crashes[1], store.crashes[2]
	if c1.Fingerprint != c2.Fingerprint {
		t.Errorf("the same methods in two builds are two groups: %s %s", c1.Fingerprint, c2.Fingerprint)
	}
	if c3.Fingerprint == c1.Fingerprint || c3.CrashSite != "libil2cpp.so+0x1010" {
		t.Errorf("without symbols: by address: %+v", c3.CrashDetails)
	}
	if c1.CrashSite != "libil2cpp.so!Game.Rooms.RoomLoader.LoadTemplate" ||
		c1.Summary != "SIGSEGV (SEGV_MAPERR) at 0x0000000000000008 in Game.Rooms.RoomLoader.LoadTemplate (libil2cpp.so)" {
		t.Errorf("site %q summary %q", c1.CrashSite, c1.Summary)
	}
	if c1.ClientFingerprint != "sdk-c1" || c1.FingerprintVersion != fingerprint.Version {
		t.Errorf("client fingerprint %q version %d", c1.ClientFingerprint, c1.FingerprintVersion)
	}
	if c1.ExitReason != "CRASH_NATIVE" || c1.ExitStatus != 11 || c1.PssKB != 512000 || !bool(c1.Foreground) ||
		c1.ABI != "arm64-v8a" || c1.SessionMs != 34000 || c1.Importance != 100 {
		t.Errorf("exit info: %+v", c1.CrashDetails)
	}
	if len(c1.Details) != 2 || c1.Details["fps"] != "58.9" || len(c1.Breadcrumbs) != 2 || c1.Context == nil {
		t.Errorf("details %v breadcrumbs %v", c1.Details, c1.Breadcrumbs)
	}
	if len(resp.Errors) != 1 || !strings.Contains(resp.Errors[0], "crash details: 3 entries dropped") { // one per event
		t.Errorf("errors %v", resp.Errors)
	}
}

func TestIngest_CrashSymbolLookupFailureStillStores(t *testing.T) {
	store := &symbolEventStore{fail: errors.New("clickhouse down")}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(nativeCrashJSON("c1", "aaaaaaaa", "0000000000001010")), nil)
	if resp.Accepted != 1 || len(store.crashes) != 1 || store.crashes[0].CrashSite != "libil2cpp.so+0x1010" {
		t.Fatalf("response %+v crashes %+v", resp, store.crashes)
	}
	// A store without symbol tables (older test doubles): by address.
	plain := &fakeEventStore{}
	h = newIngestHandler(plain, zap.NewNop())
	if h.symbols != nil {
		t.Fatal("no symbolizer without a symbol store")
	}
	_, resp = postBatch(t, h, batchOf(nativeCrashJSON("c2", "aaaaaaaa", "0000000000001010")), nil)
	if resp.Accepted != 1 || plain.crashes[0].CrashSite != "libil2cpp.so+0x1010" {
		t.Fatalf("plain: %+v", plain.crashes)
	}
}

func TestIngest_KilledAndOOMCrashes(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(
		sdkEventJSON("crash", "k1", 1, `,"crash_type":"oom","fingerprint":"x","exit_reason":"LOW_MEMORY","importance":100,"pss_kb":1048576,"foreground":true`),
		sdkEventJSON("crash", "k2", 2, `,"crash_type":"oom","fingerprint":"y","exit_reason":"LOW_MEMORY","foreground":true,"exit_status":"nine","pss_kb":-5`),
		sdkEventJSON("crash", "k3", 3, `,"crash_type":"Weird Type","fingerprint":"z","exit_reason":"SIGNALED \n<b>"`),
		sdkEventJSON("crash", "k4", 4, `,"crash_type":"anr","fingerprint":"a","exit_description":"user request after error: Input dispatching timed out","stack":"Thread \"main\" tid=1\nat com.ozx.Foo.bar(Foo.java:3)\n"`),
	), nil)
	if resp.Accepted != 4 {
		t.Fatalf("response %+v", resp)
	}
	oom1, oom2, weird, anr := store.crashes[0], store.crashes[1], store.crashes[2], store.crashes[3]
	if oom1.Fingerprint != oom2.Fingerprint || oom1.Summary != "Killed for low memory in the foreground (PSS 1024 MB)" {
		t.Errorf("oom: %q %q / %q", oom1.Fingerprint, oom2.Fingerprint, oom1.Summary)
	}
	if oom2.ExitStatus != 0 || oom2.PssKB != 0 {
		t.Errorf("lenient numbers: %+v", oom2.CrashDetails)
	}
	if weird.CrashType != "unknown" || weird.ExitReason != "SIGNALED<b>" {
		t.Errorf("weird: %q %q", weird.CrashType, weird.ExitReason)
	}
	if anr.Summary != "ANR: user request after error: Input dispatching timed out in com.ozx.Foo.bar" {
		t.Errorf("anr summary %q", anr.Summary)
	}
}

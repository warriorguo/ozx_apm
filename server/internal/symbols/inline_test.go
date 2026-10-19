package symbols

import (
	"bufio"
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The inline fixture (testdata/inline.cpp, the S1 device crash's shape):
// callers whose call is in inlined code, and a function that starts with
// inlined code.
func readInlineFixture(t *testing.T) *Library {
	t.Helper()
	g, err := os.Open("testdata/LineNumberMappings-inline.json")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	lm, err := ReadLineMap(bufio.NewReader(g))
	if err != nil {
		t.Fatal(err)
	}
	return readTestLibrary(t, "testdata/libinline-arm64.elf", "libil2cpp.so", nil, lm)
}

func funcByRaw(t *testing.T, lib *Library, raw string) Func {
	t.Helper()
	for _, f := range lib.Funcs {
		if f.RawName == raw {
			return f
		}
	}
	t.Fatalf("no function %s", raw)
	return Func{}
}

// callReturnAddresses returns, for each BL/BLR in an arm64 function, the
// address after it (what a caller frame of a native stack holds).
func callReturnAddresses(t *testing.T, path string, fn Func) []uint64 {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	text := f.Section(".text")
	data, err := text.Data()
	if err != nil {
		t.Fatal(err)
	}
	var out []uint64
	for a := fn.Addr; a+4 <= fn.Addr+fn.Size; a += 4 {
		w := binary.LittleEndian.Uint32(data[a-text.Addr:])
		if w&0xFC000000 == 0x94000000 || w&0xFFFFFC1F == 0xD63F0000 {
			out = append(out, a+4)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no call in %s", fn.RawName)
	}
	return out
}

func resolveOne(t *testing.T, lib *Library, pc uint64, ret bool) Resolved {
	t.Helper()
	s := NewSymbolizer(LibraryStore{Lib: lib}, 64)
	got, err := s.Resolve(context.Background(), []Frame{{Module: lib.Module, BuildID: lib.BuildID, PC: pc, ReturnAddress: ret}}, Fallback{})
	if err != nil {
		t.Fatal(err)
	}
	return got[0]
}

func TestInlinedCall_CallerGetsItsOwnCallSite(t *testing.T) {
	lib := readInlineFixture(t)
	if lib.InlineRanges == 0 || lib.InlinedRows == 0 {
		t.Fatalf("no inlined calls read: %d ranges, %d rows", lib.InlineRanges, lib.InlinedRows)
	}
	update := funcByRaw(t, lib, "ApmTestDriver_UpdateApm7_mAC92D61E")
	// S1: `a?.Invoke()` is ApmTestDriver.cs:1088; the inlined Invoke's
	// body lies under SessionManager.get_SessionId's comment.
	for _, ret := range callReturnAddresses(t, "testdata/libinline-arm64.elf", update) {
		r := resolveOne(t, lib, ret, true)
		if r.Name != "ApmTestDriver.UpdateApm7" || r.File != "Assets/Scripts/ApmTestDriver.cs" || r.Line != 1088 {
			t.Errorf("caller at 0x%x: %+v, want ApmTestDriver.UpdateApm7 at ApmTestDriver.cs:1088", ret, r)
		}
	}
	// No address of UpdateApm7 resolves to another function's file.
	for a := update.Addr; a < update.Addr+update.Size; a += 4 {
		if _, line, _ := lib.Lookup(a); line.File != "" && line.File != "Assets/Scripts/ApmTestDriver.cs" {
			t.Errorf("0x%x in UpdateApm7 resolves to %s:%d", a, line.File, line.Line)
		}
	}
	// The getter itself still has its own line.
	getter := funcByRaw(t, lib, "SessionManager_get_SessionId_m2E1B3C4D")
	if _, line, _ := lib.Lookup(getter.Addr); line.File != "SessionManager.cs" || line.Line != 30 {
		t.Errorf("SessionManager.get_SessionId: %+v", line)
	}

	// A NullCheck inlined after the prologue: the call into the runtime is line 558.
	complete := funcByRaw(t, lib, "OfflineStorage_CompleteUpload_m23B6F0BC")
	for _, ret := range callReturnAddresses(t, "testdata/libinline-arm64.elf", complete) {
		if r := resolveOne(t, lib, ret, true); r.File != "OfflineStorage.cs" || r.Line != 558 {
			t.Errorf("CompleteUpload caller at 0x%x: %+v, want OfflineStorage.cs:558", ret, r)
		}
	}
	// A function that starts with inlined code (the declaration row at its
	// entry is replaced by the call site): the faulting frame at its entry
	// is line 600, whose comment is between the declaration and the call.
	count := funcByRaw(t, lib, "OfflineStorage_get_Count_m5A6B7C8D")
	if r := resolveOne(t, lib, count.Addr, false); r.File != "OfflineStorage.cs" || r.Line != 600 {
		t.Errorf("get_Count entry: %+v, want OfflineStorage.cs:600", r)
	}
}

// attribute splits a row's range where an inlined call ends, so the code
// after it keeps the row's own line (not the call site), and a line-0 row
// inside an inlined call takes the call site.
func TestInlineSites_Attribute(t *testing.T) {
	sites := inlineSites{{low: 0x110, high: 0x118, file: "a.cpp", line: 20}, {low: 0x130, high: 0x140, file: "a.cpp", line: 30}}
	raw := []rawRow{
		{addr: 0x100, file: "a.cpp", line: 10},
		{addr: 0x10c, file: "a.cpp", line: 0},
		{addr: 0x110, file: "a.cpp", line: 900}, // the inlined body
		{addr: 0x114, file: "", line: 0},        // line 0 inside the call
		{addr: 0x120, file: "a.cpp", line: 11},
		{addr: 0x120, file: "a.cpp", line: 12}, // two rows at one address are both kept
	}
	got, n := sites.attribute(raw, 0x138)
	want := []rawRow{
		{0x100, "a.cpp", 10, 0},
		{0x10c, "a.cpp", 0, 0},
		{0x110, "a.cpp", 20, 900},
		{0x114, "a.cpp", 20, 0},
		{0x118, "", 0, 0}, // the call ended: the row before it (line 0) again
		{0x120, "a.cpp", 11, 0},
		{0x120, "a.cpp", 12, 0},
		{0x130, "a.cpp", 30, 0},
	}
	if n != 2 || len(got) != len(want) {
		t.Fatalf("attribute: %d inlined, %+v", n, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if s, ok := sites.at(0x118); ok {
		t.Errorf("0x118 is after the call: %+v", s)
	}
}

// A C# file of a package referenced from outside the project ("file:" in
// manifest.json) gets "Packages/<name>/<path in the package>".
func TestLineMap_PackagePathsForLocalPackages(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "sdk")
	if err := os.MkdirAll(filepath.Join(pkg, "Runtime", "Core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name": "com.ozx.apm", "version": "2.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A package.json that is not a UPM package (no reverse-domain name) is ignored.
	other := filepath.Join(root, "tools", "gen")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "package.json"), []byte(`{"name": "My Tools"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sm := filepath.ToSlash(filepath.Join(pkg, "Runtime", "Core", "SessionManager.cs"))
	top := filepath.ToSlash(filepath.Join(pkg, "Top.cs"))
	gen := filepath.ToSlash(filepath.Join(other, "Gen.cs"))
	js := `{"/b/il2cppOutput/cpp/OzxApm.cpp": {` +
		`"` + sm + `": {"10": 30},` +
		`"` + top + `": {"20": 5},` +
		`"` + gen + `": {"30": 7},` +
		`"/b/proj/Assets/Scripts/A.cs": {"40": 9}}}`
	lm, err := ReadLineMap(strings.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	for cpp, want := range map[uint32]string{
		10: "Packages/com.ozx.apm/Runtime/Core/SessionManager.cs",
		20: "Packages/com.ozx.apm/Top.cs",
		30: "Gen.cs",
		40: "Assets/Scripts/A.cs",
	} {
		if file, _, ok := lm.Lookup("/b/il2cppOutput/cpp/OzxApm.cpp", cpp, 0); !ok || file != want {
			t.Errorf("cpp line %d: %q, want %q", cpp, file, want)
		}
	}
}

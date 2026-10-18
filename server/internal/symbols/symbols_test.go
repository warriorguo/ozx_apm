package symbols

import (
	"bufio"
	"debug/dwarf"
	"debug/elf"
	"os"
	"strings"
	"testing"
)

func TestCSharpFromIL2CPPName(t *testing.T) {
	cases := map[string]string{
		"RoomLoader_LoadTemplate_m6F444615B7A27593A26813DB3252F8AA1ADBD2E1": "RoomLoader.LoadTemplate",
		"ApmTestDriver_Apply_m6F444615":                                     "ApmTestDriver.Apply",
		"List_1_get_Item_m9C2F2E2C2B0D0B6A":                                 "List`1.get_Item",
		"Dictionary_2_TryGetValue_mABCDEF0123456789":                        "Dictionary`2.TryGetValue",
		"U3CU3Ec_U3CStartU3Eb__5_0_m11223344":                               "<>c.<Start>b__5_0",
		"U3CLoadU3Ed__12_MoveNext_mAAAAAAAA":                                "<Load>d__12.MoveNext",
		"il2cpp_runtime_invoke":                                             "",
		"Foo_mXYZ12345":                                                     "",
		"Foo_m1234567":                                                      "", // 7 hex digits: not IL2CPP's
	}
	for in, want := range cases {
		if got := CSharpFromIL2CPPName(in); got != want {
			t.Errorf("CSharpFromIL2CPPName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSharpFromCecil(t *testing.T) {
	cases := map[string]string{
		"System.Void Game.Rooms.RoomLoader::LoadTemplate(System.Int32)":                  "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)",
		"System.Collections.Generic.Dictionary`2<System.String,System.Int32> A.B::Get()": "A.B.Get()",
		"System.Void OzxApm.Core.ApmClient/<>c::<Start>b__5_0()":                         "OzxApm.Core.ApmClient+<>c.<Start>b__5_0()",
		"no separator": "",
	}
	for in, want := range cases {
		if got := CSharpFromCecil(in); got != want {
			t.Errorf("CSharpFromCecil(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDemangleItanium(t *testing.T) {
	cases := map[string]string{
		"_ZN6il2cpp2vm9Exception5RaiseEPv":                     "il2cpp::vm::Exception::Raise",
		"_ZN6il2cpp2vm9Exception5RaiseEP15Il2CppExceptionb":    "il2cpp::vm::Exception::Raise",
		"_ZNK5Unity6Engine4LoadEv":                             "Unity::Engine::Load",
		"_ZN5Utils10ForceCrashE17ForcedCrashCategory":          "Utils::ForceCrash",
		"_Z10ForceCrashi":                                      "ForceCrash",
		"_ZNSt6__ndk16vectorIiNS_9allocatorIiEEE9push_backEOi": "std::__ndk1::vector<...>::push_back",
		"_ZN3FooC2Ev":      "Foo::Foo",
		"_ZN3FooD1Ev":      "Foo::~Foo",
		"_ZN3MapIiE3GetEv": "Map<...>::Get",
		"plain_c_function": "plain_c_function",
		"_Z":               "_Z",
		"_ZN99999999x":     "_ZN99999999x",
		"_ZN1A1BIS_EE":     "A::B<...>",
		"_ZN1AS_1BE":       "_ZN1AS_1BE", // substitution outside template arguments
	}
	for in, want := range cases {
		if got := DemangleItanium(in); got != want {
			t.Errorf("DemangleItanium(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSourcePath(t *testing.T) {
	cases := map[string]string{
		"/Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs":                         "Assets/Scripts/Rooms/RoomLoader.cs",
		`C:\build\ozx\Assets\Scripts\A.cs`:                                              "Assets/Scripts/A.cs",
		"/Users/b/ozx/Library/PackageCache/com.ozx.apm@2.0.0/Runtime/Core/ApmClient.cs": "Library/PackageCache/com.ozx.apm@2.0.0/Runtime/Core/ApmClient.cs",
		"/Users/b/ozx/Packages/com.ozx.apm/Runtime/A.cs":                                "Packages/com.ozx.apm/Runtime/A.cs",
		"/home/someone/generated/Thing.cs":                                              "Thing.cs",
		"Assets/X.cs":                                                                   "Assets/X.cs",
	}
	for in, want := range cases {
		if got := SourcePath(in); got != want {
			t.Errorf("SourcePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func openTestdata(t *testing.T) (MethodMap, *LineMap) {
	t.Helper()
	f, err := os.Open("testdata/MethodMap.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	mm, err := ReadMethodMap(f)
	if err != nil {
		t.Fatal(err)
	}
	g, err := os.Open("testdata/LineNumberMappings.json")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	lm, err := ReadLineMap(bufio.NewReader(g))
	if err != nil {
		t.Fatal(err)
	}
	return mm, lm
}

func TestReadLineMap(t *testing.T) {
	_, lm := openTestdata(t)
	if lm.Mappings != 7 {
		t.Fatalf("mappings = %d", lm.Mappings)
	}
	// Matched by base name, whatever the directory the debug info records.
	file, line, ok := lm.Lookup("/other/dir/Assembly-CSharp.cpp", 37, 0)
	if !ok || file != "Assets/Scripts/Rooms/RoomLoader.cs" || line != 71 {
		t.Fatalf("lookup 37 = %q %d %v", file, line, ok)
	}
	if _, _, ok := lm.Lookup("Assembly-CSharp.cpp", 37, 38); ok {
		t.Fatal("a comment above the function's first line must not be used")
	}
	if _, _, ok := lm.Lookup("Assembly-CSharp.cpp", 10, 0); ok {
		t.Fatal("before the first comment there is no mapping")
	}
	if lm.HasFile("Generics.cpp") {
		t.Fatal("unknown file")
	}
	for _, bad := range []string{"", "[", `{"a": [1]}`, `{"a": {"b": {"1": "x"}}}`, `{"a": {"b": {"1": 2}}`} {
		if _, err := ReadLineMap(strings.NewReader(bad)); err == nil {
			t.Errorf("ReadLineMap(%q) accepted", bad)
		}
	}
}

// lineAddr finds the address of the DWARF row for a line of demo.cpp.
func lineAddr(t *testing.T, path string, line int) uint64 {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := f.DWARF()
	if err != nil {
		t.Fatal(err)
	}
	r := d.Reader()
	for {
		e, err := r.Next()
		if err != nil || e == nil {
			break
		}
		if e.Tag != dwarf.TagCompileUnit {
			r.SkipChildren()
			continue
		}
		lr, _ := d.LineReader(e)
		var le dwarf.LineEntry
		for lr != nil && lr.Next(&le) == nil {
			if le.Line == line && !le.EndSequence {
				return le.Address
			}
		}
	}
	t.Fatalf("no row for line %d", line)
	return 0
}

func readTestLibrary(t *testing.T, path, name string, mm MethodMap, lm *LineMap) *Library {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lib, err := ReadLibrary(name, f, mm, lm)
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func TestReadLibrary_Arm64WithLineMapAndMethodMap(t *testing.T) {
	mm, lm := openTestdata(t)
	lib := readTestLibrary(t, "testdata/libdemo-arm64.elf", "out/libil2cpp.dbg.so", mm, lm)
	if lib.Module != "libil2cpp.so" || lib.Arch != "arm64" || len(lib.BuildID) != 40 || !lib.HasDWARF {
		t.Fatalf("library = %+v", lib)
	}
	if lib.MethodMapNames != 2 || lib.IL2CPPMethods != 4 {
		t.Fatalf("names: method map %d, il2cpp %d", lib.MethodMapNames, lib.IL2CPPMethods)
	}
	names := map[string]Func{}
	for _, f := range lib.Funcs {
		names[f.RawName] = f
	}
	load := names["RoomLoader_LoadTemplate_m6F444615B7A27593A26813DB3252F8AA1ADBD2E1"]
	if load.Name != "Game.Rooms.RoomLoader.LoadTemplate(System.Object,System.Int32)" || load.Size == 0 {
		t.Fatalf("method map name: %+v", load)
	}
	if f := names["Helper_Twice_m0A1B2C3D"]; f.Name != "Helper.Twice" {
		t.Fatalf("guessed name: %+v", f)
	}
	if f := names["_ZN6il2cpp2vm9Exception5RaiseEPv"]; f.Name != "il2cpp::vm::Exception::Raise" {
		t.Fatalf("demangled: %+v", f)
	}
	if f := names["U3CU3Ec_U3CStartU3Eb__5_0_m11223344"]; f.Name != "OzxApm.Core.ApmClient+<>c.<Start>b__5_0()" {
		t.Fatalf("nested type: %+v", f)
	}

	// Statements map to their C# lines; the call into Helper (line 37) is RoomLoader.cs:71.
	for cppLine, want := range map[int]uint32{35: 70, 37: 71, 44: 74} {
		addr := lineAddr(t, "testdata/libdemo-arm64.elf", cppLine)
		fn, line, ok := lib.Lookup(addr)
		if !ok || fn.RawName != load.RawName || line.File != "Assets/Scripts/Rooms/RoomLoader.cs" || line.Line != want {
			t.Errorf("cpp line %d (0x%x): %+v %+v %v, want RoomLoader.cs:%d", cppLine, addr, fn, line, ok, want)
		}
	}
	addr := lineAddr(t, "testdata/libdemo-arm64.elf", 26)
	if _, line, _ := lib.Lookup(addr); line.File != "Assets/Scripts/Util/Helper.cs" || line.Line != 12 {
		t.Errorf("Helper: %+v", line)
	}
	// The runtime function (not generated code) has a name but no C# line.
	raise := names["_ZN6il2cpp2vm9Exception5RaiseEPv"]
	if fn, line, ok := lib.Lookup(raise.Addr + 4); !ok || fn.Name != raise.Name || line.File != "" {
		t.Errorf("runtime function: %+v %+v", fn, line)
	}
	// List_1_get_Item has no comment: name only.
	item := names["List_1_get_Item_m9C2F2E2C2B0D0B6A"]
	if _, line, _ := lib.Lookup(item.Addr); line.File != "" {
		t.Errorf("get_Item has no C# line: %+v", line)
	}
	if _, _, ok := lib.Lookup(0x10); ok {
		t.Error("an address outside every function resolves to nothing")
	}
	// Lines are sorted, and every mapped range is closed.
	for i := 1; i < len(lib.Lines); i++ {
		if lib.Lines[i].Addr <= lib.Lines[i-1].Addr {
			t.Fatalf("lines not sorted/unique at %d", i)
		}
	}
	if last := lib.Lines[len(lib.Lines)-1]; last.File != "" {
		t.Fatalf("last line row must end the range: %+v", last)
	}
}

func TestReadLibrary_ArmThumbAndStripped(t *testing.T) {
	mm, lm := openTestdata(t)
	lib := readTestLibrary(t, "testdata/libdemo-arm.elf", "libil2cpp.sym.so", mm, lm)
	if lib.Arch != "arm" {
		t.Fatalf("arch = %s", lib.Arch)
	}
	for _, f := range lib.Funcs {
		if f.Addr&1 != 0 {
			t.Fatalf("thumb bit kept: %+v", f)
		}
	}
	addr := lineAddr(t, "testdata/libdemo-arm.elf", 37)
	if fn, line, ok := lib.Lookup(addr); !ok || line.Line != 71 || !strings.HasPrefix(fn.Name, "Game.Rooms.RoomLoader.LoadTemplate") {
		t.Fatalf("thumb lookup: %+v %+v", fn, line)
	}

	// The shipped library: the build id is there, the symbols are not
	// (only the exported dynamic ones, if any).
	f, err := os.Open("testdata/libdemo-arm64-stripped.elf")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ef, err := elf.NewFile(f)
	if err != nil {
		t.Fatal(err)
	}
	id, err := BuildID(ef)
	full := readTestLibrary(t, "testdata/libdemo-arm64.elf", "libil2cpp.so", nil, nil)
	if err != nil || id != full.BuildID {
		t.Fatalf("stripped build id %q, want %q (%v)", id, full.BuildID, err)
	}
	if full.HasDWARF != true || len(full.Lines) != 0 {
		t.Fatalf("without a line map: no lines (%d)", len(full.Lines))
	}
}

func TestModuleName(t *testing.T) {
	for in, want := range map[string]string{
		"arm64-v8a/libil2cpp.sym.so": "libil2cpp.so", "libil2cpp.dbg.so": "libil2cpp.so", "libunity.so.debug": "libunity.so",
		"libmain.so": "libmain.so", "x/libil2cpp.so.sym": "libil2cpp.so",
	} {
		if got := ModuleName(in); got != want {
			t.Errorf("ModuleName(%q) = %q", in, got)
		}
	}
}

func TestPartDecodeAndCommitValidate(t *testing.T) {
	p := Part{BuildID: "0D65A2D2978F75F0", UploadID: strings.Repeat("a", 32),
		Funcs: [][]interface{}{{float64(4096), float64(16), "A.B\x00<script>", "raw"}},
		Lines: [][]interface{}{{float64(4100), "Assets/A.cs", float64(12)}, {float64(4110), "", float64(5)}}}
	fs, ls, err := p.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if p.BuildID != "0d65a2d2978f75f0" || fs[0].Name != "A.B <script>" || ls[1].Line != 0 {
		t.Fatalf("decoded %+v %+v", fs, ls)
	}
	bad := []Part{
		{BuildID: "xyz", UploadID: strings.Repeat("a", 32)},
		{BuildID: "0d65a2d2", UploadID: "A"},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("a", 32), Funcs: [][]interface{}{{float64(-1), float64(1), "a", "b"}}},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("a", 32), Funcs: [][]interface{}{{float64(1) / 2, float64(1), "a", "b"}}},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("a", 32), Funcs: [][]interface{}{{float64(1 << 50), float64(1), "a", "b"}}},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("a", 32), Funcs: [][]interface{}{{float64(1), float64(1), "", ""}}},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("a", 32), Lines: [][]interface{}{{float64(1), 3, float64(1)}}},
	}
	for i, b := range bad {
		if _, _, err := b.Decode(); err == nil {
			t.Errorf("bad part %d accepted", i)
		}
	}
	c := Commit{BuildID: "0d65a2d2", UploadID: strings.Repeat("b", 32), Module: "libil2cpp.so", Arch: "arm64", Functions: 3}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for i, bc := range []Commit{
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("b", 32), Module: "../libil2cpp.so", Arch: "arm64", Functions: 3},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("b", 32), Module: "libil2cpp.so", Arch: "mips", Functions: 3},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("b", 32), Module: "libil2cpp.so", Arch: "arm64"},
		{BuildID: "0d65a2d2", UploadID: strings.Repeat("b", 32), Module: "libil2cpp.so", Arch: "arm64", Functions: MaxFunctionsPerBuild + 1},
	} {
		if err := bc.Validate(); err == nil {
			t.Errorf("bad commit %d accepted", i)
		}
	}
}

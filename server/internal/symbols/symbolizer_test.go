package symbols

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

// memStore serves uploads from in-memory tables, like the ClickHouse queries.
type memStore struct {
	refs    map[string]UploadRef // by build id
	funcs   map[string][]Func    // by upload id, sorted
	lines   map[string][]Line
	lookups int
	queries int
	fail    error
}

func (m *memStore) LatestSymbolUpload(_ context.Context, buildID string) (UploadRef, bool, error) {
	m.queries++
	if m.fail != nil {
		return UploadRef{}, false, m.fail
	}
	r, ok := m.refs[buildID]
	return r, ok, nil
}

func (m *memStore) SymbolUploadsForVersion(_ context.Context, v, module string) ([]UploadRef, error) {
	m.queries++
	var out []UploadRef
	for _, r := range m.refs {
		if r.AppVersion == v && r.Module == module {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UploadedAt.After(out[j].UploadedAt) })
	return out, nil
}

func (m *memStore) LookupSymbols(_ context.Context, ref UploadRef, addrs []uint64) (map[uint64]Hit, error) {
	m.lookups++
	out := map[uint64]Hit{}
	for _, a := range addrs {
		var h Hit
		fs := m.funcs[ref.UploadID]
		if i := sort.Search(len(fs), func(i int) bool { return fs[i].Addr > a }) - 1; i >= 0 {
			h.Func = fs[i]
		} else {
			continue
		}
		ls := m.lines[ref.UploadID]
		if j := sort.Search(len(ls), func(j int) bool { return ls[j].Addr > a }) - 1; j >= 0 {
			h.Line = ls[j]
		}
		out[a] = h
	}
	return out, nil
}

func newMemStore() *memStore {
	up := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return &memStore{
		refs: map[string]UploadRef{
			"0d65a2d2": {BuildID: "0d65a2d2", UploadID: up, Module: "libil2cpp.so", Arch: "arm64", AppVersion: "1.2.0", UploadedAt: time.Unix(100, 0)},
		},
		funcs: map[string][]Func{up: {
			{Addr: 0x1000, Size: 0x100, Name: "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)"},
			{Addr: 0x1100, Size: 0x40, Name: "Game.Util.Helper.Twice(System.Int32)"},
			{Addr: 0x2000, Size: 0x10, Name: "il2cpp::vm::Exception::Raise"},
		}},
		lines: map[string][]Line{up: {
			{Addr: 0x1000, File: "Assets/Rooms/RoomLoader.cs", Line: 70},
			{Addr: 0x1010, File: "Assets/Rooms/RoomLoader.cs", Line: 71},
			{Addr: 0x1020, File: "", Line: 0},
			{Addr: 0x1100, File: "Assets/Util/Helper.cs", Line: 12},
		}},
	}
}

func TestSymbolizer_ResolveByBuildID(t *testing.T) {
	st := newMemStore()
	s := NewSymbolizer(st, 100)
	frames := []Frame{
		{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 0x1104},                       // top frame, Helper.cs:12
		{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 0x1011, ReturnAddress: true},  // caller: 0x1010 -> 71
		{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 0x1010, ReturnAddress: true},  // pc-1 = 0x100f -> 70
		{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 0x1030, ReturnAddress: true},  // after the range end: name only
		{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 0x1500, ReturnAddress: true},  // in no function
		{Module: "libil2cpp.so", BuildID: "ffffffff", PC: 0x1000},                       // no symbols for this build
		{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 0x2004, ReturnAddress: false}, // runtime function, no line
	}
	got, err := s.Resolve(context.Background(), frames, Fallback{})
	if err != nil {
		t.Fatal(err)
	}
	check := func(i int, name, file string, line uint32, off uint64) {
		t.Helper()
		r, ok := got[i]
		if !ok {
			t.Fatalf("frame %d not resolved", i)
		}
		if r.Name != name || r.File != file || r.Line != line || r.Offset != off || r.By != "build_id" {
			t.Errorf("frame %d = %+v", i, r)
		}
	}
	check(0, "Game.Util.Helper.Twice(System.Int32)", "Assets/Util/Helper.cs", 12, 4)
	check(1, "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)", "Assets/Rooms/RoomLoader.cs", 71, 0x11)
	check(2, "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)", "Assets/Rooms/RoomLoader.cs", 70, 0x10)
	check(3, "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)", "", 0, 0x30)
	check(6, "il2cpp::vm::Exception::Raise", "", 0, 4)
	if _, ok := got[4]; ok {
		t.Error("an address between functions must not resolve")
	}
	if _, ok := got[5]; ok {
		t.Error("a build without symbols must not resolve")
	}

	// Cached: the same frames again need no lookup and no upload query.
	lookups, queries := st.lookups, st.queries
	if _, err := s.Resolve(context.Background(), frames, Fallback{}); err != nil {
		t.Fatal(err)
	}
	if st.lookups != lookups || st.queries != queries {
		t.Errorf("cache missed: %d lookups, %d queries more", st.lookups-lookups, st.queries-queries)
	}
	// After the TTL a build that had no symbols is asked for again.
	now := time.Now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	if _, err := s.Resolve(context.Background(), frames[5:6], Fallback{}); err != nil {
		t.Fatal(err)
	}
	if st.queries != queries+1 {
		t.Errorf("expired entry not refreshed")
	}
}

func TestSymbolizer_FallbackByVersionAndABI(t *testing.T) {
	st := newMemStore()
	s := NewSymbolizer(st, 100)
	frames := []Frame{{Module: "libil2cpp.so", PC: 0x1004}}
	got, _ := s.Resolve(context.Background(), frames, Fallback{AppVersion: "1.2.0", ABI: "arm64-v8a"})
	if got[0].Name == "" || got[0].By != "app_version" {
		t.Fatalf("fallback: %+v", got)
	}
	got, _ = s.Resolve(context.Background(), frames, Fallback{AppVersion: "1.2.0", ABI: "armeabi-v7a"})
	if len(got) != 0 {
		t.Fatalf("other ABI must not match: %+v", got)
	}
	got, _ = s.Resolve(context.Background(), frames, Fallback{AppVersion: "9.9", ABI: "arm64-v8a"})
	if len(got) != 0 {
		t.Fatalf("other version: %+v", got)
	}
	got, _ = s.Resolve(context.Background(), frames, Fallback{AppVersion: "1.2.0"})
	if len(got) != 0 {
		t.Fatalf("unknown ABI: no fallback: %+v", got)
	}
}

func TestSymbolizer_StoreErrorAndBatching(t *testing.T) {
	st := newMemStore()
	st.fail = errors.New("down")
	s := NewSymbolizer(st, 100)
	if _, err := s.Resolve(context.Background(), []Frame{{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 1}}, Fallback{}); err == nil {
		t.Fatal("store error not returned")
	}
	st.fail = nil
	s = NewSymbolizer(st, 10)
	s.maxBatch = 4
	var frames []Frame
	for i := 0; i < 10; i++ {
		frames = append(frames, Frame{Module: "libil2cpp.so", BuildID: "0d65a2d2", PC: 0x1000 + uint64(i)})
	}
	got, err := s.Resolve(context.Background(), frames, Fallback{})
	if err != nil || len(got) != 10 || st.lookups != 3 {
		t.Fatalf("batched: %d resolved, %d lookups (%v)", len(got), st.lookups, err)
	}
	if s.hits.len() != 10 {
		t.Fatalf("cache size %d", s.hits.len())
	}
	// The cache is bounded.
	for i := 0; i < 50; i++ {
		s.hits.put(hitKey{"x", uint64(i)}, Hit{})
	}
	if s.hits.len() != 10 {
		t.Fatalf("lru not bounded: %d", s.hits.len())
	}
}

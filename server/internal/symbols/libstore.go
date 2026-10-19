package symbols

import (
	"context"
	"sort"
)

// LibraryStore serves one extracted library as if it had been uploaded, so
// the tool's "lookup" (and the tests) resolve addresses through the same
// Symbolizer, with the same rules, as the server.
type LibraryStore struct {
	Lib *Library
}

// Ref is the upload LibraryStore serves.
func (s LibraryStore) Ref() UploadRef {
	return UploadRef{BuildID: s.Lib.BuildID, UploadID: "local", Module: s.Lib.Module, Arch: s.Lib.Arch,
		Functions: uint64(len(s.Lib.Funcs)), Lines: uint64(len(s.Lib.Lines))}
}

func (s LibraryStore) LatestSymbolUpload(_ context.Context, buildID string) (UploadRef, bool, error) {
	if buildID != s.Lib.BuildID {
		return UploadRef{}, false, nil
	}
	return s.Ref(), true, nil
}

func (s LibraryStore) SymbolUploadsForVersion(context.Context, string, string) ([]UploadRef, error) {
	return nil, nil
}

// LookupSymbols is the storage query: the function and the line row at or
// before each address.
func (s LibraryStore) LookupSymbols(_ context.Context, _ UploadRef, addrs []uint64) (map[uint64]Hit, error) {
	out := map[uint64]Hit{}
	fs, ls := s.Lib.Funcs, s.Lib.Lines
	for _, a := range addrs {
		var h Hit
		if i := sort.Search(len(fs), func(i int) bool { return fs[i].Addr > a }) - 1; i >= 0 {
			h.Func = fs[i]
		}
		if j := sort.Search(len(ls), func(j int) bool { return ls[j].Addr > a }) - 1; j >= 0 {
			h.Line = ls[j]
		}
		if h.Func.Name != "" || h.Line.File != "" {
			out[a] = h
		}
	}
	return out, nil
}

package symbols

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"
)

// UploadRef is a committed upload of one library's symbols.
type UploadRef struct {
	BuildID    string    `json:"build_id"`
	UploadID   string    `json:"upload_id"`
	Module     string    `json:"module"`
	Arch       string    `json:"arch"`
	AppVersion string    `json:"app_version"`
	AppBuild   string    `json:"app_build"`
	Functions  uint64    `json:"functions"`
	Lines      uint64    `json:"lines"`
	LineMap    bool      `json:"line_map"`
	MethodMap  bool      `json:"method_map"`
	Source     string    `json:"source"`
	Tool       string    `json:"tool"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// Hit is what an upload knows about one address: the function containing
// it (Func.Size > 0 and the address inside) and the line row at or before
// it (Line.Addr; File empty: none).
type Hit struct {
	Func Func
	Line Line
}

// Store is what the Symbolizer reads (storage.Repository).
type Store interface {
	// LatestSymbolUpload returns the newest committed upload of a build id.
	LatestSymbolUpload(ctx context.Context, buildID string) (UploadRef, bool, error)
	// SymbolUploadsForVersion returns the committed uploads of a module for
	// an app version, newest first.
	SymbolUploadsForVersion(ctx context.Context, appVersion, module string) ([]UploadRef, error)
	// LookupSymbols returns, per address, the function at or before it and
	// the line row at or before it (absent when there is none).
	LookupSymbols(ctx context.Context, ref UploadRef, addrs []uint64) (map[uint64]Hit, error)
}

// Frame is a native frame to symbolicate.
type Frame struct {
	Module  string // file name ("libil2cpp.so")
	BuildID string // lowercase hex, "" if the device did not report one
	PC      uint64 // relative to the module
	// ReturnAddress: the frame is a caller (not the frame that faulted):
	// its pc is the instruction after the call, so pc-1 is looked up.
	ReturnAddress bool
}

// Resolved is a symbolicated frame.
type Resolved struct {
	Name   string `json:"name"`
	Offset uint64 `json:"offset"` // pc - function start
	File   string `json:"file,omitempty"`
	Line   uint32 `json:"line,omitempty"`
	// By says how the upload was found: "build_id" (exact), or
	// "app_version" (the frame had no build id; the newest upload of the
	// module for the crash's version and ABI).
	By       string `json:"by"`
	UploadID string `json:"upload_id"`
}

// Fallback identifies the build when frames carry no build id.
type Fallback struct {
	AppVersion string
	ABI        string // "arm64-v8a", "armeabi-v7a", ... (as the SDK sends it)
}

// ArchOfABI maps an Android ABI to the arch of an upload.
func ArchOfABI(abi string) string {
	switch strings.ToLower(abi) {
	case "arm64-v8a", "arm64":
		return "arm64"
	case "armeabi-v7a", "armeabi", "arm":
		return "arm"
	case "x86":
		return "x86"
	case "x86_64":
		return "x86_64"
	}
	return ""
}

// Symbolizer resolves native frames against the uploaded symbols, with a
// cache: resolved addresses per upload (LRU), and the upload of a build id
// or version for UploadTTL (so a new upload is used within that time, and a
// build without symbols is not looked up on every request).
type Symbolizer struct {
	store     Store
	UploadTTL time.Duration
	now       func() time.Time

	mu       sync.Mutex
	uploads  map[string]uploadEntry // "b:<build id>" / "v:<version>|<module>|<arch>"
	hits     *lru
	maxBatch int
}

type uploadEntry struct {
	ref     UploadRef
	found   bool
	expires time.Time
}

// NewSymbolizer returns a symbolizer over store with a cache of cacheSize addresses.
func NewSymbolizer(store Store, cacheSize int) *Symbolizer {
	return &Symbolizer{
		store:     store,
		UploadTTL: time.Minute,
		now:       time.Now,
		uploads:   map[string]uploadEntry{},
		hits:      newLRU(cacheSize),
		maxBatch:  128,
	}
}

func (s *Symbolizer) cachedUpload(key string) (uploadEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.uploads[key]
	if !ok || s.now().After(e.expires) {
		return uploadEntry{}, false
	}
	return e, true
}

func (s *Symbolizer) storeUpload(key string, e uploadEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.uploads) > 10000 {
		s.uploads = map[string]uploadEntry{}
	}
	e.expires = s.now().Add(s.UploadTTL)
	s.uploads[key] = e
}

// UploadFor returns the upload used for a frame and how it was found ("build_id" or
// "app_version"); found is false when there is none.
func (s *Symbolizer) UploadFor(ctx context.Context, f Frame, fb Fallback) (UploadRef, string, bool, error) {
	if f.BuildID != "" {
		key := "b:" + f.BuildID
		if e, ok := s.cachedUpload(key); ok {
			return e.ref, "build_id", e.found, nil
		}
		ref, found, err := s.store.LatestSymbolUpload(ctx, f.BuildID)
		if err != nil {
			return UploadRef{}, "", false, err
		}
		s.storeUpload(key, uploadEntry{ref: ref, found: found})
		return ref, "build_id", found, nil
	}
	if fb.AppVersion == "" || f.Module == "" {
		return UploadRef{}, "", false, nil
	}
	arch := ArchOfABI(fb.ABI)
	if arch == "" {
		return UploadRef{}, "", false, nil // another ABI's symbols would name the wrong code
	}
	key := "v:" + fb.AppVersion + "|" + f.Module + "|" + arch
	if e, ok := s.cachedUpload(key); ok {
		return e.ref, "app_version", e.found, nil
	}
	refs, err := s.store.SymbolUploadsForVersion(ctx, fb.AppVersion, f.Module)
	if err != nil {
		return UploadRef{}, "", false, err
	}
	var e uploadEntry
	for _, r := range refs {
		if r.Arch == arch {
			e = uploadEntry{ref: r, found: true}
			break
		}
	}
	s.storeUpload(key, e)
	return e.ref, "app_version", e.found, nil
}

type hitKey struct {
	upload string
	addr   uint64
}

// Resolve symbolicates frames. The result has an entry per frame index
// that was resolved; frames without an upload, or whose address is in no
// known function, have none. An error of the store is returned with what
// was resolved before it.
func (s *Symbolizer) Resolve(ctx context.Context, frames []Frame, fb Fallback) (map[int]Resolved, error) {
	out := map[int]Resolved{}
	type pending struct {
		idx  int
		addr uint64
		by   string
	}
	byUpload := map[string][]pending{}
	refs := map[string]UploadRef{}
	for i, f := range frames {
		ref, by, found, err := s.UploadFor(ctx, f, fb)
		if err != nil {
			return out, err
		}
		if !found {
			continue
		}
		addr := f.PC
		if f.ReturnAddress && addr > 0 {
			addr--
		}
		refs[ref.UploadID] = ref
		byUpload[ref.UploadID] = append(byUpload[ref.UploadID], pending{i, addr, by})
	}
	for uploadID, ps := range byUpload {
		ref := refs[uploadID]
		hits := map[uint64]Hit{}
		var missing []uint64
		seen := map[uint64]bool{}
		for _, p := range ps {
			if h, ok := s.hits.get(hitKey{uploadID, p.addr}); ok {
				hits[p.addr] = h
				continue
			}
			if !seen[p.addr] {
				seen[p.addr] = true
				missing = append(missing, p.addr)
			}
		}
		for len(missing) > 0 {
			n := len(missing)
			if n > s.maxBatch {
				n = s.maxBatch
			}
			got, err := s.store.LookupSymbols(ctx, ref, missing[:n])
			if err != nil {
				return out, err
			}
			for _, a := range missing[:n] {
				h := got[a] // the zero Hit (no function) is cached too
				hits[a] = h
				s.hits.put(hitKey{uploadID, a}, h)
			}
			missing = missing[n:]
		}
		for _, p := range ps {
			h := hits[p.addr]
			fn := h.Func
			if fn.Name == "" || p.addr < fn.Addr || (fn.Size > 0 && p.addr >= fn.Addr+fn.Size) {
				continue
			}
			r := Resolved{Name: fn.Name, Offset: frames[p.idx].PC - fn.Addr, By: p.by, UploadID: uploadID}
			if h.Line.File != "" && h.Line.Addr >= fn.Addr && h.Line.Addr <= p.addr {
				r.File, r.Line = h.Line.File, h.Line.Line
			}
			out[p.idx] = r
		}
	}
	return out, nil
}

// lru is a small LRU cache of hits.
type lru struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[hitKey]*list.Element
}

type lruEntry struct {
	key hitKey
	val Hit
}

func newLRU(max int) *lru {
	if max < 1 {
		max = 1
	}
	return &lru{max: max, ll: list.New(), items: map[hitKey]*list.Element{}}
}

func (c *lru) get(k hitKey) (Hit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*lruEntry).val, true
	}
	return Hit{}, false
}

func (c *lru) put(k hitKey, v Hit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		e.Value.(*lruEntry).val = v
		c.ll.MoveToFront(e)
		return
	}
	c.items[k] = c.ll.PushFront(&lruEntry{k, v})
	for c.ll.Len() > c.max {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.items, last.Value.(*lruEntry).key)
	}
}

func (c *lru) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

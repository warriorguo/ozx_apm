// Command apmsym extracts the symbols of an Android IL2CPP build and uploads
// them to the OZX APM admin API, so that native crashes show C# method names,
// files and lines (APM-7; RELEASE.md and README.md describe the build
// settings and the CI step).
//
//	apmsym upload  -url https://apm-admin.example.com [options] <path>...
//	apmsym extract [options] <path>...          what would be uploaded (nothing is sent)
//	apmsym lookup  [options] <library> <hex address>...
//
// A path is a directory (searched recursively), Unity's symbols.zip, or a
// file: ELF libraries (libil2cpp.sym.so, libil2cpp.dbg.so, libunity.sym.so,
// ...), IL2CPP's MethodMap.tsv and LineNumberMappings.json. The bearer token
// of the upload endpoints is read from OZX_SYMBOLS_UPLOAD_TOKEN.
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

const toolVersion = "apmsym 1"

// TokenEnv holds the upload token.
const TokenEnv = "OZX_SYMBOLS_UPLOAD_TOKEN"

type options struct {
	url        string
	methodMap  string
	lineMap    string
	appVersion string
	appBuild   string
	modules    string
	partBytes  int
	timeout    time.Duration
	frame0     bool
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var o options
	fs.StringVar(&o.url, "url", "", "admin base URL (upload), e.g. https://apm-admin.example.com or http://host:8081")
	fs.StringVar(&o.methodMap, "method-map", "", "MethodMap.tsv (default: found under the paths)")
	fs.StringVar(&o.lineMap, "line-map", "", "LineNumberMappings.json (default: found under the paths)")
	fs.StringVar(&o.appVersion, "app-version", "", "Application.version of the build (recorded with the upload)")
	fs.StringVar(&o.appBuild, "app-build", "", "version code of the build (recorded with the upload)")
	fs.StringVar(&o.modules, "modules", "", "comma-separated libraries to upload (default: every library found)")
	fs.IntVar(&o.partBytes, "part-bytes", 900*1024, "maximum compressed size of one request")
	fs.DurationVar(&o.timeout, "timeout", 60*time.Second, "timeout of one request")
	fs.BoolVar(&o.frame0, "frame0", false, "lookup: the first address is the frame that faulted (looked up as is; callers are looked up at pc-1)")
	_ = fs.Parse(os.Args[2:])
	var err error
	switch cmd {
	case "upload":
		err = runUpload(o, fs.Args())
	case "extract":
		err = runExtract(o, fs.Args())
	case "lookup":
		err = runLookup(o, fs.Args())
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "apmsym:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  apmsym upload  -url <admin base URL> [-app-version V] [-app-build N] [-modules libil2cpp.so,...] <path>...
  apmsym extract [-modules ...] <path>...
  apmsym lookup  [-frame0] [-modules libil2cpp.so] <path>... <hex address>...
                 (resolved as the server shows them; callers at pc-1)
paths: directories, symbols.zip, .so/.sym.so/.dbg.so, MethodMap.tsv, LineNumberMappings.json
token: environment variable `+TokenEnv+`
`)
}

// inputs is what was found under the paths.
type inputs struct {
	libraries []string // ELF files (extracted from zips into tmp)
	methodMap string
	lineMap   string
	tmp       string
}

func (in *inputs) cleanup() {
	if in.tmp != "" {
		os.RemoveAll(in.tmp)
	}
}

func collect(paths []string) (*inputs, error) {
	if len(paths) == 0 {
		return nil, errors.New("no paths given")
	}
	in := &inputs{}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return in, err
		}
		if st.IsDir() {
			err = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				return in.addFile(path)
			})
			if err != nil {
				return in, err
			}
			continue
		}
		if err := in.addFile(p); err != nil {
			return in, err
		}
	}
	return in, nil
}

func (in *inputs) addFile(path string) error {
	base := filepath.Base(path)
	switch {
	case base == "MethodMap.tsv":
		if in.methodMap == "" {
			in.methodMap = path
		}
	case base == "LineNumberMappings.json":
		if in.lineMap == "" {
			in.lineMap = path
		}
	case strings.HasSuffix(strings.ToLower(base), ".zip"):
		return in.addZip(path)
	case strings.Contains(base, ".so"):
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		ok := symbols.IsELF(f)
		f.Close()
		if ok {
			in.libraries = append(in.libraries, path)
		}
	}
	return nil
}

// addZip extracts the libraries of a symbols.zip into a temporary directory
// (entry names are reduced to their base name: no path from the archive is
// used) and the maps found inside.
func (in *inputs) addZip(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer zr.Close()
	if in.tmp == "" {
		if in.tmp, err = os.MkdirTemp("", "apmsym-"); err != nil {
			return err
		}
	}
	for i, zf := range zr.File {
		base := filepath.Base(filepath.Clean("/" + zf.Name))
		if zf.FileInfo().IsDir() || base == "." || base == "/" {
			continue
		}
		if !(strings.Contains(base, ".so") || base == "MethodMap.tsv" || base == "LineNumberMappings.json") {
			continue
		}
		// A per-entry directory keeps arm64-v8a/ and armeabi-v7a/ copies apart.
		dir := filepath.Join(in.tmp, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		dst := filepath.Join(dir, base)
		if err := extractEntry(zf, dst); err != nil {
			return fmt.Errorf("%s: %s: %w", path, zf.Name, err)
		}
		if err := in.addFile(dst); err != nil {
			return err
		}
	}
	return nil
}

func extractEntry(zf *zip.File, dst string) error {
	const maxEntry = 4 << 30
	if zf.UncompressedSize64 > maxEntry {
		return errors.New("entry too large")
	}
	r, err := zf.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.LimitReader(r, maxEntry))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

func loadMaps(o options, in *inputs) (symbols.MethodMap, *symbols.LineMap, error) {
	mmPath, lmPath := o.methodMap, o.lineMap
	if mmPath == "" {
		mmPath = in.methodMap
	}
	if lmPath == "" {
		lmPath = in.lineMap
	}
	var mm symbols.MethodMap
	var lm *symbols.LineMap
	if mmPath != "" {
		f, err := os.Open(mmPath)
		if err != nil {
			return nil, nil, err
		}
		mm, err = symbols.ReadMethodMap(f)
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", mmPath, err)
		}
		fmt.Fprintf(os.Stderr, "method map: %s (%d methods)\n", mmPath, len(mm))
	} else {
		fmt.Fprintln(os.Stderr, "method map: none (C# names are guessed from IL2CPP's function names; build with --emit-method-map for exact ones)")
	}
	if lmPath != "" {
		f, err := os.Open(lmPath)
		if err != nil {
			return nil, nil, err
		}
		lm, err = symbols.ReadLineMap(bufio.NewReaderSize(f, 1<<20))
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", lmPath, err)
		}
		fmt.Fprintf(os.Stderr, "line map: %s (%d mappings)\n", lmPath, lm.Mappings)
	} else {
		fmt.Fprintln(os.Stderr, "line map: none (no C# files and lines; build with --emit-source-mapping)")
	}
	return mm, lm, nil
}

// readLibraries reads every library and keeps, per build id, the richest
// file (debug information first, then the most functions).
func readLibraries(o options, in *inputs, mm symbols.MethodMap, lm *symbols.LineMap) ([]*symbols.Library, error) {
	want := map[string]bool{}
	for _, m := range strings.Split(o.modules, ",") {
		if m = strings.TrimSpace(m); m != "" {
			want[m] = true
		}
	}
	best := map[string]*symbols.Library{}
	for _, path := range in.libraries {
		if len(want) > 0 && !want[symbols.ModuleName(path)] {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		lib, err := symbols.ReadLibrary(path, f, mm, lm)
		f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipped %s: %v\n", path, err)
			continue
		}
		cur := best[lib.BuildID]
		if cur == nil || better(lib, cur) {
			best[lib.BuildID] = lib
		}
	}
	var libs []*symbols.Library
	for _, l := range best {
		libs = append(libs, l)
	}
	sort.Slice(libs, func(i, j int) bool {
		if libs[i].Module != libs[j].Module {
			return libs[i].Module < libs[j].Module
		}
		return libs[i].Arch < libs[j].Arch
	})
	if len(libs) == 0 {
		return nil, errors.New("no library with symbols found (expected libil2cpp.sym.so / libil2cpp.dbg.so, or Unity's symbols.zip)")
	}
	return libs, nil
}

func better(a, b *symbols.Library) bool {
	if len(a.Lines) != len(b.Lines) {
		return len(a.Lines) > len(b.Lines)
	}
	if a.HasDWARF != b.HasDWARF {
		return a.HasDWARF
	}
	return len(a.Funcs) > len(b.Funcs)
}

func describe(lib *symbols.Library) string {
	return fmt.Sprintf("%s %s build id %s: %d functions (%d C# methods, %d named from the method map), %d line rows (%d DWARF rows, %d mapped to C#, %d in %d inlined calls) from %s",
		lib.Module, lib.Arch, lib.BuildID, len(lib.Funcs), lib.IL2CPPMethods, lib.MethodMapNames, len(lib.Lines), lib.LineRows, lib.MappedRows, lib.InlinedRows, lib.InlineRanges, lib.Path)
}

func prepare(o options, paths []string) ([]*symbols.Library, symbols.MethodMap, *symbols.LineMap, func(), error) {
	in, err := collect(paths)
	cleanup := func() {}
	if in != nil {
		cleanup = in.cleanup
	}
	if err != nil {
		return nil, nil, nil, cleanup, err
	}
	mm, lm, err := loadMaps(o, in)
	if err != nil {
		return nil, nil, nil, cleanup, err
	}
	libs, err := readLibraries(o, in, mm, lm)
	return libs, mm, lm, cleanup, err
}

func runExtract(o options, paths []string) error {
	libs, _, _, cleanup, err := prepare(o, paths)
	defer cleanup()
	if err != nil {
		return err
	}
	for _, lib := range libs {
		fmt.Println(describe(lib))
	}
	return nil
}

// runLookup resolves addresses the way the server resolves a crash's frames
// once these symbols are uploaded (the same Symbolizer): a caller's pc is a
// return address, so pc-1 is looked up; with -frame0 the first address is
// the frame that faulted and is looked up as is.
func runLookup(o options, args []string) error {
	var paths []string
	var addrs []uint64
	for _, a := range args {
		if _, err := os.Stat(a); err == nil {
			paths = append(paths, a)
			continue
		}
		addr, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(a), "0x"), 16, 64)
		if err != nil {
			return fmt.Errorf("%q is neither a path nor a hex address", a)
		}
		addrs = append(addrs, addr)
	}
	if len(paths) == 0 || len(addrs) == 0 {
		return errors.New("lookup [-frame0] [-modules libil2cpp.so] <path>... <hex address>...")
	}
	libs, _, _, cleanup, err := prepare(o, paths)
	defer cleanup()
	if err != nil {
		return err
	}
	lib := libs[0]
	for _, l := range libs {
		if l.Module == "libil2cpp.so" {
			lib = l
			break
		}
	}
	fmt.Fprintln(os.Stderr, "looking up in "+describe(lib))
	store := symbols.LibraryStore{Lib: lib}
	sym := symbols.NewSymbolizer(store, 1024)
	frames := make([]symbols.Frame, len(addrs))
	for i, a := range addrs {
		frames[i] = symbols.Frame{Module: lib.Module, BuildID: lib.BuildID, PC: a, ReturnAddress: !(o.frame0 && i == 0)}
	}
	got, err := sym.Resolve(context.Background(), frames, symbols.Fallback{})
	if err != nil {
		return err
	}
	for i, f := range frames {
		kind := "caller, pc-1 looked up"
		if !f.ReturnAddress {
			kind = "faulting frame"
		}
		r, ok := got[i]
		switch {
		case !ok:
			fmt.Printf("0x%x\t?\t(%s)\n", f.PC, kind)
		case r.File != "":
			fmt.Printf("0x%x\t%s+0x%x\t%s:%d\t(%s)\n", f.PC, r.Name, r.Offset, r.File, r.Line, kind)
		default:
			fmt.Printf("0x%x\t%s+0x%x\tno file:line\t(%s)\n", f.PC, r.Name, r.Offset, kind)
		}
	}
	return nil
}

func runUpload(o options, paths []string) error {
	if o.url == "" {
		return errors.New("-url is required")
	}
	token := os.Getenv(TokenEnv)
	if token == "" {
		return errors.New(TokenEnv + " is not set")
	}
	libs, mm, lm, cleanup, err := prepare(o, paths)
	defer cleanup()
	if err != nil {
		return err
	}
	c := &client{base: strings.TrimRight(o.url, "/"), token: token, http: &http.Client{Timeout: o.timeout}, partBytes: o.partBytes}
	for _, lib := range libs {
		fmt.Fprintln(os.Stderr, "uploading", describe(lib))
		var source []string
		source = append(source, filepath.Base(lib.Path))
		if mm != nil {
			source = append(source, "MethodMap.tsv")
		}
		if lm != nil {
			source = append(source, "LineNumberMappings.json")
		}
		commit := symbols.Commit{
			BuildID: lib.BuildID, UploadID: newUploadID(), Module: lib.Module, Arch: lib.Arch,
			AppVersion: o.appVersion, AppBuild: o.appBuild,
			Functions: uint64(len(lib.Funcs)), Lines: uint64(len(lib.Lines)),
			LineMap: len(lib.Lines) > 0, MethodMap: lib.MethodMapNames > 0,
			Source: strings.Join(source, " + "), Tool: toolVersion,
		}
		if err := c.upload(lib, commit); err != nil {
			return fmt.Errorf("%s (%s): %w", lib.Module, lib.BuildID, err)
		}
		fmt.Fprintf(os.Stderr, "done: %s %s build id %s, upload %s\n", lib.Module, lib.Arch, lib.BuildID, commit.UploadID)
	}
	return nil
}

func newUploadID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type client struct {
	base      string
	token     string
	http      *http.Client
	partBytes int
}

func (c *client) upload(lib *symbols.Library, commit symbols.Commit) error {
	part := 0
	fi, li := 0, 0
	rows := 20000
	for fi < len(lib.Funcs) || li < len(lib.Lines) {
		n := rows
		if n > symbols.MaxPartRows {
			n = symbols.MaxPartRows
		}
		nf := min(n, len(lib.Funcs)-fi)
		nl := min(n-nf, len(lib.Lines)-li)
		p := symbols.Part{BuildID: lib.BuildID, UploadID: commit.UploadID, Part: part,
			Funcs: symbols.EncodeFuncs(lib.Funcs[fi : fi+nf]), Lines: symbols.EncodeLines(lib.Lines[li : li+nl])}
		body, err := gzipJSON(p)
		if err != nil {
			return err
		}
		if len(body) > c.partBytes && n > 100 {
			rows = n / 2
			continue
		}
		if err := c.post("/api/symbols/part", body, true); err != nil {
			return fmt.Errorf("part %d: %w", part, err)
		}
		fi += nf
		li += nl
		part++
		fmt.Fprintf(os.Stderr, "  part %d: %d/%d functions, %d/%d lines\n", part, fi, len(lib.Funcs), li, len(lib.Lines))
	}
	body, err := json.Marshal(commit)
	if err != nil {
		return err
	}
	return c.post("/api/symbols/commit", body, false)
}

func gzipJSON(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// post sends with up to 4 attempts (a part is idempotent: the server keeps
// one row per address of an upload).
func (c *client) post(path string, body []byte, gzipped bool) error {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		if gzipped {
			req.Header.Set("Content-Encoding", "gzip")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			last = err
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			return nil
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			last = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		default:
			// 400/401/403/409/413: retrying will not help.
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}
	}
	return last
}

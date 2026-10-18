// Package symbols turns the addresses of a native crash stack into C#
// method names, files and lines (APM-7).
//
// A release build's libil2cpp.so is stripped: a crash on the device reports
// only "libil2cpp.so" and an offset. The build produces the symbols on the
// build machine (Unity's symbols.zip / libil2cpp.sym.so / libil2cpp.dbg.so,
// IL2CPP's MethodMap.tsv and LineNumberMappings.json). The apmsym tool
// (cmd/apmsym) reads them there and uploads a compact table per library,
// keyed by the library's ELF build id (which the device reports with every
// frame):
//
//   - functions: start address, size and name of every function (the C#
//     method for IL2CPP code);
//   - lines: the addresses where the C# file and line change (only with the
//     debug information and LineNumberMappings.json).
//
// The server stores them in ClickHouse (apm_symbol_files, apm_symbol_funcs,
// apm_symbol_lines) and symbolicates when a crash is read (Symbolizer), so
// symbols uploaded after a crash still apply. Uploads are stateless
// (several admin replicas, no disk): the tool sends parts of at most a few
// hundred KB (below the 1 MB request limit of nginx in front of the admin
// API) tagged with its upload id, then a commit that the server checks
// against the rows it has; only committed uploads are used.
package symbols

import (
	"errors"
	"fmt"
	"strings"
)

// Limits of an upload. The server enforces them; the tool stays below.
const (
	MaxFileLength        = 256        // bytes of a source file name
	MaxPartRows          = 100_000    // functions + lines in one part
	MaxPartBodyBytes     = 1 << 20    // compressed request body of a part
	MaxPartDecodedBytes  = 16 << 20   // the part's JSON once decompressed
	MaxFunctionsPerBuild = 5_000_000  // per upload
	MaxLinesPerBuild     = 20_000_000 // per upload
	MaxAddress           = 1 << 48
	MaxFunctionSize      = 1 << 28
	MaxBuildIDLength     = 80 // hex digits (a GNU build id is 40)
	MaxModuleLength      = 128
	MaxVersionLength     = 128
)

// Func is a function of a library: [Addr, Addr+Size).
type Func struct {
	Addr    uint64 `json:"addr"`
	Size    uint64 `json:"size"`
	Name    string `json:"name"`     // display name (C# method, demangled C++)
	RawName string `json:"raw_name"` // the ELF symbol
}

// Line says that from Addr on (up to the next Line or the end of the
// function) the code is File:Line. An empty File ends the previous range.
type Line struct {
	Addr uint64 `json:"addr"`
	File string `json:"file"`
	Line uint32 `json:"line"`
}

// Part is one request of an upload. Functions and lines are sent as arrays
// ([addr, size, name, raw_name] and [addr, file, line]) to keep parts small.
type Part struct {
	BuildID  string          `json:"build_id"`
	UploadID string          `json:"upload_id"`
	Part     int             `json:"part"`
	Funcs    [][]interface{} `json:"funcs"`
	Lines    [][]interface{} `json:"lines"`
}

// Commit ends an upload: the server checks that it has Functions functions
// and Lines lines for the upload id, then records the upload.
type Commit struct {
	BuildID    string `json:"build_id"`
	UploadID   string `json:"upload_id"`
	Module     string `json:"module"`      // "libil2cpp.so"
	Arch       string `json:"arch"`        // "arm64", "arm", "x86", "x86_64"
	AppVersion string `json:"app_version"` // Application.version of the build (optional)
	AppBuild   string `json:"app_build"`   // version code (optional)
	Functions  uint64 `json:"functions"`
	Lines      uint64 `json:"lines"`
	LineMap    bool   `json:"line_map"`   // C# lines from LineNumberMappings.json
	MethodMap  bool   `json:"method_map"` // C# names from MethodMap.tsv
	Source     string `json:"source"`     // what the tool read, for the record
	Tool       string `json:"tool"`
}

// NormalizeBuildID lower-cases a build id and checks it is 8-80 hex digits.
func NormalizeBuildID(id string) (string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if len(id) < 8 || len(id) > MaxBuildIDLength {
		return "", fmt.Errorf("build_id must be 8-%d hex digits", MaxBuildIDLength)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", errors.New("build_id must be hex digits")
		}
	}
	return id, nil
}

// ValidUploadID reports whether id is 32 lowercase hex digits.
func ValidUploadID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Arches are the accepted values of Commit.Arch.
var Arches = map[string]bool{"arm64": true, "arm": true, "x86": true, "x86_64": true}

// Validate checks a commit and cleans its texts.
func (c *Commit) Validate() error {
	id, err := NormalizeBuildID(c.BuildID)
	if err != nil {
		return err
	}
	c.BuildID = id
	if !ValidUploadID(c.UploadID) {
		return errors.New("upload_id must be 32 lowercase hex digits")
	}
	c.Module = Clean(c.Module, MaxModuleLength)
	if c.Module == "" || strings.ContainsAny(c.Module, "/\\") {
		return errors.New("module must be a library file name such as libil2cpp.so")
	}
	if !Arches[c.Arch] {
		return errors.New("arch must be arm64, arm, x86 or x86_64")
	}
	c.AppVersion = Clean(c.AppVersion, MaxVersionLength)
	c.AppBuild = Clean(c.AppBuild, MaxVersionLength)
	c.Source = Clean(c.Source, 1024)
	c.Tool = Clean(c.Tool, 128)
	if c.Functions == 0 {
		return errors.New("an upload without functions is useless")
	}
	if c.Functions > MaxFunctionsPerBuild || c.Lines > MaxLinesPerBuild {
		return fmt.Errorf("at most %d functions and %d lines per upload", MaxFunctionsPerBuild, MaxLinesPerBuild)
	}
	return nil
}

// Decode validates a part and returns its rows; texts are cleaned and cut.
func (p *Part) Decode() ([]Func, []Line, error) {
	id, err := NormalizeBuildID(p.BuildID)
	if err != nil {
		return nil, nil, err
	}
	p.BuildID = id
	if !ValidUploadID(p.UploadID) {
		return nil, nil, errors.New("upload_id must be 32 lowercase hex digits")
	}
	if p.Part < 0 || p.Part > 100_000 {
		return nil, nil, errors.New("part out of range")
	}
	if len(p.Funcs)+len(p.Lines) > MaxPartRows {
		return nil, nil, fmt.Errorf("at most %d rows per part", MaxPartRows)
	}
	funcs := make([]Func, 0, len(p.Funcs))
	for i, row := range p.Funcs {
		if len(row) != 4 {
			return nil, nil, fmt.Errorf("funcs[%d]: want [addr, size, name, raw_name]", i)
		}
		addr, ok1 := number(row[0])
		size, ok2 := number(row[1])
		name, ok3 := row[2].(string)
		raw, ok4 := row[3].(string)
		if !ok1 || !ok2 || !ok3 || !ok4 || addr >= MaxAddress || size > MaxFunctionSize {
			return nil, nil, fmt.Errorf("funcs[%d]: invalid row", i)
		}
		f := Func{Addr: addr, Size: size, Name: Clean(name, MaxNameLength), RawName: Clean(raw, MaxNameLength)}
		if f.Name == "" {
			f.Name = f.RawName
		}
		if f.Name == "" {
			return nil, nil, fmt.Errorf("funcs[%d]: no name", i)
		}
		funcs = append(funcs, f)
	}
	lines := make([]Line, 0, len(p.Lines))
	for i, row := range p.Lines {
		if len(row) != 3 {
			return nil, nil, fmt.Errorf("lines[%d]: want [addr, file, line]", i)
		}
		addr, ok1 := number(row[0])
		file, ok2 := row[1].(string)
		ln, ok3 := number(row[2])
		if !ok1 || !ok2 || !ok3 || addr >= MaxAddress || ln > 1<<31 {
			return nil, nil, fmt.Errorf("lines[%d]: invalid row", i)
		}
		l := Line{Addr: addr, File: Clean(file, MaxFileLength), Line: uint32(ln)}
		if l.File == "" {
			l.Line = 0
		}
		lines = append(lines, l)
	}
	return funcs, lines, nil
}

// number accepts a JSON number that is a non-negative integer (decoded as
// float64 by encoding/json, exact below 2^53, which MaxAddress is).
func number(v interface{}) (uint64, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || f != float64(uint64(f)) || f > 1<<53 {
		return 0, false
	}
	return uint64(f), true
}

// EncodeFuncs and EncodeLines build a part's arrays (the tool).
func EncodeFuncs(fs []Func) [][]interface{} {
	out := make([][]interface{}, len(fs))
	for i, f := range fs {
		out[i] = []interface{}{f.Addr, f.Size, f.Name, f.RawName}
	}
	return out
}

func EncodeLines(ls []Line) [][]interface{} {
	out := make([][]interface{}, len(ls))
	for i, l := range ls {
		out[i] = []interface{}{l.Addr, l.File, l.Line}
	}
	return out
}

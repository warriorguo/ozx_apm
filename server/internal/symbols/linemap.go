package symbols

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// LineMap is IL2CPP's LineNumberMappings.json: for every generated C++ file,
// which C# file and line each "//<source_info:File.cs:Line>" comment it
// wrote stands for. IL2CPP writes it with "--emit-source-mapping" (Unity
// passes that when Cloud Diagnostics crash reporting is on, or through
// PlayerSettings.SetAdditionalIl2CppArgs). The file is
//
//	{ "<cpp file>" : { "<cs file>" : { "<cpp line>" : <cs line>, ... }, ... }, ... }
//
// The C++ lines are those of the comments: the code of that C# statement
// follows on the next lines, up to the next comment. Lookup returns the
// mapping of the nearest comment at or above a C++ line.
type LineMap struct {
	files    map[string][]*cppFile // by base name
	csFiles  []string
	csIndex  map[string]int32
	Mappings int
}

type cppFile struct {
	path    string
	entries []lineEntry // sorted by cppLine
}

type lineEntry struct {
	cppLine uint32
	cs      int32
	csLine  uint32
}

// ReadLineMap parses LineNumberMappings.json (streaming: the file can be
// hundreds of MB for a large game).
func ReadLineMap(r io.Reader) (*LineMap, error) {
	lm := &LineMap{files: map[string][]*cppFile{}, csIndex: map[string]int32{}}
	pkgs := packageResolver{}
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	for dec.More() {
		cppPath, err := stringToken(dec)
		if err != nil {
			return nil, err
		}
		cf := &cppFile{path: normalizePath(cppPath)}
		if err := expectDelim(dec, '{'); err != nil {
			return nil, err
		}
		for dec.More() {
			csPath, err := stringToken(dec)
			if err != nil {
				return nil, err
			}
			cs := lm.csFile(pkgs.sourcePath(csPath))
			if err := expectDelim(dec, '{'); err != nil {
				return nil, err
			}
			for dec.More() {
				key, err := stringToken(dec)
				if err != nil {
					return nil, err
				}
				tok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				num, ok := tok.(json.Number)
				if !ok {
					return nil, fmt.Errorf("LineNumberMappings: line of %q is not a number", key)
				}
				cppLine, err1 := strconv.ParseUint(key, 10, 32)
				csLine, err2 := strconv.ParseUint(num.String(), 10, 32)
				if err1 != nil || err2 != nil {
					continue
				}
				cf.entries = append(cf.entries, lineEntry{uint32(cppLine), cs, uint32(csLine)})
			}
			if err := expectDelim(dec, '}'); err != nil {
				return nil, err
			}
		}
		if err := expectDelim(dec, '}'); err != nil {
			return nil, err
		}
		if len(cf.entries) == 0 {
			continue
		}
		sort.Slice(cf.entries, func(i, j int) bool { return cf.entries[i].cppLine < cf.entries[j].cppLine })
		lm.Mappings += len(cf.entries)
		base := path.Base(cf.path)
		lm.files[base] = append(lm.files[base], cf)
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	return lm, nil
}

func (lm *LineMap) csFile(p string) int32 {
	if i, ok := lm.csIndex[p]; ok {
		return i
	}
	i := int32(len(lm.csFiles))
	lm.csFiles = append(lm.csFiles, p)
	lm.csIndex[p] = i
	return i
}

func expectDelim(dec *json.Decoder, d json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("LineNumberMappings: %w", err)
	}
	if got, ok := tok.(json.Delim); !ok || got != d {
		return fmt.Errorf("LineNumberMappings: expected %q, got %v", d, tok)
	}
	return nil
}

func stringToken(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("LineNumberMappings: %w", err)
	}
	s, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("LineNumberMappings: expected a string, got %v", tok)
	}
	return s, nil
}

// file returns the mappings of a C++ file of the debug information: by its
// base name, and when several generated files share it, the one whose path
// shares the longest suffix with cppPath.
func (lm *LineMap) file(cppPath string) *cppFile {
	p := normalizePath(cppPath)
	cands := lm.files[path.Base(p)]
	switch len(cands) {
	case 0:
		return nil
	case 1:
		return cands[0]
	}
	best, bestLen := cands[0], -1
	for _, c := range cands {
		if n := commonSuffix(c.path, p); n > bestLen {
			best, bestLen = c, n
		}
	}
	return best
}

// HasFile reports whether the map has mappings for a C++ file.
func (lm *LineMap) HasFile(cppPath string) bool {
	return lm != nil && lm.file(cppPath) != nil
}

// Lookup returns the C# file and line of a C++ line: the mapping of the
// nearest comment at or above it, but not above minLine (the first line of
// the function the address is in: a comment above it belongs to another
// function). ok is false without one.
func (lm *LineMap) Lookup(cppPath string, line, minLine uint32) (file string, csLine uint32, ok bool) {
	if lm == nil {
		return "", 0, false
	}
	cf := lm.file(cppPath)
	if cf == nil {
		return "", 0, false
	}
	i := sort.Search(len(cf.entries), func(i int) bool { return cf.entries[i].cppLine > line }) - 1
	if i < 0 || cf.entries[i].cppLine < minLine {
		return "", 0, false
	}
	e := cf.entries[i]
	return lm.csFiles[e.cs], e.csLine, true
}

func commonSuffix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

func normalizePath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// SourcePath shortens a C# source path of the build machine to the part a
// developer recognizes, starting at "Assets/", "Packages/" or
// "Library/PackageCache/" (the build machine's directories are left out);
// other paths keep their file name only.
func SourcePath(p string) string {
	p = normalizePath(p)
	for _, marker := range []string{"/Library/PackageCache/", "/Assets/", "/Packages/"} {
		if i := strings.LastIndex(p, marker); i >= 0 {
			return Clean(p[i+1:], MaxFileLength)
		}
	}
	for _, prefix := range []string{"Assets/", "Packages/", "Library/PackageCache/"} {
		if strings.HasPrefix(p, prefix) {
			return Clean(p, MaxFileLength)
		}
	}
	return Clean(path.Base(p), MaxFileLength)
}

// packageResolver gives the C# files of a UPM package referenced from
// outside the project ("file:../sdk" in manifest.json: its sources are
// neither under Assets/ nor Packages/) the path Unity shows for them,
// "Packages/<package name>/<path in the package>", instead of a bare file
// name. The tool runs on the build machine, so the package's package.json
// is found next to the sources. Results are cached per directory.
type packageResolver map[string]string // directory -> "Packages/<name>/<rel dir>" ("" none)

const maxPackageDepth = 16

func (r packageResolver) sourcePath(p string) string {
	n := normalizePath(p)
	for _, marker := range []string{"/Library/PackageCache/", "/Assets/", "/Packages/"} {
		if strings.Contains(n, marker) {
			return SourcePath(p)
		}
	}
	for _, prefix := range []string{"Assets/", "Packages/", "Library/PackageCache/"} {
		if strings.HasPrefix(n, prefix) {
			return SourcePath(p)
		}
	}
	if !path.IsAbs(n) && !filepath.IsAbs(p) {
		return SourcePath(p)
	}
	dir := r.dir(path.Dir(n))
	if dir == "" {
		return SourcePath(p)
	}
	return Clean(dir+"/"+path.Base(n), MaxFileLength)
}

// dir returns "Packages/<name>/<rel>" for a directory inside a package, "" when it is in none.
func (r packageResolver) dir(d string) string {
	if v, ok := r[d]; ok {
		return v
	}
	v := ""
	cur, rel := d, ""
	for i := 0; i < maxPackageDepth; i++ {
		if name := packageName(filepath.FromSlash(cur + "/package.json")); name != "" {
			v = "Packages/" + name
			if rel != "" {
				v += "/" + rel
			}
			break
		}
		parent := path.Dir(cur)
		if parent == cur {
			break
		}
		if rel == "" {
			rel = path.Base(cur)
		} else {
			rel = path.Base(cur) + "/" + rel
		}
		cur = parent
	}
	r[d] = v
	return v
}

// packageName reads the "name" of a UPM package.json ("" when the file is
// missing, unreadable or not a package: a name like "com.company.package").
func packageName(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&pkg); err != nil {
		return ""
	}
	if !validPackageName(pkg.Name) {
		return ""
	}
	return pkg.Name
}

func validPackageName(n string) bool {
	if n == "" || len(n) > 214 || !strings.Contains(n, ".") {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

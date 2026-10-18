package symbols

import (
	"debug/dwarf"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// Library is what the tool extracts from one ELF file of a build.
type Library struct {
	Path    string
	Module  string // the library's name on the device ("libil2cpp.so" for libil2cpp.sym.so / .dbg.so)
	BuildID string // lowercase hex of the GNU build id note
	Arch    string
	Funcs   []Func // sorted by address
	Lines   []Line // sorted by address; empty without DWARF or line map
	// HasDWARF: the file has line information (.debug_line).
	HasDWARF bool
	// MethodMapNames: functions named from MethodMap.tsv.
	MethodMapNames int
	// IL2CPPMethods: functions that are IL2CPP-generated C# methods.
	IL2CPPMethods int
	// LineRows: DWARF line rows read; MappedRows: of those, mapped to C#.
	LineRows, MappedRows int
}

// ModuleName is the library's name on the device for a symbol file name:
// "libil2cpp.sym.so", "libil2cpp.dbg.so", "libil2cpp.so.debug" and
// "libil2cpp.so" are all "libil2cpp.so".
func ModuleName(file string) string {
	b := filepath.Base(file)
	for _, suf := range []string{".sym.so", ".dbg.so"} {
		if strings.HasSuffix(b, suf) {
			return strings.TrimSuffix(b, suf) + ".so"
		}
	}
	for _, suf := range []string{".debug", ".sym", ".dbg"} {
		if strings.HasSuffix(b, ".so"+suf) {
			return strings.TrimSuffix(b, suf)
		}
	}
	return b
}

// IsELF reports whether r starts with the ELF magic.
func IsELF(r io.ReaderAt) bool {
	var m [4]byte
	_, err := r.ReadAt(m[:], 0)
	return err == nil && string(m[:]) == elf.ELFMAG
}

// BuildID returns the GNU build id of an ELF file (lowercase hex).
func BuildID(f *elf.File) (string, error) {
	read := func(data []byte) string {
		order := f.ByteOrder
		for len(data) >= 12 {
			namesz := order.Uint32(data[0:4])
			descsz := order.Uint32(data[4:8])
			typ := order.Uint32(data[8:12])
			off := 12
			nameEnd := off + int((namesz+3)&^3)
			descEnd := nameEnd + int((descsz+3)&^3)
			if namesz > 64 || descsz > 1024 || descEnd > len(data) {
				return ""
			}
			name := strings.TrimRight(string(data[off:off+int(namesz)]), "\x00")
			if name == "GNU" && typ == 3 && descsz > 0 {
				return hex.EncodeToString(data[nameEnd : nameEnd+int(descsz)])
			}
			data = data[descEnd:]
		}
		return ""
	}
	for _, s := range f.Sections {
		if s.Type != elf.SHT_NOTE {
			continue
		}
		data, err := s.Data()
		if err != nil {
			continue
		}
		if id := read(data); id != "" {
			return id, nil
		}
	}
	for _, p := range f.Progs {
		if p.Type != elf.PT_NOTE {
			continue
		}
		data := make([]byte, p.Filesz)
		if _, err := p.ReadAt(data, 0); err != nil {
			continue
		}
		if id := read(data); id != "" {
			return id, nil
		}
	}
	return "", errors.New("no GNU build id note")
}

func archOf(m elf.Machine) string {
	switch m {
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_386:
		return "x86"
	case elf.EM_X86_64:
		return "x86_64"
	}
	return ""
}

// ReadLibrary extracts the build id, the functions and, with DWARF line
// information and a line map, the C# lines of an ELF file. mm and lm may
// be nil.
func ReadLibrary(path string, r io.ReaderAt, mm MethodMap, lm *LineMap) (*Library, error) {
	f, err := elf.NewFile(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	lib := &Library{Path: path, Module: ModuleName(path), Arch: archOf(f.Machine)}
	if lib.Arch == "" {
		return nil, fmt.Errorf("%s: unsupported machine %v", path, f.Machine)
	}
	if lib.BuildID, err = BuildID(f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if lib.Funcs, err = readFuncs(f, mm, lib); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if lm != nil {
		if err := readLines(f, lm, lib); err != nil {
			return nil, fmt.Errorf("%s: line information: %w", path, err)
		}
	} else {
		lib.HasDWARF = f.Section(".debug_line") != nil
	}
	return lib, nil
}

// sttGNUIFunc is STT_GNU_IFUNC (debug/elf names it from Go 1.23; the module is 1.21).
const sttGNUIFunc = elf.SymType(10)

func readFuncs(f *elf.File, mm MethodMap, lib *Library) ([]Func, error) {
	syms, err := f.Symbols()
	if err != nil || len(syms) == 0 {
		dyn, derr := f.DynamicSymbols()
		if derr != nil && err != nil {
			return nil, fmt.Errorf("no symbol table (%v)", err)
		}
		syms = dyn
	}
	thumb := f.Machine == elf.EM_ARM
	type cand struct {
		Func
		rank int
	}
	var cands []cand
	for _, s := range syms {
		t := elf.ST_TYPE(s.Info)
		if t != elf.STT_FUNC && t != sttGNUIFunc {
			continue
		}
		if s.Value == 0 || s.Section == elf.SHN_UNDEF || s.Name == "" {
			continue
		}
		addr := s.Value
		if thumb {
			addr &^= 1
		}
		if addr >= MaxAddress {
			continue
		}
		raw := Clean(s.Name, MaxNameLength)
		rank := 0
		if elf.ST_BIND(s.Info) == elf.STB_GLOBAL {
			rank = 1
		}
		if IsIL2CPPMethod(raw) {
			rank = 2
		}
		if mm != nil && mm[s.Name] != "" {
			rank = 3
		}
		cands = append(cands, cand{Func{Addr: addr, Size: s.Size, RawName: raw}, rank})
	}
	if len(cands) == 0 {
		return nil, errors.New("no function symbols (is this a stripped library? upload the .sym.so or .dbg.so)")
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Addr != cands[j].Addr {
			return cands[i].Addr < cands[j].Addr
		}
		return cands[i].rank > cands[j].rank
	})
	out := make([]Func, 0, len(cands))
	for i, c := range cands {
		if i > 0 && c.Addr == cands[i-1].Addr {
			// Aliases (identical code folding): the best-ranked name, the largest size.
			if c.Size > out[len(out)-1].Size {
				out[len(out)-1].Size = c.Size
			}
			continue
		}
		fn := c.Func
		fn.Name = DisplayName(c.RawName, mm)
		if mm != nil && mm[c.RawName] != "" {
			lib.MethodMapNames++
		}
		if IsIL2CPPMethod(c.RawName) {
			lib.IL2CPPMethods++
		}
		out = append(out, fn)
	}
	// A size of 0 (some assembly symbols) reaches to the next function.
	for i := range out {
		if out[i].Size == 0 && i+1 < len(out) {
			if d := out[i+1].Addr - out[i].Addr; d <= MaxFunctionSize {
				out[i].Size = d
			}
		}
		if out[i].Size > MaxFunctionSize {
			out[i].Size = MaxFunctionSize
		}
	}
	return out, nil
}

// funcIndex returns the index of the function containing addr, -1 if none.
func funcIndex(fs []Func, addr uint64) int {
	i := sort.Search(len(fs), func(i int) bool { return fs[i].Addr > addr }) - 1
	if i < 0 || addr >= fs[i].Addr+fs[i].Size {
		return -1
	}
	return i
}

// readLines reads the DWARF line tables one sequence (normally one
// function) at a time and keeps, in address order, the addresses where the
// C# location changes, plus an entry with an empty file where a mapped
// range ends (code without a C# line, the end of a sequence).
func readLines(f *elf.File, lm *LineMap, lib *Library) error {
	if f.Section(".debug_line") == nil && f.Section(".zdebug_line") == nil {
		return nil
	}
	d, err := f.DWARF()
	if err != nil {
		return err
	}
	lib.HasDWARF = true
	type row struct {
		addr uint64
		file string
		line uint32
		fn   int
	}
	var seq []row
	var lines []Line
	type fnFile struct {
		fn   int
		file string
	}
	minLine := map[fnFile]uint32{}
	flush := func(end uint64) {
		if len(seq) == 0 {
			return
		}
		for k := range minLine {
			delete(minLine, k)
		}
		for _, r := range seq {
			if r.fn < 0 {
				continue
			}
			k := fnFile{r.fn, r.file}
			if m, ok := minLine[k]; !ok || r.line < m {
				minLine[k] = r.line
			}
		}
		last := Line{File: "\x00"} // nothing emitted yet in this sequence
		emit := func(l Line) {
			if l.File == last.File && l.Line == last.Line {
				return
			}
			if n := len(lines); n > 0 && lines[n-1].Addr == l.Addr {
				lines[n-1] = l // the last row at an address wins
			} else {
				if l.File == "" && last.File == "\x00" {
					return // nothing to end
				}
				lines = append(lines, l)
			}
			last = l
		}
		for _, r := range seq {
			var l Line
			l.Addr = r.addr
			if r.fn >= 0 {
				if file, csLine, ok := lm.Lookup(r.file, r.line, minLine[fnFile{r.fn, r.file}]); ok {
					l.File, l.Line = file, csLine
					lib.MappedRows++
				}
			}
			emit(l)
		}
		if last.File != "" && last.File != "\x00" {
			emit(Line{Addr: end})
		}
		seq = seq[:0]
	}

	rd := d.Reader()
	for {
		e, err := rd.Next()
		if err != nil {
			return err
		}
		if e == nil {
			break
		}
		if e.Tag != dwarf.TagCompileUnit {
			rd.SkipChildren()
			continue
		}
		lr, err := d.LineReader(e)
		rd.SkipChildren()
		if err != nil {
			return err
		}
		if lr == nil {
			continue
		}
		var le dwarf.LineEntry
		for {
			if err := lr.Next(&le); err != nil {
				if err == io.EOF {
					break
				}
				return err
			}
			if le.EndSequence {
				flush(le.Address)
				continue
			}
			lib.LineRows++
			if le.File == nil || le.Line <= 0 || le.Address >= MaxAddress {
				continue
			}
			name := le.File.Name
			fn := -1
			if lm.HasFile(name) {
				fn = funcIndex(lib.Funcs, le.Address)
			}
			seq = append(seq, row{le.Address, name, uint32(le.Line), fn})
		}
		flush(0)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Addr < lines[j].Addr })
	// Rows of overlapping sequences at the same address: keep the last.
	out := lines[:0]
	for _, l := range lines {
		if n := len(out); n > 0 && out[n-1].Addr == l.Addr {
			out[n-1] = l
			continue
		}
		out = append(out, l)
	}
	lib.Lines = out
	return nil
}

// Lookup resolves an address against the library's own tables (the tool's
// "lookup" command, and the tests): the function and, when known, the C# line.
func (lib *Library) Lookup(addr uint64) (fn Func, line Line, ok bool) {
	i := funcIndex(lib.Funcs, addr)
	if i < 0 {
		return Func{}, Line{}, false
	}
	fn = lib.Funcs[i]
	j := sort.Search(len(lib.Lines), func(j int) bool { return lib.Lines[j].Addr > addr }) - 1
	if j >= 0 && lib.Lines[j].Addr >= fn.Addr && lib.Lines[j].File != "" {
		line = lib.Lines[j]
	}
	return fn, line, true
}

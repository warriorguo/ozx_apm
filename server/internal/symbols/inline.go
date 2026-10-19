package symbols

import (
	"debug/dwarf"
	"sort"
)

// inlineSite is the address range of an outermost inlined call
// (DW_TAG_inlined_subroutine directly in a function body, not inside another
// inlined call) and where the enclosing function makes that call.
//
// The DWARF line rows of inlined code are the lines of the inlined
// function's body. In IL2CPP output that body is somewhere else in the C++
// file (Action_Invoke_..._inline, a property getter, a runtime helper), under
// the "//<source_info>" comment of a different C# method. Mapping those rows
// gives the caller another method's C# file and line (S1 on device:
// ApmTestDriver.UpdateApm7 resolved to SessionManager.cs:30 at its
// `a?.Invoke()`). A frame is named after the function its address is in,
// so its line must be that function's own line: the call site.
type inlineSite struct {
	low, high uint64
	file      string // "" when the call site is not recorded
	line      uint32
}

type inlineSites []inlineSite

// at returns the outermost inlined call that contains addr.
func (s inlineSites) at(addr uint64) (inlineSite, bool) {
	i := sort.Search(len(s), func(i int) bool { return s[i].low > addr }) - 1
	if i < 0 || addr >= s[i].high {
		return inlineSite{}, false
	}
	return s[i], true
}

// readInlineSites reads the children of a compile unit (rd is positioned just
// after cu) up to the end of the unit, and returns its outermost inlined
// calls sorted by address. files is the unit's line table file list
// (DW_AT_call_file indexes it).
func readInlineSites(d *dwarf.Data, rd *dwarf.Reader, cu *dwarf.Entry, files []*dwarf.LineFile) (inlineSites, error) {
	var out inlineSites
	if !cu.Children {
		return out, nil
	}
	depth := 1
	for depth > 0 {
		e, err := rd.Next()
		if err != nil {
			return nil, err
		}
		if e == nil {
			break
		}
		switch e.Tag {
		case 0: // the end of a list of children
			depth--
			continue
		case dwarf.TagInlinedSubroutine:
			ranges, err := d.Ranges(e)
			if err != nil {
				return nil, err
			}
			site := inlineSite{}
			if idx, ok := e.Val(dwarf.AttrCallFile).(int64); ok && idx >= 0 && idx < int64(len(files)) && files[idx] != nil {
				site.file = files[idx].Name
			}
			if l, ok := e.Val(dwarf.AttrCallLine).(int64); ok && l > 0 && l <= 1<<31 {
				site.line = uint32(l)
			}
			for _, r := range ranges {
				if r[1] > r[0] {
					s := site
					s.low, s.high = r[0], r[1]
					out = append(out, s)
				}
			}
			// Calls inlined into this one are inside its ranges: the
			// outermost call site is the one that belongs to the function.
			rd.SkipChildren()
			continue
		case dwarf.TagStructType, dwarf.TagClassType, dwarf.TagUnionType, dwarf.TagEnumerationType,
			dwarf.TagTypedef, dwarf.TagBaseType, dwarf.TagPointerType, dwarf.TagArrayType,
			dwarf.TagSubroutineType, dwarf.TagVariable, dwarf.TagFormalParameter:
			// No code below these.
			rd.SkipChildren()
			continue
		}
		if e.Children {
			depth++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].low < out[j].low })
	return out, nil
}

// rawRow is a DWARF line row (file "" or line 0: no line).
type rawRow struct {
	addr uint64
	file string
	line uint32
	// own: for a row replaced by a call site in the same file, the row's
	// own line (at a function's entry, its declaration line).
	own uint32
}

// attribute rewrites the rows of one line sequence (raw, sorted by address,
// ending at end; 0: at the last row) so that every address inside an
// outermost inlined call gets the call site, and the rows split where an
// inlined call starts and ends (a row's range does not end there by
// itself: the code after an inlined call keeps the row's own line).
// inlined counts the rows that were inside an inlined call.
func (s inlineSites) attribute(raw []rawRow, end uint64) (out []rawRow, inlined int) {
	if len(s) == 0 || len(raw) == 0 {
		return raw, 0
	}
	start := raw[0].addr
	if end <= start {
		end = raw[len(raw)-1].addr + 1
	}
	// Where inlined calls start and end inside the sequence.
	var bounds []uint64
	for i := sort.Search(len(s), func(i int) bool { return s[i].high > start }); i < len(s) && s[i].low < end; i++ {
		if s[i].low > start {
			bounds = append(bounds, s[i].low)
		}
		if s[i].high < end {
			bounds = append(bounds, s[i].high)
		}
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	// Every raw row is kept (several rows at one address included: the
	// first row of a function, its declaration line, bounds the comments
	// that belong to it), with a row added at each bound inside a row's range.
	out = make([]rawRow, 0, len(raw)+len(bounds))
	add := func(r rawRow, real bool) {
		if site, ok := s.at(r.addr); ok {
			if real && r.file == site.file && r.line > 0 {
				r.own = r.line
			}
			r.file, r.line = site.file, site.line
		}
		out = append(out, r)
	}
	b := 0
	for i, r := range raw {
		for ; b < len(bounds) && bounds[b] <= r.addr; b++ {
			if i > 0 && bounds[b] < r.addr && bounds[b] > raw[i-1].addr && (b == 0 || bounds[b] != bounds[b-1]) {
				add(rawRow{addr: bounds[b], file: raw[i-1].file, line: raw[i-1].line}, false)
			}
		}
		if _, ok := s.at(r.addr); ok {
			inlined++
		}
		add(r, true)
	}
	last := raw[len(raw)-1]
	for ; b < len(bounds); b++ {
		if bounds[b] > last.addr && (b == 0 || bounds[b] != bounds[b-1]) {
			add(rawRow{addr: bounds[b], file: last.file, line: last.line}, false)
		}
	}
	return out, inlined
}

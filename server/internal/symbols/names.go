package symbols

import (
	"bufio"
	"io"
	"strings"
)

// Function names.
//
// IL2CPP compiles every C# method to a C function named
// "<Type>_<Method>_m<hash>" (e.g. "RoomLoader_LoadTemplate_m6F444615B7A27593A26813DB3252F8AA1ADBD2E1"),
// with '<' and '>' written as "U3C" / "U3E" and other characters not valid
// in C as '_'. Namespaces, generic arguments and parameter types are not in
// the name. IL2CPP can also write MethodMap.tsv ("--emit-method-map"): one
// line per method, "<C function>\t<C# method as Cecil prints it>\t<assembly>",
// which gives the exact C# name. DisplayName uses it when it has it, and the
// name itself otherwise. Other symbols (libil2cpp's runtime, libunity) are
// C++ names, shown demangled as far as DemangleItanium goes.

// MaxNameLength bounds a stored function name (bytes).
const MaxNameLength = 1024

// MethodMap maps IL2CPP's C function names to C# method names.
type MethodMap map[string]string

// ReadMethodMap reads MethodMap.tsv. Lines that do not have at least two
// tab-separated fields are skipped.
func ReadMethodMap(r io.Reader) (MethodMap, error) {
	m := MethodMap{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, '\t')
		if i <= 0 {
			continue
		}
		rest := line[i+1:]
		if j := strings.IndexByte(rest, '\t'); j >= 0 {
			rest = rest[:j]
		}
		if name := CSharpFromCecil(rest); name != "" {
			m[line[:i]] = name
		}
	}
	return m, sc.Err()
}

// CSharpFromCecil turns Cecil's method full name
// ("System.Void Game.Rooms.RoomLoader::LoadTemplate(System.Int32)") into
// "Game.Rooms.RoomLoader.LoadTemplate(System.Int32)": the return type is
// dropped, "::" becomes "." and a nested type's "/" becomes "+".
func CSharpFromCecil(full string) string {
	full = strings.TrimSpace(full)
	sep := strings.Index(full, "::")
	if sep < 0 {
		return ""
	}
	// The return type ends at the last space before "::" outside <...>.
	depth, start := 0, 0
	for i := 0; i < sep; i++ {
		switch full[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ' ':
			if depth == 0 {
				start = i + 1
			}
		}
	}
	typ := strings.ReplaceAll(full[start:sep], "/", "+")
	return typ + "." + full[sep+2:]
}

// il2cppHashSuffix returns the length of a trailing "_m<hex>" (8 to 40 hex
// digits), 0 if there is none.
func il2cppHashSuffix(name string) int {
	i := strings.LastIndex(name, "_m")
	if i <= 0 {
		return 0
	}
	hex := name[i+2:]
	if len(hex) < 8 || len(hex) > 40 {
		return 0
	}
	for j := 0; j < len(hex); j++ {
		c := hex[j]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'F') {
			return 0
		}
	}
	return len(name) - i
}

// IsIL2CPPMethod reports whether a symbol is an IL2CPP-generated C# method.
func IsIL2CPPMethod(name string) bool {
	return il2cppHashSuffix(name) > 0
}

// CSharpFromIL2CPPName guesses "Type.Method" from an IL2CPP C function name
// without MethodMap.tsv: the hash is removed, "U3C"/"U3E" become '<'/'>',
// and the name is split at the first '_' (a following "_<digits>" is the
// generic arity: "List_1_Add" -> "List`1.Add"; the method keeps its own
// underscores: "List_1_get_Item" -> "List`1.get_Item"). Names
// of nested types and types with '_' of their own split at the wrong place;
// MethodMap.tsv avoids the guess. "" if name is not an IL2CPP method.
func CSharpFromIL2CPPName(name string) string {
	n := il2cppHashSuffix(name)
	if n == 0 {
		return ""
	}
	base := name[:len(name)-n]
	base = strings.ReplaceAll(base, "U3C", "<")
	base = strings.ReplaceAll(base, "U3E", ">")
	// The first single '_': compiler-generated type names have "__<n>" of
	// their own ("<Load>d__12").
	i := -1
	for j := 1; j < len(base)-1; j++ {
		if base[j] != '_' {
			continue
		}
		if base[j+1] == '_' {
			j += 2
			for j < len(base) && base[j] >= '0' && base[j] <= '9' {
				j++
			}
			j--
			continue
		}
		i = j
		break
	}
	if i <= 0 {
		return base
	}
	typ, rest := base[:i], base[i+1:]
	// Generic arity: "_1_", "_2_".
	for {
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 || j == len(rest) || rest[j] != '_' {
			break
		}
		typ += "`" + rest[:j]
		rest = rest[j+1:]
	}
	return typ + "." + rest
}

// DisplayName is the name stored for a symbol: the C# method from the
// method map, else the IL2CPP guess, else the demangled C++ name, else the
// symbol itself; cut to MaxNameLength.
func DisplayName(raw string, mm MethodMap) string {
	name := ""
	if mm != nil {
		name = mm[raw]
	}
	if name == "" {
		name = CSharpFromIL2CPPName(raw)
	}
	if name == "" {
		name = DemangleItanium(raw)
	}
	return Clean(name, MaxNameLength)
}

// DemangleItanium returns the qualified name of an Itanium C++ symbol
// ("_ZN6il2cpp2vm9Exception5RaiseEP15Il2CppException" -> "il2cpp::vm::Exception::Raise"),
// without parameter types, or the symbol unchanged when it is not mangled
// or uses something this reader does not handle.
func DemangleItanium(sym string) string {
	if !strings.HasPrefix(sym, "_Z") {
		return sym
	}
	s := sym[2:]
	if strings.HasPrefix(s, "L") { // internal linkage
		s = s[1:]
	}
	var parts []string
	if strings.HasPrefix(s, "N") {
		s = s[1:]
		// CV / ref qualifiers of a member function.
		for len(s) > 0 && (s[0] == 'K' || s[0] == 'V' || s[0] == 'r' || s[0] == 'R' || s[0] == 'O') {
			s = s[1:]
		}
		for len(s) > 0 && s[0] != 'E' {
			switch {
			case s[0] >= '0' && s[0] <= '9':
				name, rest, ok := sourceName(s)
				if !ok {
					return sym
				}
				parts = append(parts, name)
				s = rest
			case strings.HasPrefix(s, "St"):
				parts = append(parts, "std")
				s = s[2:]
			case s[0] == 'I':
				// Template arguments: skipped (balanced I...E).
				rest, ok := skipTemplateArgs(s)
				if !ok {
					return sym
				}
				if len(parts) > 0 {
					parts[len(parts)-1] += "<...>"
				}
				s = rest
			case strings.HasPrefix(s, "C1") || strings.HasPrefix(s, "C2") || strings.HasPrefix(s, "C3"):
				if len(parts) == 0 {
					return sym
				}
				parts = append(parts, parts[len(parts)-1])
				s = s[2:]
			case strings.HasPrefix(s, "D0") || strings.HasPrefix(s, "D1") || strings.HasPrefix(s, "D2"):
				if len(parts) == 0 {
					return sym
				}
				parts = append(parts, "~"+parts[len(parts)-1])
				s = s[2:]
			case s[0] == 'S' || s[0] == 'T':
				// Substitutions and template parameters need the full grammar.
				return sym
			default:
				return sym
			}
		}
		if len(parts) == 0 {
			return sym
		}
		return strings.Join(parts, "::")
	}
	if strings.HasPrefix(s, "St") {
		s = s[2:]
		parts = append(parts, "std")
	}
	name, _, ok := sourceName(s)
	if !ok {
		return sym
	}
	return strings.Join(append(parts, name), "::")
}

func sourceName(s string) (string, string, bool) {
	n, i := 0, 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		if n > 4096 {
			return "", s, false
		}
		i++
	}
	if i == 0 || n == 0 || i+n > len(s) {
		return "", s, false
	}
	name := s[i : i+n]
	if strings.HasPrefix(name, "_GLOBAL__N") {
		name = "(anonymous namespace)"
	}
	return name, s[i+n:], true
}

func skipTemplateArgs(s string) (string, bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'I', 'N':
			depth++
		case 'E':
			depth--
			if depth == 0 {
				return s[i+1:], true
			}
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			// A source name inside the arguments may contain 'I' or 'E'.
			_, rest, ok := sourceName(s[i:])
			if !ok {
				return s, false
			}
			i = len(s) - len(rest) - 1
		}
	}
	return s, false
}

// Clean cuts s to n bytes on a UTF-8 boundary, replaces invalid UTF-8 and
// control characters, and trims surrounding spaces. Every text stored from
// an upload goes through it.
func Clean(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) > n {
		for n > 0 && s[n]&0xC0 == 0x80 {
			n--
		}
		s = s[:n]
	}
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		b := []byte(s)
		for i, c := range b {
			if c < 0x20 || c == 0x7f {
				b[i] = ' '
			}
		}
		s = string(b)
	}
	return strings.TrimSpace(s)
}

package fingerprint

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Every rule below decides on ASCII characters only and copies anything else
// unchanged, so that the C# port (which sees UTF-16 code units where Go sees
// UTF-8 bytes) produces the same text. Lengths are counted in UTF-16 units.

const (
	templateSourceLength = 300 // of the first line, before normalization
	templateLength       = 200 // of the result
)

// NormalizeMethod reduces a frame's method to its identity (package comment,
// step 2): "Ns.Type:Publish<RoomEnteredEvent>" and "Ns.Type.Publish[T]" are
// both "Ns.Type.Publish"; "Ns.Type+<Start>b__5_0" is "Ns.Type+<Start>b".
func NormalizeMethod(m string) string {
	var b strings.Builder
	b.Grow(len(m))
	var prev byte
	for i := 0; i < len(m); i++ {
		c := m[i]
		switch {
		case c == '[':
			i = skipBracket(m, i, '[', ']')
			continue
		case c == '<' && (isAlnum(prev) || prev == '_' || prev >= 0x80):
			// generic arguments ("Publish<T>"); a '<' after a separator starts
			// a compiler-generated name ("<Start>b__0") and is kept.
			i = skipBracket(m, i, '<', '>')
			continue
		case c == '`':
			for i+1 < len(m) && isDigit(m[i+1]) {
				i++
			}
			continue
		case c == ' ' || c == '\t':
			continue
		case c == ':':
			c = '.'
		case c == '/':
			c = '+'
		}
		b.WriteByte(c)
		prev = c
	}
	return trimGeneratedSuffixes(b.String())
}

// skipBracket returns the index of the bracket closing the one at i (or the
// last index when it is not closed).
func skipBracket(s string, i int, open, close byte) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(s) - 1
}

// trimGeneratedSuffixes drops the trailing digits, '_' and '|' of every
// compiler-generated name segment (one that starts with '<'): the numbers
// change when unrelated code in the same class changes.
func trimGeneratedSuffixes(m string) string {
	if !strings.Contains(m, "<") {
		return m
	}
	var b strings.Builder
	b.Grow(len(m))
	start := 0
	for i := 0; i <= len(m); i++ {
		if i < len(m) && m[i] != '.' && m[i] != '+' {
			continue
		}
		seg := m[start:i]
		if strings.HasPrefix(seg, "<") {
			seg = strings.TrimRight(seg, "0123456789_|")
		}
		b.WriteString(seg)
		if i < len(m) {
			b.WriteByte(m[i])
		}
		start = i + 1
	}
	return b.String()
}

// ShortType returns a type name without namespace, nesting and generic
// arguments: "System.IO.InvalidDataException" -> "InvalidDataException".
func ShortType(t string) string {
	if i := strings.IndexAny(t, "`["); i >= 0 {
		t = t[:i]
	}
	if i := strings.LastIndexAny(t, ".+"); i >= 0 {
		t = t[i+1:]
	}
	return t
}

// Template normalizes the first line of a message (package comment): the
// same bug with a different roomId gets the same template.
func Template(msg string) string {
	s := truncUTF16(firstLine(msg), templateSourceLength)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		prevAlnum := i > 0 && isAlnum(s[i-1])
		switch {
		case (c == '\'' || c == '"' || c == '`') && !prevAlnum:
			if j := closingQuote(s, i); j > 0 {
				b.WriteByte(c)
				b.WriteByte('*')
				b.WriteByte(c)
				i = j + 1
				continue
			}
		case !prevAlnum && isGUIDAt(s, i):
			b.WriteString("<guid>")
			i += 36
			continue
		case !prevAlnum && c == '0' && i+2 < len(s) && (s[i+1] == 'x' || s[i+1] == 'X') && isHex(s[i+2]):
			j := i + 2
			for j < len(s) && isHex(s[j]) {
				j++
			}
			b.WriteString("<hex>")
			i = j
			continue
		}
		if !prevAlnum && isAlnum(c) {
			e := i
			for e < len(s) && isAlnum(s[e]) {
				e++
			}
			if isHexToken(s[i:e]) {
				b.WriteString("<hex>")
				i = e
				continue
			}
		}
		if isDigit(c) || c == '-' && !prevAlnum && i+1 < len(s) && isDigit(s[i+1]) {
			j := i + 1
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			for j+1 < len(s) && (s[j] == '.' || s[j] == ',') && isDigit(s[j+1]) {
				j++
				for j < len(s) && isDigit(s[j]) {
					j++
				}
			}
			b.WriteString("<n>")
			i = j
			continue
		}
		if c == '=' {
			b.WriteByte('=')
			e := i + 1
			if e < len(s) && !strings.ContainsRune(" \t'\"`", rune(s[e])) {
				for e < len(s) && !strings.ContainsRune(" \t,;)]}", rune(s[e])) {
					e++
				}
			}
			if e > i+1 {
				b.WriteString("<v>")
			}
			i = e
			continue
		}
		if c == ' ' || c == '\t' {
			if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
				b.WriteByte(' ')
			}
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return truncUTF16(strings.TrimRight(b.String(), " "), templateLength)
}

// closingQuote returns the index of the quote closing the one at i (same
// character, not followed by a letter or digit), or -1.
func closingQuote(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		if s[j] == q && (j+1 == len(s) || !isAlnum(s[j+1])) {
			return j
		}
	}
	return -1
}

// isGUIDAt reports whether a GUID (8-4-4-4-12 hex digits) starts at i and is
// not followed by a letter or digit.
func isGUIDAt(s string, i int) bool {
	if i+36 > len(s) {
		return false
	}
	for k := 0; k < 36; k++ {
		c := s[i+k]
		if k == 8 || k == 13 || k == 18 || k == 23 {
			if c != '-' {
				return false
			}
		} else if !isHex(c) {
			return false
		}
	}
	return i+36 == len(s) || !isAlnum(s[i+36])
}

// isHexToken reports whether a word is a hash-like hex string: at least 8
// hex digits, with both a digit and a letter (pure numbers are numbers).
func isHexToken(t string) bool {
	if len(t) < 8 {
		return false
	}
	digit, letter := false, false
	for i := 0; i < len(t); i++ {
		switch c := t[i]; {
		case isDigit(c):
			digit = true
		case isHex(c):
			letter = true
		default:
			return false
		}
	}
	return digit && letter
}

// truncUTF16 cuts s to at most n UTF-16 code units, without splitting a
// surrogate pair: the same cut as C# Substring with that adjustment.
func truncUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 && r <= utf8.MaxRune {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// Hash is FNV-1a 64 of the UTF-8 bytes of s, as 16 lowercase hex digits.
func Hash(s string) string {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	out := strconv.FormatUint(h, 16)
	return strings.Repeat("0", 16-len(out)) + out
}

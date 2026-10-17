package fingerprint

import "strings"

// Markers of Exception.ToString() and of Unity's exception formatting.
const (
	endOfInnerMarker  = "--- End of inner exception stack trace ---"
	endOfStackMarker  = "--- End of stack trace from previous location"
	aggregateInnerTag = "---> (Inner Exception #"
	aggregateInnerEnd = "<---"
	innerSeparator    = " ---> "
	unityRethrowAs    = "Rethrow as "
)

// NotInAppPrefixes are the namespaces of engine, runtime and library code:
// frames whose method starts with one of them are not in-app.
var NotInAppPrefixes = []string{
	"System.", "Mono.", "Microsoft.", "UnityEngine.", "UnityEditor.", "Unity.",
	"TMPro.", "Newtonsoft.", "OzxApm.",
}

func isInApp(method string) bool {
	for _, p := range NotInAppPrefixes {
		if strings.HasPrefix(method, p) {
			return false
		}
	}
	return true
}

// splitLines splits on "\n" and drops a trailing "\r" from every line.
func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// parseExceptionText parses the output of Exception.ToString() (Mono, IL2CPP
// or .NET): the header "Type: message ---> Inner: message", then the frames of
// the innermost exception, then, after each "--- End of inner exception stack
// trace ---", those of the next outer one. The result is outermost first.
func parseExceptionText(text string) []Exception {
	lines := splitLines(text)
	i := 0
	var header []string
	for ; i < len(lines); i++ {
		if isMarker(lines[i]) {
			break
		}
		if _, ok := parseFrame(lines[i]); ok {
			break
		}
		header = append(header, lines[i])
	}
	var chain []Exception
	for _, part := range strings.Split(strings.Join(header, "\n"), innerSeparator) {
		if len(chain) == maxChain {
			break
		}
		h, _ := parseHeader(part)
		chain = append(chain, h)
	}

	sections := [][]Frame{nil}
	var extras []Exception
	inExtra := false
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, endOfInnerMarker):
			if !inExtra {
				sections = append(sections, nil)
			}
			continue
		case strings.HasPrefix(t, endOfStackMarker):
			continue
		case strings.HasPrefix(t, aggregateInnerTag):
			// "---> (Inner Exception #1) Type: message", frames, "... <---"
			inExtra = true
			rest := t[len(aggregateInnerTag):]
			if j := strings.Index(rest, ") "); j >= 0 {
				rest = rest[j+2:]
			}
			ended := strings.HasSuffix(rest, aggregateInnerEnd)
			h, _ := parseHeader(strings.TrimSpace(strings.TrimSuffix(rest, aggregateInnerEnd)))
			if len(extras) < maxChain {
				extras = append(extras, h)
			}
			if ended {
				inExtra = false
			}
			continue
		}
		ended := false
		if inExtra && strings.HasSuffix(t, aggregateInnerEnd) {
			t = strings.TrimSpace(strings.TrimSuffix(t, aggregateInnerEnd))
			ended = true
		}
		if f, ok := parseFrame(t); ok {
			if inExtra {
				if n := len(extras); n > 0 && len(extras[n-1].Frames) < maxFramesPerException {
					extras[n-1].Frames = append(extras[n-1].Frames, f)
				}
			} else if s := len(sections) - 1; len(sections[s]) < maxFramesPerException {
				sections[s] = append(sections[s], f)
			}
		}
		if ended {
			inExtra = false
		}
	}

	// The last section belongs to the outermost exception, the one before it
	// to the first inner exception, and so on. Surplus sections (a header cut
	// off by truncation) go to the innermost exception.
	n, m := len(chain), len(sections)
	for k := 0; k < m; k++ {
		idx := m - 1 - k // section index, from the last
		target := k
		if target >= n {
			target = n - 1
		}
		chain[target].Frames = append(sections[idx], chain[target].Frames...)
	}

	// AggregateException lists its inner exceptions again after the outer
	// frames; add those that are not in the chain already.
	for _, e := range extras {
		if len(chain) == maxChain {
			break
		}
		dup := false
		for _, c := range chain {
			if c.Type == e.Type && c.Message == e.Message {
				dup = true
				break
			}
		}
		if !dup {
			chain = append(chain, e)
		}
	}
	for i := range chain {
		if chain[i].Frames == nil {
			chain[i].Frames = []Frame{}
		}
	}
	return chain
}

// parseUnityException parses an exception as Unity reports it to log
// callbacks: the message is "Type: message" of the innermost exception, and
// the stack has the frames of the innermost exception, then for each outer
// exception a line "Rethrow as Type: message" followed by its frames
// (UnityEngine.StackTraceUtility.ExtractStringFromExceptionInternal). The
// result is outermost first.
func parseUnityException(message, stack string) []Exception {
	head, _ := parseHeader(message)
	innerToOuter := []Exception{head}
	sections := [][]Frame{nil}
	for _, l := range splitLines(stack) {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, unityRethrowAs) {
			if len(innerToOuter) < maxChain {
				h, _ := parseHeader(t[len(unityRethrowAs):])
				innerToOuter = append(innerToOuter, h)
				sections = append(sections, nil)
			}
			continue
		}
		if f, ok := parseFrame(t); ok {
			if s := len(sections) - 1; len(sections[s]) < maxFramesPerException {
				sections[s] = append(sections[s], f)
			}
		}
	}
	chain := make([]Exception, len(innerToOuter))
	for k, e := range innerToOuter {
		e.Frames = sections[k]
		if e.Frames == nil {
			e.Frames = []Frame{}
		}
		chain[len(innerToOuter)-1-k] = e
	}
	return chain
}

// parseStackFrames parses the frames of a stack (Unity's stack of a log call).
func parseStackFrames(stack string) []Frame {
	var frames []Frame
	for _, l := range splitLines(stack) {
		if len(frames) == maxFramesPerException {
			break
		}
		if f, ok := parseFrame(l); ok {
			frames = append(frames, f)
		}
	}
	return frames
}

func isMarker(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, endOfInnerMarker) || strings.HasPrefix(t, endOfStackMarker) ||
		strings.HasPrefix(t, aggregateInnerTag)
}

// parseHeader splits "Type: message" (or a bare "Type"). ok is false when the
// text does not start with a type name; the whole text is then the message.
func parseHeader(text string) (Exception, bool) {
	text = strings.TrimSpace(text)
	first := text
	rest := ""
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first, rest = strings.TrimRight(text[:i], "\r"), text[i+1:]
	}
	if i := strings.Index(first, ": "); i > 0 && isTypeName(first[:i]) {
		return Exception{Type: first[:i], Message: strings.TrimSpace(text[i+2:]), Frames: []Frame{}}, true
	}
	if t := strings.TrimSuffix(strings.TrimSpace(first), ":"); isTypeName(t) {
		return Exception{Type: t, Message: strings.TrimSpace(rest), Frames: []Frame{}}, true
	}
	return Exception{Message: text, Frames: []Frame{}}, false
}

// isTypeName reports whether s looks like a (possibly namespaced) type name.
func isTypeName(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isLetter(c) || c == '_' || c >= 0x80:
		case isDigit(c) || c == '.' || c == '+' || c == '`':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// maxEmbeddedCandidates bounds how many type names FindEmbedded tries to
// parse as an exception (each try parses the rest of the message).
const maxEmbeddedCandidates = 8

// FindEmbedded returns the index in msg where an embedded Exception.ToString()
// starts ("System.InvalidOperationException: ..." followed by stack frames or
// an inner exception, see hasExceptionBody), or -1. Only a type name ending in "Exception" counts,
// at the start of the message or after a space, a colon or an opening
// bracket or quote.
func FindEmbedded(msg string) int {
	msg = capBytes(msg, maxParseBytes)
	from := 0
	for tries := 0; tries < maxEmbeddedCandidates; {
		j := strings.Index(msg[from:], "Exception")
		if j < 0 {
			return -1
		}
		j += from
		end := j + len("Exception")
		from = end
		if end < len(msg) {
			c := msg[end]
			if c != ':' && c != '\r' && c != '\n' && !strings.HasPrefix(msg[end:], innerSeparator) {
				continue
			}
		}
		s := j
		for s > 0 && isTypeByte(msg[s-1]) {
			s--
		}
		if !isTypeName(msg[s:end]) {
			continue
		}
		if s > 0 && !isBoundary(msg[s-1]) {
			continue
		}
		tries++
		if hasExceptionBody(msg[s:]) {
			return s
		}
	}
	return -1
}

// hasExceptionBody reports whether text, which starts with an exception
// type, continues like Exception.ToString(): a stack frame on some line, an
// inner exception (" ---> " before the first frame or marker line), or an
// AggregateException inner block. It stops at the first frame.
func hasExceptionBody(text string) bool {
	inHeader := true
	for len(text) > 0 {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, aggregateInnerTag):
			return true
		case strings.HasPrefix(t, endOfInnerMarker) || strings.HasPrefix(t, endOfStackMarker):
			inHeader = false
			continue
		case inHeader && strings.Contains(line, innerSeparator):
			return true
		}
		if _, ok := parseFrame(line); ok {
			return true
		}
	}
	return false
}

func isTypeByte(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_' || c == '.' || c == '+' || c == '`'
}

func isBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ':', '(', '[', '{', '"', '\'', '=':
		return true
	}
	return false
}

// parseFrame parses one stack line in any supported format (package comment).
func parseFrame(line string) (Frame, bool) {
	t := strings.TrimSpace(line)
	raw := t
	isAt := false
	if strings.HasPrefix(t, "at ") {
		t = strings.TrimSpace(t[3:])
		isAt = true
	}
	if strings.HasPrefix(t, "(wrapper ") {
		i := strings.Index(t, ") ")
		if i < 0 {
			return Frame{}, false
		}
		t = strings.TrimSpace(t[i+2:])
	}
	if t == "" || strings.HasPrefix(t, "...") || strings.HasPrefix(t, unityRethrowAs) {
		return Frame{}, false
	}
	var method, rest string
	if open := strings.IndexByte(t, '('); open >= 0 {
		method = strings.TrimSpace(t[:open])
		if close := matchParen(t, open); close >= 0 {
			rest = strings.TrimSpace(t[close+1:])
		}
	} else {
		if !isAt {
			return Frame{}, false
		}
		method, rest = t, ""
		if sp := strings.IndexByte(t, ' '); sp >= 0 {
			method, rest = t[:sp], strings.TrimSpace(t[sp+1:])
		}
	}
	if !looksLikeMethod(method) {
		return Frame{}, false
	}
	norm := NormalizeMethod(method)
	if norm == "" {
		return Frame{}, false
	}
	file, ln := parseLocation(rest)
	return Frame{Method: norm, File: file, Line: ln, InApp: isInApp(norm), Raw: raw}, true
}

// matchParen returns the index of the parenthesis closing the one at open, or -1.
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// looksLikeMethod reports whether m (the text before the argument list) is a
// method name: "Ns.Type.Method" or "Ns.Type:Method", without spaces outside
// generic brackets. It rejects message lines such as "Error: x (y)".
func looksLikeMethod(m string) bool {
	if m == "" || len(m) > 1024 || strings.Contains(m, ": ") {
		return false
	}
	depth := 0
	sep := false
	for i := 0; i < len(m); i++ {
		c := m[i]
		switch {
		case c == '[' || c == '<':
			depth++
		case c == ']' || c == '>':
			if depth > 0 {
				depth--
			}
		case depth > 0:
		case c == '.' || c == ':':
			sep = true
		case isLetter(c) || isDigit(c) || c >= 0x80:
		case c == '_' || c == '+' || c == '/' || c == '`' || c == '|' || c == '$' || c == '@' || c == '-' || c == ',':
		default:
			return false
		}
	}
	return sep
}

// parseLocation reads the file and line after a frame's argument list.
func parseLocation(rest string) (string, int) {
	switch {
	case strings.HasPrefix(rest, "[0x") || strings.HasPrefix(rest, "<0x"):
		// Mono / IL2CPP: "[0x0001a] in /path/File.cs:42", "in <hash>:0"
		if i := strings.Index(rest, " in "); i >= 0 {
			return splitFileLine(strings.TrimSpace(rest[i+4:]))
		}
	case strings.HasPrefix(rest, "in "):
		// .NET: "in C:\path\File.cs:line 42"
		loc := strings.TrimSpace(rest[3:])
		if k := strings.LastIndex(loc, ":line "); k >= 0 {
			n, _ := atoi(strings.TrimSpace(loc[k+6:]))
			return cleanFile(loc[:k]), n
		}
		return splitFileLine(loc)
	case strings.HasPrefix(rest, "(at ") && strings.HasSuffix(rest, ")"):
		// Unity: "(at Assets/File.cs:42)"
		return splitFileLine(rest[4 : len(rest)-1])
	}
	return "", 0
}

func splitFileLine(loc string) (string, int) {
	loc = strings.TrimSpace(loc)
	i := strings.LastIndexByte(loc, ':')
	if i < 0 {
		return cleanFile(loc), 0
	}
	n, ok := atoi(loc[i+1:])
	if !ok {
		return cleanFile(loc), 0
	}
	return cleanFile(loc[:i]), n
}

// cleanFile drops placeholders such as "<00000000000000000000000000000000>"
// (no symbols) and "<filename unknown>".
func cleanFile(f string) string {
	f = strings.TrimSpace(f)
	if strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">") {
		return ""
	}
	return f
}

func atoi(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isAlnum(c byte) bool  { return isLetter(c) || isDigit(c) }
func isHex(c byte) bool    { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

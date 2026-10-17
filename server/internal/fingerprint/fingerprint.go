// Package fingerprint parses the exceptions and error logs reported by the
// Unity SDK and computes the fingerprint that groups them (APM-4).
//
// The server computes the fingerprint from the raw text it stores (message,
// Unity stack, embedded exception), so the algorithm can change without an
// SDK release: bump Version, and events stored from then on group by the new
// fingerprint (RELEASE.md, APM-4, has the procedure). The SDK runs the same
// algorithm (sdk/Runtime/Collectors/ExceptionParser.cs) only to de-duplicate
// repeats on the device; the server keeps what the SDK sent as
// client_fingerprint. testdata/vectors.json holds cases both implementations
// are checked against.
//
// # Algorithm, version 1
//
// Input: the log type the SDK reported ("exception", "error", "assert"; ""
// from SDKs before APM-4), the log message, Unity's stack trace of the log
// call, and the exception the SDK found embedded in an error log's message
// (e.g. OZX's GameEventBus logs "[GameEventBus] Error handling event
// RoomEnteredEvent: {ex}", which puts the handler's exception, with its type,
// message, stack and inner exceptions, into the message).
//
// 1. Classification.
//   - exception: an exception logged by Debug.LogException or unhandled. The
//     message is "Type: message" of the innermost exception and the stack has
//     Unity's "Rethrow as Outer: message" lines between the sections of an
//     inner exception chain (UnityEngine.StackTraceUtility).
//   - wrapped exception: an error or assert log whose message embeds the
//     text of Exception.ToString(); the embedded text is parsed (outer type
//     and message, " ---> " inner chain, frames per exception separated by
//     "--- End of inner exception stack trace ---", AggregateException's
//     "---> (Inner Exception #N)" blocks). The first in-app frame of Unity's
//     stack is kept as the call site (where the error was logged).
//   - plain error / assert: anything else.
//     For SDKs before APM-4 (no log type) the kind is inferred: a message that
//     starts with "SomethingException:" is an exception, otherwise an embedded
//     exception is searched for in the (500-character) message.
//
// 2. Frames. Every supported stack format is reduced to a method identity:
//   - Mono / IL2CPP Exception.ToString(): "at Ns.Type.Method (args) [0x0001a] in /path/File.cs:42"
//     ("in <00000000000000000000000000000000>:0" without symbols);
//   - .NET: "at Ns.Type.Method(args) in C:\path\File.cs:line 42";
//   - Unity: "Ns.Type:Method (args) (at Assets/File.cs:42)", "Ns.Type:Method(args)" (IL2CPP
//     without line numbers), "Ns.Type.Method (args) (at ...)".
//
// The method identity drops the arguments, the location, generic arguments
// ("Publish[T]", "Publish<RoomEnteredEvent>"), arity markers ("`2"), Mono's
// "(wrapper ...)" prefix, and the numbers the compiler appends to generated
// names ("<Start>b__5_0" -> "<Start>b", "<Load>d__12" -> "<Load>d"); "Type:Method"
// becomes "Type.Method" and "Outer/Inner" becomes "Outer+Inner". File and line
// are kept for display but never take part in the fingerprint. A frame is
// in-app unless its method starts with one of NotInAppPrefixes.
//
// 3. Fingerprint input ("\n"-separated):
//
//	exceptions (thrown or wrapped):
//	  "v1", "exception", short type of the outermost exception,
//	  short types of the inner exceptions joined by ">",
//	  then "f:" + the first 2 distinct in-app frames of the outermost
//	  exception (any frames if none is in-app), or, without frames,
//	  "m:" + the message template of the outermost exception.
//	plain errors / asserts:
//	  "v1", "error" or "assert", "m:" + message template of the first line,
//	  "f:" + the first in-app frame of the log call's stack.
//
// The short type is the name without namespace ("System.IO.InvalidDataException"
// and Unity's "InvalidDataException" are the same). Only two frames are used
// because IL2CPP release builds inline small methods, which removes frames
// that Mono shows; fewer frames keep Mono and IL2CPP stacks of the same code
// together, and the second frame still separates two handlers that call the
// same throwing helper.
//
// The message template (Template) replaces variable parts of a message:
// quoted text ('room_12', "x" -> '*'), GUIDs (<guid>), hex numbers and
// hashes (<hex>), numbers, also inside identifiers (room_12 -> room_<n>), and
// the value after "=" (roomId=forest -> roomId=<v>); whitespace is collapsed,
// and the result is cut to 200 characters (UTF-16 units, as in C#).
//
// 4. Fingerprint: FNV-1a 64 of the UTF-8 input, as 16 lowercase hex digits.
package fingerprint

import (
	"strconv"
	"strings"
)

// Version is the version of the algorithm implemented here, stored with every
// fingerprint (fingerprint_version; 0 = the SDK's fingerprint, before APM-4).
const Version = 1

// Log types as sent by the SDK (ExceptionEvent.log_type).
const (
	LogTypeException = "exception"
	LogTypeError     = "error"
	LogTypeAssert    = "assert"
)

// Limits. The parser reads at most maxParseBytes of each input and keeps at
// most maxFramesPerException frames per exception: enough for any real stack,
// and a bound on the work a hostile payload can cause.
const (
	maxParseBytes         = 64 * 1024
	maxFramesPerException = 200
	maxChain              = 20
	fingerprintFrames     = 2
)

// Input is what the SDK reported for one exception event.
type Input struct {
	LogType  string // "exception", "error", "assert"; "" from SDKs before APM-4
	Message  string // the log message (SDK: up to 500 characters)
	Stack    string // Unity's stack trace of the log call
	Embedded string // the exception text found in an error log's message, if any (SDK >= APM-4)
}

// Frame is one parsed stack frame.
type Frame struct {
	Method string `json:"method"` // normalized method identity (what the fingerprint uses)
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	InApp  bool   `json:"in_app"`
	Raw    string `json:"raw"` // the line as received, trimmed
}

// Exception is one exception of a chain.
type Exception struct {
	Type    string  `json:"type"` // as reported: full name from Exception.ToString(), short name from Unity
	Message string  `json:"message"`
	Frames  []Frame `json:"frames"`
}

// Result is the parsed event and its fingerprint.
type Result struct {
	// Kind is "exception" (thrown, or wrapped in an error log), "error" or "assert".
	Kind    string `json:"kind"`
	Wrapped bool   `json:"wrapped"` // the exception was embedded in an error/assert log
	// Chain is the exception and its inner exceptions, outermost first
	// (empty for plain errors and asserts).
	Chain []Exception `json:"chain"`
	// LogFrames is Unity's stack of the log call: for wrapped exceptions and
	// plain logs (for thrown exceptions the stack is the exception's own).
	LogFrames []Frame `json:"log_frames"`
	// MessageTemplate is the normalized message of the outermost exception, or
	// of the first line of a plain log message.
	MessageTemplate string `json:"message_template"`
	// ThrowSite is the first in-app frame of the outermost exception (where it
	// was thrown), CallSite the first in-app frame of the log call (where it
	// was logged; wrapped exceptions and plain logs), each "Method (at File:Line)".
	ThrowSite   string `json:"throw_site"`
	CallSite    string `json:"call_site"`
	Fingerprint string `json:"fingerprint"`
	Version     int    `json:"fingerprint_version"`
}

// Type returns the outermost exception type ("" for plain logs).
func (r *Result) Type() string {
	if len(r.Chain) == 0 {
		return ""
	}
	return r.Chain[0].Type
}

// InnerExceptions returns "Type: first line of message" for every inner
// exception, outermost first.
func (r *Result) InnerExceptions() []string {
	if len(r.Chain) < 2 {
		return []string{}
	}
	out := make([]string, 0, len(r.Chain)-1)
	for _, e := range r.Chain[1:] {
		s := e.Type
		if m := firstLine(e.Message); m != "" {
			s += ": " + m
		}
		out = append(out, s)
	}
	return out
}

// Analyze parses one exception event and computes its fingerprint.
func Analyze(in Input) Result {
	logType := normalizeLogType(in.LogType)
	legacy := in.LogType == ""
	msg := capBytes(in.Message, maxParseBytes)
	stack := capBytes(in.Stack, maxParseBytes)
	embedded := capBytes(in.Embedded, maxParseBytes)
	if legacy {
		msg = dropTruncatedLine(msg)
	}

	r := Result{Version: Version}
	switch {
	case logType == LogTypeException:
		r.Kind = LogTypeException
		r.Chain = parseUnityException(msg, stack)
	case embedded != "":
		r.Kind = LogTypeException
		r.Wrapped = true
		r.Chain = parseExceptionText(embedded)
		r.LogFrames = parseStackFrames(stack)
	case legacy && startsWithExceptionType(msg):
		r.Kind = LogTypeException
		r.Chain = parseUnityException(msg, stack)
	default:
		if legacy {
			if i := FindEmbedded(msg); i >= 0 {
				r.Kind = LogTypeException
				r.Wrapped = true
				r.Chain = parseExceptionText(msg[i:])
				r.LogFrames = parseStackFrames(stack)
				break
			}
		}
		r.Kind = LogTypeError
		if logType == LogTypeAssert {
			r.Kind = LogTypeAssert
		}
		r.LogFrames = parseStackFrames(stack)
	}

	if len(r.Chain) > 0 {
		r.MessageTemplate = Template(r.Chain[0].Message)
		r.ThrowSite = site(r.Chain[0].Frames)
	} else {
		r.MessageTemplate = Template(msg)
	}
	r.CallSite = site(r.LogFrames)
	if r.Chain == nil {
		r.Chain = []Exception{}
	}
	if r.LogFrames == nil {
		r.LogFrames = []Frame{}
	}
	r.Fingerprint = Hash(canonical(&r))
	return r
}

// canonical builds the fingerprint input (package comment, step 3).
func canonical(r *Result) string {
	var b strings.Builder
	b.WriteString("v" + strconv.Itoa(Version) + "\n")
	if len(r.Chain) > 0 {
		outer := r.Chain[0]
		b.WriteString(LogTypeException + "\n")
		b.WriteString(ShortType(outer.Type) + "\n")
		for i, e := range r.Chain[1:] {
			if i > 0 {
				b.WriteByte('>')
			}
			b.WriteString(ShortType(e.Type))
		}
		b.WriteByte('\n')
		frames := topFrames(outer.Frames, fingerprintFrames, true)
		if len(frames) == 0 {
			frames = topFrames(outer.Frames, fingerprintFrames, false)
		}
		if len(frames) > 0 {
			b.WriteString("f:" + strings.Join(frames, "\n"))
		} else {
			b.WriteString("m:" + r.MessageTemplate)
		}
		return b.String()
	}
	b.WriteString(r.Kind + "\n")
	b.WriteString("m:" + r.MessageTemplate + "\n")
	b.WriteString("f:" + strings.Join(topFrames(r.LogFrames, 1, true), "\n"))
	return b.String()
}

// topFrames returns the methods of the first n frames (in-app ones only if
// inApp), skipping a frame whose method equals the previous one (recursion).
func topFrames(frames []Frame, n int, inApp bool) []string {
	var out []string
	for _, f := range frames {
		if len(out) == n {
			break
		}
		if inApp && !f.InApp {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == f.Method {
			continue
		}
		out = append(out, f.Method)
	}
	return out
}

// site formats the first in-app frame for display, "" without one.
func site(frames []Frame) string {
	for _, f := range frames {
		if f.InApp {
			return FormatFrame(f)
		}
	}
	return ""
}

// FormatFrame renders a frame as "Method (at File:Line)", or "Method" without a file.
func FormatFrame(f Frame) string {
	if f.File == "" {
		return f.Method
	}
	if f.Line > 0 {
		return f.Method + " (at " + f.File + ":" + strconv.Itoa(f.Line) + ")"
	}
	return f.Method + " (at " + f.File + ")"
}

func normalizeLogType(t string) string {
	switch t {
	case LogTypeException, LogTypeError, LogTypeAssert:
		return t
	}
	return ""
}

// dropTruncatedLine removes the last, cut-off line of a message the SDK
// before APM-4 truncated to 500 characters ("..." appended): a half frame
// line would otherwise parse as a different method.
func dropTruncatedLine(msg string) string {
	if len(msg) < 497 || !strings.HasSuffix(msg, "...") {
		return msg
	}
	if i := strings.LastIndexByte(msg, '\n'); i > 0 {
		return msg[:i]
	}
	return msg
}

// startsWithExceptionType reports whether a message starts with
// "SomethingException:" (or is just the type), the form of Unity's message
// for a logged exception.
func startsWithExceptionType(msg string) bool {
	h, ok := parseHeader(msg)
	return ok && strings.HasSuffix(ShortType(h.Type), "Exception")
}

// capBytes cuts s to at most n bytes on a UTF-8 boundary.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "\r \t")
}

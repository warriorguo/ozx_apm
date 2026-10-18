package fingerprint

import (
	"strconv"
	"strings"
)

// Crashes (APM-7)
//
// The SDK reports a crash of the previous process (native crash, ANR, Java
// crash, a kill by the system) on the next launch. Its stack is a text in
// the SDK's normalized form, one thread after the other, the crashed (or,
// for an ANR, the blocked) thread first:
//
//	Thread "main" tid=12345 crashed
//	#00 pc 000000000004e0dc /apex/com.android.runtime/lib64/bionic/libc.so (abort+164) (BuildId: 9c7a...)
//	#01 pc 00000000005417bc libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)
//	at com.unity3d.player.UnityPlayer.nativeRender(Native Method)
//	Thread "UnityMain" tid=12380
//	#00 pc ...
//
// A native frame is "#NN pc <hex> <module> [(<function>+<offset>)] [(BuildId: <hex>)]":
// pc is relative to the start of the module's ELF file (the tombstone's
// rel_pc), the module is the library's path for system libraries
// (/system/..., /apex/..., /vendor/...) and its file name for the app's own
// (libil2cpp.so, libunity.so: the install path is left out). The function
// is what the device knew (exported or system symbols); the app's stripped
// libraries have none and are symbolicated on the server (internal/symbols).
// The parser also accepts the forms of Android's text tombstones and ANR
// traces (leading spaces, "native: ", "(offset 0x...)", full paths, the
// thread header of a trace: `"main" prio=5 tid=1 Native`).
//
// # Crash fingerprint, version 1
//
// Input ("\n"-separated), FNV-1a 64 like exceptions:
//
//	"v1", "crash", the crash type, then by type:
//	  native: the signal name, then "f:" + up to 3 distinct frame keys of the
//	          crashed thread's in-app frames (any frames if none is in-app);
//	  anr:    "f:" + up to 3 distinct in-app frame keys of the "UnityMain"
//	          thread (where the game's code runs), else of the first thread,
//	          else its first 3 frames;
//	  java:   "m:" + the template of the exit description;
//	  others (oom, killed, unknown): "r:" + the exit reason.
//
// A frame key is "module!function" when the function is known (resolved
// from uploaded symbols, else as the device reported it; C# names reduced by
// NormalizeMethod, "(args)" removed), else "module+0x<pc> <build id>" (the
// first 16 digits, when the device reported one): without symbols a crash
// groups per build, since the addresses change with every build.
// Java frames are keyed by their method. In-app: native frames of the app's
// own libraries (no leading '/' or '['), Java frames outside the Android,
// Java, Kotlin and Unity player packages.

// Crash types as sent by the SDK.
const (
	CrashTypeNative  = "native"
	CrashTypeANR     = "anr"
	CrashTypeJava    = "java"
	CrashTypeOOM     = "oom"
	CrashTypeKilled  = "killed"
	CrashTypeUnknown = "unknown"
)

// Limits of the crash stack parser: a bound on the work a hostile payload causes.
const (
	maxCrashThreads         = 8
	maxCrashFramesPerThread = 256
	crashFingerprintFrames  = 3
)

// CrashFrame is one frame of a crash stack.
type CrashFrame struct {
	Index    int    `json:"index"`
	Native   bool   `json:"native"`
	PC       uint64 `json:"-"`
	Module   string `json:"module,omitempty"`   // as sent: a system path or the app library's file name
	Function string `json:"function,omitempty"` // native: what the device knew; Java: the method
	Offset   uint64 `json:"-"`                  // native: offset into Function
	BuildID  string `json:"build_id,omitempty"` // lowercase hex
	File     string `json:"file,omitempty"`     // Java: "Bar.java"
	Line     int    `json:"line,omitempty"`
	InApp    bool   `json:"in_app"`
	Raw      string `json:"raw"`
}

// ModuleName is the file name of the frame's module ("libil2cpp.so").
func (f CrashFrame) ModuleName() string {
	return ModuleBaseName(f.Module)
}

// ModuleBaseName returns the file name of a module path, also for a library
// inside an APK ("base.apk!libil2cpp.so" -> "libil2cpp.so").
func ModuleBaseName(m string) string {
	if i := strings.LastIndexByte(m, '!'); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// CrashThread is one thread of a crash stack.
type CrashThread struct {
	Name    string       `json:"name"`
	Tid     int          `json:"tid,omitempty"`
	Crashed bool         `json:"crashed"`
	Frames  []CrashFrame `json:"frames"`
	// Omitted counts the frames beyond the parser's limit.
	Omitted int `json:"omitted,omitempty"`
}

// ParseCrashStack parses a crash stack (see the section comment). Lines that
// are neither a thread header nor a frame are ignored. Frames before any
// header form an unnamed thread. At most maxCrashThreads threads of
// maxCrashFramesPerThread frames, of the first maxParseBytes bytes.
func ParseCrashStack(stack string) []CrashThread {
	stack = capBytes(stack, maxParseBytes)
	var threads []CrashThread
	cur := -1
	for _, line := range splitLines(stack) {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if th, ok := parseThreadHeader(t); ok {
			if len(threads) == maxCrashThreads {
				cur = -2 // ignore the rest
				continue
			}
			threads = append(threads, th)
			cur = len(threads) - 1
			continue
		}
		if cur == -2 {
			continue
		}
		if strings.HasPrefix(t, "|") {
			// A trace's thread details: "| sysTid=12345 nice=-10 ...".
			if cur >= 0 {
				for _, field := range strings.Fields(t[1:]) {
					if strings.HasPrefix(field, "sysTid=") {
						if n, err := strconv.Atoi(field[7:]); err == nil && n >= 0 {
							threads[cur].Tid = n
						}
					}
				}
			}
			continue
		}
		f, ok := ParseCrashFrame(t)
		if !ok {
			continue
		}
		if cur == -1 {
			threads = append(threads, CrashThread{})
			cur = 0
		}
		th := &threads[cur]
		if len(th.Frames) >= maxCrashFramesPerThread {
			th.Omitted++
			continue
		}
		f.Index = len(th.Frames)
		th.Frames = append(th.Frames, f)
	}
	for i := range threads {
		if threads[i].Frames == nil {
			threads[i].Frames = []CrashFrame{}
		}
	}
	if len(threads) > 0 && !anyCrashed(threads) {
		threads[0].Crashed = true
	}
	return threads
}

func anyCrashed(ts []CrashThread) bool {
	for _, t := range ts {
		if t.Crashed {
			return true
		}
	}
	return false
}

// parseThreadHeader accepts the SDK's `Thread "main" tid=123 crashed` and a
// trace's `"main" prio=5 tid=1 Native`.
func parseThreadHeader(t string) (CrashThread, bool) {
	var th CrashThread
	rest := t
	if strings.HasPrefix(rest, "Thread ") {
		rest = rest[len("Thread "):]
	} else if !strings.HasPrefix(rest, `"`) {
		return th, false
	}
	if !strings.HasPrefix(rest, `"`) {
		return th, false
	}
	end := strings.LastIndexByte(rest, '"')
	if end <= 0 {
		return th, false
	}
	th.Name = cleanText(rest[1:end], 128)
	for _, field := range strings.Fields(rest[end+1:]) {
		switch {
		case strings.HasPrefix(field, "tid="):
			if n, err := strconv.Atoi(field[4:]); err == nil && n >= 0 {
				th.Tid = n
			}
		case strings.HasPrefix(field, "sysTid="):
			if n, err := strconv.Atoi(field[7:]); err == nil && n >= 0 {
				th.Tid = n
			}
		case field == "crashed":
			th.Crashed = true
		}
	}
	return th, true
}

// ParseCrashFrame parses one native ("#00 pc ...") or Java ("at ...") frame line.
func ParseCrashFrame(t string) (CrashFrame, bool) {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "native: ")
	if strings.HasPrefix(t, "at ") {
		return parseJavaFrame(t)
	}
	if !strings.HasPrefix(t, "#") {
		return CrashFrame{}, false
	}
	f := CrashFrame{Native: true, Raw: cleanText(t, 512)}
	i := 1
	for i < len(t) && isDigit(t[i]) {
		i++
	}
	if i == 1 {
		return CrashFrame{}, false
	}
	rest := strings.TrimLeft(t[i:], " ")
	if !strings.HasPrefix(rest, "pc ") {
		return CrashFrame{}, false
	}
	rest = strings.TrimLeft(rest[3:], " ")
	j := 0
	for j < len(rest) && isHex(rest[j]) {
		j++
	}
	if j == 0 || j > 16 {
		return CrashFrame{}, false
	}
	pc, err := strconv.ParseUint(rest[:j], 16, 64)
	if err != nil {
		return CrashFrame{}, false
	}
	f.PC = pc
	rest = strings.TrimSpace(rest[j:])

	// Trailing groups, right to left: (BuildId: x), (function+off), (offset 0x..).
	for {
		if !strings.HasSuffix(rest, ")") {
			break
		}
		open := lastGroupOpen(rest)
		if open < 0 {
			break
		}
		group := rest[open+1 : len(rest)-1]
		switch {
		case strings.HasPrefix(group, "BuildId: "):
			id := strings.ToLower(strings.TrimSpace(group[len("BuildId: "):]))
			if isHexString(id) && len(id) <= 128 {
				f.BuildID = id
			}
		case strings.HasPrefix(group, "offset 0x"):
			// the library's offset inside an APK: not needed (pc is ELF-relative)
		case strings.HasPrefix(group, "deleted"):
		default:
			if f.Function == "" {
				fn, off := splitFunctionOffset(group)
				f.Function = cleanText(fn, 512)
				f.Offset = off
			}
		}
		rest = strings.TrimSpace(rest[:open])
	}
	f.Module = cleanText(rest, 256)
	f.InApp = nativeInApp(f.Module)
	return f, true
}

// lastGroupOpen returns the index of the '(' that opens the group ending at
// the last byte of s (nested parentheses in C++ names included), -1 if none.
func lastGroupOpen(s string) int {
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				// A group is preceded by a space (or starts the text).
				if i == 0 || s[i-1] == ' ' {
					return i
				}
				return -1
			}
		}
	}
	return -1
}

func splitFunctionOffset(g string) (string, uint64) {
	i := strings.LastIndexByte(g, '+')
	if i <= 0 {
		return g, 0
	}
	off, err := strconv.ParseUint(g[i+1:], 10, 64)
	if err != nil {
		return g, 0
	}
	return g[:i], off
}

func parseJavaFrame(t string) (CrashFrame, bool) {
	body := strings.TrimSpace(t[3:])
	if body == "" {
		return CrashFrame{}, false
	}
	f := CrashFrame{Raw: cleanText(t, 512)}
	method := body
	if i := strings.IndexByte(body, '('); i > 0 {
		method = body[:i]
		loc := strings.TrimSuffix(body[i+1:], ")")
		if c := strings.LastIndexByte(loc, ':'); c > 0 {
			if n, ok := atoi(loc[c+1:]); ok {
				f.File = cleanText(loc[:c], 256)
				f.Line = n
			}
		} else if loc != "" && !strings.Contains(loc, " ") {
			f.File = cleanText(loc, 256)
		}
	}
	f.Function = cleanText(method, 512)
	f.InApp = javaInApp(f.Function)
	return f, true
}

// systemLibraries are system modules that may appear with their file name only.
var systemLibraries = map[string]bool{
	"libc.so": true, "libm.so": true, "libdl.so": true, "liblog.so": true, "libart.so": true,
	"libbase.so": true, "libutils.so": true, "libbinder.so": true, "libandroid_runtime.so": true,
	"libc++.so": true, "linker": true, "linker64": true, "app_process32": true, "app_process64": true,
	"libEGL.so": true, "libGLESv2.so": true, "libGLESv3.so": true, "libvulkan.so": true,
	"libhwui.so": true, "libgui.so": true, "libui.so": true, "libandroid.so": true,
	"libunwindstack.so": true, "libopenjdkjvm.so": true, "boot.oat": true, "boot-framework.oat": true,
}

func nativeInApp(module string) bool {
	if module == "" || strings.HasPrefix(module, "/") || strings.HasPrefix(module, "[") {
		return false
	}
	return !systemLibraries[ModuleBaseName(module)]
}

var javaSystemPrefixes = []string{
	"android.", "androidx.", "java.", "javax.", "kotlin.", "kotlinx.", "dalvik.", "com.android.",
	"sun.", "libcore.", "jdk.", "com.google.android.", "com.unity3d.player.",
}

func javaInApp(method string) bool {
	for _, p := range javaSystemPrefixes {
		if strings.HasPrefix(method, p) {
			return false
		}
	}
	return true
}

// CrashInput is what the crash fingerprint is computed from.
type CrashInput struct {
	CrashType       string
	Signal          string
	ExitReason      string
	ExitDescription string
	Threads         []CrashThread
	// Resolve returns the name of a native frame's function from uploaded
	// symbols ("" if unknown); nil: none.
	Resolve func(f CrashFrame) string
}

// CrashResult is the crash's fingerprint and its first in-app frame key.
type CrashResult struct {
	Fingerprint string
	Version     int
	// Site is the first frame key used ("libil2cpp.so!Game.Room.Load"), "" without frames.
	Site string
	// Symbolicated: a frame key used a name resolved from uploaded symbols.
	Symbolicated bool
}

// CrashFingerprint computes the crash fingerprint (version 1, section comment).
func CrashFingerprint(in CrashInput) CrashResult {
	r := CrashResult{Version: Version}
	var b strings.Builder
	b.WriteString("v" + strconv.Itoa(Version) + "\ncrash\n")
	typ := strings.ToLower(strings.TrimSpace(in.CrashType))
	b.WriteString(typ + "\n")
	var keys []string
	switch typ {
	case CrashTypeNative:
		b.WriteString(strings.ToUpper(strings.TrimSpace(in.Signal)) + "\n")
		if len(in.Threads) > 0 {
			t := crashedThread(in.Threads)
			keys = frameKeys(t.Frames, true, in.Resolve, &r.Symbolicated)
			if len(keys) == 0 {
				keys = frameKeys(t.Frames, false, in.Resolve, &r.Symbolicated)
			}
		}
		b.WriteString("f:" + strings.Join(keys, "\n"))
	case CrashTypeANR:
		for _, t := range in.Threads {
			if t.Name == "UnityMain" {
				keys = frameKeys(t.Frames, true, in.Resolve, &r.Symbolicated)
				break
			}
		}
		if len(keys) == 0 && len(in.Threads) > 0 {
			keys = frameKeys(in.Threads[0].Frames, true, in.Resolve, &r.Symbolicated)
			if len(keys) == 0 {
				keys = frameKeys(in.Threads[0].Frames, false, in.Resolve, &r.Symbolicated)
			}
		}
		b.WriteString("f:" + strings.Join(keys, "\n"))
	case CrashTypeJava:
		b.WriteString("m:" + Template(in.ExitDescription))
	default:
		b.WriteString("r:" + strings.ToUpper(strings.TrimSpace(in.ExitReason)))
	}
	if len(keys) > 0 {
		r.Site = siteOf(keys[0])
	}
	r.Fingerprint = Hash(b.String())
	return r
}

func crashedThread(ts []CrashThread) CrashThread {
	for _, t := range ts {
		if t.Crashed {
			return t
		}
	}
	return ts[0]
}

// frameKeys returns up to crashFingerprintFrames distinct frame keys.
func frameKeys(frames []CrashFrame, inApp bool, resolve func(CrashFrame) string, symbolicated *bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range frames {
		if len(out) == crashFingerprintFrames {
			break
		}
		if inApp && !f.InApp {
			continue
		}
		key, sym := FrameKey(f, resolve)
		if seen[key] {
			continue
		}
		seen[key] = true
		if sym {
			*symbolicated = true
		}
		out = append(out, key)
	}
	return out
}

// FrameKey is a frame's identity in the fingerprint; sym reports that it
// used a name resolved from uploaded symbols.
func FrameKey(f CrashFrame, resolve func(CrashFrame) string) (key string, sym bool) {
	if !f.Native {
		return NormalizeMethod(f.Function), false
	}
	name := ""
	if resolve != nil {
		name = resolve(f)
		sym = name != ""
	}
	if name == "" {
		name = f.Function
	}
	mod := f.ModuleName()
	if name != "" {
		return mod + "!" + NormalizeMethod(stripArgs(name)), sym
	}
	key = mod + "+0x" + strconv.FormatUint(f.PC, 16)
	if f.BuildID != "" {
		// The same address in another build is other code.
		id := f.BuildID
		if len(id) > 16 {
			id = id[:16]
		}
		key += " " + id
	}
	return key, false
}

// siteOf is a frame key without the build id of an address key.
func siteOf(key string) string {
	if i := strings.IndexByte(key, ' '); i > 0 && !strings.Contains(key, "!") {
		return key[:i]
	}
	return key
}

// stripArgs removes a trailing argument list: "Ns.Type.Method(System.Int32)"
// -> "Ns.Type.Method", "il2cpp::vm::Raise(Il2CppException*, bool)" -> "il2cpp::vm::Raise".
func stripArgs(name string) string {
	if i := strings.IndexByte(name, '('); i > 0 {
		return name[:i]
	}
	return name
}

// cleanText cuts s to n bytes and replaces control characters with spaces.
func cleanText(s string, n int) string {
	s = capBytes(s, n)
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0 {
		return strings.ToValidUTF8(s, "?")
	}
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c == 0x7f {
			b[i] = ' '
		}
	}
	return strings.ToValidUTF8(string(b), "?")
}

func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

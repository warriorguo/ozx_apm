package processor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func TestNormalizeCrash_Bounds(t *testing.T) {
	c := models.Crash{
		CrashType:   " NATIVE ",
		Stack:       strings.Repeat("é", MaxCrashStackLength+10),
		Breadcrumbs: make([]string, MaxBreadcrumbs+5),
	}
	d := &c.CrashDetails
	d.ExitReason = "CRASH_NATIVE with spaces and a very long tail beyond thirty-two chars"
	d.Signal = "SIG\x00SEGV"
	d.FaultAddr = "0x0\n<script>"
	d.ExitDescription = strings.Repeat("d", 5000)
	d.AbortMessage = strings.Repeat("a", 5000)
	d.ExitStatus = 1 << 40
	d.Importance = -3
	d.PssKB = 1 << 50
	d.SessionMs = -1
	d.RawDetails = json.RawMessage(`{"fps":"58.9","mem_mb":812,"bad key":"x","nested":{"a":1}}`)
	stats := NormalizeCrash(&c)

	if c.CrashType != "native" || len([]rune(c.Stack)) != MaxCrashStackLength || len(c.Breadcrumbs) != MaxBreadcrumbs {
		t.Fatalf("type %q stack %d crumbs %d", c.CrashType, len([]rune(c.Stack)), len(c.Breadcrumbs))
	}
	if d.ExitReason != "CRASH_NATIVEwithspacesandaveryl" && len(d.ExitReason) != 32 {
		t.Errorf("exit reason %q", d.ExitReason)
	}
	if d.Signal != "SIGSEGV" || d.FaultAddr != "0x0<script>" {
		t.Errorf("signal %q fault %q", d.Signal, d.FaultAddr)
	}
	if len(d.ExitDescription) != maxExitDescriptionLength || len(d.AbortMessage) != maxAbortMessageLength {
		t.Errorf("texts not cut")
	}
	if d.ExitStatus != 1<<31-1 || d.Importance != 0 || d.PssKB != maxCrashMemoryKB || d.SessionMs != 0 {
		t.Errorf("numbers %+v", d)
	}
	if len(d.Details) != 2 || d.Details["mem_mb"] != "812" || stats.Dropped != 2 {
		t.Errorf("details %v stats %+v", d.Details, stats)
	}
}

func TestCrashSummary(t *testing.T) {
	native := models.Crash{CrashType: "native"}
	native.Signal, native.SignalCode = "SIGABRT", "SI_TKILL"
	native.AbortMessage = "il2cpp: Unhandled exception\nsecond line"
	if s := CrashSummary(&native, "libil2cpp.so!A.B"); s != "SIGABRT (SI_TKILL): il2cpp: Unhandled exception in A.B (libil2cpp.so)" {
		t.Errorf("abort: %q", s)
	}
	native.AbortMessage, native.Signal, native.SignalCode, native.FaultAddr = "", "SIGSEGV", "SEGV_MAPERR", "0x8"
	if s := CrashSummary(&native, "libil2cpp.so+0x10"); s != "SIGSEGV (SEGV_MAPERR) at 0x8 in libil2cpp.so+0x10" {
		t.Errorf("segv: %q", s)
	}
	killed := models.Crash{CrashType: "killed"}
	killed.ExitReason, killed.ExitDescription = "EXCESSIVE_RESOURCE_USAGE", "excessive cpu"
	if s := CrashSummary(&killed, ""); s != "Killed by the system: EXCESSIVE_RESOURCE_USAGE (excessive cpu)" {
		t.Errorf("killed: %q", s)
	}
	unknown := models.Crash{CrashType: fingerprint.CrashTypeUnknown}
	if s := CrashSummary(&unknown, ""); !strings.HasPrefix(s, "Ended in the foreground") {
		t.Errorf("unknown: %q", s)
	}
	java := models.Crash{CrashType: "java"}
	if s := CrashSummary(&java, ""); s != "Java crash" {
		t.Errorf("java: %q", s)
	}
	// Unity's own tombstone: "fault addr --------" is no address.
	unity := models.Crash{CrashType: "native"}
	unity.Signal, unity.SignalCode, unity.FaultAddr = "SIGSEGV", "SI_TKILL", "--------"
	if s := CrashSummary(&unity, "libunity.so!Utils_CUSTOM_ForceCrash"); s != "SIGSEGV (SI_TKILL) in Utils_CUSTOM_ForceCrash (libunity.so)" {
		t.Errorf("unity tombstone: %q", s)
	}
	// ANR: the kind from details.cause (watchdog and trace alike), else the exit description.
	anr := models.Crash{CrashType: "anr"}
	anr.Details = map[string]string{"cause": "ANR Input dispatching timed out (925aad com.ozx.apmtest/com.unity3d.player.UnityPlayerActivity is not responding. Waited 10000ms for MotionEvent)."}
	if s := CrashSummary(&anr, "com.unity3d.player.ReflectionHelper.nativeProxyInvoke"); s != "ANR: Input dispatching timed out in com.unity3d.player.ReflectionHelper.nativeProxyInvoke" {
		t.Errorf("anr: %q", s)
	}
	anr.Details, anr.ExitDescription = nil, "user request after error: Input dispatching timed out"
	if s := CrashSummary(&anr, ""); s != "ANR: Input dispatching timed out" {
		t.Errorf("anr from the description: %q", s)
	}
	anr.ExitDescription = ""
	if s := CrashSummary(&anr, ""); s != "ANR" {
		t.Errorf("anr without a message: %q", s)
	}
}

// Device FailFast (SIGTRAP in il2cpp's CrashHelpers): the summary showed
// "il2cpp..os..CrashHelpers..CrashImpl"; C++'s "::" is kept.
func TestCrashSummary_KeepsCppScope(t *testing.T) {
	c := models.Crash{CrashType: "native"}
	c.Signal = "SIGTRAP"
	threads := fingerprint.ParseCrashStack("Thread \"UnityMain\" tid=1 crashed\n#00 pc 0000000000001000 libil2cpp.so (BuildId: aa)\n")
	ApplyCrashFingerprint(&c, threads, func(fingerprint.CrashFrame) string { return "il2cpp::os::CrashHelpers::CrashImpl()" })
	if c.Summary != "SIGTRAP in il2cpp::os::CrashHelpers::CrashImpl (libil2cpp.so)" {
		t.Errorf("summary %q", c.Summary)
	}
	if c.CrashSite != "libil2cpp.so!il2cpp::os::CrashHelpers::CrashImpl" {
		t.Errorf("site %q", c.CrashSite)
	}
}

// A crash without frames or APM-7 fields (another producer) keeps its
// client's fingerprint; one from the APM-7 SDK gets the server's.
func TestApplyCrashFingerprint_KeepsAClientFingerprintWithoutAnythingToGroupBy(t *testing.T) {
	legacy := models.Crash{CrashType: "native", Fingerprint: "C1", Stack: "st"}
	r := ApplyCrashFingerprint(&legacy, fingerprint.ParseCrashStack(legacy.Stack), nil)
	if legacy.Fingerprint != "C1" || r.Fingerprint != "C1" || legacy.FingerprintVersion != 0 || legacy.ClientFingerprint != "C1" || legacy.Summary == "" {
		t.Fatalf("legacy: %+v", legacy)
	}
	sdk := models.Crash{CrashType: "oom", Fingerprint: "C1"}
	sdk.ExitReason = "LOW_MEMORY"
	ApplyCrashFingerprint(&sdk, nil, nil)
	if sdk.Fingerprint == "C1" || sdk.FingerprintVersion != fingerprint.CrashVersion || sdk.ClientFingerprint != "C1" {
		t.Fatalf("sdk: %+v", sdk)
	}
	framed := models.Crash{CrashType: "native", Fingerprint: "C1", Stack: "#00 pc 0000000000001000 libil2cpp.so\n"}
	ApplyCrashFingerprint(&framed, fingerprint.ParseCrashStack(framed.Stack), nil)
	if framed.Fingerprint == "C1" || framed.CrashSite != "libil2cpp.so+0x1000" {
		t.Fatalf("framed: %+v", framed)
	}
}

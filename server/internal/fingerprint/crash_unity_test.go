package fingerprint

import (
	"strings"
	"testing"
)

// APM-7 device finding: the same native crash is recorded by Android 16 as
// CRASH_NATIVE (system tombstone, 31 frames + main thread) or, when
// ActivityManager's kill wins the race, as CRASH without a trace; the SDK
// then sends Unity's own tombstone (16 frames, crashed thread only) as a
// native crash. Both must be one group, with and without uploaded symbols.
func TestCrashFingerprint_UnityTombstoneJoinsTheSystemTombstoneGroup(t *testing.T) {
	unity := ParseCrashStack(unityTombstoneStack)
	system := ParseCrashStack(systemTombstoneStack)
	if len(unity) != 1 || len(unity[0].Frames) != 16 || len(system) != 2 || len(system[0].Frames) != 31 {
		t.Fatalf("parsed: unity %d threads, system %d threads", len(unity), len(system))
	}
	resolve := func(f CrashFrame) string {
		switch {
		case f.ModuleName() == "libil2cpp.so" && f.PC == 0x56a030:
			return "ApmTestDriver.UpdateApm7(System.Single)"
		case f.ModuleName() == "libil2cpp.so" && f.PC == 0x56542c:
			return "ApmTestDriver.Update()"
		}
		return ""
	}
	for _, res := range []func(CrashFrame) string{nil, resolve} {
		a := CrashFingerprint(CrashInput{CrashType: CrashTypeNative, Signal: "SIGSEGV", ExitReason: "CRASH", ExitDescription: "crash", Threads: unity, Resolve: res})
		b := CrashFingerprint(CrashInput{CrashType: CrashTypeNative, Signal: "SIGSEGV", ExitReason: "CRASH_NATIVE", Threads: system, Resolve: res})
		if a.Fingerprint != b.Fingerprint || a.Site != b.Site {
			t.Errorf("symbols=%v: unity %+v, system %+v", res != nil, a, b)
		}
		if !strings.HasPrefix(a.Site, "libunity.so") {
			t.Errorf("site %q: the first in-app frame", a.Site)
		}
	}
	// What the SDK sent before (java, no stack) was a group of its own.
	java := CrashFingerprint(CrashInput{CrashType: CrashTypeJava, ExitReason: "CRASH", ExitDescription: "crash"})
	native := CrashFingerprint(CrashInput{CrashType: CrashTypeNative, Signal: "SIGSEGV", Threads: system})
	if java.Fingerprint == native.Fingerprint {
		t.Fatal("java and native must differ")
	}
}

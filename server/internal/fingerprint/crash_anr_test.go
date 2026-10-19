package fingerprint

import "testing"

// APM-7 device round 2: the watchdog's live report (Java main thread, "Unknown
// Source", "$Proxy4.run") and the exit trace's (main with "unavailable",
// "java.lang.Runnable.run", plus UnityMain) of the same ANR were two groups.
// They are one: the main thread's Java frames only, none in-app here (Unity's
// reflection proxy), so the ANR's kind keys it.
func TestCrashFingerprint_AnrWatchdogAndExitTraceAreOneGroup(t *testing.T) {
	live := CrashFingerprint(CrashInput{CrashType: CrashTypeANR, Cause: anrWatchdogStackCause, Threads: ParseCrashStack(anrWatchdogStack)})
	trace := CrashFingerprint(CrashInput{CrashType: CrashTypeANR, ExitReason: "SIGNALED", Cause: anrExitTraceStackCause, Threads: ParseCrashStack(anrExitTraceStack)})
	if live.Fingerprint != trace.Fingerprint || live.Site != trace.Site {
		t.Fatalf("live %+v, trace %+v", live, trace)
	}
	if live.Site != "com.unity3d.player.ReflectionHelper.nativeProxyInvoke" {
		t.Fatalf("site %q", live.Site)
	}
	if k := AnrKind(anrWatchdogStackCause, ""); k != "Input dispatching timed out" || AnrKind(anrExitTraceStackCause, "") != k {
		t.Fatalf("kind %q / %q", k, AnrKind(anrExitTraceStackCause, ""))
	}

	// An in-app Java frame keys it (proxies, Unity player and framework frames are skipped).
	game := "Thread \"main\" tid=1 crashed\nat java.lang.Object.wait(Native method)\nat $Proxy4.run(Unknown Source)\n" +
		"at com.ozx.game.Save.flush(Save.java:40)\nat android.os.Handler.handleCallback(Handler.java:995)\n"
	gameTrace := "Thread \"main\" tid=1 crashed\nat java.lang.Object.wait(Native method)\nat java.lang.Runnable.run(Runnable.java:-2)\n" +
		"at com.ozx.game.Save.flush(unavailable:41)\nat android.os.Handler.handleCallback(Handler.java:995)\n" +
		"Thread \"UnityMain\" tid=2\n#00 pc 0000000000001000 libil2cpp.so (BuildId: aa)\n"
	a := CrashFingerprint(CrashInput{CrashType: CrashTypeANR, Cause: "ANR Input dispatching timed out (x)", Threads: ParseCrashStack(game)})
	b := CrashFingerprint(CrashInput{CrashType: CrashTypeANR, Cause: "Input dispatching timed out (y)", Threads: ParseCrashStack(gameTrace)})
	if a.Fingerprint != b.Fingerprint || a.Site != "com.ozx.game.Save.flush" || a.Fingerprint == live.Fingerprint {
		t.Fatalf("in-app: %+v / %+v", a, b)
	}

	// Kinds.
	for in, want := range map[string]string{
		"": "ANR",
		"user request after error: Input dispatching timed out (Waiting 5000ms)": "Input dispatching timed out",
		"bg anr: Broadcast of Intent { act=android.intent.action.SCREEN_ON }":    "Broadcast of Intent",
		"ANR executing service com.ozx.game/.Svc":                                "executing service com.ozx.game/.Svc",
	} {
		if got := AnrKind(in, ""); got != want {
			t.Errorf("AnrKind(%q) = %q, want %q", in, got, want)
		}
	}
}

package fingerprint

import (
	"strings"
	"testing"
)

const sdkNativeStack = `Thread "UnityMain" tid=12380 crashed
#00 pc 00000000005417bc libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)
#01 pc 0000000000541470 libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)
#02 pc 0000000000541470 libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)
#03 pc 000000000053ffcc libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)
#04 pc 0000000000a1b2c4 libunity.so (BuildId: 1111111111111111111111111111111111111111)
#05 pc 00000000000c8d2c /apex/com.android.runtime/lib64/bionic/libc.so (__pthread_start(void*)+208) (BuildId: 9c7af2d1)
Thread "main" tid=12345
#00 pc 000000000009c048 /apex/com.android.runtime/lib64/bionic/libc.so (__futex_wait_ex+168) (BuildId: 9c7af2d1)
at com.unity3d.player.UnityPlayer.nativeRender(Native Method)
at com.unity3d.player.UnityPlayer.access$300(UnityPlayer.java:46)
`

func TestParseCrashStack_SDKForm(t *testing.T) {
	ts := ParseCrashStack(sdkNativeStack)
	if len(ts) != 2 {
		t.Fatalf("threads = %d", len(ts))
	}
	if ts[0].Name != "UnityMain" || ts[0].Tid != 12380 || !ts[0].Crashed || ts[1].Crashed {
		t.Fatalf("thread 0 = %+v", ts[0])
	}
	f := ts[0].Frames[0]
	if !f.Native || f.PC != 0x5417bc || f.Module != "libil2cpp.so" || f.BuildID != "0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077" || !f.InApp || f.Function != "" {
		t.Fatalf("frame 0 = %+v", f)
	}
	libc := ts[0].Frames[5]
	if libc.InApp || libc.Function != "__pthread_start(void*)" || libc.Offset != 208 || libc.ModuleName() != "libc.so" {
		t.Fatalf("libc frame = %+v", libc)
	}
	java := ts[1].Frames[2]
	if java.Native || java.Function != "com.unity3d.player.UnityPlayer.access$300" || java.File != "UnityPlayer.java" || java.Line != 46 || java.InApp {
		t.Fatalf("java frame = %+v", java)
	}
}

func TestParseCrashStack_AndroidTextForms(t *testing.T) {
	tombstone := `backtrace:
      #00 pc 000000000004e0dc  /apex/com.android.runtime/lib64/bionic/libc.so (abort+164) (BuildId: 2c4f)
      #01 pc 00000000005417bc  /data/app/~~Ab12==/com.ozx.apmtest-Xy==/base.apk!libil2cpp.so (offset 0x1234000) (BuildId: 0D65A2D2)
      #02 pc 0000000000123456  /data/app/~~Ab12==/com.ozx.apmtest-Xy==/lib/arm64/libunity.so (UnityMain(Foo<int>)+12) (BuildId: aa)
`
	ts := ParseCrashStack(tombstone)
	if len(ts) != 1 || len(ts[0].Frames) != 3 || !ts[0].Crashed {
		t.Fatalf("%+v", ts)
	}
	f := ts[0].Frames[1]
	if f.ModuleName() != "libil2cpp.so" || f.BuildID != "0d65a2d2" || f.Function != "" || f.PC != 0x5417bc {
		t.Fatalf("apk frame = %+v", f)
	}
	g := ts[0].Frames[2]
	if g.Function != "UnityMain(Foo<int>)" || g.Offset != 12 || g.ModuleName() != "libunity.so" {
		t.Fatalf("nested parens = %+v", g)
	}
	// A full /data/app path is not a system path: only the SDK shortens it.
	if g.InApp {
		t.Fatalf("absolute paths are not in-app: %+v", g)
	}

	anr := `"main" prio=5 tid=1 Sleeping
  | group="main" sCount=1 ucsCount=0 flags=1 obj=0x72f0 self=0xb400
  | sysTid=12345 nice=-10 cgrp=top-app sched=0/0 handle=0x7d
  native: #00 pc 00000000000a1a6c  /apex/com.android.runtime/lib64/bionic/libc.so (nanosleep+12) (BuildId: 2c4f)
  at java.lang.Thread.sleep(Native method)
  - sleeping on <0x0e7c> (a java.lang.Object)
  at com.ozx.game.Foo.bar(Foo.java:12)
"UnityMain" prio=5 tid=20 Native
  native: #00 pc 0000000000541470  libil2cpp.so (BuildId: 0d65)
`
	ts = ParseCrashStack(anr)
	if len(ts) != 2 || ts[0].Name != "main" || ts[0].Tid != 12345 || ts[1].Name != "UnityMain" {
		t.Fatalf("anr threads = %+v", ts)
	}
	if len(ts[0].Frames) != 3 || ts[0].Frames[1].Function != "java.lang.Thread.sleep" || ts[0].Frames[1].File != "" || !ts[0].Frames[2].InApp {
		t.Fatalf("anr main frames = %+v", ts[0].Frames)
	}
}

func TestParseCrashStack_Hostile(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("Thread \"t\" tid=1\n")
		for j := 0; j < 400; j++ {
			b.WriteString("#00 pc 00000000000000ff libx.so (\x01<script>(()))+9) (BuildId: zz)\n")
		}
	}
	ts := ParseCrashStack(b.String())
	if len(ts) > maxCrashThreads {
		t.Fatalf("threads not bounded: %d", len(ts))
	}
	for _, th := range ts {
		if len(th.Frames) > maxCrashFramesPerThread {
			t.Fatalf("frames not bounded")
		}
		for _, f := range th.Frames {
			if strings.ContainsRune(f.Raw, '\x01') || strings.ContainsRune(f.Function, '\x01') {
				t.Fatalf("control character kept: %q", f.Raw)
			}
			if f.BuildID != "" {
				t.Fatalf("invalid build id kept: %q", f.BuildID)
			}
		}
	}
	for _, s := range []string{"", "#", "# pc", "#00 pc zz", "#00 pc 12345678901234567 x", "Thread \"", "at "} {
		_ = ParseCrashStack(s)
	}
}

func TestCrashFingerprint_NativeGroupsBySymbolsAcrossBuilds(t *testing.T) {
	build1 := ParseCrashStack(sdkNativeStack)
	build2 := ParseCrashStack(strings.ReplaceAll(strings.ReplaceAll(sdkNativeStack, "5417bc", "6417bc"), "0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077", "ffff"))
	names := map[uint64]string{0x5417bc: "ApmTestDriver.DeepC(System.Int32)", 0x6417bc: "ApmTestDriver.DeepC(System.Int32)",
		0x541470: "ApmTestDriver.DeepB()", 0x53ffcc: "ApmTestDriver.DeepA()"}
	resolve := func(f CrashFrame) string { return names[f.PC] }

	a := CrashFingerprint(CrashInput{CrashType: "native", Signal: "SIGSEGV", Threads: build1, Resolve: resolve})
	b := CrashFingerprint(CrashInput{CrashType: "native", Signal: "SIGSEGV", Threads: build2, Resolve: resolve})
	if a.Fingerprint != b.Fingerprint || !a.Symbolicated {
		t.Fatalf("same code in two builds: %+v %+v", a, b)
	}
	if a.Site != "libil2cpp.so!ApmTestDriver.DeepC" {
		t.Fatalf("site = %q", a.Site)
	}
	// Recursion (the repeated frame) counts once; the third key is DeepA.
	other := CrashFingerprint(CrashInput{CrashType: "native", Signal: "SIGABRT", Threads: build1, Resolve: resolve})
	if other.Fingerprint == a.Fingerprint {
		t.Fatal("the signal must take part")
	}

	// Without symbols: never keyed by address (APM-8 alerts on every new
	// fingerprint: a new build must not turn known crashes into new issues).
	// The same crash in two builds without symbols is one fingerprint; the
	// site still shows the address.
	ra := CrashFingerprint(CrashInput{CrashType: "native", Signal: "SIGSEGV", Threads: build1})
	rb := CrashFingerprint(CrashInput{CrashType: "native", Signal: "SIGSEGV", Threads: build2})
	if ra.Fingerprint != rb.Fingerprint || ra.Symbolicated || ra.Site != "libil2cpp.so+0x5417bc" || rb.Site != "libil2cpp.so+0x6417bc" {
		t.Fatalf("without symbols: %+v %+v", ra, rb)
	}
	if ra.Fingerprint == a.Fingerprint {
		t.Fatal("named and unnamed frames are different keys")
	}
	// A third build with other addresses and another build id: still the same.
	build3 := ParseCrashStack(strings.ReplaceAll(strings.ReplaceAll(sdkNativeStack, "000000000053ffcc", "0000000000777ffc"), "0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077", "abcdef01"))
	if rc := CrashFingerprint(CrashInput{CrashType: "native", Signal: "SIGSEGV", Threads: build3}); rc.Fingerprint != ra.Fingerprint {
		t.Fatalf("third build: %+v", rc)
	}
	// Partly named (the symbols of one build name only some frames): the
	// named frames take part, the others count as their library.
	partial := CrashFingerprint(CrashInput{CrashType: "native", Signal: "SIGSEGV", Threads: build1,
		Resolve: func(f CrashFrame) string {
			if f.PC == 0x5417bc {
				return "ApmTestDriver.DeepC(System.Int32)"
			}
			return ""
		}})
	if partial.Fingerprint == ra.Fingerprint || partial.Fingerprint == a.Fingerprint || partial.Site != "libil2cpp.so!ApmTestDriver.DeepC" {
		t.Fatalf("partly named: %+v", partial)
	}
}

func TestCrashFingerprint_OtherTypes(t *testing.T) {
	anr := ParseCrashStack(`Thread "main" tid=1 crashed
at java.lang.Thread.sleep(Native method)
at com.ozx.game.Foo.bar(Foo.java:12)
Thread "UnityMain" tid=20
#00 pc 0000000000541470 libil2cpp.so (BuildId: 0d65)
`)
	r := CrashFingerprint(CrashInput{CrashType: "anr", Threads: anr})
	if r.Site != "com.ozx.game.Foo.bar" {
		t.Fatalf("anr keys the main thread's in-app Java frame: %+v", r)
	}
	// UnityMain is not part of the key (the watchdog's report has no UnityMain).
	noUnity := ParseCrashStack("Thread \"main\" tid=1\nat java.lang.Thread.sleep(Native method)\nat com.ozx.game.Foo.bar(Foo.java:12)\n")
	if r2 := CrashFingerprint(CrashInput{CrashType: "anr", Threads: noUnity}); r2.Site != "com.ozx.game.Foo.bar" || r2.Fingerprint != r.Fingerprint {
		t.Fatalf("anr without UnityMain: %+v vs %+v", r2, r)
	}

	oom1 := CrashFingerprint(CrashInput{CrashType: "oom", ExitReason: "LOW_MEMORY"})
	oom2 := CrashFingerprint(CrashInput{CrashType: "oom", ExitReason: "low_memory"})
	killed := CrashFingerprint(CrashInput{CrashType: "killed", ExitReason: "LOW_MEMORY"})
	if oom1.Fingerprint != oom2.Fingerprint || oom1.Fingerprint == killed.Fingerprint || oom1.Site != "" {
		t.Fatalf("oom: %+v %+v %+v", oom1, oom2, killed)
	}
	j1 := CrashFingerprint(CrashInput{CrashType: "java", ExitDescription: "java.lang.IllegalStateException: room 12"})
	j2 := CrashFingerprint(CrashInput{CrashType: "java", ExitDescription: "java.lang.IllegalStateException: room 13"})
	if j1.Fingerprint != j2.Fingerprint {
		t.Fatal("java crashes group by the template of the description")
	}
}

func TestFrameKey(t *testing.T) {
	addr, _ := ParseCrashFrame("#03 pc 0000000000001000 libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)")
	if k, _ := FrameKey(addr, nil); k != "libil2cpp.so!?" {
		t.Fatalf("unnamed frame: %q", k)
	}
	if site := frameSite(addr, "libil2cpp.so!?"); site != "libil2cpp.so+0x1000" {
		t.Fatalf("site %q", site)
	}
	f, _ := ParseCrashFrame("#03 pc 0000000000001000 libunity.so (_ZN5Unity4Load+4) (BuildId: aa)")
	if k, sym := FrameKey(f, nil); k != "libunity.so!_ZN5Unity4Load" || sym {
		t.Fatalf("device name: %q", k)
	}
	k, sym := FrameKey(f, func(CrashFrame) string { return "Game.Rooms.RoomLoader.Load<T>(System.Int32)" })
	if k != "libunity.so!Game.Rooms.RoomLoader.Load" || !sym {
		t.Fatalf("resolved: %q", k)
	}
	// C++ keeps "::" (device FailFast: il2cpp::os::CrashHelpers::CrashImpl showed as "il2cpp..os..").
	cpp := func(CrashFrame) string { return "il2cpp::os::CrashHelpers::CrashImpl()" }
	if k, _ := FrameKey(f, cpp); k != "libunity.so!il2cpp::os::CrashHelpers::CrashImpl" {
		t.Fatalf("C++ name: %q", k)
	}
	tmpl := func(CrashFrame) string { return "std::__ndk1::vector<int>::push_back(int const&)" }
	if k, _ := FrameKey(f, tmpl); k != "libunity.so!std::__ndk1::vector::push_back" {
		t.Fatalf("C++ template: %q", k)
	}
}

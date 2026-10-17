package fingerprint

import (
	"strings"
	"testing"
	"time"
)

// APM-1 verification 1: two different exceptions thrown in two different bus
// handlers are two groups, each with its real type and throw location, in
// every stack format.
func TestBusHandlers_TwoGroupsWithRealTypeAndThrowSite(t *testing.T) {
	for _, f := range []struct {
		name          string
		il2cpp        bool
		spawner, logs string
	}{
		{"mono", false, spawnerNREMono, busLogErrorStackEditor},
		{"il2cpp", true, spawnerNREIL2CPP, busLogErrorStackIL2CPP},
	} {
		t.Run(f.name, func(t *testing.T) {
			spawner := mustFingerprint(t, wrapped(f.spawner, f.logs))
			loader := mustFingerprint(t, wrapped(roomLoaderIOE("12", f.il2cpp), f.logs))
			if spawner.Fingerprint == loader.Fingerprint {
				t.Fatalf("two handlers, one group: %s", spawner.Fingerprint)
			}
			check := func(r Result, typ, throwMethod string) {
				t.Helper()
				if !r.Wrapped || r.Kind != LogTypeException {
					t.Errorf("kind %q wrapped %v", r.Kind, r.Wrapped)
				}
				if r.Type() != typ {
					t.Errorf("type %q, want %q", r.Type(), typ)
				}
				if !strings.HasPrefix(r.ThrowSite, throwMethod) {
					t.Errorf("throw site %q, want %s", r.ThrowSite, throwMethod)
				}
				if !strings.HasPrefix(r.CallSite, "Ozx.Core.GameEventBus.Publish") {
					t.Errorf("call site %q", r.CallSite)
				}
			}
			check(spawner, "System.NullReferenceException", "Ozx.Rooms.EnemySpawner.SpawnWave")
			check(loader, "System.InvalidOperationException", "Ozx.Rooms.RoomLoader.LoadTemplate")
		})
	}
	// With line numbers the throw site has the file and line.
	r := Analyze(wrapped(spawnerNREIL2CPPLines, busLogErrorStackIL2CPPLines))
	if r.ThrowSite != "Ozx.Rooms.EnemySpawner.SpawnWave (at /Users/builder/ozx/Assets/Scripts/Rooms/EnemySpawner.cs:97)" {
		t.Errorf("throw site %q", r.ThrowSite)
	}
	if r.CallSite != "Ozx.Core.GameEventBus.Publish (at /Users/builder/ozx/Assets/Scripts/Core/GameEventBus.cs:55)" {
		t.Errorf("call site %q", r.CallSite)
	}
}

// APM-1 verification 2: the same exception with different roomIds is one group.
func TestBusHandler_SameBugDifferentRoomIDIsOneGroup(t *testing.T) {
	// Real Mono output for roomId 12 and 7 (the NRE does not mention it).
	a := mustFingerprint(t, Input{LogType: LogTypeError, Message: "x", Embedded: strings.TrimPrefix(readFixture(t, "bus_room12.logerror_message.txt"), busPrefix)})
	b := mustFingerprint(t, Input{LogType: LogTypeError, Message: "x", Embedded: strings.TrimPrefix(readFixture(t, "bus_room7.logerror_message.txt"), busPrefix)})
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("room 12 %s, room 7 %s", a.Fingerprint, b.Fingerprint)
	}
	// The roomId in the message.
	var fps []string
	for _, id := range []string{"12", "7", "1043"} {
		for _, il2cpp := range []bool{false, true} {
			fps = append(fps, mustFingerprint(t, wrapped(roomLoaderIOE(id, il2cpp), busLogErrorStackEditor)).Fingerprint)
		}
	}
	for _, fp := range fps {
		if fp != fps[0] {
			t.Fatalf("roomIds split the group: %v", fps)
		}
	}
	r := Analyze(wrapped(roomLoaderIOE("1043", false), busLogErrorStackEditor))
	if r.MessageTemplate != "Room template not found for roomId=<v> (room_<n>)" {
		t.Errorf("template %q", r.MessageTemplate)
	}
}

// Mono and IL2CPP stacks of the same code, with and without line numbers,
// and with every line number changed, give the same fingerprint.
func TestMonoAndIL2CPPStacksOfSameCodeGiveSameFingerprint(t *testing.T) {
	inputs := map[string]Input{
		"mono":         wrapped(spawnerNREMono, busLogErrorStackEditor),
		"il2cpp":       wrapped(spawnerNREIL2CPP, busLogErrorStackIL2CPP),
		"il2cpp-lines": wrapped(spawnerNREIL2CPPLines, busLogErrorStackIL2CPPLines),
		"dotnet":       wrapped(spawnerNREDotNet, busLogErrorStackEditor),
		"mono-legacy":  legacy(spawnerNREMono, busLogErrorStackEditor),
		"il2cpp-old":   legacy(spawnerNREIL2CPP, busLogErrorStackIL2CPP),
	}
	want := Analyze(inputs["mono"]).Fingerprint
	for name, in := range inputs {
		if got := Analyze(in).Fingerprint; got != want {
			t.Errorf("%s: %s, mono: %s", name, got, want)
		}
	}

	// Same for a logged exception (Debug.LogException / unhandled): Unity's
	// format in the Editor and on IL2CPP.
	editor := Input{LogType: LogTypeException, Message: "NullReferenceException: Object reference not set to an instance of an object",
		Stack: "Ozx.Enemies.EnemyAI.UpdateTarget () (at Assets/Scripts/Enemies/EnemyAI.cs:57)\nOzx.Enemies.EnemyAI.Update () (at Assets/Scripts/Enemies/EnemyAI.cs:31)\n"}
	il2cpp := Input{LogType: LogTypeException, Message: "NullReferenceException: Object reference not set to an instance of an object.",
		Stack: "  at Ozx.Enemies.EnemyAI.UpdateTarget () [0x00000] in <00000000000000000000000000000000>:0 \n  at Ozx.Enemies.EnemyAI.Update () [0x00000] in <00000000000000000000000000000000>:0 \n"}
	il2cppLines := Input{LogType: LogTypeException, Message: "NullReferenceException: Object reference not set to an instance of an object.",
		Stack: "Ozx.Enemies.EnemyAI.UpdateTarget () (at /Users/builder/ozx/Assets/Scripts/Enemies/EnemyAI.cs:60)\nOzx.Enemies.EnemyAI.Update () (at /Users/builder/ozx/Assets/Scripts/Enemies/EnemyAI.cs:33)\n"}
	oldSDK := editor
	oldSDK.LogType = ""
	a, b, c, d := Analyze(editor), Analyze(il2cpp), Analyze(il2cppLines), Analyze(oldSDK)
	if a.Fingerprint != b.Fingerprint || a.Fingerprint != c.Fingerprint || a.Fingerprint != d.Fingerprint {
		t.Errorf("LogException: editor %s il2cpp %s il2cpp+lines %s old sdk %s", a.Fingerprint, b.Fingerprint, c.Fingerprint, d.Fingerprint)
	}
	if a.Type() != "NullReferenceException" || a.ThrowSite != "Ozx.Enemies.EnemyAI.UpdateTarget (at Assets/Scripts/Enemies/EnemyAI.cs:57)" {
		t.Errorf("type %q throw site %q", a.Type(), a.ThrowSite)
	}
}

func TestLineNumbersAndPathsDoNotChangeTheFingerprint(t *testing.T) {
	base := Analyze(wrapped(spawnerNREMono, busLogErrorStackEditor)).Fingerprint
	moved := strings.NewReplacer(":88", ":1088", ":82", ":5", ":48", ":49", "/Users/dev/ozx", "D:/ci/workspace").Replace(spawnerNREMono)
	if got := Analyze(wrapped(moved, busLogErrorStackEditor)).Fingerprint; got != base {
		t.Errorf("moved lines: %s, want %s", got, base)
	}
	// A different call site of the LogError does not matter either.
	if got := Analyze(wrapped(spawnerNREMono, "Ozx.Core.Other:Call ()\n")).Fingerprint; got != base {
		t.Errorf("other call site: %s, want %s", got, base)
	}
	// Renumbered compiler-generated lambda (code added above it in the class).
	renum := strings.Replace(spawnerNREMono, "<Register>b__2_0", "<Register>b__7_1", 1)
	if got := Analyze(wrapped(renum, busLogErrorStackEditor)).Fingerprint; got != base {
		t.Errorf("renumbered lambda: %s, want %s", got, base)
	}
	// But a different throwing method, or a different type, is another group.
	other := strings.Replace(spawnerNREMono, "SpawnWave", "SpawnBoss", 1)
	if Analyze(wrapped(other, busLogErrorStackEditor)).Fingerprint == base {
		t.Error("different throw site, same group")
	}
	otherType := strings.Replace(spawnerNREMono, "NullReferenceException", "MissingReferenceException", 1)
	if Analyze(wrapped(otherType, busLogErrorStackEditor)).Fingerprint == base {
		t.Error("different type, same group")
	}
}

// Inner exception chains (APM-4 review: InvalidDataException -> JsonException)
// are kept completely, in both the ToString() and Unity's "Rethrow as" form,
// and both forms group together.
func TestInnerExceptionChain(t *testing.T) {
	text := readFixture(t, "inner_chain.tostring.txt")
	r := mustFingerprint(t, Input{LogType: LogTypeError, Message: "Load failed", Embedded: text})
	if len(r.Chain) != 2 {
		t.Fatalf("chain %+v", r.Chain)
	}
	outer, inner := r.Chain[0], r.Chain[1]
	if outer.Type != "System.IO.InvalidDataException" || outer.Message != "Save file '/data/user/0/com.ozx/files/inventory.json' is corrupt (size 1234 bytes)" {
		t.Errorf("outer %q %q", outer.Type, outer.Message)
	}
	if inner.Type != "Newtonsoft.Json.JsonReaderException" || !strings.HasPrefix(inner.Message, "Unexpected character encountered") {
		t.Errorf("inner %q %q", inner.Type, inner.Message)
	}
	if len(outer.Frames) != 3 || outer.Frames[0].Method != "Ozx.Rooms.SaveLoader.Load" || outer.Frames[0].Line != 102 {
		t.Errorf("outer frames %+v", outer.Frames)
	}
	if len(inner.Frames) != 2 || inner.Frames[0].Method != "Newtonsoft.Json.JsonConvert.DeserializeObject" || inner.Frames[0].InApp ||
		inner.Frames[1].Line != 98 {
		t.Errorf("inner frames %+v", inner.Frames)
	}
	if got := r.InnerExceptions(); len(got) != 1 || !strings.HasPrefix(got[0], "Newtonsoft.Json.JsonReaderException: Unexpected character") {
		t.Errorf("inner exceptions %q", got)
	}

	// Unity's form of the same exception (Debug.LogException): the message is
	// the innermost exception, the outer one follows "Rethrow as".
	unity := readFixture(t, "inner_chain.unity_logexception.txt")
	nl := strings.IndexByte(unity, '\n')
	u := mustFingerprint(t, Input{LogType: LogTypeException, Message: unity[:nl], Stack: unity[nl+1:]})
	if len(u.Chain) != 2 || u.Chain[0].Type != "InvalidDataException" || u.Chain[1].Type != "JsonReaderException" {
		t.Fatalf("unity chain %+v", u.Chain)
	}
	if u.Chain[0].Frames[0].Line != 102 || u.Chain[1].Frames[1].Line != 98 {
		t.Errorf("unity frames %+v", u.Chain)
	}
	if u.Fingerprint != r.Fingerprint {
		t.Errorf("LogException %s, embedded %s", u.Fingerprint, r.Fingerprint)
	}

	// A different cause wrapped at the same place is another group.
	other := strings.Replace(text, "Newtonsoft.Json.JsonReaderException", "System.IO.IOException", 1)
	if Analyze(Input{LogType: LogTypeError, Message: "x", Embedded: other}).Fingerprint == r.Fingerprint {
		t.Error("different inner type, same group")
	}

	// Three levels.
	three := "System.Exception: outer ---> System.InvalidOperationException: middle ---> System.FormatException: inner 42\n" +
		"  at A.B.Parse () [0x00000] in <00000000000000000000000000000000>:0 \n" +
		"   --- End of inner exception stack trace ---\n" +
		"  at A.B.Load () [0x00000] in <00000000000000000000000000000000>:0 \n" +
		"   --- End of inner exception stack trace ---\n" +
		"  at A.B.Start () [0x00000] in <00000000000000000000000000000000>:0 \n"
	t3 := Analyze(Input{LogType: LogTypeError, Message: "x", Embedded: three})
	if len(t3.Chain) != 3 || t3.Chain[0].Frames[0].Method != "A.B.Start" || t3.Chain[1].Frames[0].Method != "A.B.Load" ||
		t3.Chain[2].Frames[0].Method != "A.B.Parse" || t3.Chain[2].Message != "inner 42" {
		t.Errorf("three levels %+v", t3.Chain)
	}
}

func TestAggregateException(t *testing.T) {
	r := mustFingerprint(t, Input{LogType: LogTypeError, Message: "x", Embedded: readFixture(t, "aggregate.tostring.txt")})
	types := []string{}
	for _, e := range r.Chain {
		types = append(types, e.Type)
	}
	want := "System.AggregateException System.Collections.Generic.KeyNotFoundException System.InvalidOperationException"
	if strings.Join(types, " ") != want {
		t.Errorf("chain %v", types)
	}
	if r.Chain[0].Frames[3].Method != "Samples.<Main>m" || r.Chain[1].Frames[0].Method != "Samples.ThrowAsync" ||
		r.Chain[2].Frames[0].Line != 164 {
		t.Errorf("frames %+v", r.Chain)
	}
	if r.ThrowSite != "Samples.<Main>m (at /Users/dev/ozx/Assets/Scripts/Samples.cs:165)" {
		t.Errorf("throw site %q", r.ThrowSite)
	}
}

func TestAsyncRethrowMarkerKeepsOneSection(t *testing.T) {
	r := mustFingerprint(t, Input{LogType: LogTypeError, Message: "x", Embedded: readFixture(t, "async_rethrow.tostring.txt")})
	if len(r.Chain) != 1 || len(r.Chain[0].Frames) != 4 || r.Chain[0].Frames[0].Method != "Samples.ThrowAsync" {
		t.Errorf("chain %+v", r.Chain)
	}
}

// Events from SDKs before APM-4 (no log type; the message cut to 500
// characters, which usually cuts the embedded stack) still get a fingerprint
// from what is left, and the same one as the new SDK when the top frames
// survived.
func TestLegacyEvents(t *testing.T) {
	long := "System.InvalidOperationException: Room template not found for roomId=12 (room_12)\n" +
		"  at Ozx.Rooms.RoomLoader.LoadTemplate (System.Int32 roomId) [0x00000] in <00000000000000000000000000000000>:0 \n" +
		"  at Ozx.Rooms.RoomLoader.OnRoomEntered (Ozx.Core.RoomEnteredEvent e) [0x00000] in <00000000000000000000000000000000>:0 \n" +
		strings.Repeat("  at Ozx.Rooms.RoomLoader.Filler (System.Int32 roomId) [0x00000] in <00000000000000000000000000000000>:0 \n", 3)
	old := legacy(long, busLogErrorStackIL2CPP)
	if len(old.Message) != 500 || !strings.HasSuffix(old.Message, "...") {
		t.Fatalf("fixture: %d", len(old.Message))
	}
	r := Analyze(old)
	if !r.Wrapped || r.Type() != "System.InvalidOperationException" {
		t.Fatalf("legacy wrapped not found: %+v", r)
	}
	for _, f := range r.Chain[0].Frames {
		if !strings.HasPrefix(f.Method, "Ozx.Rooms.RoomLoader.") {
			t.Errorf("cut line parsed as a frame: %+v", f)
		}
	}
	if r.Fingerprint != Analyze(wrapped(long, busLogErrorStackIL2CPP)).Fingerprint {
		t.Error("legacy and new fingerprints differ although the top frames are complete")
	}

	// A legacy logged exception: inferred from "XxxException:".
	e := Analyze(Input{Message: "KeyNotFoundException: The given key 'boss_3' was not present in the dictionary.",
		Stack: "System.Collections.Generic.Dictionary`2[TKey,TValue].get_Item (TKey key) (at <00000>:0)\nOzx.Data.Repo.Get (System.String id) (at Assets/Data/Repo.cs:20)\n"})
	if e.Kind != LogTypeException || e.Type() != "KeyNotFoundException" || e.ThrowSite != "Ozx.Data.Repo.Get (at Assets/Data/Repo.cs:20)" {
		t.Errorf("legacy exception %+v", e)
	}
	// A legacy plain error.
	p := Analyze(Input{Message: "Asset not found: anim/arack_red", Stack: "UnityEngine.Debug:LogError (object)\nOzx.Pool.Factory:Load (string) (at Assets/Pool/Factory.cs:33)\n"})
	if p.Kind != LogTypeError || p.Type() != "" || p.CallSite != "Ozx.Pool.Factory.Load (at Assets/Pool/Factory.cs:33)" {
		t.Errorf("legacy error %+v", p)
	}
}

// Plain errors group by message template and the calling method.
func TestPlainErrors(t *testing.T) {
	stack := "UnityEngine.Debug:LogError (object)\nOzx.Save.SaveSystem:Write (string) (at Assets/Save/SaveSystem.cs:88)\n"
	ilStack := "Ozx.Save.SaveSystem:Write(String)\n"
	a := mustFingerprint(t, Input{LogType: LogTypeError, Message: "Save write failed for slot 3 (1024 bytes, id=7c9e6679-7425-40de-944b-e07fc1f90ae7)", Stack: stack})
	b := mustFingerprint(t, Input{LogType: LogTypeError, Message: "Save write failed for slot 1 (88 bytes, id=0a1b2c3d-0000-4000-8000-000000000000)", Stack: ilStack})
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("same error, different values: %s %s (%q, %q)", a.Fingerprint, b.Fingerprint, a.MessageTemplate, b.MessageTemplate)
	}
	c := mustFingerprint(t, Input{LogType: LogTypeError, Message: "Save read failed for slot 3", Stack: stack})
	if c.Fingerprint == a.Fingerprint {
		t.Error("different message, same group")
	}
	d := mustFingerprint(t, Input{LogType: LogTypeAssert, Message: a.MessageTemplate, Stack: stack})
	e := mustFingerprint(t, Input{LogType: LogTypeError, Message: "Save write failed for slot 3 (1024 bytes, id=x)", Stack: "Ozx.Other:Call ()\n"})
	if d.Fingerprint == a.Fingerprint || e.Fingerprint == a.Fingerprint {
		t.Error("assert or other caller in the same group")
	}
	// Only the first line counts (a dump below it does not split the group).
	f := mustFingerprint(t, Input{LogType: LogTypeError, Message: "Save write failed for slot 9 (5 bytes, id=y)\n{\"a\": 1}", Stack: stack})
	if f.Fingerprint != a.Fingerprint {
		t.Error("second line changed the group")
	}
	// An error message that mentions an exception without a stack is a plain error.
	g := mustFingerprint(t, Input{LogType: LogTypeError, Message: busPrefix + "System.InvalidOperationException: Room 12 not found", Stack: ilStack})
	if g.Kind != LogTypeError || g.MessageTemplate != "[GameEventBus] Error handling event RoomEnteredEvent: System.InvalidOperationException: Room <n> not found" {
		t.Errorf("message-only exception: %+v", g)
	}
}

func TestTemplate(t *testing.T) {
	for in, want := range map[string]string{
		"Room 12 not found":                            "Room <n> not found",
		"Room room_12 not found":                       "Room room_<n> not found",
		"Room 'forest_boss' not found":                 "Room '*' not found",
		`Key "a b" missing`:                            `Key "*" missing`,
		"Can't load level3":                            "Can't load level<n>",
		"seed=184467440737095516, plan=p-7":            "seed=<v>, plan=<v>",
		"id 3f2504e0-4f89-11d3-9a0c-0305e82c3301 gone": "id <guid> gone",
		"ptr 0x7ffd4a2c fault":                         "ptr <hex> fault",
		"hash a3f9c2e1b7d4 mismatch":                   "hash <hex> mismatch",
		"deadbeef stays":                               "deadbeef stays",
		"took 1.25s, 1,024 items, delta -3":            "took <n>s, <n> items, delta <n>",
		"r-12 and v1.2.3":                              "r-<n> and v<n>",
		"  many   spaces\there  ":                      "many spaces here",
		"first line\nsecond 123":                       "first line",
		"房间 12 未找到":                                    "房间 <n> 未找到",
		"Unclosed 'quote 12":                           "Unclosed 'quote <n>",
		"empty '' quotes":                              "empty '*' quotes",
		"":                                             "",
		"Path '', line 1, position 1.":                 "Path '*', line <n>, position <n>.",
		"Object reference not set to an instance of an object": "Object reference not set to an instance of an object",
	} {
		if got := Template(in); got != want {
			t.Errorf("Template(%q) = %q, want %q", in, got, want)
		}
	}
	// Cut to 200 UTF-16 units; a surrogate pair is never split.
	long := strings.Repeat("a", 199) + "😀b"
	if got := Template(long); got != strings.Repeat("a", 199) {
		t.Errorf("surrogate split: %q", got)
	}
	if got := Template(strings.Repeat("x", 1000)); len(got) != 200 {
		t.Errorf("length %d", len(got))
	}
}

func TestNormalizeMethod(t *testing.T) {
	for in, want := range map[string]string{
		"Ozx.Core.GameEventBus.Publish[T]":                              "Ozx.Core.GameEventBus.Publish",
		"Ozx.Core.GameEventBus:Publish<Ozx.Core.RoomEnteredEvent>":      "Ozx.Core.GameEventBus.Publish",
		"Ozx.Rooms.EnemySpawner.<Register>b__2_0":                       "Ozx.Rooms.EnemySpawner.<Register>b",
		"Ozx.Rooms.EnemySpawner+<>c.<Register>b__2_0":                   "Ozx.Rooms.EnemySpawner+<>c.<Register>b",
		"Ozx.Rooms.EnemySpawner/<>c:<Register>b__2_0":                   "Ozx.Rooms.EnemySpawner+<>c.<Register>b",
		"Ozx.Rooms.Loader+<LoadRoutine>d__12.MoveNext":                  "Ozx.Rooms.Loader+<LoadRoutine>d.MoveNext",
		"Ozx.A+<>c__DisplayClass3_0.<Start>b__0":                        "Ozx.A+<>c__DisplayClass.<Start>b",
		"Ozx.A.<Start>g__Local|5_0":                                     "Ozx.A.<Start>g__Local",
		"System.Collections.Generic.Dictionary`2[TKey,TValue].get_Item": "System.Collections.Generic.Dictionary.get_Item",
		"System.Collections.Generic.Dictionary`2:get_Item":              "System.Collections.Generic.Dictionary.get_Item",
		"Ozx.Pool`1[T].Get":                                             "Ozx.Pool.Get",
		"Ozx.A..ctor":                                                   "Ozx.A..ctor",
		"Samples:Main":                                                  "Samples.Main",
	} {
		if got := NormalizeMethod(in); got != want {
			t.Errorf("NormalizeMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFrame(t *testing.T) {
	for _, c := range []struct {
		line, method, file string
		lineNo             int
		inApp              bool
	}{
		{"  at Ozx.A.B (System.Int32 x) [0x0001a] in /p/A.cs:42 ", "Ozx.A.B", "/p/A.cs", 42, true},
		{"  at Ozx.A.B (System.Int32 x) [0x00000] in <00000000000000000000000000000000>:0 ", "Ozx.A.B", "", 0, true},
		{"  at Ozx.A.B () [0x00000] in <filename unknown>:0 ", "Ozx.A.B", "", 0, true},
		{"   at Ozx.A.B(Int32 x) in C:\\p\\A.cs:line 42", "Ozx.A.B", "C:\\p\\A.cs", 42, true},
		{"   at Ozx.A.B(Int32 x)", "Ozx.A.B", "", 0, true},
		{"Ozx.A:B (int) (at Assets/A.cs:42)", "Ozx.A.B", "Assets/A.cs", 42, true},
		{"Ozx.A:B(Int32) (at C:/ci/Assets/A.cs:42)", "Ozx.A.B", "C:/ci/Assets/A.cs", 42, true},
		{"Ozx.A:B(Int32)", "Ozx.A.B", "", 0, true},
		{"Ozx.A.B () (at Assets/A.cs:7)", "Ozx.A.B", "Assets/A.cs", 7, true},
		{"UnityEngine.Debug:LogError (object)", "UnityEngine.Debug.LogError", "", 0, false},
		{"  at (wrapper managed-to-native) UnityEngine.Object.Internal_InstantiateSingle(UnityEngine.Object,UnityEngine.Vector3,UnityEngine.Quaternion)", "UnityEngine.Object.Internal_InstantiateSingle", "", 0, false},
		{"  at (wrapper delegate-invoke) System.Action`1[Ozx.Core.RoomEnteredEvent].invoke_void_T(Ozx.Core.RoomEnteredEvent)", "System.Action.invoke_void_T", "", 0, false},
		{"Ozx.Core.GameEventBus:Publish<Ozx.Core.RoomEnteredEvent, System.Int32> (Ozx.Core.RoomEnteredEvent) (at Assets/Bus.cs:5)", "Ozx.Core.GameEventBus.Publish", "Assets/Bus.cs", 5, true},
		{"OzxApm.Collectors.ExceptionCollector:OnLogMessage (string,string,UnityEngine.LogType)", "OzxApm.Collectors.ExceptionCollector.OnLogMessage", "", 0, false},
	} {
		f, ok := parseFrame(c.line)
		if !ok || f.Method != c.method || f.File != c.file || f.Line != c.lineNo || f.InApp != c.inApp {
			t.Errorf("parseFrame(%q) = %+v %v", c.line, f, ok)
		}
	}
	for _, line := range []string{
		"", "System.NullReferenceException: Object reference not set (x)", "Error: something (details)",
		"Rethrow as InvalidDataException: x (y)", "... (12 frames omitted)", "--- End of inner exception stack trace ---",
		"Room template not found for roomId=12 (room_12)", "just words", "{\"json\": 1}",
	} {
		if f, ok := parseFrame(line); ok {
			t.Errorf("parseFrame(%q) = %+v, want not a frame", line, f)
		}
	}
}

func TestFindEmbedded(t *testing.T) {
	for msg, want := range map[string]int{
		busPrefix + spawnerNREMono:                                                           len(busPrefix),
		spawnerNREIL2CPP:                                                                     0,
		"Failed: (" + spawnerNREDotNet + ")":                                                 len("Failed: ("),
		busPrefix + "System.Exception: no stack":                                             -1, // a message, not an exception dump
		"ExceptionHandler:Run failed\n  at A.B () [0x0] in <x>:0":                            -1, // not a type ending in Exception
		"MyException: a ---> System.IO.IOException: b":                                       0,  // inner exception, no frames
		"x: System.AggregateException: a\n---> (Inner Exception #0) System.Exception: b<---": 3,  // aggregate block, no frames
		"x: SomeException: a\n--- End of inner exception stack trace ---\nb ---> c":          -1, // "--->" after the header
		"foo-System.Exception: glued\n  at A.B () [0x0] in <x>:0":                            -1, // no boundary before the type
		"Plain error with no exception at all":                                               -1,
	} {
		if got := FindEmbedded(msg); got != want {
			t.Errorf("FindEmbedded(%.60q) = %d, want %d", msg, got, want)
		}
	}
}

// A new SDK reports a wrapped exception only through "embedded"; the server
// does not search the message for one (the SDK did, before truncating it).
func TestNewSDKErrorWithoutEmbeddedIsPlain(t *testing.T) {
	in := wrapped(spawnerNREMono, busLogErrorStackEditor)
	in.Embedded = ""
	if r := Analyze(in); r.Kind != LogTypeError || r.Wrapped {
		t.Errorf("%+v", r)
	}
}

// Hostile or broken input never panics and is parsed in bounded time.
func TestHostileInput(t *testing.T) {
	huge := strings.Repeat("System.Exception: x ---> ", 10000) + strings.Repeat("\n  at A.B (((((((", 50000)
	start := time.Now()
	for _, in := range []Input{
		{LogType: LogTypeError, Message: huge, Embedded: huge, Stack: huge},
		{Message: huge, Stack: huge},
		{LogType: LogTypeException, Message: huge, Stack: strings.Repeat("Rethrow as X: y\n", 100000)},
		{LogType: "weird", Message: "\x00\xff\xfe", Stack: "at\n(\n)\n<\n["},
		{LogType: LogTypeError, Message: strings.Repeat("Exception: ", 100000)},
		{},
	} {
		r := Analyze(in)
		if len(r.Chain) > maxChain {
			t.Errorf("chain of %d", len(r.Chain))
		}
		for _, e := range r.Chain {
			if len(e.Frames) > maxFramesPerException {
				t.Errorf("%d frames", len(e.Frames))
			}
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func FuzzAnalyze(f *testing.F) {
	f.Add("error", busPrefix+spawnerNREMono, busLogErrorStackEditor, spawnerNREMono)
	f.Add("exception", "X: y", "Rethrow as Z: w\nA.B ()", "")
	f.Add("", "x\n  at A.B () [0x0] in <z>:0", "", "")
	f.Fuzz(func(t *testing.T, logType, msg, stack, embedded string) {
		r := Analyze(Input{LogType: logType, Message: msg, Stack: stack, Embedded: embedded})
		if len(r.Fingerprint) != 16 {
			t.Fatalf("fingerprint %q", r.Fingerprint)
		}
		_ = Template(msg)
		_ = FindEmbedded(msg)
	})
}

func BenchmarkAnalyzeWrappedChain(b *testing.B) {
	in := Input{LogType: LogTypeError, Message: "x", Stack: busLogErrorStackEditor,
		Embedded: strings.Repeat("  at Ozx.A.B (System.Int32 x) [0x0001a] in /Users/dev/ozx/Assets/Scripts/A.cs:42 \n", 1)}
	in.Embedded = "System.IO.InvalidDataException: bad ---> System.FormatException: worse\n" +
		strings.Repeat("  at Ozx.A.B (System.Int32 x) [0x0001a] in /Users/dev/ozx/Assets/Scripts/A.cs:42 \n", 30) +
		"   --- End of inner exception stack trace ---\n" +
		strings.Repeat("  at Ozx.C.D (System.Int32 x) [0x0001a] in /Users/dev/ozx/Assets/Scripts/C.cs:42 \n", 30)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Analyze(in)
	}
}

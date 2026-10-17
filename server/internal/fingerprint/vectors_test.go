package fingerprint

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

// vector is one case of testdata/vectors.json. The file pins the output of
// this version of the algorithm: if this test fails, either the change was
// not meant to regroup anything (fix it), or it was (bump Version and
// regenerate with go test ./internal/fingerprint -run TestVectors -update).
// The SDK's ExceptionParser is checked against the same file, so that the
// device and the server group alike.
type vector struct {
	Name     string `json:"name"`
	Input    Input  `json:"input"`
	Expected struct {
		Fingerprint     string   `json:"fingerprint"`
		Kind            string   `json:"kind"`
		Wrapped         bool     `json:"wrapped"`
		Type            string   `json:"type"`
		MessageTemplate string   `json:"message_template"`
		ThrowSite       string   `json:"throw_site"`
		CallSite        string   `json:"call_site"`
		Inner           []string `json:"inner_exceptions"`
	} `json:"expected"`
}

func vectorInputs(t *testing.T) []vector {
	unity := readFixture(t, "inner_chain.unity_logexception.txt")
	nl := strings.IndexByte(unity, '\n')
	ins := []struct {
		name string
		in   Input
	}{
		{"bus nre mono", wrapped(spawnerNREMono, busLogErrorStackEditor)},
		{"bus nre il2cpp", wrapped(spawnerNREIL2CPP, busLogErrorStackIL2CPP)},
		{"bus nre il2cpp lines", wrapped(spawnerNREIL2CPPLines, busLogErrorStackIL2CPPLines)},
		{"bus nre dotnet", wrapped(spawnerNREDotNet, busLogErrorStackEditor)},
		{"bus ioe room 12 mono", wrapped(roomLoaderIOE("12", false), busLogErrorStackEditor)},
		{"bus ioe room 7 il2cpp", wrapped(roomLoaderIOE("7", true), busLogErrorStackIL2CPP)},
		{"bus real mono room 12", Input{LogType: LogTypeError, Message: "x", Embedded: strings.TrimPrefix(readFixture(t, "bus_room12.logerror_message.txt"), busPrefix)}},
		{"inner chain tostring", Input{LogType: LogTypeError, Message: "Load failed", Embedded: readFixture(t, "inner_chain.tostring.txt")}},
		{"inner chain unity", Input{LogType: LogTypeException, Message: unity[:nl], Stack: unity[nl+1:]}},
		{"aggregate", Input{LogType: LogTypeError, Message: "x", Embedded: readFixture(t, "aggregate.tostring.txt")}},
		{"async rethrow", Input{LogType: LogTypeError, Message: "x", Embedded: readFixture(t, "async_rethrow.tostring.txt")}},
		{"logexception editor", Input{LogType: LogTypeException, Message: "NullReferenceException: Object reference not set to an instance of an object",
			Stack: "Ozx.Enemies.EnemyAI.UpdateTarget () (at Assets/Scripts/Enemies/EnemyAI.cs:57)\nOzx.Enemies.EnemyAI.Update () (at Assets/Scripts/Enemies/EnemyAI.cs:31)\n"}},
		{"logexception il2cpp", Input{LogType: LogTypeException, Message: "NullReferenceException: Object reference not set to an instance of an object.",
			Stack: "  at Ozx.Enemies.EnemyAI.UpdateTarget () [0x00000] in <00000000000000000000000000000000>:0 \n  at Ozx.Enemies.EnemyAI.Update () [0x00000] in <00000000000000000000000000000000>:0 \n"}},
		{"logexception no frames", Input{LogType: LogTypeException, Message: "TimeoutException: Request 42 timed out after 3000 ms", Stack: ""}},
		{"plain error editor", Input{LogType: LogTypeError, Message: "Save write failed for slot 3 (1024 bytes, id=7c9e6679-7425-40de-944b-e07fc1f90ae7)",
			Stack: "UnityEngine.Debug:LogError (object)\nOzx.Save.SaveSystem:Write (string) (at Assets/Save/SaveSystem.cs:88)\n"}},
		{"plain error il2cpp", Input{LogType: LogTypeError, Message: "Save write failed for slot 1 (88 bytes, id=0a1b2c3d-0000-4000-8000-000000000000)",
			Stack: "Ozx.Save.SaveSystem:Write(String)\n"}},
		{"assert", Input{LogType: LogTypeAssert, Message: "Assertion failed: hp >= 0 (hp=-3)", Stack: "UnityEngine.Debug:Assert (bool,string)\nOzx.Combat.Health:Apply (int) (at Assets/Combat/Health.cs:12)\n"}},
		{"plain error unicode", Input{LogType: LogTypeError, Message: "房间 12 未找到 '森林_3' 😀 ok=是", Stack: "Ozx.UI.Panel:Open(String)\n"}},
		{"plain error no stack", Input{LogType: LogTypeError, Message: "Pool not found: enemy_goblin_2", Stack: ""}},
		{"legacy exception", Input{Message: "KeyNotFoundException: The given key 'boss_3' was not present in the dictionary.",
			Stack: "System.Collections.Generic.Dictionary`2[TKey,TValue].get_Item (TKey key) (at <00000>:0)\nOzx.Data.Repo.Get (System.String id) (at Assets/Data/Repo.cs:20)\n"}},
		{"legacy wrapped", legacy(spawnerNREIL2CPP, busLogErrorStackIL2CPP)},
		{"legacy plain", Input{Message: "Asset not found: anim/arack_red", Stack: "UnityEngine.Debug:LogError (object)\nOzx.Pool.Factory:Load (string) (at Assets/Pool/Factory.cs:33)\n"}},
	}
	out := make([]vector, len(ins))
	for i, c := range ins {
		out[i].Name, out[i].Input = c.name, c.in
	}
	return out
}

func TestVectors(t *testing.T) {
	const path = "testdata/vectors.json"
	vs := vectorInputs(t)
	for i := range vs {
		r := Analyze(vs[i].Input)
		e := &vs[i].Expected
		e.Fingerprint, e.Kind, e.Wrapped, e.Type = r.Fingerprint, r.Kind, r.Wrapped, r.Type()
		e.MessageTemplate, e.ThrowSite, e.CallSite, e.Inner = r.MessageTemplate, r.ThrowSite, r.CallSite, r.InnerExceptions()
	}
	got, err := json.MarshalIndent(map[string]interface{}{"version": Version, "vectors": vs}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s does not match the algorithm's output (see the vector type's comment); diff it with -update", path)
	}
}

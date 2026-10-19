package sessionexport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/processor"
)

var t0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func fp(v float64) *float64 { return &v }
func ip(v int64) *int64     { return &v }

// rowsFromSDKFixture decodes the events the SDK serialized
// (processor/testdata/sdk_events_apm10.jsonl) as ingest does and turns them
// into the rows the repository reads back.
func rowsFromSDKFixture(t *testing.T) Input {
	t.Helper()
	b, err := os.ReadFile("../processor/testdata/sdk_events_apm10.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var in Input
	for i, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var head struct {
			Type      string `json:"type"`
			Timestamp int64  `json:"timestamp"`
			Seq       uint64 `json:"seq"`
		}
		if err := json.Unmarshal([]byte(l), &head); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		ts := time.UnixMilli(head.Timestamp).UTC()
		switch head.Type {
		case "custom_event":
			var e models.CustomEvent
			if err := json.Unmarshal([]byte(l), &e); err != nil {
				t.Fatal(err)
			}
			processor.NormalizeEventContext(&e.EventContext, e.Platform)
			processor.NormalizeCustomEvent(&e)
			in.Rows = append(in.Rows, Row{Source: SourceCustomEvent, Time: ts, Seq: head.Seq, GameT: e.GameT, Frame: e.Frame,
				RunID: e.Context["runId"], Name: e.Name, Count: int64(e.Count), Aggregated: bool(e.Aggregated), CtxMerged: bool(e.CtxMerged),
				Props: e.Props, PropTypes: e.PropTypes})
			in.Session = Session{SessionID: e.SessionID, DeviceID: e.DeviceID, AppVersion: e.AppVersion, Platform: e.Platform,
				DeviceModel: e.DeviceModel, OSVersion: e.OSVersion, BuildType: e.BuildType,
				Meta: map[string]string{"gitCommit": e.Context["gitCommit"], "scene": "Gameplay"}}
		case "exception":
			var e models.Exception
			if err := json.Unmarshal([]byte(l), &e); err != nil {
				t.Fatal(err)
			}
			processor.NormalizeEventContext(&e.EventContext, e.Platform)
			in.Rows = append(in.Rows, Row{Source: SourceException, Time: ts, Seq: head.Seq, GameT: e.GameT, Frame: e.Frame,
				RunID: e.Context["runId"], Fields: []Field{{Key: "message", Value: e.Message}, {Key: "count", Value: int64(e.Count)}}})
		case "jank":
			var e models.Jank
			if err := json.Unmarshal([]byte(l), &e); err != nil {
				t.Fatal(err)
			}
			processor.NormalizeEventContext(&e.EventContext, e.Platform)
			in.Rows = append(in.Rows, Row{Source: SourceJank, Time: ts, Seq: head.Seq, GameT: e.GameT, Frame: e.Frame,
				RunID: e.Context["runId"], Fields: []Field{{Key: "durationMs", Value: float64(e.DurationMs)}, {Key: "maxFrameMs", Value: float64(e.MaxFrameMs)}}})
		default:
			t.Fatalf("unexpected type %s", head.Type)
		}
	}
	first, last := in.Rows[0].Time, in.Rows[0].Time
	for _, r := range in.Rows {
		if r.Time.Before(first) {
			first = r.Time
		}
		if r.Time.After(last) {
			last = r.Time
		}
	}
	in.Session.FirstSeen, in.Session.LastSeen = first, last
	return in
}

func fileOf(t *testing.T, res *Result, name string) string {
	t.Helper()
	for _, f := range res.Files {
		if f.Name == name {
			return string(f.Data)
		}
	}
	t.Fatalf("no %s in %v", name, names(res))
	return ""
}

func names(res *Result) []string {
	var n []string
	for _, f := range res.Files {
		n = append(n, f.Name)
	}
	return n
}

// jsonLines parses every line of a JSONL file, failing on anything else.
func jsonLines(t *testing.T, name, data string) []map[string]any {
	t.Helper()
	if !strings.HasSuffix(data, "\n") {
		t.Errorf("%s does not end with a newline", name)
	}
	var out []map[string]any
	for i, l := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		dec := json.NewDecoder(strings.NewReader(l))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("%s line %d is not JSON: %v\n%s", name, i+1, err, l)
		}
		if dec.More() {
			t.Fatalf("%s line %d has more than one value", name, i+1)
		}
		out = append(out, m)
	}
	return out
}

// The SDK's scripted run becomes AiTelemetry's files: channel lines with the
// client's t and frame, run numbers, typed fields and vectors; APM's events
// in apm.jsonl.
func TestBuild_SDKFixture(t *testing.T) {
	in := rowsFromSDKFixture(t)
	res := Build(in, t0)
	if got := strings.Join(names(res), ","); got != "session.json,apm.jsonl,custom.jsonl,encounter.jsonl,loot.jsonl,playermovement.jsonl,runstart.jsonl" {
		t.Fatalf("files %s", got)
	}
	if !strings.HasPrefix(res.Dir, "2026-10-") && !strings.HasPrefix(res.Dir, "2025-10-") {
		t.Logf("dir %s", res.Dir)
	}
	if !strings.HasSuffix(res.Dir, "_1761000000000_a10a10a1") {
		t.Errorf("dir %s", res.Dir)
	}

	pm := fileOf(t, res, "playermovement.jsonl")
	want := []string{
		`{"t":1.2500,"frame":76,"run":1,"ev":"room_enter","pos":{"x":5.000,"y":0.660},"roomId":"f0_room_0_0"}`,
		`{"t":6.6600,"frame":378,"run":1,"ev":"damaged","amount":34.322,"damageType":"Physical","hitDirection":{"x":0.347,"y":-0.938},` +
			`"hitPosition":{"x":4.972,"y":1.290},"isCrit":false,"pos":{"x":4.990,"y":1.021},"rawAmount":70.000,"remainingHp":1372.178,` +
			`"roomId":"f0_room_0_0","srcId":"rail_heavy","srcInstanceId":2,"srcKind":"EnemyProjectile","srcTypeId":"rail_heavy"}`,
		`{"t":14.3600,"frame":840,"run":1,"ev":"room_exit","pos":{"x":4.534,"y":15.979},"roomId":"f0_room_0_0"}`,
		`{"t":14.3600,"frame":840,"run":1,"ev":"room_enter","pos":{"x":5.000,"y":0.660},"roomId":"f0_room_1_0"}`,
		`{"t":30.0833,"frame":1767,"run":1,"ev":"died","damageType":"Physical","hit0Amount":49.032,"hit0Src":"quick_sand_worm",` +
			`"hit0SrcInstanceId":80,"hit0T":29.6000,"hit0Type":"Physical","hitDirection":{"x":0.000,"y":-1.000},"hitPosition":{"x":1.700,"y":8.500},` +
			`"killerId":"quick_sand_worm","killerInstanceId":80,"killerKind":"EnemyMelee","killerTypeId":"quick_sand_worm","liveEnemies":7,` +
			`"pos":{"x":1.703,"y":8.090},"roomId":"f0_room_1_0"}`,
		`{"t":32.0997,"frame":1870,"run":1,"ev":"revived","currentHp":2976.500,"pos":{"x":1.703,"y":8.090},"roomId":"f0_room_1_0"}`,
	}
	if got := strings.Split(strings.TrimSuffix(pm, "\n"), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("playermovement.jsonl:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := fileOf(t, res, "runstart.jsonl"); got != `{"t":0.5000,"frame":31,"run":1,"ev":"run"}`+"\n"+
		`{"t":30.0833,"frame":1767,"run":1,"ev":"death","canRevive":true,"killer":"quick_sand_worm","level":4,"maxRevives":3,"revivesUsed":0,"room":"f0_room_1_0"}`+"\n"+
		`{"t":32.0997,"frame":1870,"run":1,"ev":"revive","maxRevives":3,"revivesUsed":1}`+"\n"+
		`{"t":57.8865,"frame":3426,"run":1,"ev":"end","dead":true,"deaths":1,"exp":20,"level":4,"levelUps":3,"reason":"defeat","revives":1,"startLevel":1}`+"\n" {
		t.Errorf("runstart.jsonl:\n%s", got)
	}
	if got := fileOf(t, res, "encounter.jsonl"); got != `{"t":1.2500,"frame":76,"run":1,"ev":"room_ready","folder":"room01_f0_room_0_0","isCave":false,"roomId":"f0_room_0_0"}`+"\n"+
		`{"t":12.0000,"frame":700,"run":1,"ev":"room_cleared","roomId":"f0_room_0_0"}`+"\n"+
		`{"t":14.3600,"frame":840,"run":1,"ev":"room_exit","roomId":"f0_room_0_0"}`+"\n" {
		t.Errorf("encounter.jsonl:\n%s", got)
	}
	if got := fileOf(t, res, "loot.jsonl"); got != `{"t":9.0847,"frame":514,"run":1,"ev":"drop","enemy":"rail_scout","item":"item_exp_small","pos":{"x":6.750,"y":2.500},"room":"f0_room_0_0","src":"enemy","stage":"teaching","table":"loot_common"}`+"\n" {
		t.Errorf("loot.jsonl:\n%s", got)
	}
	if got := fileOf(t, res, "custom.jsonl"); got != `{"t":20.5000,"frame":1210,"run":1,"ev":"boss_spawn","bossId":"worm_king"}`+"\n" {
		t.Errorf("custom.jsonl:\n%s", got)
	}
	apm := jsonLines(t, "apm.jsonl", fileOf(t, res, "apm.jsonl"))
	if len(apm) != 3 || apm[0]["ev"] != "apm_exception" || apm[1]["ev"] != "apm_jank" || apm[2]["ev"] != "apm_custom_event" {
		t.Fatalf("apm.jsonl %v", apm)
	}
	if apm[0]["t"] != json.Number("21.0000") || apm[0]["frame"] != json.Number("1240") || apm[1]["t"] != json.Number("22.4000") {
		t.Errorf("apm clocks %v %v", apm[0], apm[1])
	}
	agg := apm[2]
	if _, has := agg["t"]; has || agg["name"] != "playermovement.damaged" || agg["count"] != json.Number("5") || agg["aggregated"] != true || agg["run"] != json.Number("1") {
		t.Errorf("aggregated %v", agg)
	}
	if res.WithoutClock != 1 || res.DroppedProps != 0 || len(res.RunIDs) != 1 || res.RunIDs[0] != "run-2001" {
		t.Errorf("result %+v", res)
	}

	var session map[string]any
	if err := json.Unmarshal([]byte(fileOf(t, res, "session.json")), &session); err != nil {
		t.Fatal(err)
	}
	if session["gitCommit"] != "6116e2e4ff38de10eac61ba3c425e8f50a3aacdb" || session["scene"] != "Gameplay" || session["timeBase"] != TimeBase {
		t.Errorf("session.json %v", session)
	}
	a := session["apm"].(map[string]any)
	if a["sessionId"] != "1761000000000_a10a10a1" || a["linesWithoutGameClock"] != float64(1) {
		t.Errorf("apm %v", a)
	}
	runs := a["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["runId"] != "run-2001" {
		t.Errorf("runs %v", runs)
	}
	lines := a["lines"].(map[string]any)
	if lines["playermovement.jsonl"] != float64(6) || lines["runstart.jsonl"] != float64(4) || lines["apm.jsonl"] != float64(3) {
		t.Errorf("lines %v", lines)
	}
}

// Every line of every channel file has AiTelemetry's leading fields, in order.
func TestBuild_ChannelLinesStartWithTFrameRunEv(t *testing.T) {
	res := Build(rowsFromSDKFixture(t), t0)
	for _, f := range res.Files {
		if f.Name == FileSession || f.Name == FileAPM {
			continue
		}
		for i, l := range strings.Split(strings.TrimSuffix(string(f.Data), "\n"), "\n") {
			if !strings.HasPrefix(l, `{"t":`) || !strings.Contains(l, `,"frame":`) || !strings.Contains(l, `,"run":`) || !strings.Contains(l, `,"ev":`) {
				t.Errorf("%s line %d: %s", f.Name, i+1, l)
			}
			m := jsonLines(t, f.Name, l+"\n")[0]
			if _, err := m["t"].(json.Number).Float64(); err != nil {
				t.Errorf("%s line %d: t %v", f.Name, i+1, m["t"])
			}
			if _, err := m["frame"].(json.Number).Int64(); err != nil {
				t.Errorf("%s line %d: frame %v", f.Name, i+1, m["frame"])
			}
		}
	}
}

// Untrusted game text cannot break a line: every line stays one JSON object.
func TestBuild_HostileTextStaysValidJSONL(t *testing.T) {
	hostile := []string{
		"line\nbreak", "cr\rlf", "quote\" and \\ backslash", "tab\tand \x00 nul and \x1f", "\u2028 and \u2029",
		"emoji 🙂 and 中文", string([]byte{0xff, 0xfe, 'x'}), `</script><img onerror=alert(1)>`, `"},{"ev":"x`,
	}
	var rows []Row
	for i, s := range hostile {
		rows = append(rows, Row{Source: SourceCustomEvent, Time: t0.Add(time.Duration(i) * time.Second), Seq: uint64(i), GameT: fp(float64(i)), Frame: ip(int64(i)),
			RunID: s, Name: "playermovement.died", Count: 1, Props: map[string]string{"killerId": s, "n": "1", s: "v"}, PropTypes: map[string]uint8{"n": 1}})
		rows = append(rows, Row{Source: SourceException, Time: t0.Add(time.Duration(i) * time.Second), Seq: uint64(100 + i), RunID: s,
			Fields: []Field{{Key: "message", Value: s}, {Key: "stack", Value: s + "\n  at X"}, {Key: "lines", Value: []string{s, s}}}})
	}
	res := Build(Input{Session: Session{SessionID: "s\n../../etc", FirstSeen: t0, LastSeen: t0, Meta: map[string]string{"gitCommit": hostile[0], "scene": hostile[8]}}, Rows: rows}, t0)
	if res.Dir != "2026-10-20_090000_setc" {
		t.Errorf("dir %q", res.Dir)
	}
	for _, f := range res.Files {
		if f.Name == FileSession {
			var m map[string]any
			if err := json.Unmarshal(f.Data, &m); err != nil {
				t.Fatalf("session.json: %v", err)
			}
			continue
		}
		got := jsonLines(t, f.Name, string(f.Data))
		if len(got) != len(hostile) {
			t.Errorf("%s: %d lines, want %d", f.Name, len(got), len(hostile))
		}
	}
	pm := jsonLines(t, "playermovement.jsonl", fileOf(t, res, "playermovement.jsonl"))
	if pm[1]["killerId"] != "cr\rlf" || pm[0]["n"] != json.Number("1") {
		t.Errorf("round trip %v", pm[1])
	}
	if len(res.RunIDs) != len(hostile) {
		t.Errorf("runs %v", res.RunIDs)
	}
}

func TestChannel(t *testing.T) {
	cases := map[string][2]string{
		"playermovement.room_enter":    {"playermovement", "room_enter"},
		"ozx1059_aim.x":                {"ozx1059_aim", "x"},
		"loot.drop.extra":              {"loot", "drop.extra"},
		"room_enter":                   {"", ""},
		".x":                           {"", ""},
		"x.":                           {"", ""},
		"Player.x":                     {"", ""},
		"apm.x":                        {"", ""},
		"custom.x":                     {"", ""},
		"session.x":                    {"", ""},
		"level.x":                      {"", ""},
		"a-b.x":                        {"", ""},
		"..x":                          {"", ""},
		"_x.y":                         {"", ""},
		strings.Repeat("a", 49) + ".x": {"", ""},
	}
	for name, want := range cases {
		ch, ev, ok := Channel(name)
		if ok != (want[0] != "") || ch != want[0] || ev != want[1] {
			t.Errorf("%q: %q %q %v", name, ch, ev, ok)
		}
	}
}

// Lines without the client's clock never go to a channel file; reserved keys
// are left out; vectors only from two numbers; run 0 outside runs; a number
// type over text that is not a JSON number writes the text.
func TestBuild_Rules(t *testing.T) {
	rows := []Row{
		{Source: SourceCustomEvent, Time: t0, Seq: 1, Name: "loot.drop", Count: 1, Props: map[string]string{"item": "a"}}, // old SDK: no clock
		{Source: SourceCustomEvent, Time: t0.Add(time.Second), Seq: 2, GameT: fp(1), Frame: ip(60), Name: "loot.drop", Count: 1,
			Props:     map[string]string{"t": "9", "run": "9", "ev": "x", "frame": "9", "item": "b", "pos.x": "1.5", "pos.y": "abc", "dir.x": "1", "dir.y": "2", "dir": "s", "n": "1e400"},
			PropTypes: map[string]uint8{"pos.x": 1, "pos.y": 1, "dir.x": 1, "dir.y": 1, "n": 1}},
		{Source: SourceCustomEvent, Time: t0.Add(2 * time.Second), Seq: 3, GameT: fp(2), Frame: ip(120), RunID: "r-9", Name: "runstart.run", Count: 1},
		{Source: SourceCustomEvent, Time: t0.Add(3 * time.Second), Seq: 4, GameT: fp(3), Frame: ip(180), RunID: "r-10", Name: "runstart.run", Count: 1},
		{Source: SourceCustomEvent, Time: t0.Add(4 * time.Second), Seq: 5, GameT: fp(4), Frame: ip(240), RunID: "r-9", Name: "runstart.end", Count: 1},
		{Source: SourceRun, Time: t0.Add(5 * time.Second), Seq: 6, GameT: fp(5), Frame: ip(300), RunID: "r-10", Fields: []Field{{Key: "runId", Value: "r-10"}, {Key: "peakRssMb", Value: 512.5}}},
	}
	res := Build(Input{Session: Session{SessionID: "s", FirstSeen: t0, LastSeen: t0}, Rows: rows}, t0)
	loot := fileOf(t, res, "loot.jsonl")
	if loot != `{"t":1.0000,"frame":60,"run":0,"ev":"drop","dir":"s","dir.x":1,"dir.y":2,"item":"b","n":"1e400","pos.x":1.5,"pos.y":"abc"}`+"\n" {
		t.Errorf("loot.jsonl %s", loot)
	}
	if res.DroppedProps != 4 {
		t.Errorf("dropped %d", res.DroppedProps)
	}
	if rs := fileOf(t, res, "runstart.jsonl"); rs != `{"t":2.0000,"frame":120,"run":1,"ev":"run"}`+"\n"+`{"t":3.0000,"frame":180,"run":2,"ev":"run"}`+"\n"+`{"t":4.0000,"frame":240,"run":1,"ev":"end"}`+"\n" {
		t.Errorf("runstart.jsonl %s", rs)
	}
	apm := fileOf(t, res, "apm.jsonl")
	if apm != `{"run":0,"ev":"apm_custom_event","ts":"2026-10-20T09:00:00.000Z","name":"loot.drop","count":1,"props":{"item":"a"}}`+"\n"+
		`{"t":5.0000,"frame":300,"run":2,"ev":"apm_run","ts":"2026-10-20T09:00:05.000Z","runId":"r-10","peakRssMb":512.5}`+"\n" {
		t.Errorf("apm.jsonl %s", apm)
	}
}

// The files are cut in time order at the byte limit, and that is reported.
func TestBuild_ByteLimit(t *testing.T) {
	old := maxBytes
	defer func() { maxBytes = old }()
	maxBytes = 8000
	var rows []Row
	for i := 0; i < 200; i++ {
		rows = append(rows, Row{Source: SourceCustomEvent, Time: t0.Add(time.Duration(i) * time.Millisecond), Seq: uint64(i), GameT: fp(float64(i)), Frame: ip(int64(i)),
			Name: "playermovement.damaged", Count: 1, Props: map[string]string{"amount": "1.000"}, PropTypes: map[string]uint8{"amount": 1}})
	}
	res := Build(Input{Session: Session{SessionID: "s", FirstSeen: t0, LastSeen: t0}, Rows: rows, Truncated: []string{SourcePerf}}, t0)
	n := res.Lines["playermovement.jsonl"]
	if n == 0 || n >= 200 || res.TotalBytes > maxBytes {
		t.Errorf("%d lines, %d bytes", n, res.TotalBytes)
	}
	if strings.Join(res.Truncated, ",") != "perf_sample,bytes" {
		t.Errorf("truncated %v", res.Truncated)
	}
	got := jsonLines(t, "pm", fileOf(t, res, "playermovement.jsonl"))
	if got[len(got)-1]["frame"] != json.Number(itoa(n-1)) {
		t.Errorf("not the earliest lines: last %v", got[len(got)-1])
	}
	if !strings.Contains(fileOf(t, res, "session.json"), `"bytes"`) {
		t.Error("session.json does not report the cut")
	}
}

// Review fixes: many channels stay under the limit (session.json's "lines"
// counted); the counters only count written lines; a vector named like a
// leading field is written as its two plain keys.
func TestBuild_ByteLimitManyChannelsAndCounters(t *testing.T) {
	old := maxBytes
	defer func() { maxBytes = old }()
	maxBytes = 12000
	var rows []Row
	for i := 0; i < 400; i++ {
		rows = append(rows, Row{Source: SourceCustomEvent, Time: t0.Add(time.Duration(i) * time.Millisecond), Seq: uint64(i), GameT: fp(1), Frame: ip(1),
			Name: "channel_with_a_long_name_" + itoa(i) + ".x", Count: 1, Props: map[string]string{"t": "1"}})
		rows = append(rows, Row{Source: SourceCustomEvent, Time: t0.Add(time.Duration(i) * time.Millisecond), Seq: uint64(1000 + i), Name: "loot.drop", Count: 1})
	}
	res := Build(Input{Session: Session{SessionID: "s", FirstSeen: t0, LastSeen: t0}, Rows: rows}, t0)
	if res.TotalBytes > maxBytes {
		t.Errorf("%d bytes > %d", res.TotalBytes, maxBytes)
	}
	written := 0
	for f, n := range res.Lines {
		if f != FileAPM {
			written += n
		}
	}
	if res.DroppedProps != written || res.WithoutClock != res.Lines[FileAPM] || written >= 400 {
		t.Errorf("dropped %d, without clock %d, lines %v", res.DroppedProps, res.WithoutClock, res.Lines)
	}

	res = Build(Input{Session: Session{SessionID: "s", FirstSeen: t0, LastSeen: t0}, Rows: []Row{{Source: SourceCustomEvent, Time: t0, GameT: fp(1), Frame: ip(1),
		Name: "loot.drop", Count: 1, Props: map[string]string{"t.x": "1", "t.y": "2"}, PropTypes: map[string]uint8{"t.x": 1, "t.y": 1}}}}, t0)
	if got := fileOf(t, res, "loot.jsonl"); got != `{"t":1.0000,"frame":1,"run":0,"ev":"drop","t.x":1,"t.y":2}`+"\n" || res.DroppedProps != 0 {
		t.Errorf("%s %d", got, res.DroppedProps)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestWriteZip(t *testing.T) {
	res := Build(rowsFromSDKFixture(t), t0)
	var buf bytes.Buffer
	if err := WriteZip(&buf, res); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != len(res.Files) {
		t.Fatalf("%d entries", len(z.File))
	}
	for i, f := range z.File {
		if f.Name != res.Dir+"/"+res.Files[i].Name {
			t.Errorf("entry %s", f.Name)
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(b, res.Files[i].Data) {
			t.Errorf("%s differs", f.Name)
		}
	}
}

// OZX's own analyzer reads the export without modification. Runs when
// OZX_AI_TRACE names scripts/ai_trace.py of an OZX checkout (python3 -I: the
// script's directory is not on the module path); APM10_EXPORT_DIR, if set,
// keeps the exported directory there.
func TestAiTrace_ReadsTheExport(t *testing.T) {
	script := os.Getenv("OZX_AI_TRACE")
	if script == "" {
		t.Skip("OZX_AI_TRACE not set (path to OZX's scripts/ai_trace.py)")
	}
	res := Build(rowsFromSDKFixture(t), t0)
	root := os.Getenv("APM10_EXPORT_DIR")
	if root == "" {
		root = t.TempDir()
	}
	dir := filepath.Join(root, res.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("python3", "-I", script, dir, "--json").Output()
	if err != nil {
		t.Fatalf("ai_trace.py --json: %v", err)
	}
	var report struct {
		Overview struct {
			Meta         map[string]any   `json:"meta"`
			EnemyCount   int              `json:"enemyCount"`
			PlayerDeaths []map[string]any `json:"playerDeaths"`
		} `json:"overview"`
		Anomalies []map[string]any `json:"anomalies"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	deaths := report.Overview.PlayerDeaths
	if len(deaths) != 1 || deaths[0]["t"] != 30.0833 || deaths[0]["killerId"] != "quick_sand_worm" || deaths[0]["killerTypeId"] != "quick_sand_worm" ||
		deaths[0]["roomId"] != "f0_room_1_0" || deaths[0]["liveEnemies"] != float64(7) {
		t.Errorf("deaths %v", deaths)
	}
	if pos, _ := deaths[0]["pos"].(map[string]any); pos["x"] != 1.703 || pos["y"] != 8.09 {
		t.Errorf("pos %v", deaths[0]["pos"])
	}
	if report.Overview.Meta["gitCommit"] != "6116e2e4ff38de10eac61ba3c425e8f50a3aacdb" || len(report.Anomalies) != 0 || report.Overview.EnemyCount != 0 {
		t.Errorf("meta %v anomalies %v enemies %d", report.Overview.Meta, report.Anomalies, report.Overview.EnemyCount)
	}
	text, err := exec.Command("python3", "-I", script, dir).Output()
	if err != nil {
		t.Fatalf("ai_trace.py: %v", err)
	}
	if !strings.Contains(string(text), "PLAYER DEATHS  (1)") || !strings.Contains(string(text), "quick_sand_worm") {
		t.Errorf("report:\n%s", text)
	}
	t.Logf("ai_trace.py %s:\n%s", dir, text)
}

package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// APM-10 end to end, on both insert paths: events serialized by the SDK
// (processor/testdata/sdk_events_apm10.jsonl: a scripted run of OZX's
// AiTelemetry-overlap events with the game clock and typed properties, an
// exception, a jank and a count beyond the budget) through the ingest router
// into a fresh database, then GET /api/sessions/export -> a zip of
// AiTelemetry's session directory: the same lines as the unit test builds
// from the fixture (the clock, the number texts and the property types
// survive ClickHouse), APM's events in apm.jsonl, another session's rows not
// included, rows of older binaries without a clock, hostile text as valid
// JSONL, 404 and 400. With OZX_AI_TRACE set (path to OZX's
// scripts/ai_trace.py), the unzipped export is read by it.

// sdkFixtureEvents returns the SDK's fixture lines with their timestamps moved
// to base (keeping their offsets) and their session id replaced.
func sdkFixtureEvents(t *testing.T, base int64, session string) []string {
	t.Helper()
	b, err := os.ReadFile("../../internal/processor/testdata/sdk_events_apm10.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ts := regexp.MustCompile(`"timestamp":(\d+)`)
	const fixtureBase = 1761000100000
	var out []string
	for i, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		l = ts.ReplaceAllStringFunc(l, func(m string) string {
			v, _ := strconv.ParseInt(m[len(`"timestamp":`):], 10, 64)
			return `"timestamp":` + strconv.FormatInt(base+v-fixtureBase, 10)
		})
		l = strings.ReplaceAll(l, "1761000000000_a10a10a1", session)
		l = strings.Replace(l, `"event_id":"`, `"event_id":"`+session+"-"+strconv.Itoa(i)+"-", 1)
		out = append(out, l)
	}
	return out
}

func exportZip(t *testing.T, admin http.Handler, params url.Values) (int, http.Header, map[string]string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/export?"+params.Encode(), nil)
	w := httptest.NewRecorder()
	admin.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return w.Code, w.Header(), nil, ""
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	dir := ""
	for _, f := range z.File {
		i := strings.IndexByte(f.Name, '/')
		if i <= 0 {
			t.Fatalf("entry %s", f.Name)
		}
		dir = f.Name[:i]
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name[i+1:]] = string(b)
	}
	return w.Code, w.Header(), files, dir
}

func jsonl(t *testing.T, name, data string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for i, l := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		var m map[string]interface{}
		dec := json.NewDecoder(strings.NewReader(l))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil || dec.More() {
			t.Fatalf("%s line %d is not one JSON object: %v\n%s", name, i+1, err, l)
		}
		out = append(out, m)
	}
	return out
}

func TestAPM10_SessionExport(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM9(t, async)
			session := fmt.Sprintf("x10-%v-%d", async, time.Now().UnixNano()%1e6)
			base := e.base
			events := sdkFixtureEvents(t, base, session)
			// APM-only rows of the session: a perf sample and a scene load with
			// the clock, a run (APM-9) and a crash without, and another session.
			events = append(events,
				e.ev("perf_sample", session, "2.4.0", 5000, map[string]interface{}{"fps": 58.5, "frame_time_ms": 17.1, "interval_ms": 1000, "t": 5.0, "frame": 300,
					"ctx": map[string]string{"runId": "run-2001"}}),
				e.ev("scene_load", session, "2.4.0", 200, map[string]interface{}{"scene_name": "Gameplay", "load_ms": 812.5, "activate_ms": 40, "t": 0.2, "frame": 12}),
				e.ev("run", session, "2.4.0", 58000, map[string]interface{}{"run_id": "run-2001", "end_reason": "end", "result": "defeat", "duration_ms": 57400,
					"samples": 57, "peak_rss_mb": 1250.5, "t": 57.9, "frame": 3427}),
				e.ev("crash", session, "2.4.0", 59000, map[string]interface{}{"crash_type": "anr", "stack": "main\n  at android.os.MessageQueue.nativePollOnce"}),
				e.ev("custom_event", "other-"+session, "2.4.0", 1000, map[string]interface{}{"name": "playermovement.room_enter", "count": 1, "t": 1.0, "frame": 60,
					"props": map[string]interface{}{"roomId": "not-this-session"}}),
			)
			e.ingest(t, events)

			// The clock and the types are stored as the SDK sent them.
			var gt *float64
			var frame *int64
			var types map[string]uint8
			must(t, e.client.Conn().QueryRow(context.Background(),
				"SELECT game_t, frame, prop_types FROM apm_custom_events WHERE session_id = ? AND name = 'playermovement.damaged' AND aggregated = 0", session).
				Scan(&gt, &frame, &types))
			if gt == nil || *gt != 6.66 || frame == nil || *frame != 378 || types["isCrit"] != 2 || types["pos.x"] != 1 || len(types) != 11 {
				t.Errorf("stored %v %v %v", gt, frame, types)
			}
			var nulls uint64
			must(t, e.client.Conn().QueryRow(context.Background(),
				"SELECT countIf(game_t IS NULL) FROM apm_custom_events WHERE session_id = ?", session).Scan(&nulls))
			if nulls != 1 {
				t.Errorf("%d custom events without the clock, want 1 (the aggregated count)", nulls)
			}

			p := url.Values{"session_id": {session}}
			for k, v := range e.params {
				p[k] = v
			}
			code, hdr, files, dir := exportZip(t, e.admin, p)
			if code != 200 || hdr.Get("Content-Type") != "application/zip" || !strings.HasSuffix(dir, "_"+strings.ReplaceAll(session, "x10-", "x10-")) {
				t.Fatalf("export: %d %v %s", code, hdr, dir)
			}
			var names []string
			for n := range files {
				names = append(names, n)
			}
			if len(files) != 7 {
				t.Errorf("files %v", names)
			}
			wantPM := []string{
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
			if got := strings.TrimSuffix(files["playermovement.jsonl"], "\n"); got != strings.Join(wantPM, "\n") {
				t.Errorf("playermovement.jsonl:\n%s\nwant:\n%s", got, strings.Join(wantPM, "\n"))
			}
			if !strings.HasPrefix(files["runstart.jsonl"], `{"t":0.5000,"frame":31,"run":1,"ev":"run"}`+"\n") || strings.Count(files["runstart.jsonl"], "\n") != 4 ||
				strings.Count(files["encounter.jsonl"], "\n") != 3 || strings.Count(files["loot.jsonl"], "\n") != 1 ||
				files["custom.jsonl"] != `{"t":20.5000,"frame":1210,"run":1,"ev":"boss_spawn","bossId":"worm_king"}`+"\n" {
				t.Errorf("runstart %s\nencounter %s\nloot %s\ncustom %s", files["runstart.jsonl"], files["encounter.jsonl"], files["loot.jsonl"], files["custom.jsonl"])
			}
			apm := jsonl(t, "apm.jsonl", files["apm.jsonl"])
			var evs []string
			for _, l := range apm {
				evs = append(evs, l["ev"].(string))
			}
			if strings.Join(evs, ",") != "apm_scene_load,apm_perf_sample,apm_exception,apm_jank,apm_run,apm_crash,apm_custom_event" {
				t.Errorf("apm.jsonl events %v", evs)
			}
			for _, l := range apm {
				switch l["ev"] {
				case "apm_crash", "apm_custom_event":
					if _, ok := l["t"]; ok {
						t.Errorf("%v has a clock", l)
					}
				case "apm_run":
					if l["runId"] != "run-2001" || l["run"] != json.Number("1") || l["t"] != json.Number("57.9000") || l["peakRssMb"] != json.Number("1250.5") {
						t.Errorf("run %v", l)
					}
				case "apm_exception":
					if l["t"] != json.Number("21.0000") || !strings.Contains(l["message"].(string), "NullReferenceException") {
						t.Errorf("exception %v", l)
					}
				}
			}
			if strings.Contains(strings.Join(func() []string {
				var all []string
				for _, v := range files {
					all = append(all, v)
				}
				return all
			}(), ""), "not-this-session") {
				t.Error("another session's row in the export")
			}
			var sess map[string]interface{}
			if err := json.Unmarshal([]byte(files["session.json"]), &sess); err != nil {
				t.Fatal(err)
			}
			a := sess["apm"].(map[string]interface{})
			if sess["gitCommit"] != "6116e2e4ff38de10eac61ba3c425e8f50a3aacdb" || sess["scene"] != "Gameplay" || a["sessionId"] != session ||
				a["linesWithoutGameClock"] != float64(2) || a["appVersion"] != "2.4.0" || a["buildType"] != "release" {
				t.Errorf("session.json %s", files["session.json"])
			}

			if script := os.Getenv("OZX_AI_TRACE"); script != "" {
				root := filepath.Join(t.TempDir(), dir)
				must(t, os.MkdirAll(root, 0o755))
				for n, data := range files {
					must(t, os.WriteFile(filepath.Join(root, n), []byte(data), 0o644))
				}
				out, err := exec.Command("python3", "-I", script, root, "--json").Output()
				if err != nil {
					t.Fatalf("ai_trace.py: %v", err)
				}
				var report struct {
					Overview struct {
						PlayerDeaths []map[string]interface{} `json:"playerDeaths"`
					} `json:"overview"`
					Anomalies []interface{} `json:"anomalies"`
				}
				must(t, json.Unmarshal(out, &report))
				d := report.Overview.PlayerDeaths
				if len(d) != 1 || d[0]["killerId"] != "quick_sand_worm" || d[0]["roomId"] != "f0_room_1_0" || d[0]["t"] != 30.0833 || len(report.Anomalies) != 0 {
					t.Errorf("ai_trace.py: %s", out)
				}
			}

			// 404 for an unknown session; 400 for a bad range.
			if code, _, _, _ := exportZip(t, e.admin, url.Values{"session_id": {"no-such-session"}}); code != http.StatusNotFound {
				t.Errorf("unknown session: %d", code)
			}
			if code, _, _, _ := exportZip(t, e.admin, url.Values{"session_id": {session}, "start_time": {"2026-01-01T00:00:00Z"}, "end_time": {"2026-03-01T00:00:00Z"}}); code != http.StatusBadRequest {
				t.Errorf("long range: %d", code)
			}
		})
	}
}

// Game text with newlines, quotes, control characters and invalid UTF-8,
// through ClickHouse: every line of the export is one JSON object.
func TestAPM10_ExportHostileText(t *testing.T) {
	e := setupAPM9(t, false)
	session := fmt.Sprintf("x10h-%d", time.Now().UnixNano()%1e6)
	hostile := []string{"a\nb", "q\"\\", "\x01\x1f", " ", "🙂中文", `"},{"ev":"x`, "<script>"}
	var events []string
	for i, s := range hostile {
		events = append(events,
			e.ev("custom_event", session, "2.4.0", int64(i)*100, map[string]interface{}{"name": "playermovement.died", "count": 1, "t": float64(i), "frame": i,
				"props": map[string]interface{}{"killerId": s, "n": i}, "ctx": map[string]string{"runId": s}}),
			e.ev("exception", session, "2.4.0", int64(i)*100+50, map[string]interface{}{"message": s, "stack": s, "count": 1, "fingerprint": "f"}))
	}
	e.ingest(t, events)
	p := url.Values{"session_id": {session}}
	for k, v := range e.params {
		p[k] = v
	}
	code, _, files, _ := exportZip(t, e.admin, p)
	if code != 200 {
		t.Fatalf("export %d", code)
	}
	pm := jsonl(t, "playermovement.jsonl", files["playermovement.jsonl"])
	apm := jsonl(t, "apm.jsonl", files["apm.jsonl"])
	if len(pm) != len(hostile) || len(apm) != len(hostile) {
		t.Fatalf("%d / %d lines", len(pm), len(apm))
	}
	for i, s := range hostile {
		if pm[i]["killerId"] != s || pm[i]["n"] != json.Number(strconv.Itoa(i)) || pm[i]["run"] != json.Number(strconv.Itoa(i+1)) {
			t.Errorf("line %d: %v", i, pm[i])
		}
	}
	var sess map[string]interface{}
	if err := json.Unmarshal([]byte(files["session.json"]), &sess); err != nil {
		t.Fatalf("session.json: %v", err)
	}
}

// Rows written by the binary before APM-10 (no game clock columns in its
// INSERT) export: everything goes to apm.jsonl, without t.
func TestAPM10_ExportLegacyRows(t *testing.T) {
	e := setupAPM4(t, false)
	session := seedLegacyRows(t, e.client)
	ts := time.Now().Add(-2 * time.Hour)
	code, _, files, _ := exportZip(t, e.admin, url.Values{"session_id": {session}, "at": {ts.UTC().Format(time.RFC3339Nano)}})
	if code != 200 {
		t.Fatalf("export %d", code)
	}
	if len(files) != 2 || files["apm.jsonl"] == "" {
		t.Fatalf("files %v", files)
	}
	for _, l := range jsonl(t, "apm.jsonl", files["apm.jsonl"]) {
		if _, ok := l["t"]; ok || l["run"] != json.Number("0") {
			t.Errorf("legacy line %v", l)
		}
	}
}

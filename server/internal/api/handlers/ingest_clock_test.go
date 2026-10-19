package handlers

import (
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// The SDK's own events (testdata/sdk_events_apm10.jsonl, serialized on
// Unity's Mono by the APM-10 SDK: ApmClient.Event's builder, TrackEvent, an
// exception and a jank) are stored with the game clock they carry and the
// types of their properties; the count of calls beyond the budget has no
// clock.
func TestIngest_APM10EventsFromTheSDK(t *testing.T) {
	b, err := os.ReadFile("../../processor/testdata/sdk_events_apm10.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ts := regexp.MustCompile(`"timestamp":\d+`)
	now := `"timestamp":` + strconv.FormatInt(time.Now().UnixMilli(), 10)
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		lines = append(lines, ts.ReplaceAllString(l, now))
	}
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	w, resp := postBatch(t, h, batchOf(lines...), map[string]string{SDKVersionHeader: "2.0.0"})
	if w.Code != http.StatusOK || resp.Accepted != len(lines) || resp.Rejected != 0 || len(resp.Errors) != 0 {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(store.customs) != 16 || len(store.exceptions) != 1 || len(store.janks) != 1 {
		t.Fatalf("%d custom events, %d exceptions, %d janks", len(store.customs), len(store.exceptions), len(store.janks))
	}
	byName := map[string]models.CustomEvent{}
	for _, e := range store.customs {
		if !bool(e.Aggregated) {
			byName[e.Name] = e
		}
	}
	d := byName["playermovement.damaged"]
	if d.GameT == nil || *d.GameT != 6.66 || d.Frame == nil || *d.Frame != 378 {
		t.Fatalf("damaged clock %v %v", d.GameT, d.Frame)
	}
	if d.Props["amount"] != "34.322" || d.Props["pos.x"] != "4.990" || d.Props["isCrit"] != "false" || d.Props["srcKind"] != "EnemyProjectile" {
		t.Errorf("damaged props %v", d.Props)
	}
	wantTypes := map[string]uint8{"amount": 1, "rawAmount": 1, "remainingHp": 1, "srcInstanceId": 1, "isCrit": 2,
		"hitDirection.x": 1, "hitDirection.y": 1, "hitPosition.x": 1, "hitPosition.y": 1, "pos.x": 1, "pos.y": 1}
	if len(d.PropTypes) != len(wantTypes) {
		t.Errorf("damaged types %v", d.PropTypes)
	}
	for k, v := range wantTypes {
		if d.PropTypes[k] != v {
			t.Errorf("type of %s: %d, want %d", k, d.PropTypes[k], v)
		}
	}
	if run := byName["runstart.run"]; run.GameT == nil || *run.GameT != 0.5 || *run.Frame != 31 || run.Context["runId"] != "run-2001" || len(run.Props) != 0 {
		t.Errorf("run %+v", run)
	}
	if boss := byName["boss_spawn"]; boss.Frame == nil || *boss.Frame != 1210 || boss.PropTypes != nil || boss.Props["bossId"] != "worm_king" {
		t.Errorf("legacy TrackEvent %+v", boss)
	}
	var agg *models.CustomEvent
	for i := range store.customs {
		if bool(store.customs[i].Aggregated) {
			agg = &store.customs[i]
		}
	}
	if agg == nil || agg.Count != 5 || agg.GameT != nil || agg.Frame != nil {
		t.Errorf("aggregated %+v", agg)
	}
	if e := store.exceptions[0]; e.GameT == nil || *e.GameT != 21 || *e.Frame != 1240 {
		t.Errorf("exception clock %v %v", e.GameT, e.Frame)
	}
	if j := store.janks[0]; j.GameT == nil || *j.GameT != 22.4 || *j.Frame != 1301 {
		t.Errorf("jank clock %v %v", j.GameT, j.Frame)
	}
}

// APM-10: a clock of the wrong shape never fails the event; it is not stored.
func TestIngest_GameClockValidation(t *testing.T) {
	base := `"type":"jank","timestamp":` + strconv.FormatInt(time.Now().UnixMilli(), 10) +
		`,"app_version":"1.0","platform":"Android","device_id":"d","session_id":"s","duration_ms":60,"max_frame_ms":60`
	cases := []struct {
		clock string
		ok    bool
		t     float64
		frame int64
	}{
		{`"t":12.5,"frame":700`, true, 12.5, 700},
		{`"t":0,"frame":0`, true, 0, 0},
		{``, false, 0, 0},
		{`"t":12.5`, false, 0, 0},
		{`"frame":700`, false, 0, 0},
		{`"t":"12.5","frame":700`, false, 0, 0},
		{`"t":12.5,"frame":"700"`, false, 0, 0},
		{`"t":-1,"frame":700`, false, 0, 0},
		{`"t":1e9,"frame":700`, false, 0, 0},
		{`"t":12.5,"frame":7.5`, false, 0, 0},
		{`"t":12.5,"frame":-1`, false, 0, 0},
		{`"t":12.5,"frame":4294967296`, false, 0, 0},
		{`"t":null,"frame":null`, false, 0, 0},
		{`"t":{},"frame":[]`, false, 0, 0},
	}
	for _, c := range cases {
		ev := "{" + base
		if c.clock != "" {
			ev += "," + c.clock
		}
		ev += "}"
		store := &fakeEventStore{}
		h := newIngestHandler(store, zap.NewNop())
		w, resp := postBatch(t, h, batchOf(ev), nil)
		if w.Code != http.StatusOK || resp.Accepted != 1 || len(store.janks) != 1 {
			t.Fatalf("%s: code %d resp %+v", c.clock, w.Code, resp)
		}
		j := store.janks[0]
		if !c.ok {
			if j.GameT != nil || j.Frame != nil {
				t.Errorf("%s: stored %v %v", c.clock, *j.GameT, *j.Frame)
			}
			continue
		}
		if j.GameT == nil || j.Frame == nil || *j.GameT != c.t || *j.Frame != c.frame {
			t.Errorf("%s: %v %v", c.clock, j.GameT, j.Frame)
		}
	}
}

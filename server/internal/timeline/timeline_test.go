package timeline

import (
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestParseBreadcrumb_KindsAndTime(t *testing.T) {
	carrier := utc("2026-10-18T10:00:05.000Z")
	cases := []struct {
		line, kind, text, time string
	}{
		{"[10:00:01.512] Event: room_enter roomId=12", models.CrumbEvent, "room_enter roomId=12", "2026-10-18T10:00:01.512Z"},
		{"[10:00:00.000] Scene: Main", models.CrumbScene, "Main", "2026-10-18T10:00:00Z"},
		{"[09:59:59.999] Warning: Asset not found: anim/x", models.CrumbWarning, "Asset not found: anim/x", "2026-10-18T09:59:59.999Z"},
		{"[10:00:04.000] Error: Inventory sync failed", models.CrumbError, "Inventory sync failed", "2026-10-18T10:00:04Z"},
		{"[10:00:04.000] Assert: x != null", models.CrumbAssert, "x != null", "2026-10-18T10:00:04Z"},
		{"[10:00:04.900] Exception: NullReferenceException: Object reference", models.CrumbException, "NullReferenceException: Object reference", "2026-10-18T10:00:04.9Z"},
		{"[10:00:04.900] boss phase 2", models.CrumbText, "boss phase 2", "2026-10-18T10:00:04.9Z"},
		// Up to 2 s after the carrier: the same day (a count's first-seen time).
		{"[10:00:06.500] Event: late", models.CrumbEvent, "late", "2026-10-18T10:00:06.5Z"},
		// Later in the day than the carrier: the previous day.
		{"[23:59:58.000] Event: before midnight", models.CrumbEvent, "before midnight", "2026-10-17T23:59:58Z"},
	}
	for _, c := range cases {
		it := ParseBreadcrumb(c.line, carrier, models.TimelineException)
		if it.Type != models.TimelineBreadcrumb || it.Kind != c.kind || it.Text != c.text || !it.Time.Equal(utc(c.time)) || it.TimeUnknown || it.Source != "exception" {
			t.Errorf("%q: %+v", c.line, it)
		}
	}
}

func TestParseBreadcrumb_AfterMidnight(t *testing.T) {
	carrier := utc("2026-10-18T00:00:01.000Z")
	it := ParseBreadcrumb("[23:59:59.500] Warning: w", carrier, "jank")
	if !it.Time.Equal(utc("2026-10-17T23:59:59.5Z")) {
		t.Errorf("%v", it.Time)
	}
}

func TestParseBreadcrumb_NoTimeOrMalformed(t *testing.T) {
	carrier := utc("2026-10-18T10:00:05Z")
	for _, line := range []string{"x", "", "[10:00] Event: a", "[25:00:00.000] Event: a", "[10:00:00.00x] Event: a"} {
		it := ParseBreadcrumb(line, carrier, "crash")
		if !it.TimeUnknown || !it.Time.Equal(carrier) {
			t.Errorf("%q: %+v", line, it)
		}
	}
	// Untrusted text is kept as it is (the UI renders it as text).
	it := ParseBreadcrumb("[10:00:00.000] Event: <script>alert(1)</script>", carrier, "exception")
	if it.Text != "<script>alert(1)</script>" || it.Kind != models.CrumbEvent {
		t.Errorf("%+v", it)
	}
}

func TestParseBreadcrumb_RepeatedLogLine(t *testing.T) {
	carrier := utc("2026-10-18T10:00:30Z")
	it := ParseBreadcrumb("[10:00:01.000] Warning: Pool not found for enemy 12 (x300 until 10:00:20.120)", carrier, "exception")
	if it.Text != "Pool not found for enemy 12" || it.Repeats != 300 || it.RepeatUntil == nil || !it.RepeatUntil.Equal(utc("2026-10-18T10:00:20.12Z")) {
		t.Errorf("%+v", it)
	}
	// Across midnight.
	it = ParseBreadcrumb("[23:59:59.000] Warning: w (x2 until 00:00:01.000)", utc("2026-10-18T00:00:02Z"), "exception")
	if !it.Time.Equal(utc("2026-10-17T23:59:59Z")) || !it.RepeatUntil.Equal(utc("2026-10-18T00:00:01Z")) {
		t.Errorf("%+v %v", it, it.RepeatUntil)
	}
	// Not a count: an event's text, or a malformed suffix.
	for _, line := range []string{
		"[10:00:01.000] Event: e (x3 until 10:00:02.000)",
		"[10:00:01.000] Warning: w (x1 until 10:00:02.000)",
		"[10:00:01.000] Warning: w (xx until 10:00:02.000)",
		"[10:00:01.000] Warning: w (x3 until 10:00:02)",
		"[10:00:01.000] Warning: (x3 until)",
	} {
		if it := ParseBreadcrumb(line, carrier, "exception"); it.Repeats != 0 || it.RepeatUntil != nil {
			t.Errorf("%q: %+v", line, it)
		}
	}
}

func crumb(ts, kind, text string, repeats int64) models.TimelineItem {
	return models.TimelineItem{Type: models.TimelineBreadcrumb, Time: utc(ts), Kind: kind, Text: text, Repeats: repeats}
}

func TestDedupeBreadcrumbs(t *testing.T) {
	long := strings.Repeat("a", 300)
	items := []models.TimelineItem{
		crumb("2026-10-18T10:00:01Z", models.CrumbWarning, "w", 3),
		crumb("2026-10-18T10:00:01Z", models.CrumbWarning, "w", 30), // the same line, counted further
		crumb("2026-10-18T10:00:01Z", models.CrumbError, "w", 0),    // another kind
		crumb("2026-10-18T10:00:02Z", models.CrumbWarning, "w", 0),  // another time
		crumb("2026-10-18T10:00:03Z", models.CrumbEvent, long[:185], 0),
		crumb("2026-10-18T10:00:03Z", models.CrumbEvent, long, 0), // cut differently by two carriers
		{Type: models.TimelineBreadcrumb, Time: utc("2026-10-18T10:00:04Z"), Kind: models.CrumbText, Text: "x", TimeUnknown: true},
		{Type: models.TimelineBreadcrumb, Time: utc("2026-10-18T10:00:04Z"), Kind: models.CrumbText, Text: "x", TimeUnknown: true}, // an old SDK repeating its lines: once
		{Type: models.TimelineBreadcrumb, Time: utc("2026-10-18T10:00:04Z"), Kind: models.CrumbText, Text: "x", TimeUnknown: true, Source: "crash"},
	}
	out, dropped := DedupeBreadcrumbs(items)
	if dropped != 3 || len(out) != 6 {
		t.Fatalf("dropped %d, kept %d: %+v", dropped, len(out), out)
	}
	if out[0].Repeats != 30 || out[3].Text != long {
		t.Errorf("kept %+v / %d chars", out[0], len(out[3].Text))
	}
}

func TestDropCovered(t *testing.T) {
	rows := []models.TimelineItem{
		{Type: models.TimelineCustomEvent, Time: utc("2026-10-18T10:00:01Z"), Name: "room_enter"},
		{Type: models.TimelineCustomEvent, Time: utc("2026-10-18T10:00:02Z"), Name: "enemy_killed", Aggregated: true, Count: 9},
		{Type: models.TimelineSceneLoad, Time: utc("2026-10-18T10:00:03Z"), SceneName: "Main"},
		{Type: models.TimelineException, Time: utc("2026-10-18T10:00:05Z"), Message: "NullReferenceException: Object reference not set\n  at X"},
		{Type: models.TimelineException, Time: utc("2026-10-18T10:00:06Z"), LogType: "error", Message: "Inventory sync failed after spam"},
	}
	crumbs := []models.TimelineItem{
		crumb("2026-10-18T10:00:01Z", models.CrumbEvent, "room_enter roomId=12", 0),                                 // covered
		crumb("2026-10-18T10:00:03Z", models.CrumbEvent, "room_enter roomId=13", 0),                                 // 2 s later: another call
		crumb("2026-10-18T10:00:02.5Z", models.CrumbEvent, "enemy_killed", 0),                                       // the row is a count: kept
		crumb("2026-10-18T10:00:00Z", models.CrumbScene, "Main", 0),                                                 // within 5 s: covered
		crumb("2026-10-18T10:00:05Z", models.CrumbException, "NullReferenceException: Object reference not set", 0), // covered
		crumb("2026-10-18T10:00:05Z", models.CrumbException, "InvalidOperationException: x", 0),                     // another exception
		crumb("2026-10-18T10:00:05Z", models.CrumbWarning, "room_enter", 0),                                         // not an event
		crumb("2026-10-18T10:00:06Z", models.CrumbError, "Inventory sync failed", 0),                                // the LogError row: covered
		crumb("2026-10-18T10:00:06Z", models.CrumbAssert, "Inventory sync failed", 0),                               // an assert line: the row is an error
	}
	out, dropped := DropCovered(crumbs, rows)
	if dropped != 4 || len(out) != 5 {
		t.Fatalf("dropped %d: %+v", dropped, out)
	}
	for _, c := range out {
		if c.Text == "room_enter roomId=12" || c.Kind == models.CrumbScene {
			t.Errorf("not dropped: %+v", c)
		}
	}
}

func TestDiff(t *testing.T) {
	got := Diff(map[string]string{"levelId": "3", "roomId": "r-1", "mode": "hard"}, map[string]string{"levelId": "3", "roomId": "r-2", "seed": "9"})
	want := []models.ContextChange{{Key: "mode", From: "hard", To: ""}, {Key: "roomId", From: "r-1", To: "r-2"}, {Key: "seed", From: "", To: "9"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v", i, got[i])
		}
	}
	if len(Diff(nil, nil)) != 0 || len(Diff(map[string]string{"a": "1"}, map[string]string{"a": "1"})) != 0 {
		t.Error("no change expected")
	}
}

func TestSort_SameMillisecond(t *testing.T) {
	ts := utc("2026-10-18T10:00:00Z")
	items := []models.TimelineItem{
		{Type: models.TimelineException, Time: ts, Seq: 8},
		{Type: models.TimelineCustomEvent, Time: ts, Seq: 7},
		{Type: models.TimelineBreadcrumb, Time: ts},
		{Type: models.TimelineContext, Time: ts},
		{Type: models.TimelineJank, Time: ts.Add(-time.Millisecond), Seq: 99},
	}
	Sort(items)
	var got []string
	for _, it := range items {
		got = append(got, it.Type)
	}
	if strings.Join(got, ",") != "jank,context,breadcrumb,custom_event,exception" {
		t.Errorf("%v", got)
	}
}

func TestNearest(t *testing.T) {
	var items []models.TimelineItem
	base := utc("2026-10-18T10:00:00Z")
	for i := 0; i < 10; i++ {
		items = append(items, models.TimelineItem{Type: models.TimelineBreadcrumb, Time: base.Add(time.Duration(i) * time.Second)})
	}
	at := base.Add(7 * time.Second)
	out, cut := Nearest(items, 3, &at)
	if !cut || len(out) != 3 || !out[0].Time.Equal(base.Add(6*time.Second)) || !out[2].Time.Equal(base.Add(8*time.Second)) {
		t.Errorf("%v %+v", cut, out)
	}
	out, cut = Nearest(items, 3, nil)
	if !cut || !out[0].Time.Equal(base) || !out[2].Time.Equal(base.Add(2*time.Second)) {
		t.Errorf("%+v", out)
	}
	if out, cut := Nearest(items, 10, nil); cut || len(out) != 10 {
		t.Error("no cut expected")
	}
}

// The half-built room on a device: room_enter (a row) and the warning logged
// right after it share a millisecond. The exception's lines are in recording
// order, so the warning comes after room_enter, not before every row of that
// millisecond; a line before any covered one keeps its place before the rows.
func TestSort_LineAfterCoveredRowInSameMillisecond(t *testing.T) {
	ts := utc("2026-10-17T20:18:24.913Z")
	rows := []models.TimelineItem{
		{Type: models.TimelineCustomEvent, Time: ts, Seq: 40, Name: "room_enter"},
		{Type: models.TimelineCustomEvent, Time: ts, Seq: 42, Name: "room_built"},
		{Type: models.TimelineException, Time: ts.Add(12 * time.Millisecond), Seq: 43, Message: "InvalidOperationException: x"},
	}
	crumbs := []models.TimelineItem{
		crumb("2026-10-17T20:18:24.913Z", models.CrumbWarning, "before anything", 0),
		crumb("2026-10-17T20:18:24.913Z", models.CrumbEvent, "room_enter roomId=r-12", 0),
		crumb("2026-10-17T20:18:24.913Z", models.CrumbWarning, "Room layout missing for r-12", 0),
		crumb("2026-10-17T20:18:24.920Z", models.CrumbWarning, "later", 0),
	}
	kept, dropped := DropCovered(crumbs, rows)
	if dropped != 1 {
		t.Fatalf("dropped %d", dropped)
	}
	items := append(append([]models.TimelineItem{{Type: models.TimelineContext, Time: ts}}, rows...), kept...)
	Sort(items)
	var got []string
	for _, it := range items {
		got = append(got, it.Type+":"+it.Name+it.Text)
	}
	want := "context:,breadcrumb:before anything,custom_event:room_enter,breadcrumb:Room layout missing for r-12,custom_event:room_built,breadcrumb:later,exception:"
	if strings.Join(got, ",") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, ","), want)
	}
}

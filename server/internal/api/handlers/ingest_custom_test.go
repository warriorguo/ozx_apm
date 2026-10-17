package handlers

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/metrics"
)

// sdkFixtureAPM6 returns the events of testdata/sdk_events_apm6.jsonl (as the
// APM-6 SDK serializes them on Unity's Mono) with their timestamps moved to now.
func sdkFixtureAPM6(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../processor/testdata/sdk_events_apm6.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ts := regexp.MustCompile(`"timestamp":\d+`)
	now := `"timestamp":` + strconv.FormatInt(time.Now().UnixMilli(), 10)
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		lines = append(lines, ts.ReplaceAllString(l, now))
	}
	return lines
}

// The SDK's events are stored as sent: properties, counts, context, and the
// metric rows with their histograms (whose counts add up).
func TestIngest_CustomEventsAndMetricsFromTheSDK(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	lines := sdkFixtureAPM6(t)
	w, resp := postBatch(t, h, batchOf(lines...), map[string]string{SDKVersionHeader: "2.0.0"})
	if w.Code != http.StatusOK || !resp.Stored || resp.Accepted != len(lines) || resp.Rejected != 0 {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(store.customs) != 3 || len(store.metricRows) != len(lines)-3 {
		t.Fatalf("%d events, %d metric rows", len(store.customs), len(store.metricRows))
	}
	enter := store.customs[0]
	if enter.Name != "room_enter" || enter.Count != 1 || bool(enter.Aggregated) || enter.Props["seed"] != "991" ||
		enter.Props["mode"] != `hard "x"` || len(enter.Props) != 2 || enter.Context["roomId"] != "r-12" || enter.BuildType != "release" {
		t.Errorf("room_enter: %+v", enter)
	}
	killed := store.customs[2]
	if killed.Name != "enemy_killed" || killed.Count != 5 || !bool(killed.Aggregated) || len(killed.Props) != 0 {
		t.Errorf("aggregated: %+v", killed)
	}
	var sw, merged int
	for _, m := range store.metricRows {
		var total uint64
		for _, c := range m.HistCounts {
			total += c
		}
		if total != m.Count || m.HistVersion != metrics.Version {
			t.Errorf("%s: counts %d vs %d", m.Name, total, m.Count)
		}
		if m.Name == "room_switch_ms" {
			sw++
			if m.Count != 10 || *m.Sum != 10160.875 || *m.Min != 640.25 || *m.Max != 2210.125 || m.Unit != "ms" {
				t.Errorf("room_switch_ms: %+v", m)
			}
		}
		if bool(m.CtxMerged) {
			merged++
			if _, ok := m.Context["roomId"]; ok || m.Context["levelId"] != "3" || m.Count != 2 {
				t.Errorf("merged: %+v", m)
			}
		}
	}
	if sw != 1 || merged != 1 {
		t.Errorf("room_switch rows %d, merged rows %d", sw, merged)
	}
}

func TestIngest_CustomEventValidation(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(
		sdkEventJSON("custom_event", "a", 1, `,"name":"room enter","count":1`),
		sdkEventJSON("custom_event", "b", 2, `,"name":"ok","props":{"bad key":"x","n":3,"obj":{"a":1},"long":"`+strings.Repeat("v", 300)+`"}`),
		sdkEventJSON("custom_event", "c", 3, `,"name":"`+strings.Repeat("n", 65)+`"`),
		sdkEventJSON("custom_event", "d", 4, `,"name":"ok","count":"x"`),
	), nil)
	if resp.Accepted != 2 || resp.Rejected != 2 || !resp.Stored {
		t.Fatalf("resp %+v", resp)
	}
	ok := store.customs[0]
	if ok.Count != 1 || ok.Props["n"] != "3" || len(ok.Props["long"]) != 256 || len(ok.Props) != 2 {
		t.Errorf("props: %+v", ok.Props)
	}
	if store.customs[1].Count != 1 {
		t.Errorf("a count of the wrong type reads as 1: %+v", store.customs[1])
	}
	joined := strings.Join(resp.Errors, "\n")
	for _, want := range []string{"event 0: invalid name", "event 2: invalid name", "props: 2 entries dropped, 1 values truncated"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors lack %q: %v", want, resp.Errors)
		}
	}
}

func metricJSON(id string, extra string) string {
	return sdkEventJSON("custom_metric", id, 1, `,"name":"load_ms","unit":"ms"`+extra)
}

func TestIngest_CustomMetricValidation(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	k := func(v float64) string { return strconv.Itoa(metrics.Key(v)) }
	cases := []struct {
		extra, reason string
	}{
		{`,"count":2,"sum":3,"min":1,"max":2,"hist_v":1,"hist_keys":[` + k(1) + `,` + k(2) + `],"hist_counts":[1,1]`, ""},
		{`,"count":3,"sum":3,"min":1,"max":2,"hist_v":1,"hist_keys":[` + k(1) + `,` + k(2) + `],"hist_counts":[1,1]`, "do not add up"},
		{`,"count":2,"sum":null,"min":1,"max":2,"hist_v":1,"hist_keys":[` + k(1) + `],"hist_counts":[2]`, "finite"},
		{`,"count":2,"sum":3,"min":1,"max":2,"hist_v":2,"hist_keys":[` + k(1) + `],"hist_counts":[2]`, "unsupported hist_v 2"},
		{`,"count":2,"sum":3,"min":1,"max":2,"hist_v":1,"hist_keys":[5],"hist_counts":[2]`, "invalid histogram key 5"},
		{`,"count":2,"sum":3,"min":3,"max":2,"hist_v":1,"hist_keys":[` + k(1) + `],"hist_counts":[2]`, "min is greater"},
		{`,"count":0,"sum":0,"min":0,"max":0,"hist_v":1,"hist_keys":[],"hist_counts":[]`, "count must be positive"},
		{`,"count":2,"sum":3,"min":1,"max":2,"hist_v":1,"hist_keys":[` + k(1) + `],"hist_counts":[2,0]`, "same length"},
		{`,"count":"2","sum":3,"min":1,"max":2,"hist_v":1,"hist_keys":[` + k(1) + `],"hist_counts":[2]`, "invalid fields"},
	}
	var events []string
	for i, c := range cases {
		events = append(events, metricJSON(fmt.Sprintf("m%d", i), c.extra))
	}
	_, resp := postBatch(t, h, batchOf(events...), nil)
	if resp.Accepted != 1 || resp.Rejected != len(cases)-1 {
		t.Fatalf("resp %+v", resp)
	}
	joined := strings.Join(resp.Errors, "\n")
	for i, c := range cases {
		if c.reason != "" && !strings.Contains(joined, fmt.Sprintf("event %d: ", i)) || c.reason != "" && !strings.Contains(joined, c.reason) {
			t.Errorf("case %d: errors lack %q: %v", i, c.reason, resp.Errors)
		}
	}
	if m := store.metricRows[0]; m.Unit != "ms" || m.Count != 2 {
		t.Errorf("stored %+v", m)
	}

	// An unprintable unit is dropped, the row kept.
	_, resp = postBatch(t, h, batchOf(sdkEventJSON("custom_metric", "u", 1,
		`,"name":"x","unit":"m\u0001s","count":1,"sum":1,"min":1,"max":1,"hist_v":1,"hist_keys":[`+k(1)+`],"hist_counts":[1]`)), nil)
	if resp.Accepted != 1 || store.metricRows[1].Unit != "" {
		t.Errorf("unit: %+v %+v", resp, store.metricRows[1])
	}
}

// Unknown types and the planned ones (asset_load, http) are rejected with
// their reason and counted per type; nothing is silently dropped.
func TestIngest_UnsupportedTypesAreRejectedWithAReason(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(
		sdkEventJSON("asset_load", "a", 1, `,"key":"k"`),
		sdkEventJSON("http", "b", 2, `,"api_name":"x"`),
		sdkEventJSON("http", "c", 3, ``),
		sdkEventJSON("perf_sample", "d", 4, `,"fps":60`),
		sdkEventJSON(strings.Repeat("z", 100), "e", 5, ``),
		sdkEventJSON("", "f", 6, ``),
	), nil)
	if resp.Accepted != 1 || resp.Rejected != 5 || !resp.Stored {
		t.Fatalf("resp %+v", resp)
	}
	if resp.RejectedTypes["asset_load"] != 1 || resp.RejectedTypes["http"] != 2 || resp.RejectedTypes[""] != 1 ||
		resp.RejectedTypes[strings.Repeat("z", 64)+"..."] != 1 {
		t.Errorf("rejected types %v", resp.RejectedTypes)
	}
	joined := strings.Join(resp.Errors, "\n")
	for _, want := range []string{`event 0: type "asset_load" is not supported yet`, `event 1: type "http" is not supported yet`,
		`event 5: unknown type ""`, `event 4: unknown type "zzzz`} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors lack %q: %v", want, resp.Errors)
		}
	}
}

func TestIngest_ErrorsAreCapped(t *testing.T) {
	h := newIngestHandler(&fakeEventStore{}, zap.NewNop())
	var events []string
	for i := 0; i < 120; i++ {
		events = append(events, sdkEventJSON("http", fmt.Sprintf("h%d", i), i, ``))
	}
	_, resp := postBatch(t, h, batchOf(events...), nil)
	if resp.Rejected != 120 || len(resp.Errors) != maxResponseErrors+1 || resp.Errors[maxResponseErrors] != "... and 70 more" ||
		resp.RejectedTypes["http"] != 120 {
		t.Fatalf("rejected %d, %d errors, last %q, types %v", resp.Rejected, len(resp.Errors), resp.Errors[len(resp.Errors)-1], resp.RejectedTypes)
	}
}

// A failed insert of the new tables is never counted as accepted: the
// client keeps the batch and resends it; the tables that succeeded drop the
// resend as duplicates.
func TestIngest_CustomInsertFailureIsNotAccepted(t *testing.T) {
	store := &fakeEventStore{failCustom: true}
	h := newIngestHandler(store, zap.NewNop())
	lines := sdkFixtureAPM6(t)
	body := batchOf(append([]string{sdkEventJSON("perf_sample", "p1", 1, `,"fps":60`)}, lines...)...)
	w, resp := postBatch(t, h, body, map[string]string{SDKVersionHeader: "2.0.0"})
	if w.Code != http.StatusServiceUnavailable || resp.Stored || !resp.Retry || resp.Accepted != 1 || resp.Failed != len(lines) {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	store.failCustom = false
	w, resp = postBatch(t, h, body, map[string]string{SDKVersionHeader: "2.0.0"})
	if w.Code != http.StatusOK || !resp.Stored || resp.Accepted != len(lines)+1 || resp.Duplicates != 1 {
		t.Fatalf("resend: code %d resp %+v", w.Code, resp)
	}
	// And a resend of everything is all duplicates.
	_, resp = postBatch(t, h, body, map[string]string{SDKVersionHeader: "2.0.0"})
	if resp.Duplicates != len(lines)+1 || len(store.customs) != 3 {
		t.Fatalf("second resend: %+v, %d custom events stored", resp, len(store.customs))
	}
}

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
)

// APM-24: counts the SDK recovered from its on-device checkpoint carry
// "recovered":true and are stored with it; every other row reads false.
func TestIngest_RecoveredCountsAreStoredWithTheFlag(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	metric := `,"name":"frame_ms","unit":"ms","count":3,"sum":30,"min":9,"max":11,"window_ms":4000,"hist_v":1,"hist_keys":[1069,1074,1079],"hist_counts":[1,1,1]`
	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("exception", "x1", 0, `,"fingerprint":"f","message":"APMT k3-n 1 1: test error","stack":"","count":57,"log_type":"error","recovered":true`),
		sdkEventJSON("exception", "x2", 2, `,"fingerprint":"f","message":"other","stack":"","count":1,"log_type":"error"`),
		sdkEventJSON("custom_event", "c1", 0, `,"name":"enemy_killed","count":120,"aggregated":true,"recovered":true`),
		sdkEventJSON("custom_event", "c2", 3, `,"name":"enemy_killed","count":1`),
		sdkEventJSON("custom_metric", "m1", 0, metric+`,"recovered":true`),
		sdkEventJSON("custom_metric", "m2", 4, metric),
		// Wrong JSON type: read as false, the event is kept.
		sdkEventJSON("custom_event", "c3", 5, `,"name":"enemy_killed","count":2,"aggregated":true,"recovered":1`),
	), nil)
	if w.Code != http.StatusOK || resp.Accepted != 7 || resp.Rejected != 0 {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(store.exceptions) != 2 || !bool(store.exceptions[0].Recovered) || bool(store.exceptions[1].Recovered) ||
		store.exceptions[0].Count != 57 {
		t.Errorf("exceptions: %+v", store.exceptions)
	}
	if len(store.customs) != 3 || !bool(store.customs[0].Recovered) || bool(store.customs[1].Recovered) || bool(store.customs[2].Recovered) {
		t.Errorf("custom events: %+v", store.customs)
	}
	if len(store.metricRows) != 2 || !bool(store.metricRows[0].Recovered) || bool(store.metricRows[1].Recovered) {
		t.Errorf("metric rows: %+v", store.metricRows)
	}
}

// APM-24: a count's checkpoint copy and its final event have the same
// event_id and timestamp. Whichever reaches the server second is dropped as a
// duplicate (APM-19), in the same batch or a later one, so sum(count) never
// adds both and the daily views (fed by inserts) never see the second.
func TestIngest_CheckpointCopyAndFinalCount_OnlyOneIsStored(t *testing.T) {
	ts := time.Now().Add(-time.Minute)
	final := func(id string) string {
		return eventAt("exception", id, 9, ts, `,"fingerprint":"f","message":"APMT k3-n 5 1: test error","stack":"","count":115,"log_type":"error"`)
	}
	copyOf := func(id string) string {
		return eventAt("exception", id, 0, ts, `,"fingerprint":"f","message":"APMT k3-n 5 1: test error","stack":"","count":100,"log_type":"error","recovered":true`)
	}

	// The usual order: the final count (written before the process died)
	// is uploaded first; the checkpoint copy recovered at the next launch
	// comes later and is dropped.
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	if _, resp := postBatch(t, h, batchOf(final("b1")), nil); resp.Accepted != 1 || resp.Duplicates != 0 {
		t.Fatalf("final: %+v", resp)
	}
	if _, resp := postBatch(t, h, batchOf(copyOf("b1")), nil); resp.Accepted != 1 || resp.Duplicates != 1 {
		t.Fatalf("copy: %+v", resp)
	}
	if len(store.exceptions) != 1 || store.exceptions[0].Count != 115 || bool(store.exceptions[0].Recovered) {
		t.Errorf("stored: %+v", store.exceptions)
	}

	// Both in one request: the first is kept.
	store = &fakeEventStore{}
	h = newIngestHandler(store, zap.NewNop())
	if _, resp := postBatch(t, h, batchOf(final("b2"), copyOf("b2")), nil); resp.Accepted != 2 || resp.Duplicates != 1 {
		t.Fatalf("one batch: %+v", resp)
	}
	if len(store.exceptions) != 1 || store.exceptions[0].Count != 115 {
		t.Errorf("stored: %+v", store.exceptions)
	}

	// The copy first (only if the process died between writing the final
	// count and deleting the checkpoint, and the copy was sent first): the
	// copy's count is kept, never both.
	store = &fakeEventStore{}
	h = newIngestHandler(store, zap.NewNop())
	postBatch(t, h, batchOf(copyOf("b3")), nil)
	if _, resp := postBatch(t, h, batchOf(final("b3")), nil); resp.Duplicates != 1 {
		t.Fatalf("final after copy: %+v", resp)
	}
	if len(store.exceptions) != 1 || store.exceptions[0].Count != 100 {
		t.Errorf("stored: %+v", store.exceptions)
	}
}

// The checkpoint lines as the APM-24 SDK writes them (serialized on Unity's
// Mono by the SDK's storage worker: testdata/sdk_events_apm24.jsonl) are
// stored with the flag and their counts.
func TestIngest_RecoveredCountsFromTheSDK(t *testing.T) {
	b, err := os.ReadFile("../../processor/testdata/sdk_events_apm24.jsonl")
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
	if w.Code != http.StatusOK || resp.Accepted != 3 || resp.Rejected != 0 {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(store.exceptions) != 1 || store.exceptions[0].Count != 57 || !bool(store.exceptions[0].Recovered) ||
		store.exceptions[0].Seq != 0 || store.exceptions[0].ErrorSeq != 2 || store.exceptions[0].Context["roomId"] != "r-12" {
		t.Errorf("exception: %+v", store.exceptions)
	}
	if len(store.customs) != 1 || store.customs[0].Count != 120 || !bool(store.customs[0].Aggregated) || !bool(store.customs[0].Recovered) {
		t.Errorf("custom event: %+v", store.customs)
	}
	if len(store.metricRows) != 1 || store.metricRows[0].Count != 4 || !bool(store.metricRows[0].Recovered) {
		t.Errorf("metric: %+v", store.metricRows)
	}
}

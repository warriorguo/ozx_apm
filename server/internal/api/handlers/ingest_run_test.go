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

// The SDK's own events (testdata/sdk_events_apm9.jsonl, serialized on
// Unity's Mono by the APM-9 SDK: MemorySampler, RunTracker, CrashReporter)
// are stored as sent: the perf sample's breakdown, a run ended by EndRun, an
// OOM crash with the memory before death and the run that crash ended.
func TestIngest_APM9EventsFromTheSDK(t *testing.T) {
	b, err := os.ReadFile("../../processor/testdata/sdk_events_apm9.jsonl")
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
	if w.Code != http.StatusOK || resp.Accepted != 4 || resp.Rejected != 0 || len(resp.Errors) != 0 {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(store.perfSamples) != 1 || len(store.runs) != 2 || len(store.crashes) != 1 {
		t.Fatalf("%d samples, %d runs, %d crashes", len(store.perfSamples), len(store.runs), len(store.crashes))
	}
	p := store.perfSamples[0]
	if p.MemMB != 80.25 || p.IntervalMs != 1004 || p.MemMonoHeapMB != 96.5 || p.MemAllocMB != 410.25 || p.MemReservedMB != 640 ||
		p.MemGfxMB != 180.75 || p.MemRssMB <= 0 || p.Context["aliveEnemies"] != "11-20" || p.Context["runId"] != "run-1042" {
		t.Errorf("perf %+v %v", p.PerfMemory, p.Context)
	}
	r := store.runs[0]
	if r.RunID != "run-1042" || r.EndReason != "end" || r.Result != "win" || r.Samples != 2 || r.PeakAllocMB != 905.5 || r.StartAllocMB != 410.25 || r.PeakMemMB != 120 {
		t.Errorf("run %+v", r)
	}
	c := store.crashes[0]
	if c.CrashType != "oom" || c.Details["mem_rss_mb"] != "4650.5" || c.Details["run_peak_alloc_mb"] != "2600.0" || c.Details["run_id"] != "run-1043" ||
		c.Details["mem_age_ms"] == "" || c.Details["proc_peak_rss_mb"] != "4650.5" || c.PssKB != 4500000 {
		t.Errorf("crash %s %+v", c.CrashType, c.Details)
	}
	cr := store.runs[1]
	if cr.RunID != "run-1043" || cr.EndReason != "crash" || cr.CrashType != "oom" || cr.PeakRssMB != 4650.5 || cr.EndAllocMB != 2600 || cr.SessionID != c.SessionID {
		t.Errorf("crashed run %+v", cr)
	}
}

// APM-9: perf samples with the memory breakdown and run events.
func TestIngest_RunsAndPerfMemory(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5,"mem_mb":80.5,"interval_ms":1004,"mem_mono_heap_mb":96,"mem_alloc_mb":410.25,"mem_reserved_mb":512,"mem_gfx_mb":120,"mem_rss_mb":1500.5`),
		// An older SDK's sample: no memory breakdown.
		sdkEventJSON("perf_sample", "p2", 2, `,"fps":30,"mem_mb":70`),
		// A wrong type in a new field does not reject the sample.
		sdkEventJSON("perf_sample", "p3", 3, `,"fps":30,"mem_mb":70,"mem_rss_mb":"lots","interval_ms":-4`),
		sdkEventJSON("run", "r1", 4, `,"run_id":"run-42","end_reason":"end","result":"win","duration_ms":600000,"samples":600,`+
			`"peak_mem_mb":90,"peak_mono_heap_mb":100,"peak_alloc_mb":700,"peak_reserved_mb":800,"peak_gfx_mb":150,"peak_rss_mb":1900,`+
			`"start_alloc_mb":400,"start_rss_mb":1400,"end_alloc_mb":650,"end_rss_mb":1850,"ctx":{"runId":"run-42","stageType":"forest"}`),
		sdkEventJSON("run", "r2", 5, `,"run_id":"run-43","end_reason":"crash","crash_type":"oom","peak_rss_mb":4800`),
		sdkEventJSON("run", "r3", 6, `,"end_reason":"end"`), // no run id: rejected
	), nil)
	if w.Code != http.StatusOK || resp.Accepted != 5 || resp.Rejected != 1 || len(resp.Errors) != 1 || !strings.Contains(resp.Errors[0], "run_id") {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(store.perfSamples) != 3 || len(store.runs) != 2 {
		t.Fatalf("%d samples, %d runs", len(store.perfSamples), len(store.runs))
	}
	p := store.perfSamples[0]
	if p.MemMB != 80.5 || p.IntervalMs != 1004 || p.MemMonoHeapMB != 96 || p.MemAllocMB != 410.25 || p.MemReservedMB != 512 || p.MemGfxMB != 120 || p.MemRssMB != 1500.5 {
		t.Errorf("sample: %+v", p)
	}
	if old := store.perfSamples[1]; old.MemMB != 70 || old.MemRssMB != 0 || old.IntervalMs != 0 {
		t.Errorf("old sample: %+v", old)
	}
	if bad := store.perfSamples[2]; bad.MemRssMB != 0 || bad.IntervalMs != 0 {
		t.Errorf("lenient sample: %+v", bad.PerfMemory)
	}
	r := store.runs[0]
	if r.RunID != "run-42" || r.EndReason != "end" || r.Result != "win" || r.CrashType != "" || r.DurationMs != 600000 || r.Samples != 600 ||
		r.PeakRssMB != 1900 || r.PeakAllocMB != 700 || r.StartRssMB != 1400 || r.EndRssMB != 1850 || r.Context["stageType"] != "forest" || r.EventID != "r1" {
		t.Errorf("run: %+v", r)
	}
	if c := store.runs[1]; c.EndReason != "crash" || c.CrashType != "oom" || c.PeakRssMB != 4800 {
		t.Errorf("crashed run: %+v", c)
	}
	// A re-sent run is dropped as a duplicate.
	_, resp = postBatch(t, h, batchOf(sdkEventJSON("run", "r1", 4, `,"run_id":"run-42","end_reason":"end"`)), nil)
	if resp.Duplicates != 1 || len(store.runs) != 2 {
		t.Errorf("resend: %+v, %d runs", resp, len(store.runs))
	}
	// The journal's copy of a run (end_reason crash, the crash's time) has the
	// live copy's id: looked up whatever its end reason, a week around.
	last := store.lookups[len(store.lookups)-1]
	if last.Table.Name != "apm_runs" || !last.AnyKey || last.MaxTime.Sub(last.MinTime) < 13*24*time.Hour {
		t.Errorf("lookup %+v", last)
	}
	_, resp = postBatch(t, h, batchOf(sdkEventJSON("run", "r1", 9, `,"run_id":"run-42","end_reason":"crash","crash_type":"oom"`)), nil)
	if resp.Duplicates != 1 || len(store.runs) != 2 {
		t.Errorf("journal copy: %+v, %d runs", resp, len(store.runs))
	}
}

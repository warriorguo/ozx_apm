package handlers

import (
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

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
}

package integration

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-9 end to end, through the ingest router into a fresh database and back
// through the admin API, on both insert paths: the perf samples' memory
// breakdown, run peaks with OOM runs, FPS / jank rate by a bucketed context
// key, a funnel over custom events (aggregated rows, property filters, the
// window, per version) and crash-free sessions / users with ANR and
// exception rates (per version, per build type, per data pack).

// apm9Env wraps the APM-4 environment with a wider time range (the funnel
// window test needs hours) and an event builder.
type apm9Env struct {
	*apm4Env
	base int64 // ms; events are placed relative to it
}

func setupAPM9(t *testing.T, async bool) *apm9Env {
	e := setupAPM4(t, async)
	now := time.Now()
	e.params = url.Values{"start_time": {now.Add(-6 * time.Hour).UTC().Format(time.RFC3339)}, "end_time": {now.Add(time.Minute).UTC().Format(time.RFC3339)}}
	return &apm9Env{apm4Env: e, base: now.Add(-5 * time.Hour).UnixMilli()}
}

// ev builds an event of typ in the SDK's wire format at base+offsetMs.
func (e *apm9Env) ev(typ, session, version string, offsetMs int64, fields map[string]interface{}) string {
	e.n++
	m := map[string]interface{}{
		"type": typ, "event_id": fmt.Sprintf("apm9-%d", e.n), "seq": e.n, "timestamp": e.base + offsetMs,
		"app_version": version, "platform": "Android", "device_model": "SM-A336B", "os_version": "14",
		"session_id": session, "device_id": "d-" + session, "scene": "Main", "build_type": "release",
	}
	for k, v := range fields {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (e *apm9Env) ingest(t *testing.T, events []string) {
	t.Helper()
	for len(events) > 0 {
		n := len(events)
		if n > 200 {
			n = 200
		}
		if r := postIngest(t, e.apm4Env.ingest, events[:n]...); r.Accepted != n || r.Rejected != 0 || !r.Stored {
			t.Fatalf("ingest: %+v", r)
		}
		events = events[n:]
	}
}

func (e *apm9Env) get(t *testing.T, path string, extra url.Values, out interface{}) {
	t.Helper()
	p := url.Values{}
	for k, v := range e.params {
		p[k] = v
	}
	for k, v := range extra {
		p[k] = v
	}
	getAdmin(t, e.admin, path, p, out)
}

func near3(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func TestAPM9_MemoryBreakdownAndRunPeaks(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM9(t, async)
			var events []string
			// Perf samples of 3.0: three with the breakdown, one of an older SDK.
			for i, rss := range []float64{1000, 1100, 1200} {
				events = append(events, e.ev("perf_sample", "m1", "3.0", int64(i)*1000, map[string]interface{}{
					"fps": 60, "frame_time_ms": 16.6, "mem_mb": 80 + i, "interval_ms": 1000, "mem_mono_heap_mb": 96,
					"mem_alloc_mb": 400 + 10*i, "mem_reserved_mb": 512, "mem_gfx_mb": 120, "mem_rss_mb": rss}))
			}
			events = append(events, e.ev("perf_sample", "m2", "3.0", 5000, map[string]interface{}{"fps": 60, "mem_mb": 70, "build_type": nil}))
			// Runs: 3.0 four that ended and one killed for low memory; 3.1 two.
			run := func(session, version string, peak float64, reason, crashType string, at int64) string {
				f := map[string]interface{}{"run_id": "run-" + session, "end_reason": reason, "duration_ms": 600000, "samples": 600,
					"peak_rss_mb": peak, "peak_alloc_mb": peak / 2, "start_rss_mb": 800, "end_rss_mb": peak - 50,
					"start_alloc_mb": 300, "end_alloc_mb": peak/2 - 20, "ctx": map[string]string{"runId": "run-" + session, "stageType": "forest"}}
				if crashType != "" {
					f["crash_type"] = crashType
				}
				return e.ev("run", session, version, at, f)
			}
			events = append(events,
				run("r1", "3.0", 1000, "end", "", 10000),
				run("r2", "3.0", 1200, "end", "", 11000),
				run("r3", "3.0", 1400, "new_run", "", 12000),
				run("r4", "3.0", 1600, "shutdown", "", 13000),
				run("r5", "3.0", 4800, "crash", "oom", 14000),
				run("r6", "3.1", 800, "end", "", 15000),
				run("r7", "3.1", 900, "end", "", 16000),
			)
			e.ingest(t, events)

			var bd models.MemoryBreakdownResponse
			e.get(t, "/api/memory/breakdown", url.Values{"build_type": {"all"}}, &bd)
			if len(bd.Rows) != 1 || bd.Rows[0].Value != "3.0" || bd.Rows[0].Samples != 4 || bd.Rows[0].Sessions != 2 {
				t.Fatalf("breakdown %+v", bd)
			}
			figs := bd.Rows[0].Figures
			if figs["mem_rss_mb"].Rows != 3 || figs["mem_rss_mb"].Max != 1200 || figs["mem_mb"].Rows != 4 || figs["mem_gfx_mb"].P50 != 120 || figs["mem_alloc_mb"].Max != 420 {
				t.Errorf("figures %+v", figs)
			}

			var peaks models.RunPeaksResponse
			e.get(t, "/api/memory/runs", nil, &peaks)
			if peaks.Metric != "peak_rss" || len(peaks.Rows) != 2 {
				t.Fatalf("peaks %+v", peaks)
			}
			v30 := peaks.Rows[0]
			if v30.Value != "3.0" || v30.Runs != 5 || v30.WithValue != 5 || v30.CrashedRuns != 1 || v30.OOMRuns != 1 || v30.KilledRuns != 0 ||
				v30.P50 != 1400 || v30.Max != 4800 || v30.P90 < 1600 || v30.AvgDuration != 600000 {
				t.Errorf("3.0: %+v", v30)
			}
			// Growth: end - start of the runs that ended (not the crashed
			// one): (peak - 50) - 800 = 150, 350, 550, 750.
			if !near3(v30.AvgGrowthMB, 450) {
				t.Errorf("growth %v", v30.AvgGrowthMB)
			}
			wantBuckets := map[float64][2]int64{768: {3, 0}, 1024: {2, 0}, 1536: {1, 0}, 4096: {1, 1}}
			var total int64
			for _, b := range peaks.Buckets {
				total += b.Runs
				if w, ok := wantBuckets[b.Low]; ok && (b.Runs != w[0] || b.OOM != w[1]) {
					t.Errorf("bucket %+v, want %v", b, w)
				}
			}
			if total != 7 || len(peaks.Buckets) != 9 || peaks.Buckets[8].High != 0 {
				t.Errorf("buckets %+v", peaks.Buckets)
			}
			var byStage models.RunPeaksResponse
			e.get(t, "/api/memory/runs", url.Values{"metric": {"peak_alloc"}, "key": {"stageType"}, "app_version": {"3.0"}}, &byStage)
			if len(byStage.Rows) != 1 || byStage.Rows[0].Value != "forest" || byStage.Rows[0].Max != 2400 || byStage.GroupBy != "ctx:stageType" {
				t.Errorf("by stage %+v", byStage)
			}

			// The run is on its session's time line.
			var tl models.SessionTimeline
			e.get(t, "/api/sessions/timeline", url.Values{"session_id": {"r5"}}, &tl)
			found := false
			for _, it := range tl.Items {
				if it.Type == models.TimelineRun && it.RunID == "run-r5" && it.EndReason == "crash" && it.CrashType == "oom" && it.PeakRssMB == 4800 {
					found = true
				}
			}
			if !found {
				t.Errorf("time line %+v", tl.Items)
			}
		})
	}
}

func TestAPM9_PerformanceByBucketedContext(t *testing.T) {
	e := setupAPM9(t, false)
	var events []string
	at := int64(0)
	perf := func(session string, fps float64, ctx map[string]string) {
		at += 10
		events = append(events, e.ev("perf_sample", session, "3.0", at, map[string]interface{}{"fps": fps, "frame_time_ms": 1000 / fps, "interval_ms": 1000, "ctx": ctx}))
	}
	// aliveEnemies as raw numbers (bucketed by the server) and as the SDK's
	// SetContextCount labels; stageType for the jank rate.
	for i := 0; i < 20; i++ {
		perf("c1", 60, map[string]string{"aliveEnemies": "0", "stageType": "forest"})
		perf("c1", 55, map[string]string{"aliveEnemies": "3", "stageType": "forest"})
		perf("c2", 40, map[string]string{"aliveEnemies": "7", "stageType": "boss"})
		perf("c2", 30, map[string]string{"aliveEnemies": "6-10", "stageType": "boss"})
		perf("c2", 20, map[string]string{"aliveEnemies": "150", "stageType": "boss"})
		perf("c3", 50, map[string]string{"aliveEnemies": "many", "stageType": "boss"})
	}
	jank := func(stage string) {
		at += 10
		events = append(events, e.ev("jank", "c2", "3.0", at, map[string]interface{}{"duration_ms": 80, "max_frame_ms": 80, "recent_events": []string{}, "ctx": map[string]string{"stageType": stage}}))
	}
	for i := 0; i < 8; i++ {
		jank("boss")
	}
	jank("forest")
	e.ingest(t, events)

	var dist models.ContextDistribution
	e.get(t, "/api/context/distribution", url.Values{"key": {"aliveEnemies"}, "metric": {"fps"}, "bucket": {"count"}}, &dist)
	var got []string
	for _, r := range dist.Rows {
		got = append(got, fmt.Sprintf("%s:%d", r.Value, r.Count))
	}
	if dist.Bucket != "count" || strings.Join(got, ",") != "0:20,1-5:20,6-10:40,101-200:20,many:20" {
		t.Fatalf("bucketed %v", got)
	}
	for _, r := range dist.Rows {
		if r.Value == "6-10" && (r.P05 < 29 || r.P05 > 31 || r.P99 < 39 || r.P99 > 41 || r.P50 < 30 || r.P50 > 40) {
			t.Errorf("6-10 percentiles %+v", r)
		}
	}
	var raw models.ContextDistribution
	e.get(t, "/api/context/distribution", url.Values{"key": {"aliveEnemies"}, "metric": {"fps"}}, &raw)
	if raw.Bucket != "" || len(raw.Rows) != 6 {
		t.Errorf("raw %+v", raw.Rows)
	}

	var rate models.ContextDistribution
	e.get(t, "/api/context/distribution", url.Values{"key": {"stageType"}, "metric": {"jank_rate"}}, &rate)
	if len(rate.Rows) != 2 || rate.Total != 9 || rate.Unit != "janks/min" {
		t.Fatalf("jank rate %+v", rate)
	}
	// boss: 80 samples of 1 s (80/60 min) and 8 janks; forest: 40 s and 1 jank.
	boss, forest := rate.Rows[0], rate.Rows[1]
	if boss.Value != "boss" || boss.Count != 8 || !near3(boss.Minutes, 80.0/60) || !near3(boss.Rate, 8/(80.0/60)) ||
		forest.Value != "forest" || forest.Count != 1 || !near3(forest.Rate, 1/(40.0/60)) {
		t.Errorf("rows %+v", rate.Rows)
	}
	if code := adminStatus(t, e.admin, "/api/context/distribution", url.Values{"key": {"stageType"}, "metric": {"fps"}, "bucket": {"log"}}); code != http.StatusBadRequest {
		t.Errorf("bad bucket: %d", code)
	}
}

func TestAPM9_Funnel(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM9(t, async)
			ce := func(session, version, name string, at int64, props map[string]string, extra map[string]interface{}) string {
				f := map[string]interface{}{"name": name, "count": 1}
				if props != nil {
					f["props"] = props
				}
				for k, v := range extra {
					f[k] = v
				}
				return e.ev("custom_event", session, version, at, f)
			}
			boss := map[string]string{"roomType": "boss"}
			hour := int64(time.Hour / time.Millisecond)
			events := []string{
				ce("f1", "3.0", "run_start", 0, nil, nil), ce("f1", "3.0", "room_enter", 1000, boss, nil), ce("f1", "3.0", "run_end", 2000, nil, nil),
				ce("f2", "3.0", "run_start", 0, nil, nil), ce("f2", "3.0", "room_enter", 1000, map[string]string{"roomType": "normal"}, nil), ce("f2", "3.0", "run_end", 2000, nil, nil),
				ce("f3", "3.0", "run_start", 0, nil, nil),
				ce("f4", "3.0", "room_enter", 0, boss, nil), ce("f4", "3.0", "run_end", 1000, nil, nil),
				ce("f5", "3.0", "run_start", 0, nil, nil), ce("f5", "3.0", "room_enter", 1000, boss, nil), ce("f5", "3.0", "run_end", 3*hour, nil, nil),
				// A count the SDK aggregated (no properties) matches a step without property filters.
				ce("f6", "3.1", "run_start", 0, nil, map[string]interface{}{"count": 5, "aggregated": true}),
				ce("f6", "3.1", "room_enter", 1000, boss, nil), ce("f6", "3.1", "run_end", 2000, nil, nil),
				// Another event name in between does not matter.
				ce("f6", "3.1", "enemy_killed", 1500, nil, nil),
			}
			e.ingest(t, events)

			steps := `[{"name":"run_start"},{"name":"room_enter","props":{"roomType":"boss"}},{"name":"run_end"}]`
			var all models.FunnelResponse
			e.get(t, "/api/funnel", url.Values{"steps": {steps}}, &all)
			if len(all.Groups) != 1 || all.WindowSeconds != 7200 {
				t.Fatalf("funnel %+v", all)
			}
			want := []int64{5, 3, 2}
			for i, s := range all.Groups[0].Steps {
				if s.Sessions != want[i] {
					t.Errorf("step %d: %+v, want %d", i, s, want[i])
				}
			}
			if !near3(all.Groups[0].Steps[2].Conversion, 0.4) || !near3(all.Groups[0].Steps[1].FromPrevious, 0.6) {
				t.Errorf("conversion %+v", all.Groups[0].Steps)
			}
			// A window of 4 h lets f5 finish.
			var wide models.FunnelResponse
			e.get(t, "/api/funnel", url.Values{"steps": {steps}, "window": {"14400"}}, &wide)
			if wide.Groups[0].Steps[2].Sessions != 3 {
				t.Errorf("wide window %+v", wide.Groups[0].Steps)
			}
			var byVersion models.FunnelResponse
			e.get(t, "/api/funnel", url.Values{"steps": {steps}, "by": {"app_version"}}, &byVersion)
			got := map[string][]int64{}
			for _, g := range byVersion.Groups {
				for _, s := range g.Steps {
					got[g.Value] = append(got[g.Value], s.Sessions)
				}
			}
			if fmt.Sprint(got["3.0"]) != "[4 2 1]" || fmt.Sprint(got["3.1"]) != "[1 1 1]" || byVersion.Groups[0].Value != "3.0" {
				t.Errorf("by version %v", got)
			}
			var v31 models.FunnelResponse
			e.get(t, "/api/funnel", url.Values{"steps": {steps}, "app_version": {"3.1"}}, &v31)
			if fmt.Sprint(v31.Groups[0].Steps[0].Sessions) != "1" {
				t.Errorf("3.1 only %+v", v31.Groups)
			}
			// No session matches: one group of zeros.
			var none models.FunnelResponse
			e.get(t, "/api/funnel", url.Values{"steps": {`[{"name":"never"}]`}}, &none)
			if len(none.Groups) != 1 || none.Groups[0].Steps[0].Sessions != 0 {
				t.Errorf("none %+v", none)
			}
			if code := adminStatus(t, e.admin, "/api/funnel", url.Values{"steps": {`[{"name":"a b"}]`}}); code != http.StatusBadRequest {
				t.Errorf("bad step: %d", code)
			}
		})
	}
}

func TestAPM9_SessionHealth(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM9(t, async)
			var events []string
			pack := func(i int) map[string]string {
				if i <= 5 {
					return map[string]string{"dataVersion": "dp-7"}
				}
				return map[string]string{"dataVersion": "dp-8"}
			}
			crashTypes := map[int]string{1: "native", 2: "oom", 3: "anr", 6: "killed", 7: "unknown"}
			for i := 1; i <= 10; i++ {
				s := fmt.Sprintf("h%d", i)
				device := "d-" + s
				if i == 2 {
					device = "d-h1" // h1 and h2: one player
				}
				base := map[string]interface{}{"device_id": device, "ctx": pack(i)}
				f := map[string]interface{}{"fps": 60}
				for k, v := range base {
					f[k] = v
				}
				events = append(events, e.ev("perf_sample", s, "3.0", int64(i)*1000, f))
				if ct, ok := crashTypes[i]; ok {
					c := map[string]interface{}{"crash_type": ct, "fingerprint": "fp-" + ct, "stack": "", "breadcrumbs": []string{}, "exit_reason": "X", "foreground": true}
					for k, v := range base {
						c[k] = v
					}
					events = append(events, e.ev("crash", s, "3.0", int64(i)*1000+500, c))
				}
			}
			exc := func(s string, count int) {
				f := map[string]interface{}{"fingerprint": "x", "message": "boom", "stack": "", "count": count, "log_type": "exception", "ctx": pack(4)}
				events = append(events, e.ev("exception", s, "3.0", 20000, f))
			}
			exc("h4", 2)
			exc("h4", 3)
			exc("h5", 1)
			// 3.1 dev build: two sessions, one native crash.
			events = append(events,
				e.ev("perf_sample", "v1", "3.1", 1000, map[string]interface{}{"fps": 60, "build_type": "dev"}),
				e.ev("perf_sample", "v2", "3.1", 1000, map[string]interface{}{"fps": 60, "build_type": "dev"}),
				e.ev("crash", "v2", "3.1", 2000, map[string]interface{}{"build_type": "dev", "crash_type": "native", "fingerprint": "fp", "stack": "", "breadcrumbs": []string{}}),
			)
			e.ingest(t, events)

			var h models.SessionHealthResponse
			e.get(t, "/api/health/sessions", nil, &h)
			if len(h.Rows) != 1 || strings.Join(h.CrashTypes, ",") != "native,java,oom,killed" {
				t.Fatalf("health %+v", h)
			}
			r := h.Rows[0]
			if r.Value != "3.0" || r.BuildType != "release" || r.Sessions != 10 || r.Users != 9 || r.CrashedSessions != 3 || r.CrashedUsers != 2 ||
				!near3(r.CrashFreeSessions, 0.7) || !near3(r.CrashFreeUsers, 7.0/9) || r.ANRSessions != 1 || !near3(r.ANRRate, 0.1) ||
				r.ExceptionSessions != 2 || r.Exceptions != 6 || !near3(r.ExceptionsPerSession, 0.6) {
				t.Errorf("3.0: %+v", r)
			}
			if r.CrashesByType["native"] != 1 || r.CrashesByType["oom"] != 1 || r.CrashesByType["anr"] != 1 || r.CrashesByType["killed"] != 1 || r.CrashesByType["unknown"] != 1 || r.CrashesByType["java"] != 0 {
				t.Errorf("by type %v", r.CrashesByType)
			}
			var withUnknown models.SessionHealthResponse
			e.get(t, "/api/health/sessions", url.Values{"crash_types": {"native,java,oom,killed,unknown"}}, &withUnknown)
			if withUnknown.Rows[0].CrashedSessions != 4 {
				t.Errorf("with unknown %+v", withUnknown.Rows[0])
			}
			var all models.SessionHealthResponse
			e.get(t, "/api/health/sessions", url.Values{"build_type": {"all"}}, &all)
			byKey := map[string]models.SessionHealthRow{}
			for _, row := range all.Rows {
				byKey[row.Value+"/"+row.BuildType] = row
			}
			if dev := byKey["3.1/dev"]; dev.Sessions != 2 || dev.CrashedSessions != 1 || !near3(dev.CrashFreeSessions, 0.5) || len(all.Rows) != 2 {
				t.Errorf("all builds %+v", all.Rows)
			}
			var packs models.SessionHealthResponse
			e.get(t, "/api/health/sessions", url.Values{"key": {"dataVersion"}}, &packs)
			sort.Slice(packs.Rows, func(a, b int) bool { return packs.Rows[a].Value < packs.Rows[b].Value })
			if len(packs.Rows) != 2 || packs.Rows[0].Value != "dp-7" || packs.Rows[0].Sessions != 5 || packs.Rows[0].CrashedSessions != 2 ||
				packs.Rows[0].ExceptionSessions != 2 || packs.Rows[1].Value != "dp-8" || packs.Rows[1].CrashedSessions != 1 || packs.Rows[1].ANRSessions != 0 {
				t.Errorf("by data pack %+v", packs.Rows)
			}
			if code := adminStatus(t, e.admin, "/api/health/sessions", url.Values{"crash_types": {"x"}}); code != http.StatusBadRequest {
				t.Errorf("bad crash type: %d", code)
			}
		})
	}
}

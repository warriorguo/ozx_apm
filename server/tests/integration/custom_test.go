package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api/handlers"
	"github.com/warriorguo/ozx_apm/server/internal/metrics"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// APM-6 end to end: custom events and metrics through the real ingest
// router into a fresh database, read back through the admin API, on both
// insert paths; p50/p95 checked against the exact percentiles of the samples.

// customMetricRow aggregates samples the way the SDK does (one row per
// name, context and window).
func customMetricRow(ts time.Time, id, version, name, session, device string, values ...float64) models.CustomMetric {
	m := models.CustomMetric{Timestamp: ts, EventID: id, AppVersion: version, Platform: "Android", SessionID: session,
		DeviceID: device, Name: name, Unit: "ms", HistVersion: metrics.Version, WindowMs: 60000}
	hist := map[int32]uint64{}
	sum, lo, hi := 0.0, math.Inf(1), math.Inf(-1)
	for _, v := range values {
		hist[int32(metrics.Key(v))]++
		sum += v
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	for k := range hist {
		m.HistKeys = append(m.HistKeys, k)
	}
	sort.Slice(m.HistKeys, func(a, b int) bool { return m.HistKeys[a] < m.HistKeys[b] })
	for _, k := range m.HistKeys {
		m.HistCounts = append(m.HistCounts, hist[k])
	}
	m.Count = uint64(len(values))
	m.Sum, m.Min, m.Max = &sum, &lo, &hi
	return m
}

// metricJSON is the wire form of a metric row (as the SDK serializes it).
func (e *apm4Env) metricJSON(session, version, buildType string, ctx map[string]string, merged bool, values ...float64) string {
	e.n++
	m := customMetricRow(time.UnixMilli(e.ts+int64(e.n)), "", version, "room_switch_ms", session, "d-"+session, values...)
	ev := map[string]interface{}{
		"type": "custom_metric", "event_id": fmt.Sprintf("apm6-m-%d", e.n), "seq": e.n, "timestamp": e.ts + int64(e.n),
		"app_version": version, "platform": "Android", "device_model": "SM-A336B", "os_version": "14",
		"session_id": session, "device_id": "d-" + session, "scene": "Main", "build_type": buildType, "ctx": ctx,
		"name": m.Name, "unit": "ms", "count": m.Count, "sum": *m.Sum, "min": *m.Min, "max": *m.Max, "window_ms": 60000,
		"hist_v": 1, "hist_keys": m.HistKeys, "hist_counts": m.HistCounts,
	}
	if merged {
		ev["ctx_merged"] = true
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

func (e *apm4Env) customEventJSON(session, name string, ctx, props map[string]string, count int, aggregated, merged bool) string {
	e.n++
	ev := map[string]interface{}{
		"type": "custom_event", "event_id": fmt.Sprintf("apm6-e-%d", e.n), "seq": e.n, "timestamp": e.ts + int64(e.n),
		"app_version": "2.2", "platform": "Android", "device_model": "SM-A336B", "os_version": "14",
		"session_id": session, "device_id": "d-" + session, "scene": "Main", "build_type": "release", "ctx": ctx,
		"name": name, "count": count,
	}
	if props != nil {
		ev["props"] = props
	}
	if aggregated {
		ev["aggregated"] = true
	}
	if merged {
		ev["ctx_merged"] = true
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

func postIngest(t *testing.T, router http.Handler, events ...string) handlers.IngestResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewBufferString(`{"events":[`+strings.Join(events, ",")+`]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(handlers.SDKVersionHeader, "2.0.0")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var resp handlers.IngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	return resp
}

func exactQuantile(values []float64, q float64) float64 {
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	return s[int(math.Ceil(q*float64(len(s))))-1]
}

func within(got, want float64) bool {
	return math.Abs(got-want) <= metrics.RelativeAccuracy*math.Abs(want)+1e-9
}

func adminStatus(t *testing.T, router http.Handler, path string, params url.Values) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

// APM-1 verification 7: a room switch timed per room; the admin shows the
// p50/p95 per roomId (and per version), within the histogram's 2%.
func TestCustomMetrics_RoomSwitchPercentilesByRoom(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM4(t, async)
			r := rand.New(rand.NewSource(6))
			sample := func(median float64, n int) []float64 {
				out := make([]float64, n)
				for i := range out {
					out[i] = math.Round(math.Exp(math.Log(median)+0.5*r.NormFloat64())*100) / 100
				}
				return out
			}
			// Three rooms with different switch times, several sessions (devices)
			// and windows each; version 2.2 and 2.1.
			rooms := map[string]float64{"r-1": 400, "r-2": 900, "r-3": 2500}
			byRoom := map[string][]float64{}
			var all22 []float64
			var events []string
			for room, median := range rooms {
				for s := 0; s < 6; s++ {
					vals := sample(median, 20+s)
					byRoom[room] = append(byRoom[room], vals...)
					all22 = append(all22, vals...)
					events = append(events, e.metricJSON(fmt.Sprintf("S%d", s), "2.2", "release", map[string]string{"roomId": room, "levelId": "3"}, false, vals...))
				}
			}
			old := sample(1500, 50)
			events = append(events, e.metricJSON("O1", "2.1", "release", map[string]string{"roomId": "r-1", "levelId": "3"}, false, old...))
			// A merged series (more contexts than the SDK keeps apart): no roomId.
			mergedVals := sample(700, 9)
			events = append(events, e.metricJSON("S0", "2.2", "release", map[string]string{"levelId": "3"}, true, mergedVals...))
			// Dev build: never in the default (release) view.
			events = append(events, e.metricJSON("D1", "2.2", "dev", map[string]string{"roomId": "r-1", "levelId": "3"}, false, 1e6, 2e6))
			resp := postIngest(t, e.ingest, events...)
			if resp.Accepted != len(events) || !resp.Stored || len(resp.Errors) != 0 {
				t.Fatalf("ingest: %+v", resp)
			}

			// The list: every release sample of both versions.
			var list models.CustomMetricsResponse
			getAdmin(t, e.admin, "/api/custom/metrics", e.params, &list)
			allRelease := append(append(append([]float64(nil), all22...), old...), mergedVals...)
			if len(list.Metrics) != 1 {
				t.Fatalf("metrics: %+v", list)
			}
			m := list.Metrics[0]
			if m.Name != "room_switch_ms" || m.Unit != "ms" || m.Count != int64(len(allRelease)) || m.Sessions != 7 {
				t.Errorf("list: %+v", m)
			}
			if !within(m.P50, exactQuantile(allRelease, 0.5)) || !within(m.P95, exactQuantile(allRelease, 0.95)) ||
				!within(m.P99, exactQuantile(allRelease, 0.99)) || m.Max != exactQuantile(allRelease, 1) {
				t.Errorf("list percentiles %+v, exact p50 %v p95 %v", m, exactQuantile(allRelease, 0.5), exactQuantile(allRelease, 0.95))
			}

			// Per room, version 2.2.
			var dist models.CustomDistribution
			getAdmin(t, e.admin, "/api/custom/metrics/distribution", withParams(e.params, "name", "room_switch_ms", "key", "roomId", "app_version", "2.2"), &dist)
			if dist.Total != int64(len(all22)+len(mergedVals)) || dist.Merged != int64(len(mergedVals)) || len(dist.Rows) != 3 || dist.Unit != "ms" {
				t.Fatalf("by room: %+v", dist)
			}
			for _, row := range dist.Rows {
				vals := byRoom[row.Value]
				p50, p95 := exactQuantile(vals, 0.5), exactQuantile(vals, 0.95)
				if row.Count != int64(len(vals)) || row.Sessions != 6 || !within(row.P50, p50) || !within(row.P95, p95) {
					t.Errorf("room %s: %+v, exact p50 %v p95 %v", row.Value, row, p50, p95)
				}
				t.Logf("room %s: n=%d p50 %.1f (exact %.1f) p95 %.1f (exact %.1f)", row.Value, row.Count, row.P50, p50, row.P95, p95)
			}

			// Version A vs B on the same slice (roomId r-1).
			getAdmin(t, e.admin, "/api/custom/metrics/distribution", withParams(e.params, "name", "room_switch_ms", "by", "app_version", "ctx", "roomId:r-1"), &dist)
			versions := map[string]models.CustomMetricStat{}
			for _, row := range dist.Rows {
				versions[row.Value] = row
			}
			if len(versions) != 2 || !within(versions["2.1"].P95, exactQuantile(old, 0.95)) || !within(versions["2.2"].P95, exactQuantile(byRoom["r-1"], 0.95)) {
				t.Errorf("by version: %+v", dist)
			}

			// Dev rows only with build_type=dev.
			getAdmin(t, e.admin, "/api/custom/metrics", withParams(e.params, "build_type", "dev"), &list)
			if len(list.Metrics) != 1 || list.Metrics[0].Count != 2 || list.Metrics[0].Max != 2e6 {
				t.Errorf("dev: %+v", list)
			}

			// The context endpoints see the new table.
			var keys models.ContextKeysResponse
			getAdmin(t, e.admin, "/api/context/keys", withParams(e.params, "source", "custom_metrics"), &keys)
			if len(keys.Keys) != 2 {
				t.Errorf("context keys: %+v", keys)
			}

			// Bad parameters are 400s.
			for _, p := range []url.Values{
				withParams(e.params, "name", "room switch"),
				withParams(e.params, "name", "room_switch_ms", "prop", "x"),
				withParams(e.params, "name", "room_switch_ms", "by", "session_id"),
				withParams(e.params, "name", "room_switch_ms", "build_type", "prod"),
			} {
				if code := adminStatus(t, e.admin, "/api/custom/metrics/distribution", p); code != http.StatusBadRequest {
					t.Errorf("%v: %d", p, code)
				}
			}

			// The daily aggregate holds the same samples and histogram.
			n := queryUint(t, e.client, "SELECT toUInt64(sumMerge(samples)) FROM apm_custom_metric_daily WHERE build_type IN ('release', '')")
			if n != uint64(len(allRelease)) {
				t.Errorf("apm_custom_metric_daily samples %d, want %d", n, len(allRelease))
			}
			var hk []int16
			var hc []uint64
			if err := e.client.Conn().QueryRow(context.Background(),
				"SELECT h.1, h.2 FROM (SELECT sumMapMerge(hist) AS h FROM apm_custom_metric_daily WHERE build_type = 'release' AND app_version = '2.2')").Scan(&hk, &hc); err != nil {
				t.Fatal(err)
			}
			if q := metrics.Quantiles(hk, hc, math.Inf(-1), math.Inf(1), 0.95); !within(q[0], exactQuantile(append(append([]float64(nil), all22...), mergedVals...), 0.95)) {
				t.Errorf("daily view p95 %v", q[0])
			}
		})
	}
}

func TestCustomEvents_CountsAndDistributions(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM4(t, async)
			ctx := func(room string) map[string]string { return map[string]string{"roomId": room, "levelId": "3"} }
			events := []string{
				e.customEventJSON("A", "room_enter", ctx("r-1"), map[string]string{"seed": "1"}, 1, false, false),
				e.customEventJSON("A", "room_enter", ctx("r-2"), map[string]string{"seed": "2"}, 1, false, false),
				e.customEventJSON("B", "room_enter", ctx("r-1"), map[string]string{"seed": "1"}, 1, false, false),
				// Beyond the SDK's per-minute budget: counts without props.
				e.customEventJSON("B", "room_enter", ctx("r-1"), nil, 7, true, false),
				e.customEventJSON("B", "room_enter", map[string]string{"levelId": "3"}, nil, 4, true, true),
				e.customEventJSON("A", "wave_start", ctx("r-1"), nil, 1, false, false),
			}
			resp := postIngest(t, e.ingest, events...)
			if resp.Accepted != len(events) || len(resp.Errors) != 0 {
				t.Fatalf("ingest: %+v", resp)
			}

			var list models.CustomEventsResponse
			getAdmin(t, e.admin, "/api/custom/events", e.params, &list)
			if len(list.Events) != 2 || list.Events[0].Name != "room_enter" || list.Events[0].Count != 14 || list.Events[0].Sessions != 2 ||
				list.Events[1].Count != 1 {
				t.Fatalf("events: %+v", list)
			}

			var dist models.CustomDistribution
			getAdmin(t, e.admin, "/api/custom/events/distribution", withParams(e.params, "name", "room_enter", "key", "roomId"), &dist)
			rooms := map[string]int64{}
			for _, r := range dist.Rows {
				rooms[r.Value] = r.Count
			}
			if dist.Total != 14 || dist.Merged != 4 || rooms["r-1"] != 9 || rooms["r-2"] != 1 || len(rooms) != 2 {
				t.Errorf("by room: %+v", dist)
			}
			getAdmin(t, e.admin, "/api/custom/events/distribution", withParams(e.params, "name", "room_enter", "prop", "seed"), &dist)
			seeds := map[string]int64{}
			for _, r := range dist.Rows {
				seeds[r.Value] = r.Count
			}
			if dist.Total != 14 || dist.Merged != 11 || seeds["1"] != 2 || seeds["2"] != 1 {
				t.Errorf("by prop: %+v", dist)
			}
			// Context filter.
			getAdmin(t, e.admin, "/api/custom/events", withParams(e.params, "ctx", "roomId:r-2"), &list)
			if len(list.Events) != 1 || list.Events[0].Count != 1 {
				t.Errorf("ctx filter: %+v", list)
			}
			if n := queryUint(t, e.client, "SELECT toUInt64(sum(total_count)) FROM apm_custom_event_daily"); n != 15 {
				t.Errorf("apm_custom_event_daily %d, want 15", n)
			}
			var props map[string]string
			if err := e.client.Conn().QueryRow(context.Background(), "SELECT props FROM apm_custom_events WHERE session_id = 'A' AND props['seed'] = '2'").Scan(&props); err != nil || props["seed"] != "2" {
				t.Errorf("props column: %v %v", props, err)
			}
		})
	}
}

// Unknown types and the planned asset_load/http are rejected with a reason
// and stored nowhere; the rest of the batch is.
func TestIngest_UnsupportedTypesRejectedWithReason(t *testing.T) {
	e := setupAPM4(t, false)
	resp := postIngest(t, e.ingest,
		`{"type":"asset_load","event_id":"x1","timestamp":`+fmt.Sprint(e.ts)+`,"app_version":"1","platform":"Android","session_id":"s","device_id":"d"}`,
		`{"type":"http","event_id":"x2","timestamp":`+fmt.Sprint(e.ts)+`,"app_version":"1","platform":"Android","session_id":"s","device_id":"d"}`,
		`{"type":"bogus","event_id":"x3","timestamp":`+fmt.Sprint(e.ts)+`,"app_version":"1","platform":"Android","session_id":"s","device_id":"d"}`,
		e.customEventJSON("A", "room_enter", nil, nil, 1, false, false),
	)
	if resp.Accepted != 1 || resp.Rejected != 3 || !resp.Stored || resp.RejectedTypes["asset_load"] != 1 || resp.RejectedTypes["http"] != 1 || resp.RejectedTypes["bogus"] != 1 {
		t.Fatalf("resp %+v", resp)
	}
	joined := strings.Join(resp.Errors, "\n")
	if !strings.Contains(joined, `event 0: type "asset_load" is not supported yet`) || !strings.Contains(joined, `event 2: unknown type "bogus"`) {
		t.Errorf("errors %v", resp.Errors)
	}
	for _, et := range storage.EventTables {
		if n := queryUint(t, e.client, "SELECT count() FROM "+et.Name+" WHERE event_id IN ('x1', 'x2', 'x3')"); n != 0 {
			t.Errorf("%s: %d rows of rejected events", et.Name, n)
		}
	}
}

// A re-sent batch is dropped as duplicates; ReplacingMergeTree keeps one
// copy of what slips through (APM-19, for the new tables too).
func TestCustom_ResendIsDeduplicated(t *testing.T) {
	e := setupAPM4(t, false)
	events := []string{
		e.customEventJSON("A", "room_enter", nil, nil, 1, false, false),
		e.metricJSON("A", "2.2", "release", map[string]string{"roomId": "r-1"}, false, 100, 200),
	}
	if r := postIngest(t, e.ingest, events...); r.Accepted != 2 || r.Duplicates != 0 {
		t.Fatalf("first: %+v", r)
	}
	if r := postIngest(t, e.ingest, events...); r.Accepted != 2 || r.Duplicates != 2 {
		t.Fatalf("resend: %+v", r)
	}
	// Bypass the lookup: insert the same rows again directly.
	repo := storage.NewRepository(e.client, zap.NewNop())
	var ce models.CustomEvent
	var cm models.CustomMetric
	must(t, json.Unmarshal([]byte(events[0]), &ce))
	must(t, json.Unmarshal([]byte(events[1]), &cm))
	ce.Timestamp, cm.Timestamp = time.UnixMilli(e.ts+1), time.UnixMilli(e.ts+2)
	ce.Count = 1
	must(t, repo.InsertCustomEvents(context.Background(), []models.CustomEvent{ce}))
	must(t, repo.InsertCustomMetrics(context.Background(), []models.CustomMetric{cm}))
	for _, table := range []string{"apm_custom_events", "apm_custom_metrics"} {
		must(t, e.client.Exec(context.Background(), "OPTIMIZE TABLE "+table+" FINAL"))
		if n := queryUint(t, e.client, "SELECT count() FROM "+table); n != 1 {
			t.Errorf("%s: %d rows after OPTIMIZE FINAL, want 1", table, n)
		}
	}
}

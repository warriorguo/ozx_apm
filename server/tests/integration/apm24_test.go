package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// APM-24 end to end: windowed counts the SDK checkpoints on the device.
//
// A count has one event_id for its whole window: the checkpoint copy (with
// "recovered":true, uploaded at the next launch when the process died before
// the window closed) and the window's final event share it, and the
// timestamp (the count's first occurrence). Through the real ingest router,
// on both insert paths:
//   - a process that died: only the checkpoint copies arrive; they are stored
//     with recovered = 1 and count like any row (sum(count), daily views);
//   - the rare double (the process died between writing the final count and
//     deleting the checkpoint): the copy arriving second is dropped as a
//     duplicate, so sum(count) and the daily views count the window once;
//   - a copy that slips past the lookup (inserted directly) is merged away by
//     the ReplacingMergeTree.
func TestAPM24_RecoveredCounts(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			if testing.Short() {
				t.Skip("Skipping integration test in short mode")
			}
			e := setupAPM4(t, async)
			ctx := context.Background()

			exc := func(id, session string, ts int64, count int, recovered bool) string {
				ev := map[string]interface{}{
					"type": "exception", "event_id": id, "seq": 0, "timestamp": ts,
					"app_version": "2.4", "platform": "Android", "device_model": "SM-A336B", "os_version": "16",
					"session_id": session, "device_id": "d-" + session, "scene": "Main", "build_type": "release",
					"fingerprint": "client", "log_type": "error", "message": "APMT k3-n 17 1792000000000: test error",
					"stack": "ApmTestDriver:Update()\n", "count": count, "error_seq": 2, "session_ms": 5000,
				}
				if recovered {
					ev["recovered"] = true
				}
				b, _ := json.Marshal(ev)
				return string(b)
			}
			custom := func(id, session string, ts int64, count int, recovered bool) string {
				ev := map[string]interface{}{
					"type": "custom_event", "event_id": id, "seq": 0, "timestamp": ts,
					"app_version": "2.4", "platform": "Android", "device_model": "SM-A336B", "os_version": "16",
					"session_id": session, "device_id": "d-" + session, "scene": "Main", "build_type": "release",
					"name": "enemy_killed", "count": count, "aggregated": true,
				}
				if recovered {
					ev["recovered"] = true
				}
				b, _ := json.Marshal(ev)
				return string(b)
			}
			metric := func(id, session string, ts int64, recovered bool, values ...float64) string {
				m := customMetricRow(time.UnixMilli(ts), id, "2.4", "frame_ms", session, "d-"+session, values...)
				ev := map[string]interface{}{
					"type": "custom_metric", "event_id": id, "seq": 0, "timestamp": ts,
					"app_version": "2.4", "platform": "Android", "device_model": "SM-A336B", "os_version": "16",
					"session_id": session, "device_id": "d-" + session, "scene": "Main", "build_type": "release",
					"name": m.Name, "unit": "ms", "count": m.Count, "sum": *m.Sum, "min": *m.Min, "max": *m.Max,
					"window_ms": 5000, "hist_v": 1, "hist_keys": m.HistKeys, "hist_counts": m.HistCounts,
				}
				if recovered {
					ev["recovered"] = true
				}
				b, _ := json.Marshal(ev)
				return string(b)
			}

			ts := e.ts
			// Process 1 died in the foreground: only its checkpoint arrives.
			if r := postIngest(t, e.ingest,
				exc("died-exc", "died", ts+1, 57, true),
				custom("died-ev", "died", ts+2, 120, true),
				metric("died-m", "died", ts+3, true, 16, 17, 33)); r.Accepted != 3 || r.Rejected != 0 || r.Duplicates != 0 {
				t.Fatalf("recovered: %+v", r)
			}
			// Process 2 closed its window (final counts), then died before
			// the checkpoint was deleted: the next launch sends the copies.
			if r := postIngest(t, e.ingest,
				exc("live-exc", "live", ts+10, 115, false),
				custom("live-ev", "live", ts+11, 300, false),
				metric("live-m", "live", ts+12, false, 16, 17, 18, 19)); r.Accepted != 3 || r.Duplicates != 0 {
				t.Fatalf("final: %+v", r)
			}
			if r := postIngest(t, e.ingest,
				exc("live-exc", "live", ts+10, 100, true),
				custom("live-ev", "live", ts+11, 250, true),
				metric("live-m", "live", ts+12, true, 16, 17)); r.Accepted != 3 || r.Duplicates != 3 {
				t.Fatalf("copies after the final: %+v", r)
			}

			type want struct{ rows, sum, recovered uint64 }
			check := func(table, sumCol string, w want) {
				t.Helper()
				if n := queryUint(t, e.client, "SELECT count() FROM "+table); n != w.rows {
					t.Errorf("%s: %d rows, want %d", table, n, w.rows)
				}
				if n := queryUint(t, e.client, "SELECT toUInt64(sum("+sumCol+")) FROM "+table); n != w.sum {
					t.Errorf("%s: sum(%s) %d, want %d", table, sumCol, n, w.sum)
				}
				if n := queryUint(t, e.client, "SELECT toUInt64(sum("+sumCol+")) FROM "+table+" WHERE recovered = 1"); n != w.recovered {
					t.Errorf("%s: recovered %d, want %d", table, n, w.recovered)
				}
			}
			check("apm_exceptions", "count", want{2, 57 + 115, 57})
			check("apm_custom_events", "count", want{2, 120 + 300, 120})
			check("apm_custom_metrics", "count", want{2, 3 + 4, 3})

			// The daily views saw each window once.
			if n := queryUint(t, e.client, "SELECT toUInt64(sum(total_count)) FROM apm_exception_daily"); n != 57+115 {
				t.Errorf("apm_exception_daily %d, want %d", n, 57+115)
			}
			if n := queryUint(t, e.client, "SELECT toUInt64(sum(total_count)) FROM apm_custom_event_daily"); n != 120+300 {
				t.Errorf("apm_custom_event_daily %d, want %d", n, 120+300)
			}
			if n := queryUint(t, e.client, "SELECT toUInt64(sumMerge(samples)) FROM apm_custom_metric_daily"); n != 3+4 {
				t.Errorf("apm_custom_metric_daily %d, want %d", n, 3+4)
			}

			// The admin's exception group counts both windows once.
			var list models.ExceptionListResponse
			getAdmin(t, e.admin, "/api/exceptions", e.params, &list)
			if len(list.Exceptions) != 1 || list.Exceptions[0].Count != 57+115 {
				t.Errorf("exception groups: %+v", list.Exceptions)
			}

			// A copy that slipped past the lookup (two uploads at the same
			// moment): the ReplacingMergeTree keeps one row per event_id.
			repo := storage.NewRepository(e.client, zap.NewNop())
			var ce models.CustomEvent
			must(t, json.Unmarshal([]byte(custom("live-ev", "live", ts+11, 250, true)), &ce))
			ce.Timestamp = time.UnixMilli(ts + 11)
			must(t, repo.InsertCustomEvents(ctx, []models.CustomEvent{ce}))
			must(t, e.client.Exec(ctx, "OPTIMIZE TABLE apm_custom_events FINAL"))
			if n := queryUint(t, e.client, "SELECT count() FROM apm_custom_events WHERE event_id = 'live-ev'"); n != 1 {
				t.Errorf("after OPTIMIZE FINAL: %d rows of live-ev, want 1", n)
			}
		})
	}
}

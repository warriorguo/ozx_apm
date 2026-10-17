package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-3 end to end: per-session error sequence, unhandled exceptions and
// per-context repeat counts go through the real ingest router into a fresh
// database and are read back through the admin API, on both insert paths.

const apm3NRE = "System.NullReferenceException: Object reference not set to an instance of an object.\n" +
	"  at Ozx.Enemies.EnemyAI.OnTick (Ozx.Core.TickEvent e) [0x00000] in <00000000000000000000000000000000>:0 \n" +
	"  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in <00000000000000000000000000000000>:0 \n"

func (e *apm4Env) nre(session, room string, extra map[string]interface{}) string {
	f := map[string]interface{}{
		"fingerprint": "client-nre", "log_type": "error", "message": "[GameEventBus] Error handling event TickEvent: " + apm3NRE,
		"stack": "Ozx.Core.GameEventBus:Publish(TickEvent)\n", "embedded_exception": apm3NRE,
	}
	for k, v := range extra {
		f[k] = v
	}
	return e.event(session, room, f)
}

func TestExceptions_SessionSequenceUnhandledAndContextCounts(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM4(t, async)
			awake := map[string]interface{}{"fingerprint": "client-awake", "log_type": "error",
				"message": "SaveManager.Awake: save file missing", "stack": "Ozx.Save.SaveManager:Awake()\n",
				"error_seq": 1, "session_ms": 812}
			e.post(t,
				// Session A: an Awake failure is the root cause; NREs cascade.
				e.event("A", "r-1", awake),
				e.nre("A", "r-1", map[string]interface{}{"error_seq": 2, "session_ms": 830}),
				e.nre("A", "r-1", map[string]interface{}{"error_seq": 3, "session_ms": 846, "count": 40}),
				e.nre("A", "r-2", map[string]interface{}{"error_seq": 43, "session_ms": 2100, "count": 25}),
				e.nre("A", "r-3", map[string]interface{}{"error_seq": 68, "session_ms": 3300, "count": 10}),
				// More contexts than the SDK keeps apart: merged, only levelId shared.
				e.nre("A", "", map[string]interface{}{"error_seq": 78, "session_ms": 4000, "count": 7, "ctx_merged": true,
					"ctx": map[string]string{"levelId": "3"}}),
				// Session B: the NRE is the first error.
				e.nre("B", "r-2", map[string]interface{}{"error_seq": 1, "session_ms": 15000}),
				// A faulted task nobody observed.
				e.event("B", "r-2", map[string]interface{}{"fingerprint": "client-task", "log_type": "error",
					"message": "System.TimeoutException: sync timed out", "stack": "",
					"embedded_exception": "System.TimeoutException: sync timed out\n  at Ozx.Net.Sync.Run () [0x00000] in <00000000000000000000000000000000>:0 \n",
					"error_seq":          2, "session_ms": 16000, "unhandled": "task"}),
				// An SDK before APM-3.
				e.nre("C", "r-1", map[string]interface{}{"count": 3}),
			)

			groups := groupsByType(t, e, e.params)
			nre, root, task := groups["System.NullReferenceException"], groups["plain:SaveManager.Awake: save file missingSaveManager.Awake: save file missing"], groups["System.TimeoutException"]
			if len(groups) != 3 {
				t.Fatalf("groups: %+v", groups)
			}
			if nre.Count != 1+40+25+10+7+1+3 || nre.FirstErrorSessions != 1 || nre.UnhandledCount != 0 {
				t.Errorf("NRE group: %+v", nre)
			}
			if root.Count != 1 || root.FirstErrorSessions != 1 {
				t.Errorf("root group: %+v", root)
			}
			if task.UnhandledCount != 1 || task.FirstErrorSessions != 0 {
				t.Errorf("task group: %+v", task)
			}

			// "First error of the session" only (APM-11): the root cause of A
			// and the NRE of B, one occurrence each.
			first := groupsByType(t, e, withParams(e.params, "first_error", "1"))
			if len(first) != 2 || first["System.NullReferenceException"].Count != 1 ||
				first["plain:SaveManager.Awake: save file missingSaveManager.Awake: save file missing"].Count != 1 {
				t.Errorf("first errors: %+v", first)
			}
			// Restricted to session A's room: only the root cause.
			onlyA := groupsByType(t, e, withParams(e.params, "first_error", "1", "ctx", "roomId:r-1"))
			if len(onlyA) != 1 || onlyA["plain:SaveManager.Awake: save file missingSaveManager.Awake: save file missing"].Count != 1 {
				t.Errorf("first errors in r-1: %+v", onlyA)
			}

			// Detail: occurrences carry the APM-3 fields.
			var d models.ExceptionDetail
			getAdmin(t, e.admin, "/api/exceptions/detail", withParams(e.params, "fingerprint", nre.Fingerprint), &d)
			bySeq := map[int64]models.ExceptionOccurrence{}
			for _, o := range d.Occurrences {
				bySeq[o.ErrorSeq] = o
			}
			if d.FirstErrorSessions != 1 || len(d.Occurrences) != 7 || bySeq[43].Count != 25 || bySeq[43].SessionMs != 2100 ||
				bySeq[43].Context["roomId"] != "r-2" || !bySeq[78].CtxMerged || bySeq[78].Context["roomId"] != "" || bySeq[0].Count != 3 {
				t.Errorf("detail: %+v", d)
			}
			getAdmin(t, e.admin, "/api/exceptions/detail", withParams(e.params, "fingerprint", task.Fingerprint), &d)
			if len(d.Occurrences) != 1 || d.Occurrences[0].Unhandled != "task" || d.Occurrences[0].ErrorSeq != 2 {
				t.Errorf("task detail: %+v", d.Occurrences)
			}
			// The task's exception was never a session's first error.
			req := httptest.NewRequest(http.MethodGet, "/api/exceptions/detail?"+withParams(e.params, "fingerprint", task.Fingerprint, "first_error", "1").Encode(), nil)
			w := httptest.NewRecorder()
			e.admin.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Errorf("task detail with first_error=1: %d", w.Code)
			}

			// Distribution by roomId weighted by count; the merged count apart.
			var dist models.ContextDistribution
			getAdmin(t, e.admin, "/api/context/distribution", withParams(e.params, "key", "roomId", "metric", "exceptions", "fingerprint", nre.Fingerprint), &dist)
			rooms := map[string]int64{}
			for _, r := range dist.Rows {
				rooms[r.Value] = r.Count
			}
			if dist.Total != 87 || dist.Merged != 7 || len(rooms) != 3 || rooms["r-1"] != 1+40+3 || rooms["r-2"] != 25+1 || rooms["r-3"] != 10 {
				t.Errorf("roomId distribution: %+v", dist)
			}
			// By a key the merged count kept: nothing is merged.
			getAdmin(t, e.admin, "/api/context/distribution", withParams(e.params, "key", "levelId", "metric", "exceptions", "fingerprint", nre.Fingerprint), &dist)
			if dist.Total != 87 || dist.Merged != 0 || len(dist.Rows) != 1 || dist.Rows[0].Value != "3" || dist.Rows[0].Count != 87 {
				t.Errorf("levelId distribution: %+v", dist)
			}
			getAdmin(t, e.admin, "/api/context/distribution", withParams(e.params, "key", "roomId", "metric", "exceptions", "first_error", "1"), &dist)
			if dist.Total != 2 || dist.Merged != 0 {
				t.Errorf("first-error distribution: %+v", dist)
			}
			// Other metrics are not filtered by first_error.
			var fps models.ContextDistribution
			getAdmin(t, e.admin, "/api/context/distribution", withParams(e.params, "key", "roomId", "metric", "fps", "first_error", "1"), &fps)

			var values models.ContextValuesResponse
			getAdmin(t, e.admin, "/api/context/values", withParams(e.params, "key", "roomId", "source", "exceptions", "first_error", "1"), &values)
			if len(values.Values) != 2 {
				t.Errorf("values: %+v", values)
			}
			var ts models.TimeSeriesResponse
			getAdmin(t, e.admin, "/api/timeseries", withParams(e.params, "metric", "exceptions", "first_error", "1"), &ts)
			var sum float64
			for _, p := range ts.Data {
				sum += p.Value
			}
			if sum != 2 {
				t.Errorf("first-error time series: %+v", ts)
			}

			req = httptest.NewRequest(http.MethodGet, "/api/exceptions?"+withParams(e.params, "first_error", "yes").Encode(), nil)
			w = httptest.NewRecorder()
			e.admin.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("first_error=yes: %d", w.Code)
			}
		})
	}
}

// Rows written by the previous binary (its INSERT has no APM-3 columns) read
// error_seq 0 and are never "first errors".
func TestExceptions_PreAPM3RowsReadDefaults(t *testing.T) {
	e := setupAPM4(t, false)
	ctx := context.Background()
	// The INSERT of the APM-4 binary.
	if err := e.client.Exec(ctx, `INSERT INTO apm_exceptions (timestamp, app_version, platform, device_model, os_version, session_id,
		device_id, scene, fingerprint, message, stack, count, event_id, seq, build_type, gpu, ram_class, net_type, ctx,
		client_fingerprint, fingerprint_version, log_type, exception_type, message_template, embedded_exception, inner_exceptions, throw_site, call_site, breadcrumbs)
		VALUES (fromUnixTimestamp64Milli(toInt64(?)), '2.0', 'Android', 'Pixel', '14', 'old', 'd', 'Main', 'f0f0f0f0f0f0f0f0',
		'Asset not found', '', 2, 'old-1', 1, 'release', '', '', '', map('roomId', 'r-9'), 'f0f0f0f0f0f0f0f0', 1, 'error', '', 'Asset not found', '', [], '', '', [])`,
		time.UnixMilli(e.ts).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	groups := groupsByType(t, e, e.params)
	if g := groups["plain:Asset not foundAsset not found"]; g.Count != 2 || g.FirstErrorSessions != 0 || g.UnhandledCount != 0 {
		t.Errorf("old row: %+v", groups)
	}
	if first := groupsByType(t, e, withParams(e.params, "first_error", "1")); len(first) != 0 {
		t.Errorf("old rows as first errors: %+v", first)
	}
	var d models.ExceptionDetail
	getAdmin(t, e.admin, "/api/exceptions/detail", withParams(e.params, "fingerprint", "f0f0f0f0f0f0f0f0"), &d)
	if o := d.Occurrences[0]; o.ErrorSeq != 0 || o.SessionMs != 0 || o.Unhandled != "" || o.CtxMerged {
		t.Errorf("old occurrence: %+v", o)
	}
	var dist models.ContextDistribution
	getAdmin(t, e.admin, "/api/context/distribution", withParams(e.params, "key", "roomId", "metric", "exceptions"), &dist)
	if dist.Total != 2 || dist.Merged != 0 || len(dist.Rows) != 1 || dist.Rows[0].Value != "r-9" {
		t.Errorf("old distribution: %+v", dist)
	}
}

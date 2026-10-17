package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/api/handlers/admin"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// APM-14: deleting every row of one device through the admin API, on a real
// ClickHouse, as the production service account would.

func privacyTestDB(kind string) string {
	return fmt.Sprintf("ozx_apm_apm14_%s_%d", kind, time.Now().UnixNano()%1e9)
}

// seedDevice writes rows of the device into all six event tables: n rows of
// each type, plus the first perf sample once more in a separate insert (a
// re-sent duplicate in another part, as APM-19 phase B leaves until a merge).
// Returns the number of distinct rows per table (the duplicate may be merged
// away at any time, so compare with FINAL counts).
func seedDevice(t *testing.T, client *storage.ClickHouseClient, device string, n int) map[string]uint64 {
	t.Helper()
	ctx := context.Background()
	repo := storage.NewRepository(client, zap.NewNop())
	ts := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	var perf []models.PerfSample
	var janks []models.Jank
	var startups []models.Startup
	var loads []models.SceneLoad
	var excs []models.Exception
	var crashes []models.Crash
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%d", device, i)
		at := ts.Add(time.Duration(i) * time.Second)
		perf = append(perf, models.PerfSample{Timestamp: at, EventID: id + "p", AppVersion: "1.0", Platform: "Android", SessionID: "s-" + device, DeviceID: device, Scene: "A", FPS: 50})
		janks = append(janks, models.Jank{Timestamp: at, EventID: id + "j", AppVersion: "1.0", Platform: "Android", SessionID: "s-" + device, DeviceID: device, Scene: "A", DurationMs: 80, RecentEvents: []string{}})
		startups = append(startups, models.Startup{Timestamp: at, EventID: id + "s", AppVersion: "1.0", Platform: "Android", SessionID: "s-" + device, DeviceID: device, Phase1Ms: 1})
		loads = append(loads, models.SceneLoad{Timestamp: at, EventID: id + "l", AppVersion: "1.0", Platform: "Android", SessionID: "s-" + device, DeviceID: device, SceneName: "A", LoadMs: 10})
		excs = append(excs, models.Exception{Timestamp: at, EventID: id + "e", AppVersion: "1.0", Platform: "Android", SessionID: "s-" + device, DeviceID: device, Scene: "A", Fingerprint: "F", Message: "m", Stack: "s", Count: 2})
		crashes = append(crashes, models.Crash{Timestamp: at, EventID: id + "c", AppVersion: "1.0", Platform: "Android", SessionID: "s-" + device, DeviceID: device, Scene: "A", CrashType: "native", Fingerprint: "C", Stack: "s", Breadcrumbs: []string{"b"}})
	}
	must(t, repo.InsertPerfSamples(ctx, perf))
	must(t, repo.InsertPerfSamples(ctx, perf[:1])) // duplicate in a second part
	must(t, repo.InsertJanks(ctx, janks))
	must(t, repo.InsertStartups(ctx, startups))
	must(t, repo.InsertSceneLoads(ctx, loads))
	must(t, repo.InsertExceptions(ctx, excs))
	must(t, repo.InsertCrashes(ctx, crashes))
	counts := map[string]uint64{}
	for _, et := range storage.EventTables {
		counts[et.Name] = uint64(n)
	}
	return counts
}

func deviceRows(t *testing.T, client *storage.ClickHouseClient, device string, final bool) map[string]uint64 {
	t.Helper()
	out := map[string]uint64{}
	for _, et := range storage.EventTables {
		q := "SELECT count() FROM " + et.Name
		if final {
			q += " FINAL"
		}
		out[et.Name] = queryUint(t, client, q+" WHERE device_id = ?", device)
	}
	return out
}

// viewTotals reads the apm_*_daily aggregates (as the admin, so it works
// whatever the test account may read).
func viewTotals(t *testing.T, client *storage.ClickHouseClient) string {
	t.Helper()
	return fmt.Sprintf("fps=%d startup=%d exc=%d crash=%d",
		queryUint(t, client, "SELECT toUInt64(sum(sample_count)) FROM apm_fps_daily"),
		queryUint(t, client, "SELECT toUInt64(sum(sample_count)) FROM apm_startup_daily"),
		queryUint(t, client, "SELECT toUInt64(sum(total_count)) FROM apm_exception_daily"),
		queryUint(t, client, "SELECT toUInt64(sum(crash_count)) FROM apm_crash_daily"))
}

func privacyPost(t *testing.T, router http.Handler, path, id, confirm string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"id_type":"device_id","id":`+jsonString(id)+`}`))
	if confirm != "" {
		req.Header.Set(admin.ConfirmDeleteHeader, confirm)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeInto(t *testing.T, body []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
}

// runDeletionScenario: the device's rows are deleted from every table and
// the backup copy, the other device and the aggregates are untouched, the
// audit and verify endpoints tell the story, a row arriving afterwards is
// found by verify and removed by running the deletion again.
func runDeletionScenario(t *testing.T, owner *storage.ClickHouseClient, router http.Handler) {
	t.Helper()
	const target, other = "dev-target-'quote", "dev-other"
	ctx := context.Background()
	want := seedDevice(t, owner, target, 3)
	otherRows := seedDevice(t, owner, other, 4)
	// A leftover copy of a table (as the APM-19 migration keeps *_pre_apm19
	// for a few days) also holds the device's rows: it is found by column.
	must(t, owner.Exec(ctx, "CREATE TABLE apm_perf_samples_pre_apm19 AS apm_perf_samples"))
	must(t, owner.Exec(ctx, "INSERT INTO apm_perf_samples_pre_apm19 SELECT * FROM apm_perf_samples"))
	views := viewTotals(t, owner)

	if got := deviceRows(t, owner, target, true); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("seeded %v, want %v", got, want)
	}

	code, body := privacyPost(t, router, "/api/privacy/delete", target, target)
	if code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	var d models.PrivacyDeletion
	decodeInto(t, body, &d)
	// 3 per event table and the backup, plus the duplicate unless a merge removed it.
	if d.Status != models.PrivacyStatusDone || d.RowsRemaining != 0 || d.RowsDeleted < 3*7 || d.RowsDeleted > 3*7+2 || len(d.Tables) != 7 {
		t.Errorf("deletion %+v", d)
	}
	t.Logf("deleted %d rows from %d tables", d.RowsDeleted, len(d.Tables))

	for _, final := range []bool{false, true} {
		for table, n := range deviceRows(t, owner, target, final) {
			if n != 0 {
				t.Errorf("%s (final=%v): %d rows of the deleted device left", table, final, n)
			}
		}
		if got := deviceRows(t, owner, other, true); fmt.Sprint(got) != fmt.Sprint(otherRows) {
			t.Errorf("other device: %v, want %v", got, otherRows)
		}
	}
	if n := queryUint(t, owner, "SELECT count() FROM apm_perf_samples_pre_apm19 WHERE device_id = ?", target); n != 0 {
		t.Errorf("backup table: %d rows left", n)
	}
	if n := queryUint(t, owner, "SELECT count() FROM apm_perf_samples_pre_apm19 FINAL WHERE device_id = ?", other); n != 4 {
		t.Errorf("backup table: other device has %d rows, want 4", n)
	}
	// Merges never bring the deleted rows back.
	for _, et := range storage.EventTables {
		must(t, owner.Exec(ctx, "OPTIMIZE TABLE "+et.Name+" FINAL"))
	}
	for table, n := range deviceRows(t, owner, target, true) {
		if n != 0 {
			t.Errorf("%s after OPTIMIZE FINAL: %d rows", table, n)
		}
	}
	if n := queryUint(t, owner, "SELECT count() FROM apm_perf_samples FINAL WHERE device_id = ?", other); n != 4 {
		t.Errorf("other device after OPTIMIZE FINAL: %d perf rows, want 4 (duplicate merged)", n)
	}
	// The aggregates hold no device id and are left as they were.
	if got := viewTotals(t, owner); got != views {
		t.Errorf("aggregates changed: %s -> %s", views, got)
	}

	// Verify: zero rows, the audit trail (running + done), no raw id stored.
	code, body = privacyPost(t, router, "/api/privacy/verify", target, "")
	var v models.PrivacyVerification
	decodeInto(t, body, &v)
	if code != http.StatusOK || v.RowsRemaining != 0 || len(v.Tables) != 7 || len(v.Deletions) != 2 ||
		v.Deletions[0].Status != "done" || v.Deletions[0].RowsBefore != d.RowsDeleted {
		t.Errorf("verify %d %+v", code, v)
	}
	if n := queryUint(t, owner, "SELECT count() FROM "+storage.PrivacyAuditTable+" WHERE position(request_id || id_hash || requested_by || error, ?) > 0", target); n != 0 {
		t.Errorf("the audit table stores the raw id")
	}

	// A row arriving after the deletion (an upload still in flight) shows
	// up in verify; deleting again removes it. Idempotent otherwise.
	seedDevice(t, owner, target, 1)
	code, body = privacyPost(t, router, "/api/privacy/verify", target, "")
	decodeInto(t, body, &v)
	if v.RowsRemaining < 6 || v.RowsRemaining > 7 { // one row per event table (+ the duplicate perf row)
		t.Errorf("verify after a late insert: %d rows", v.RowsRemaining)
	}
	code, body = privacyPost(t, router, "/api/privacy/delete", target, target)
	decodeInto(t, body, &d)
	if code != http.StatusOK || d.Status != "done" || d.RowsDeleted < 6 || d.RowsRemaining != 0 {
		t.Errorf("second deletion %d %+v", code, d)
	}
	code, body = privacyPost(t, router, "/api/privacy/delete", target, target)
	decodeInto(t, body, &d)
	if code != http.StatusOK || d.Status != "done" || d.RowsDeleted != 0 {
		t.Errorf("third deletion %d %+v", code, d)
	}
	if got := deviceRows(t, owner, other, true); fmt.Sprint(got) != fmt.Sprint(otherRows) {
		t.Errorf("other device after all deletions: %v", got)
	}
}

// With an account that has every privilege (the local default account on
// every version, including 23.8 where users cannot be created).
func TestPrivacyDelete_DeviceRemovedFromEveryTable(t *testing.T) {
	admin := adminClient(t)
	db := privacyTestDB("full")
	createDB(t, admin, db)
	client, logs := connect(t, db, "")
	must(t, client.Migrate(context.Background()))
	if n := logs.Len(); n != 0 {
		t.Errorf("Migrate logged %d warnings: %v", n, logs.All())
	}
	router := api.NewAdminRouter(&config.Config{}, storage.NewRepository(client, zap.NewNop()), zap.NewNop())
	runDeletionScenario(t, client, router)
}

// As a copy of the production service account: without ALTER DELETE the
// request fails with 503 and the GRANT, nothing is deleted, the refusal is
// audited; after the GRANT the same request deletes the device.
func TestPrivacyDelete_ServiceAccountNeedsAlterDelete(t *testing.T) {
	admin := adminClient(t)
	ctx := context.Background()
	user := fmt.Sprintf("apm14_svc_%d", time.Now().UnixNano()%1e9)
	if err := admin.Exec(ctx, "CREATE USER "+user+" IDENTIFIED WITH no_password"); err != nil {
		t.Skipf("cannot create a user on this ClickHouse (access management off?): %v", err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP USER IF EXISTS "+user) })
	db := privacyTestDB("svc")
	createDB(t, admin, db)
	must(t, admin.Exec(ctx, "GRANT SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN ON "+db+".* TO "+user))

	svc, logs := connect(t, db, user)
	must(t, svc.Migrate(ctx)) // creates the audit table with CREATE TABLE only
	for _, w := range warnings(logs, "could not create the privacy deletion audit table") {
		t.Errorf("audit table not created by the service account: %s", w)
	}
	owner, _ := connect(t, db, "")
	router := api.NewAdminRouter(&config.Config{}, storage.NewRepository(svc, zap.NewNop()), zap.NewNop())

	const device = "dev-denied"
	want := seedDevice(t, owner, device, 2)
	code, body := privacyPost(t, router, "/api/privacy/delete", device, device)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("without ALTER DELETE: %d %s", code, body)
	}
	var d models.PrivacyDeletion
	decodeInto(t, body, &d)
	if d.Status != "failed" || !strings.Contains(d.Error, storage.PrivacyDeleteGrant) || !strings.Contains(d.Error, "497") {
		t.Errorf("refusal %+v", d)
	}
	t.Logf("refusal: %s", d.Error)
	if got := deviceRows(t, owner, device, true); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows changed by a refused deletion: %v", got)
	}
	code, body = privacyPost(t, router, "/api/privacy/verify", device, "")
	var v models.PrivacyVerification
	decodeInto(t, body, &v)
	if code != http.StatusOK || len(v.Deletions) != 2 || v.Deletions[0].Status != "failed" || v.RowsRemaining < 12 {
		t.Errorf("verify after refusal: %d %+v", code, v)
	}

	// The documented GRANT (RELEASE.md), on this database.
	must(t, admin.Exec(ctx, "GRANT ALTER DELETE ON "+db+".* TO "+user))
	svc2, _ := connect(t, db, user) // grants are read per session
	router = api.NewAdminRouter(&config.Config{}, storage.NewRepository(svc2, zap.NewNop()), zap.NewNop())
	must(t, owner.Exec(ctx, "DELETE FROM "+storage.PrivacyAuditTable+" WHERE 1")) // the scenario counts audit rows
	runDeletionScenario(t, owner, router)
}

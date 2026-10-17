package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-17: Migrate (run at every server start) must produce the same schema as
// scripts/migrate.sql: TTL on every event table and the apm_*_daily views
// (four, six since APM-6), and it must upgrade the two kinds of existing databases without
// losing rows:
//   - "startup": tables created by Migrate before APM-17 (no TTL, no views);
//   - "production": tables created by migrate.sql (TTL and views, as on n192).
//
// testdata/migrate_111d9bf.sql is scripts/migrate.sql at 111d9bf, the schema
// production runs before this change.

const prodSchemaFixture = "testdata/migrate_111d9bf.sql"

// schemaTestDB returns a database name unique to this run, so that tests run
// concurrently against the same ClickHouse do not collide.
func schemaTestDB(t *testing.T, kind string) string {
	return fmt.Sprintf("ozx_apm_apm17_%s_%d", kind, time.Now().UnixNano()%1e9)
}

func adminClient(t *testing.T) *storage.ClickHouseClient {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	client, err := storage.NewClickHouseClient(testutil.TestClickHouseConfig(), zap.NewNop())
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// createDB creates an empty database, dropped when the test ends.
func createDB(t *testing.T, admin *storage.ClickHouseClient, db string) {
	t.Helper()
	ctx := context.Background()
	must(t, admin.Exec(ctx, "DROP DATABASE IF EXISTS "+db))
	must(t, admin.Exec(ctx, "CREATE DATABASE "+db))
	t.Cleanup(func() {
		if os.Getenv("OZX_KEEP_TEST_DB") == "" {
			admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)
		}
	})
}

// connect opens a client on db whose log is observed.
func connect(t *testing.T, db, user string) (*storage.ClickHouseClient, *observer.ObservedLogs) {
	t.Helper()
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = db
	if user != "" {
		cfg.Username = user
	}
	core, logs := observer.New(zapcore.WarnLevel)
	client, err := storage.NewClickHouseClient(cfg, zap.New(core))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, logs
}

// sqlStatements splits a ClickHouse script into statements (one per ';' at
// the end of a line), dropping comment lines.
func sqlStatements(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stmts []string
	var cur strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line + "\n")
		if strings.HasSuffix(trimmed, ";") {
			stmts = append(stmts, strings.TrimSuffix(strings.TrimSpace(cur.String()), ";"))
			cur.Reset()
		}
	}
	return stmts
}

// legacySchema returns the statements that set up a pre-APM-17 database:
// "production" is migrate.sql at 111d9bf as is; "startup" is what Migrate at
// 111d9bf created (the same tables without TTL, no views).
func legacySchema(t *testing.T, kind string) []string {
	stmts := sqlStatements(t, prodSchemaFixture)
	if kind == "production" {
		return stmts
	}
	var out []string
	for _, s := range stmts {
		if strings.HasPrefix(s, "CREATE MATERIALIZED VIEW") || strings.Contains(s, " ADD INDEX ") {
			continue
		}
		var lines []string
		for _, line := range strings.Split(s, "\n") {
			if !strings.HasPrefix(line, "TTL ") {
				lines = append(lines, line)
			}
		}
		out = append(out, strings.Join(lines, "\n"))
	}
	return out
}

// execAll runs the statements. The old migrate.sql has
// "TTL timestamp + INTERVAL 30 DAY", which only ClickHouse 26.1 accepts on a
// DateTime64 column; on older versions the same retention is written with
// toDateTime(), as an operator on those versions would have had to.
func execAll(t *testing.T, client *storage.ClickHouseClient, stmts []string) {
	t.Helper()
	for _, s := range stmts {
		err := client.Exec(context.Background(), s)
		if err != nil && strings.Contains(err.Error(), "TTL expression result column should have DateTime or Date type") {
			s = strings.Replace(s, "\nTTL timestamp + INTERVAL 30 DAY", "\nTTL toDateTime(timestamp) + INTERVAL 30 DAY", 1)
			err = client.Exec(context.Background(), s)
		}
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func queryUint(t *testing.T, client *storage.ClickHouseClient, query string, args ...interface{}) uint64 {
	t.Helper()
	var n uint64
	if err := client.Conn().QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// legacyEventTables are the event tables of the legacy schemas (the
// fixture predates APM-6's apm_custom_events and apm_custom_metrics, which
// Migrate creates with their TTL).
func legacyEventTables() []storage.EventTable {
	var out []storage.EventTable
	for _, et := range storage.EventTables {
		if et != storage.TableCustomEvents && et != storage.TableCustomMetrics {
			out = append(out, et)
		}
	}
	return out
}

// allViews are the apm_*_daily views of the schema, in name order.
const allViews = "apm_crash_daily,apm_custom_event_daily,apm_custom_metric_daily,apm_exception_daily,apm_fps_daily,apm_startup_daily"

const numViews = 6

type schemaState struct {
	ttl         map[string]bool // event table -> has TTL 30 days
	views       []string        // apm_*_daily views present
	viewTargets int             // inner tables of materialized views
}

func readSchemaState(t *testing.T, client *storage.ClickHouseClient) schemaState {
	t.Helper()
	ctx := context.Background()
	st := schemaState{ttl: map[string]bool{}}
	rows, err := client.Conn().Query(ctx, "SELECT name, engine, engine_full FROM system.tables WHERE database = currentDatabase() ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, engine, full string
		must(t, rows.Scan(&name, &engine, &full))
		switch {
		case engine == "MaterializedView":
			st.views = append(st.views, name)
		case strings.HasPrefix(name, ".inner"):
			st.viewTargets++
		case strings.HasPrefix(name, "apm_"):
			// "timestamp + ..." (production, from the old migrate.sql on 26.1)
			// or "toDateTime(timestamp) + ..." (storage.EventTTL).
			st.ttl[name] = strings.Contains(full, "timestamp) + toIntervalDay(30)") || strings.Contains(full, "TTL timestamp + toIntervalDay(30)")
		}
	}
	must(t, rows.Err())
	return st
}

// warnings returns, for each logged entry whose message starts with
// msgPrefix, its table/view/statement field.
func warnings(logs *observer.ObservedLogs, msgPrefix string) []string {
	var out []string
	for _, e := range logs.All() {
		if !strings.HasPrefix(e.Message, msgPrefix) {
			continue
		}
		subject := e.Message
		for _, f := range e.Context {
			if f.Key == "table" || f.Key == "view" || f.Key == "statement" {
				subject = f.String
			}
		}
		out = append(out, subject)
	}
	return out
}

const (
	warnNoTTL  = "event table has no TTL"
	warnNoView = "could not create a materialized view"
	warnNoIdx  = "could not add the event_id index"
)

func TestMigrate_FreshInstallHasTTLAndViews(t *testing.T) {
	admin := adminClient(t)
	db := schemaTestDB(t, "fresh")
	createDB(t, admin, db)
	client, logs := connect(t, db, "")

	must(t, client.Migrate(context.Background()))
	st := readSchemaState(t, client)
	for _, et := range storage.EventTables {
		if !st.ttl[et.Name] {
			t.Errorf("%s has no 30-day TTL", et.Name)
		}
	}
	if strings.Join(st.views, ",") != allViews || st.viewTargets != numViews {
		t.Errorf("views %v, %d inner tables", st.views, st.viewTargets)
	}
	if n := logs.Len(); n != 0 {
		t.Errorf("fresh install logged %d warnings: %v", n, logs.All())
	}

	// Idempotent: a restart changes nothing and logs nothing.
	must(t, client.Migrate(context.Background()))
	if again := readSchemaState(t, client); len(again.views) != numViews || again.viewTargets != numViews {
		t.Errorf("after restart: %+v", again)
	}
	if n := logs.Len(); n != 0 {
		t.Errorf("restart logged %d warnings: %v", n, logs.All())
	}

	// The views are fed.
	repo := storage.NewRepository(client, zap.NewNop())
	ts := time.Now().Add(-time.Minute)
	must(t, repo.InsertPerfSamples(context.Background(), []models.PerfSample{{Timestamp: ts, EventID: "a", AppVersion: "1", Platform: "Android", SessionID: "s", DeviceID: "d", FPS: 30}}))
	if n := queryUint(t, client, "SELECT toUInt64(sum(sample_count)) FROM apm_fps_daily"); n != 1 {
		t.Errorf("apm_fps_daily has %d samples, want 1", n)
	}
}

// legacyInserts are the inserts of the binary before APM-5 (111d9bf), with
// one row of values each. They run before Migrate (seeding a legacy
// database) and after it (an old binary during a rolling deploy or after a
// rollback must keep inserting into the migrated tables).
var legacyInserts = []struct {
	query string
	row   func(s string, ts time.Time) []any
}{
	{"INSERT INTO apm_perf_samples (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, fps, frame_time_ms, main_thread_ms, gc_alloc_kb, mem_mb, event_id, seq)",
		func(s string, ts time.Time) []any {
			return []any{ts, "1", "Android", "m", "os", s, "d", "S", float32(50), float32(20), float32(20), float32(1), float32(100), s + "p", uint64(1)}
		}},
	{"INSERT INTO apm_janks (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, duration_ms, max_frame_ms, recent_gc_count, recent_gc_alloc_kb, recent_events, event_id, seq)",
		func(s string, ts time.Time) []any {
			return []any{ts, "1", "Android", "m", "os", s, "d", "S", float32(80), float32(60), uint32(1), float32(2), []string{"x"}, s + "j", uint64(2)}
		}},
	{"INSERT INTO apm_startups (timestamp, app_version, platform, device_model, os_version, session_id, device_id, phase1_ms, phase2_ms, tti_ms, event_id, seq)",
		func(s string, ts time.Time) []any {
			return []any{ts, "1", "Android", "m", "os", s, "d", float32(1), float32(2), float32(3), s + "s", uint64(3)}
		}},
	{"INSERT INTO apm_scene_loads (timestamp, app_version, platform, device_model, session_id, device_id, scene_name, load_ms, activate_ms, event_id, seq)",
		func(s string, ts time.Time) []any {
			return []any{ts, "1", "Android", "m", s, "d", "S", float32(10), float32(1), s + "l", uint64(4)}
		}},
	{"INSERT INTO apm_exceptions (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, fingerprint, message, stack, count, event_id, seq)",
		func(s string, ts time.Time) []any {
			return []any{ts, "1", "Android", "m", "os", s, "d", "S", "F", "msg", "stack", uint32(3), s + "e", uint64(5)}
		}},
	{"INSERT INTO apm_crashes (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, crash_type, fingerprint, stack, breadcrumbs, event_id, seq)",
		func(s string, ts time.Time) []any {
			return []any{ts, "1", "Android", "m", "os", s, "d", "S", "native", "F", "stack", []string{"b"}, s + "c", uint64(6)}
		}},
}

// seedLegacyRows inserts one row per table the way the 111d9bf binary does
// and returns the session id.
func seedLegacyRows(t *testing.T, client *storage.ClickHouseClient) string {
	t.Helper()
	ctx := context.Background()
	s := fmt.Sprintf("legacy-%d", time.Now().UnixNano())
	ts := time.Now().Add(-2 * time.Hour)
	for _, ins := range legacyInserts {
		batch, err := client.Conn().PrepareBatch(ctx, ins.query)
		if err != nil {
			t.Fatalf("%s: %v", ins.query, err)
		}
		must(t, batch.Append(ins.row(s, ts)...))
		must(t, batch.Send())
	}
	return s
}

func assertLegacyRowsIntact(t *testing.T, client *storage.ClickHouseClient, session string) {
	t.Helper()
	for _, et := range legacyEventTables() {
		if n := queryUint(t, client, "SELECT count() FROM "+et.Name+" WHERE session_id = ?", session); n != 1 {
			t.Errorf("%s: %d rows of the legacy session, want 1", et.Name, n)
		}
	}
	if n := queryUint(t, client, "SELECT toUInt64(sum(count)) FROM apm_exceptions WHERE session_id = ?", session); n != 3 {
		t.Errorf("legacy exception count %d, want 3", n)
	}
}

// A database whose tables Migrate created before APM-17: Migrate adds the
// views, warns about the missing TTL (it must not add it: that deletes data
// and needs an admin privilege), and keeps every row.
func TestMigrate_UpgradesStartupCreatedDatabase(t *testing.T) {
	admin := adminClient(t)
	db := schemaTestDB(t, "startup")
	createDB(t, admin, db)
	client, logs := connect(t, db, "")
	execAll(t, client, legacySchema(t, "startup"))
	session := seedLegacyRows(t, client)

	must(t, client.Migrate(context.Background()))
	st := readSchemaState(t, client)
	if len(st.views) != numViews || st.viewTargets != numViews {
		t.Errorf("views %v, %d inner tables", st.views, st.viewTargets)
	}
	for _, et := range legacyEventTables() {
		if st.ttl[et.Name] {
			t.Errorf("%s: Migrate must not add a TTL to an existing table", et.Name)
		}
	}
	// Tables Migrate creates (APM-6) have it.
	if !st.ttl[storage.TableCustomEvents.Name] || !st.ttl[storage.TableCustomMetrics.Name] {
		t.Errorf("new tables without TTL: %v", st.ttl)
	}
	if got := warnings(logs, warnNoTTL); len(got) != 6 {
		t.Errorf("TTL warnings %v, want one per event table", got)
	}
	if got := warnings(logs, warnNoView); len(got) != 0 {
		t.Errorf("view warnings %v", got)
	}
	assertLegacyRowsIntact(t, client, session)

	// The admin step (scripts/apm17_fix_schema.sh apply) adds the TTL; after
	// that a restart is quiet.
	for _, et := range storage.EventTables {
		must(t, admin.Exec(context.Background(), "ALTER TABLE "+db+"."+et.Name+" MODIFY TTL "+storage.EventTTL))
	}
	logs.TakeAll()
	must(t, client.Migrate(context.Background()))
	if n := logs.Len(); n != 0 {
		t.Errorf("after the admin step: %d warnings: %v", n, logs.All())
	}
	if st := readSchemaState(t, client); len(st.views) != numViews || st.viewTargets != numViews {
		t.Errorf("views doubled or lost: %+v", st)
	}
	// Rows inside the TTL survive it.
	assertLegacyRowsIntact(t, client, session)
}

// The production schema (migrate.sql at 111d9bf: TTL and views present):
// Migrate leaves views and TTL alone and logs nothing.
func TestMigrate_UpgradesProductionDatabase(t *testing.T) {
	admin := adminClient(t)
	db := schemaTestDB(t, "prod")
	createDB(t, admin, db)
	client, logs := connect(t, db, "")
	execAll(t, client, legacySchema(t, "production"))
	session := seedLegacyRows(t, client)
	viewRows := queryUint(t, client, "SELECT toUInt64(sum(total_count)) FROM apm_exception_daily")

	must(t, client.Migrate(context.Background()))
	must(t, client.Migrate(context.Background()))
	st := readSchemaState(t, client)
	if len(st.views) != numViews || st.viewTargets != numViews {
		t.Errorf("views %v, %d inner tables", st.views, st.viewTargets)
	}
	for _, et := range storage.EventTables {
		if !st.ttl[et.Name] {
			t.Errorf("%s lost its TTL (or a new table has none)", et.Name)
		}
	}
	if n := logs.Len(); n != 0 {
		t.Errorf("%d warnings: %v", n, logs.All())
	}
	assertLegacyRowsIntact(t, client, session)
	if n := queryUint(t, client, "SELECT toUInt64(sum(total_count)) FROM apm_exception_daily"); n != viewRows {
		t.Errorf("apm_exception_daily changed from %d to %d by Migrate", viewRows, n)
	}
}

func columnsOf(t *testing.T, client *storage.ClickHouseClient, table string) string {
	t.Helper()
	rows, err := client.Conn().Query(context.Background(),
		"SELECT name, type, default_expression FROM system.columns WHERE database = currentDatabase() AND table = ? ORDER BY position", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name, typ, def string
		must(t, rows.Scan(&name, &typ, &def))
		cols = append(cols, name+" "+typ+" "+def)
	}
	must(t, rows.Err())
	return strings.Join(cols, ", ")
}

// After Migrate, an upgraded table has exactly the columns (names, types,
// defaults, order) of a fresh one, and rows written before the upgrade read
// the APM-5 columns as their defaults.
func TestMigrate_UpgradedTablesMatchFreshInstall(t *testing.T) {
	admin := adminClient(t)
	fresh := schemaTestDB(t, "cols_fresh")
	createDB(t, admin, fresh)
	freshClient, _ := connect(t, fresh, "")
	must(t, freshClient.Migrate(context.Background()))

	for _, kind := range []string{"startup", "production"} {
		t.Run(kind, func(t *testing.T) {
			db := schemaTestDB(t, "cols_"+kind)
			createDB(t, admin, db)
			client, _ := connect(t, db, "")
			execAll(t, client, legacySchema(t, kind))
			session := seedLegacyRows(t, client)
			must(t, client.Migrate(context.Background()))
			for _, et := range legacyEventTables() {
				if got, want := columnsOf(t, client, et.Name), columnsOf(t, freshClient, et.Name); got != want {
					t.Errorf("%s columns differ from a fresh install:\n got %s\nwant %s", et.Name, got, want)
				}
				n := queryUint(t, client, "SELECT count() FROM "+et.Name+
					" WHERE session_id = ? AND build_type = '' AND gpu = '' AND ram_class = '' AND net_type = '' AND empty(ctx)", session)
				if n != 1 {
					t.Errorf("%s: legacy row does not read the APM-5 defaults", et.Name)
				}
			}
		})
	}
}

// Migrate as a copy of the production service account (SELECT, INSERT,
// CREATE TABLE, ALTER ADD COLUMN only): it must start on both kinds of
// database; what it is not allowed to do is only a warning.
func TestMigrate_WithServiceAccountPrivileges(t *testing.T) {
	admin := adminClient(t)
	ctx := context.Background()
	user := fmt.Sprintf("apm17_svc_%d", time.Now().UnixNano()%1e9)
	if err := admin.Exec(ctx, "CREATE USER "+user+" IDENTIFIED WITH no_password"); err != nil {
		t.Skipf("cannot create a user on this ClickHouse (access management off?): %v", err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP USER IF EXISTS "+user) })

	for _, kind := range []string{"startup", "production"} {
		t.Run(kind, func(t *testing.T) {
			db := schemaTestDB(t, "svc_"+kind)
			createDB(t, admin, db)
			owner, _ := connect(t, db, "")
			execAll(t, owner, legacySchema(t, kind))
			session := seedLegacyRows(t, owner)
			must(t, admin.Exec(ctx, "GRANT SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN ON "+db+".* TO "+user))

			client, logs := connect(t, db, user)
			if err := client.Migrate(ctx); err != nil {
				t.Fatalf("Migrate as the service account: %v", err)
			}
			wantTTL := 0
			if kind == "startup" {
				wantTTL = 6
			}
			// ClickHouse 24.1 and 26.1 let CREATE TABLE create a materialized
			// view; should a version require CREATE VIEW, every view that is
			// missing must have been reported.
			st := readSchemaState(t, client)
			viewWarnings := warnings(logs, warnNoView)
			if len(st.views)+len(viewWarnings) != numViews || st.viewTargets != len(st.views) {
				t.Errorf("views %v (inner tables %d), warnings %v", st.views, st.viewTargets, viewWarnings)
			}
			t.Logf("service account: %d views present, %d not creatable", len(st.views), len(viewWarnings))
			if got := warnings(logs, warnNoTTL); len(got) != wantTTL {
				t.Errorf("TTL warnings %v, want %d", got, wantTTL)
			}
			if got := warnings(logs, warnNoIdx); len(got) != 0 {
				t.Errorf("index warnings %v (the legacy tables have the index)", got)
			}
			// The service account still inserts and reads.
			seedLegacyRows(t, client)
			assertLegacyRowsIntact(t, client, session)
		})
	}
}

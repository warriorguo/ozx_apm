package storage

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

var eventTables = []string{
	"apm_perf_samples",
	"apm_janks",
	"apm_startups",
	"apm_scene_loads",
	"apm_exceptions",
	"apm_crashes",
}

// Existing deployments only get new columns through additive, idempotent
// ALTER statements; Migrate runs them on every start.
func TestSchemaStatements_AddEventIDAndSeqToEveryEventTable(t *testing.T) {
	for _, table := range eventTables {
		for _, col := range []string{"event_id String", "seq UInt64"} {
			want := "ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS " + col
			found := false
			for _, stmt := range schemaStatements {
				if strings.HasPrefix(stmt, want) {
					found = true
				}
			}
			if !found {
				t.Errorf("missing migration %q", want)
			}
		}
	}
}

func TestSchemaStatements_CreateTablesIncludeEventIDAndSeq(t *testing.T) {
	for _, table := range eventTables {
		var create string
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+table+" ") {
				create = stmt
			}
		}
		if create == "" {
			t.Fatalf("no CREATE TABLE for %s", table)
		}
		if !strings.Contains(create, "event_id String") || !strings.Contains(create, "seq UInt64") {
			t.Errorf("CREATE TABLE %s lacks event_id/seq", table)
		}
	}
}

// APM-5: build_type, device details and the ctx map on every event table,
// in CREATE TABLE and as an additive migration, in the same order.
func TestSchemaStatements_ContextColumnsOnEveryEventTable(t *testing.T) {
	cols := []string{
		"build_type LowCardinality(String) DEFAULT ''",
		"gpu LowCardinality(String) DEFAULT ''",
		"ram_class LowCardinality(String) DEFAULT ''",
		"net_type LowCardinality(String) DEFAULT ''",
		"ctx Map(LowCardinality(String), String)",
	}
	for _, table := range eventTables {
		var create string
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+table+" ") {
				create = stmt
			}
		}
		if !strings.Contains(create, "    seq UInt64 DEFAULT 0,\n    "+strings.Join(cols, ",\n    ")+",\n    ") {
			t.Errorf("CREATE TABLE %s: APM-5 columns missing or out of order:\n%s", table, create)
		}
		for _, col := range cols {
			want := "ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS " + col
			found := false
			for _, stmt := range schemaStatements {
				found = found || stmt == want
			}
			if !found {
				t.Errorf("missing migration %q", want)
			}
		}
	}
}

// APM-4, then APM-3: the exception columns come last in CREATE TABLE, and are
// added in the same order after the APM-5 columns, so fresh and migrated
// tables match.
func TestSchemaStatements_ExceptionColumnsAPM4AndAPM3(t *testing.T) {
	var create string
	var alters []string
	for _, stmt := range schemaStatements {
		if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS apm_exceptions ") {
			create = stmt
		}
		if strings.HasPrefix(stmt, "ALTER TABLE apm_exceptions ADD COLUMN") {
			alters = append(alters, strings.TrimPrefix(stmt, "ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS "))
		}
	}
	added := append(append([]string{}, exceptionColumnsAPM4...), exceptionColumnsAPM3...)
	want := "    ctx Map(LowCardinality(String), String),\n    " + strings.Join(added, ",\n    ") + ",\n    INDEX "
	if !strings.Contains(create, want) {
		t.Errorf("CREATE TABLE apm_exceptions: APM-4/APM-3 columns missing or out of order:\n%s", create)
	}
	tail := alters[len(alters)-len(added):]
	for i, col := range added {
		if tail[i] != col {
			t.Errorf("migration %d: %q, want %q", i, tail[i], col)
		}
	}
	for _, stmt := range schemaStatements {
		if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS apm_crashes ") &&
			(strings.Contains(stmt, "client_fingerprint") || strings.Contains(stmt, "error_seq")) {
			t.Error("exception columns on apm_crashes")
		}
	}
}

func TestSchemaStatements_NoDestructiveMigrations(t *testing.T) {
	for _, stmt := range schemaStatements {
		upper := strings.ToUpper(stmt)
		if strings.Contains(upper, "DROP ") || strings.Contains(upper, "MODIFY COLUMN") {
			t.Errorf("non-additive migration: %s", stmt)
		}
	}
}

// APM-19: every event table gets the event_id skip index, on fresh installs
// (CREATE) and on existing tables (ADD INDEX, tolerated if not permitted).
func TestSchema_EventIDIndexOnEveryEventTable(t *testing.T) {
	if len(EventTables) != len(eventTables) {
		t.Fatalf("EventTables has %d tables, want %d", len(EventTables), len(eventTables))
	}
	for _, table := range eventTables {
		want := "ALTER TABLE " + table + " ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1"
		if got := addEventIDIndexStatement(table); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+table+" ") &&
				!strings.Contains(stmt, "INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1") {
				t.Errorf("CREATE TABLE %s lacks the event_id index", table)
			}
		}
	}
}

// APM-19 phase B: fresh installs create ReplacingMergeTree tables whose sort
// key is the old one plus event_id, so queries keep the same prefix.
func TestSchema_EventTablesAreReplacingMergeTreeOnEventID(t *testing.T) {
	for _, et := range EventTables {
		var create string
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+et.Name+" ") {
				create = stmt
			}
		}
		if !strings.Contains(create, ") ENGINE = ReplacingMergeTree()\n") {
			t.Errorf("%s is not a ReplacingMergeTree", et.Name)
		}
		if !strings.Contains(create, "\nORDER BY (app_version, "+et.KeyColumn+", timestamp, event_id)\n") {
			t.Errorf("%s: sort key is not (app_version, %s, timestamp, event_id)", et.Name, et.KeyColumn)
		}
	}
}

// APM-17: tables created by Migrate expire like the ones from migrate.sql.
func TestSchema_EventTablesHaveTTL(t *testing.T) {
	for _, et := range EventTables {
		var create string
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+et.Name+" ") {
				create = stmt
			}
		}
		if !strings.HasSuffix(create, "\nTTL toDateTime(timestamp) + INTERVAL 30 DAY") {
			t.Errorf("%s has no 30-day TTL:\n%s", et.Name, create)
		}
	}
}

// APM-17: the four daily views exist in the schema and read the event tables.
func TestSchema_MaterializedViews(t *testing.T) {
	want := []string{"apm_fps_daily", "apm_startup_daily", "apm_exception_daily", "apm_crash_daily"}
	names := MaterializedViewNames()
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("views %v, want %v", names, want)
	}
	for _, v := range materializedViews {
		if !strings.HasPrefix(v.DDL, "CREATE MATERIALIZED VIEW IF NOT EXISTS "+v.Name+"\n") {
			t.Errorf("%s: %q", v.Name, v.DDL)
		}
		if strings.Contains(strings.ToUpper(v.DDL), "POPULATE") {
			t.Errorf("%s: POPULATE would double rows inserted while it runs", v.Name)
		}
	}
}

// scripts/migrate.sql is generated from this file; one source of truth.
func TestMigrateSQL_IsGeneratedFromSchema(t *testing.T) {
	b, err := os.ReadFile("../../scripts/migrate.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != SchemaSQL() {
		t.Fatal("scripts/migrate.sql is out of date: run `go run ./cmd/schemasql > scripts/migrate.sql` in server/")
	}
	for _, stmt := range schemaStatements {
		if !strings.Contains(string(b), stmt+";\n") {
			t.Errorf("migrate.sql lacks %q", stmt)
		}
	}
}

func TestEventTables_MatchTheSortKeyPrefix(t *testing.T) {
	for _, et := range EventTables {
		var create string
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+et.Name+" ") {
				create = stmt
			}
		}
		if !strings.Contains(create, "ORDER BY (app_version, "+et.KeyColumn+", timestamp") {
			t.Errorf("%s: sort key does not start with (app_version, %s, timestamp)", et.Name, et.KeyColumn)
		}
	}
}

func TestBuildEventIDLookup(t *testing.T) {
	ts := time.UnixMilli(1760600000123)
	query, args, err := buildEventIDLookup(EventIDLookup{
		Table: TableExceptions, AppVersions: []string{"1.0"}, Keys: []string{"fp1", "fp2"},
		MinTime: ts, MaxTime: ts.Add(time.Second), EventIDs: []string{"a", "b'c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT DISTINCT event_id FROM apm_exceptions WHERE app_version IN ? AND fingerprint IN ? AND " +
		"timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?)) AND event_id IN ?"
	if query != want {
		t.Fatalf("query\n got %s\nwant %s", query, want)
	}
	if len(args) != 5 || args[2] != int64(1760600000123) || args[3] != int64(1760600001123) {
		t.Fatalf("args %#v", args)
	}
	if ids := args[4].(clickhouse.GroupSet).Value; len(ids) != 2 || ids[1] != "b'c" {
		t.Fatalf("ids %#v", ids)
	}
	for _, bad := range []EventIDLookup{
		{Table: TableExceptions, AppVersions: []string{"1"}, Keys: []string{"k"}},
		{AppVersions: []string{"1"}, Keys: []string{"k"}, EventIDs: []string{"a"}},
	} {
		if _, _, err := buildEventIDLookup(bad); err == nil {
			t.Errorf("no error for %+v", bad)
		}
	}
}

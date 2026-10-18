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
	"apm_custom_events",
	"apm_custom_metrics",
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
			(strings.Contains(stmt, "exception_type") || strings.Contains(stmt, "error_seq")) {
			t.Error("exception columns on apm_crashes")
		}
	}
}

// APM-7: the crash columns come last in CREATE TABLE apm_crashes and are
// added in the same order after the APM-5 columns.
func TestSchemaStatements_CrashColumnsAPM7(t *testing.T) {
	var create string
	var alters []string
	for _, stmt := range schemaStatements {
		if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS apm_crashes ") {
			create = stmt
		}
		if strings.HasPrefix(stmt, "ALTER TABLE apm_crashes ADD COLUMN") {
			alters = append(alters, strings.TrimPrefix(stmt, "ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS "))
		}
	}
	want := "    ctx Map(LowCardinality(String), String),\n    " + strings.Join(crashColumnsAPM7, ",\n    ") + ",\n    INDEX "
	if !strings.Contains(create, want) {
		t.Errorf("CREATE TABLE apm_crashes: APM-7 columns missing or out of order:\n%s", create)
	}
	tail := alters[len(alters)-len(crashColumnsAPM7):]
	for i, col := range crashColumnsAPM7 {
		if tail[i] != col {
			t.Errorf("migration %d: %q, want %q", i, tail[i], col)
		}
		// Additive and cheap: every new column has a default or is a Map.
		if !strings.Contains(col, " DEFAULT ") && !strings.Contains(col, " Map(") {
			t.Errorf("column without a default: %s", col)
		}
	}
}

// APM-7: the symbol tables are created by Migrate (warning on failure) and
// are in migrate.sql; they have a TTL and no device_id (privacy deletion
// looks for that column; symbols are build artifacts).
func TestSchema_SymbolTables(t *testing.T) {
	sql := SchemaSQL()
	if got := strings.Join(SymbolTableNames(), ","); got != "apm_symbol_files,apm_symbol_funcs,apm_symbol_lines" {
		t.Fatalf("symbol tables %s", got)
	}
	for _, tb := range symbolTables {
		if !strings.HasPrefix(tb.DDL, "CREATE TABLE IF NOT EXISTS "+tb.Name+" (") {
			t.Errorf("%s: %q", tb.Name, tb.DDL)
		}
		if !strings.HasSuffix(tb.DDL, "\nTTL toDateTime(uploaded_at) + INTERVAL 400 DAY") {
			t.Errorf("%s has no TTL", tb.Name)
		}
		if strings.Contains(tb.DDL, "device_id") || strings.Contains(tb.DDL, "INDEX ") {
			t.Errorf("%s: no device data, no skip index (the service account may not add one)", tb.Name)
		}
		if !strings.Contains(sql, tb.DDL+";\n") {
			t.Errorf("migrate.sql lacks %s", tb.Name)
		}
		for _, stmt := range schemaStatements {
			if strings.Contains(stmt, tb.Name) {
				t.Errorf("%s must not be a statement that has to succeed", tb.Name)
			}
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

// APM-15: every event table gets the session_id skip index, on fresh
// installs (CREATE) and on existing tables (ADD INDEX in migrate.sql; Migrate
// tolerates a refusal).
func TestSchema_SessionIDIndexOnEveryEventTable(t *testing.T) {
	sql := SchemaSQL()
	for _, table := range eventTables {
		want := "ALTER TABLE " + table + " ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1"
		if got := addSessionIDIndexStatement(table); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if !strings.Contains(sql, want+";\n") {
			t.Errorf("migrate.sql lacks %q", want)
		}
		found := false
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+table+" ") {
				found = true
				if !strings.Contains(stmt, "    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,\n    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1\n)") {
					t.Errorf("CREATE TABLE %s lacks the session_id index", table)
				}
			}
			// Never part of the statements that must succeed: the service
			// account may not add an index.
			if strings.Contains(stmt, "ADD INDEX") {
				t.Errorf("schemaStatements adds an index: %s", stmt)
			}
		}
		if !found {
			t.Errorf("no CREATE TABLE for %s", table)
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
	want := []string{"apm_fps_daily", "apm_startup_daily", "apm_exception_daily", "apm_crash_daily",
		"apm_custom_event_daily", "apm_custom_metric_daily"}
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
	// Crashes: no fingerprint filter (APM-7).
	query, args, err = buildEventIDLookup(EventIDLookup{
		Table: TableCrashes, AppVersions: []string{"1.0"}, AnyKey: true,
		MinTime: ts, MaxTime: ts.Add(time.Second), EventIDs: []string{"a"},
	})
	if err != nil || query != "SELECT DISTINCT event_id FROM apm_crashes WHERE app_version IN ? AND "+
		"timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?)) AND event_id IN ?" || len(args) != 4 {
		t.Fatalf("any key: %v %s %#v", err, query, args)
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

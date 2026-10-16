package storage

import (
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
		if !strings.HasSuffix(create, "ORDER BY (app_version, "+et.KeyColumn+", timestamp, event_id)") {
			t.Errorf("%s: sort key is not (app_version, %s, timestamp, event_id)", et.Name, et.KeyColumn)
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

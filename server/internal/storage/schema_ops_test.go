package storage

import (
	"strings"
	"testing"
	"time"
)

// APM-12: the ops tables are created by Migrate (a warning on failure: the
// technical data must not depend on them) and are in migrate.sql. The
// per-player tables keep a year (TTL) and have player_id (privacy deletion
// finds tables by that column); none has a device id, device details or an
// IP column. ops_events is a ReplacingMergeTree with event_id last in the
// sort key and the event_id index (created with the table: the service
// account may not add an index later).
func TestSchema_OpsTables(t *testing.T) {
	sql := SchemaSQL()
	if got := strings.Join(OpsTableNames(), ","); got != "ops_events,ops_player_days,ops_players,ops_fx_rates,ops_ad_reports" {
		t.Fatalf("ops tables %s", got)
	}
	perPlayer := map[string]string{
		"ops_events":      "TTL day + INTERVAL 365 DAY",
		"ops_player_days": "TTL day + INTERVAL 365 DAY",
		"ops_players":     "TTL toDateTime(last_seen) + INTERVAL 365 DAY",
	}
	for _, tb := range opsTables {
		if !strings.HasPrefix(tb.DDL, "CREATE TABLE IF NOT EXISTS "+tb.Name+" (") {
			t.Errorf("%s: %q", tb.Name, tb.DDL)
		}
		for _, banned := range []string{"device_id", "device_model", "os_version", "gpu", "ram_class", " ip ", "ip_", "stack", "breadcrumb"} {
			if strings.Contains(tb.DDL, banned) {
				t.Errorf("%s has %q: ops tables keep no device or technical data", tb.Name, banned)
			}
		}
		if ttl, ok := perPlayer[tb.Name]; ok {
			if !strings.HasSuffix(tb.DDL, "\n"+ttl) || !strings.Contains(tb.DDL, "player_id String") {
				t.Errorf("%s: want player_id and %q", tb.Name, ttl)
			}
		} else if strings.Contains(tb.DDL, "player_id") {
			t.Errorf("%s: imported tables hold no player data", tb.Name)
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
	ev := opsTables[0].DDL
	for _, want := range []string{"ENGINE = ReplacingMergeTree()", "ORDER BY (name, day, event_id)", "PARTITION BY toYYYYMM(day)",
		"INDEX " + EventIDIndexDDL, "props Map(LowCardinality(String), String)", "num_props Map(LowCardinality(String), Float64)",
		"ctx Map(LowCardinality(String), String)", "client_time DateTime64(3)", "server_time DateTime64(3)", "event_time DateTime64(3)", "seq UInt64"} {
		if !strings.Contains(ev, want) {
			t.Errorf("ops_events lacks %q", want)
		}
	}
	if !strings.Contains(opsTables[2].DDL, "ENGINE = AggregatingMergeTree()") || !strings.Contains(opsTables[1].DDL, "ORDER BY (day, player_id)") {
		t.Error("ops_players / ops_player_days engines")
	}
}

// APM-12: the ops lookup filters on client_time, the same in every copy of
// an event (event_time and day may fall back to the receive time).
func TestBuildEventIDLookup_Ops(t *testing.T) {
	ts := time.UnixMilli(1760600000123)
	query, args, err := buildEventIDLookup(EventIDLookup{
		Table: TableOpsEvents, AppVersions: []string{"1.0"}, Keys: []string{"ad_show"},
		MinTime: ts, MaxTime: ts, EventIDs: []string{"a"},
	})
	want := "SELECT DISTINCT event_id FROM ops_events WHERE app_version IN ? AND name IN ? AND " +
		"client_time >= fromUnixTimestamp64Milli(toInt64(?)) AND client_time <= fromUnixTimestamp64Milli(toInt64(?)) AND event_id IN ?"
	if err != nil || query != want || len(args) != 5 {
		t.Fatalf("%v\n got %s\nwant %s", err, query, want)
	}
}

func TestOpsDate_IsTheUTCDate(t *testing.T) {
	if got := opsDate(time.Date(2026, 10, 20, 0, 30, 0, 0, time.FixedZone("x", 2*3600))); got != "2026-10-19" {
		t.Errorf("got %s", got)
	}
}

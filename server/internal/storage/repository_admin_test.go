package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func TestAdminWhere_BuildTypes(t *testing.T) {
	cases := map[string]string{
		"":        " AND build_type IN ('release', '')",
		"release": " AND build_type IN ('release', '')",
		"dev":     " AND build_type = ?",
		"editor":  " AND build_type = ?",
		"unknown": " AND build_type = ''",
		"all":     "",
	}
	for bt, want := range cases {
		where, args := adminWhere(models.AdminFilter{BuildType: bt})
		if where != "WHERE timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?))"+want {
			t.Errorf("%q: %s", bt, where)
		}
		if n := strings.Count(where, "?"); n != len(args) {
			t.Errorf("%q: %d placeholders, %d args", bt, n, len(args))
		}
	}
}

// Context keys and values are request input: they must only appear as bound
// arguments, never in the SQL text.
func TestAdminWhere_ContextFiltersAreBound(t *testing.T) {
	evil := "x' OR 1=1 --"
	f := models.AdminFilter{
		StartTime: time.Unix(0, 0), EndTime: time.Unix(1, 0), AppVersion: evil, Platform: "Android", BuildType: "all",
		Context: []models.ContextFilter{{Key: "levelId", Values: []string{"3", evil}}, {Key: "roomId", Values: []string{""}}},
	}
	where, args := adminWhere(f)
	want := "WHERE timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?)) AND app_version = ? AND platform = ? AND ctx[?] IN ? AND ctx[?] IN ?"
	if where != want {
		t.Fatalf("where\n got %s\nwant %s", where, want)
	}
	if strings.Contains(where, evil) || strings.Contains(where, "levelId") {
		t.Error("user input in the SQL text")
	}
	if len(args) != 8 || args[4] != "levelId" || args[6] != "roomId" {
		t.Fatalf("args %#v", args)
	}
	if set := args[5].(clickhouse.GroupSet).Value; len(set) != 2 || set[1] != evil {
		t.Errorf("values %#v", set)
	}
}

func TestContextSourcesAndMetrics(t *testing.T) {
	for _, s := range []string{"", "all", "perf", "janks", "startups", "scene_loads", "exceptions", "crashes"} {
		if !ValidContextSource(s) {
			t.Errorf("source %q", s)
		}
	}
	if ValidContextSource("apm_perf_samples") || ValidContextSource("perf; DROP") {
		t.Error("invalid source accepted")
	}
	if tables, _ := contextSourceTables("all"); len(tables) != 6 {
		t.Errorf("all -> %v", tables)
	}
	for _, m := range []string{"fps", "frame_time", "janks", "startup", "scene_load", "exceptions", "crashes"} {
		if !ValidContextMetric(m) {
			t.Errorf("metric %q", m)
		}
	}
	if ValidContextMetric("fps)") {
		t.Error("invalid metric accepted")
	}
}

func TestUnionOverTables_RepeatsArgumentsPerPart(t *testing.T) {
	f := models.AdminFilter{BuildType: "dev", Context: []models.ContextFilter{{Key: "k", Values: []string{"v"}}}}
	query, args := unionOverTables([]string{"a", "b"}, "SELECT ctx[?] FROM %s %s AND mapContains(ctx, ?)", f, []interface{}{"key"}, []interface{}{"key"})
	if strings.Count(query, "UNION ALL") != 1 || strings.Count(query, "?") != len(args) {
		t.Errorf("%s / %d args", query, len(args))
	}
	// key, 2 times, build type, ctx key, ctx values, key: 7 per part
	if len(args) != 14 || args[0] != "key" || args[6] != "key" || args[7] != "key" {
		t.Errorf("args %#v", args)
	}
}

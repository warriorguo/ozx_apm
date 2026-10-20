package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

const evilText = "x' OR 1=1 --"

func evilFilter() models.AdminFilter {
	return models.AdminFilter{StartTime: time.Unix(100, 0), EndTime: time.Unix(200, 0), AppVersion: evilText, BuildType: "release",
		Context: []models.ContextFilter{{Key: "dataVersion", Values: []string{evilText}}}}
}

func checkBound(t *testing.T, name, sql string, args []interface{}) {
	t.Helper()
	if strings.Contains(sql, evilText) {
		t.Errorf("%s: request input in the SQL text", name)
	}
	if n := strings.Count(sql, "?"); n != len(args) {
		t.Errorf("%s: %d placeholders, %d args\n%s", name, n, len(args), sql)
	}
}

func TestSessionHealthQuery_BindsEveryValue(t *testing.T) {
	for _, g := range []CustomGroup{{Kind: "column", Key: "app_version"}, {Kind: "ctx", Key: "dataVersion"}} {
		q, args, err := sessionHealthQuery(evilFilter(), g, HealthOptions{CrashTypes: []string{"native", "oom"}})
		if err != nil {
			t.Fatal(err)
		}
		checkBound(t, g.String(), q, args)
		// One part per session table, exceptions and crashes; each limited to the range.
		parts := len(healthSessionTables) + 2
		if n := strings.Count(q, "timestamp >= fromUnixTimestamp64Milli(toInt64(?))"); n != parts {
			t.Errorf("%s: %d parts with the range, want %d", g, n, parts)
		}
		if strings.Count(q, "UNION ALL") != parts-1 {
			t.Errorf("%s: union of %d", g, strings.Count(q, "UNION ALL")+1)
		}
		// The crash types are a bound set, after the group value's argument.
		var set clickhouse.GroupSet
		for _, a := range args {
			if s, ok := a.(clickhouse.GroupSet); ok && len(s.Value) == 2 && s.Value[0] == "native" {
				set = s
			}
		}
		if set.Value == nil {
			t.Errorf("%s: crash types not bound", g)
		}
		// Aliases never shadow a column inside an aggregate.
		for _, bad := range []string{"AS device_id", "AS build_type", "AS session_id", "max(crashed) AS crashed", "any(dev) AS dev"} {
			if strings.Contains(q, bad) {
				t.Errorf("%s: %q", g, bad)
			}
		}
	}
	if _, _, err := sessionHealthQuery(evilFilter(), CustomGroup{Kind: "column", Key: "ctx"}, HealthOptions{}); err == nil {
		t.Error("grouping by an unknown column accepted")
	}
	if _, _, err := sessionHealthQuery(evilFilter(), CustomGroup{Kind: "ctx", Key: "bad key"}, HealthOptions{}); err == nil {
		t.Error("an invalid context key accepted")
	}
}

// A kill by the player (force-stop) is not a crash unless asked for; it has
// its own entry in crashes_by_type, and "killed" holds the other kills.
func TestSessionHealthQuery_UserKills(t *testing.T) {
	g := CustomGroup{Kind: "column", Key: "app_version"}
	q, args, err := sessionHealthQuery(evilFilter(), g, HealthOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkBound(t, "default", q, args)
	user := "(crash_type = 'killed' AND exit_reason IN ('USER_REQUESTED', 'USER_STOPPED'))"
	if !strings.Contains(q, "countIf(crash_type IN ? AND NOT "+user+") > 0) AS crashed") ||
		!strings.Contains(q, "countIf(crash_type = 'killed' AND NOT "+user+")) AS t3") ||
		!strings.Contains(q, "countIf("+user+")) AS t4") {
		t.Errorf("default: %s", q)
	}
	q, args, _ = sessionHealthQuery(evilFilter(), g, HealthOptions{CountUserKills: true})
	checkBound(t, "user kills", q, args)
	if !strings.Contains(q, "toUInt8(countIf(crash_type IN ?) > 0) AS crashed") {
		t.Errorf("user kills counted: %s", q)
	}
	var set []any
	for _, a := range args {
		if s, ok := a.(clickhouse.GroupSet); ok && len(s.Value) == len(DefaultHealthCrashTypes) {
			set = s.Value
		}
	}
	if fmt.Sprint(set) != "[native java oom killed]" {
		t.Errorf("default crash types %v", set)
	}
	if len(healthTypeColumns) != 7 || healthTypeColumns[4].name != "killed_by_user" {
		t.Errorf("%v", healthTypeColumns)
	}
}

// APM-22: the dashboard counts sessions and crashed sessions from the
// session health's rows with its defaults (every session table, the
// default crash types, kills by the player left out), per version.
func TestDashboardVersionsQuery_IsTheHealthDefinition(t *testing.T) {
	q, args := dashboardVersionsQuery(evilFilter())
	checkBound(t, "dashboard", q, args)
	inner, innerArgs := sessionHealthRows(evilFilter(), "app_version", nil, HealthOptions{})
	if !strings.Contains(q, inner) || len(args) != len(innerArgs) {
		t.Errorf("not the health rows:\n%s", q)
	}
	parts := len(healthSessionTables) + 2
	if n := strings.Count(q, "timestamp >= fromUnixTimestamp64Milli(toInt64(?))"); n != parts {
		t.Errorf("%d parts with the range, want %d", n, parts)
	}
	for _, tbl := range append(append([]EventTable{}, healthSessionTables...), TableExceptions, TableCrashes) {
		if !strings.Contains(q, "FROM "+tbl.Name+" WHERE") {
			t.Errorf("no part for %s", tbl.Name)
		}
	}
	user := "(crash_type = 'killed' AND exit_reason IN ('USER_REQUESTED', 'USER_STOPPED'))"
	if !strings.Contains(q, "countIf(crash_type IN ? AND NOT "+user+") > 0) AS crashed") {
		t.Errorf("kills by the player counted: %s", q)
	}
	var set []any
	for _, a := range args {
		if s, ok := a.(clickhouse.GroupSet); ok && len(s.Value) > 0 && s.Value[0] == "native" {
			set = s.Value
		}
	}
	if fmt.Sprint(set) != "[native java oom killed]" {
		t.Errorf("crash types %v", set)
	}
	if !strings.Contains(q, "uniqExactIf(sid, sc = 1)") || !strings.Contains(q, "max(crashed) AS sc") || !strings.Contains(q, "GROUP BY value, sid") {
		t.Errorf("crashed sessions per version: %s", q)
	}
	// The totals are distinct sessions over all versions (the NULL group),
	// not sums of the versions: a session may have rows of two versions.
	if !strings.Contains(q, "arrayJoin([toNullable(toString(value)), NULL]) AS g") || !strings.Contains(q, "GROUP BY g\n") {
		t.Errorf("no total row: %s", q)
	}
}

func TestFillHealthRates(t *testing.T) {
	row := models.SessionHealthRow{Sessions: 200, Users: 50, CrashedSessions: 3, CrashedUsers: 2, ANRSessions: 1, ExceptionSessions: 40, Exceptions: 90}
	fillHealthRates(&row)
	if row.CrashFreeSessions != 0.985 || row.CrashFreeUsers != 0.96 || row.ANRRate != 0.005 || row.ExceptionSessionRate != 0.2 || row.ExceptionsPerSession != 0.45 {
		t.Errorf("%+v", row)
	}
	empty := models.SessionHealthRow{}
	fillHealthRates(&empty)
	if empty.CrashFreeSessions != 0 || empty.ANRRate != 0 {
		t.Errorf("%+v", empty)
	}
}

func TestFunnelQuery(t *testing.T) {
	steps := []models.FunnelStep{
		{Name: "run_start"},
		{Name: "room_enter", Props: map[string]string{"roomType": evilText, "boss": "1"}},
		{Name: "run_end"},
		{Name: "run_start"}, // a name twice: one name in the IN set
	}
	q, args, err := funnelQuery(evilFilter(), steps, 3600, "app_version")
	if err != nil {
		t.Fatal(err)
	}
	checkBound(t, "funnel", q, args)
	if !strings.Contains(q, "windowFunnel(3600000)(toUInt64(toUnixTimestamp64Milli(timestamp)), (name = ?), (name = ? AND props[?] = ? AND props[?] = ?), (name = ?), (name = ?))") {
		t.Errorf("conditions: %s", q)
	}
	if !strings.Contains(q, "argMin(app_version, timestamp) AS value") || !strings.Contains(q, "name IN ?") {
		t.Errorf("%s", q)
	}
	// Property filters in key order, every value bound.
	if args[1] != "room_enter" || args[2] != "boss" || args[3] != "1" || args[4] != "roomType" || args[5] != evilText {
		t.Errorf("args %v", args[:6])
	}
	if set := args[len(args)-1].(clickhouse.GroupSet).Value; len(set) != 3 {
		t.Errorf("names %v", set)
	}
	if q2, _, _ := funnelQuery(evilFilter(), steps[:1], 60, ""); !strings.Contains(q2, "'' AS value") {
		t.Errorf("unsplit: %s", q2)
	}
	for name, c := range map[string]struct {
		steps  []models.FunnelStep
		window int64
		by     string
	}{
		"no steps":     {nil, 60, ""},
		"9 steps":      {make([]models.FunnelStep, 9), 60, ""},
		"bad name":     {[]models.FunnelStep{{Name: "a b"}}, 60, ""},
		"bad prop key": {[]models.FunnelStep{{Name: "a", Props: map[string]string{"k'": "v"}}}, 60, ""},
		"5 props":      {[]models.FunnelStep{{Name: "a", Props: map[string]string{"a": "1", "b": "1", "c": "1", "d": "1", "e": "1"}}}, 60, ""},
		"window":       {[]models.FunnelStep{{Name: "a"}}, MaxFunnelWindowSec + 1, ""},
		"by":           {[]models.FunnelStep{{Name: "a"}}, 60, "device_id"},
	} {
		if _, _, err := funnelQuery(evilFilter(), c.steps, c.window, c.by); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFunnelSteps(t *testing.T) {
	steps := []models.FunnelStep{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	// 10 sessions stopped at a, 6 at b, 4 reached c.
	got := FunnelSteps(steps, []int64{10, 6, 4})
	want := []struct {
		n          int64
		conv, prev float64
	}{{20, 1, 1}, {10, 0.5, 0.5}, {4, 0.2, 0.4}}
	for i, w := range want {
		if got[i].Name != steps[i].Name || got[i].Sessions != w.n || got[i].Conversion != w.conv || got[i].FromPrevious != w.prev {
			t.Errorf("step %d: %+v, want %+v", i, got[i], w)
		}
	}
	none := FunnelSteps(steps, []int64{0, 0, 0})
	if none[0].Sessions != 0 || none[2].Conversion != 0 || none[1].FromPrevious != 0 {
		t.Errorf("%+v", none)
	}
}

func TestMemoryQueries_ValidateInput(t *testing.T) {
	r := &Repository{}
	ctx := context.Background()
	if _, err := r.GetRunPeaks(ctx, evilFilter(), "peak_rss); DROP", CustomGroup{Kind: "column", Key: "app_version"}); err == nil {
		t.Error("unknown metric accepted")
	}
	if _, err := r.GetMemoryBreakdown(ctx, evilFilter(), CustomGroup{Kind: "column", Key: "session_id"}); err == nil {
		t.Error("unknown column accepted")
	}
	for _, m := range []string{"peak_rss", "peak_alloc", "peak_reserved", "peak_gfx", "peak_mem", "peak_mono_heap"} {
		if !ValidRunPeakMetric(m) {
			t.Errorf("%s", m)
		}
	}
}

func TestContextValueExpr_CountBuckets(t *testing.T) {
	if contextValueExpr(false) != "raw_value" {
		t.Error(contextValueExpr(false))
	}
	e := contextValueExpr(true)
	if !strings.HasPrefix(e, "multiIf(num_value IS NULL, raw_value, num_value < 1, '0', num_value < 6, '1-5'") ||
		!strings.HasSuffix(e, "num_value < 201, '101-200', '201+')") || strings.Contains(e, "?") {
		t.Errorf("%s", e)
	}
	if len(models.CountBucketUpper) != len(models.CountBucketLabels)-1 {
		t.Fatal("bounds and labels differ")
	}
	rows := []models.ContextDistributionRow{{Value: "201+", Count: 1}, {Value: "boss", Count: 9}, {Value: "0", Count: 2}, {Value: "6-10", Count: 50}, {Value: "", Count: 3}}
	sortBucketRows(rows)
	var order []string
	for _, r := range rows {
		order = append(order, r.Value)
	}
	if strings.Join(order, ",") != "0,6-10,201+,boss," {
		t.Errorf("order %v", order)
	}
}

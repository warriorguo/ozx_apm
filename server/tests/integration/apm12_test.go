package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	adminapi "github.com/warriorguo/ozx_apm/server/internal/api/handlers/admin"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/opsbi"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-12 end to end: ops events through the ingest router into the ops
// tables (exactly once whatever is re-sent), the preset BI questions
// (internal/opsbi, the SQL Metabase runs) on known data, the imports,
// deletion by player and the read-only BI account. The ingest server's
// clock is controlled, so "days" are deterministic: day N is N days before
// today (UTC), the server receives each day's events that evening.

type apm12Env struct {
	db     string
	client *storage.ClickHouseClient
	repo   *storage.Repository
	ingest http.Handler
	admin  http.Handler
	mu     sync.Mutex
	now    time.Time
	seq    int
}

const apm12OpsToken = "apm12-ops-token"

func setupAPM12(t *testing.T, async bool) *apm12Env {
	t.Helper()
	admin := adminClient(t)
	e := &apm12Env{db: fmt.Sprintf("ozx_apm_apm12_%v_%d", async, time.Now().UnixNano()%1e9)}
	createDB(t, admin, e.db)
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = e.db
	cfg.AsyncInsert = async
	client, err := testutil.SetupTestDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	e.client = client
	e.repo = storage.NewRepository(client, zap.NewNop())
	e.now = time.Now().UTC()
	full := &config.Config{ClickHouse: *cfg}
	full.Ops.Now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	full.AdminServer.OpsToken = apm12OpsToken
	full.AdminServer.PrivacyToken = privacyTestToken
	e.ingest = api.NewRouter(full, e.repo, zap.NewNop())
	e.admin = api.NewAdminRouter(full, e.repo, zap.NewNop())
	return e
}

func (e *apm12Env) setNow(t time.Time) { e.mu.Lock(); e.now = t.UTC(); e.mu.Unlock() }

// day returns midnight (UTC) n days ago, plus the clock time.
func day(n int, hh, mm, ss int) time.Time {
	d := time.Now().UTC().AddDate(0, 0, -n)
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, ss, 0, time.UTC)
}

func dayStr(n int) string { return day(n, 0, 0, 0).Format("2006-01-02") }

type opsPlayer struct {
	id, country, build string
}

// ev builds an ops event as the SDK's ops queue sends it.
func (e *apm12Env) ev(id string, p opsPlayer, session, name string, at time.Time, props map[string]interface{}, ctx map[string]string) string {
	e.seq++
	m := map[string]interface{}{
		"type": "custom_event", "ops": true, "event_id": id, "seq": e.seq, "timestamp": at.UnixMilli(),
		"app_version": "1.0.0", "platform": "Android", "session_id": session, "build_type": p.build,
		"name": name, "count": 1, "player_id": p.id, "channel": "googleplay", "country": p.country,
	}
	if props != nil {
		m["props"] = props
	}
	if ctx != nil {
		m["ctx"] = ctx
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (e *apm12Env) post(t *testing.T, events ...string) {
	t.Helper()
	resp := postIngest(t, e.ingest, events...)
	if !resp.Stored || resp.Rejected != 0 || resp.Accepted != len(events) || len(resp.Errors) != 0 {
		t.Fatalf("ingest: %+v", resp)
	}
}

func (e *apm12Env) importCSV(t *testing.T, kind, query, csv string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ops/import/"+kind+query, strings.NewReader(csv))
	req.Header.Set("Authorization", "Bearer "+apm12OpsToken)
	w := httptest.NewRecorder()
	e.admin.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import %s: %d %s", kind, w.Code, w.Body.String())
	}
}

// cardRows runs a preset question (rendered as Metabase renders it) and
// returns its rows as text: dates as YYYY-MM-DD, NULL as "NULL".
func cardRows(t *testing.T, conn driver.Conn, key string, values map[string]string) []map[string]string {
	t.Helper()
	c, ok := opsbi.CardByKey(key)
	if !ok {
		t.Fatalf("no card %s", key)
	}
	sql, err := c.Render(values)
	if err != nil {
		t.Fatal(err)
	}
	return queryRows(t, conn, sql)
}

func queryRows(t *testing.T, conn driver.Conn, sql string) []map[string]string {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	defer rows.Close()
	cols := rows.ColumnTypes()
	var out []map[string]string
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		for i, c := range cols {
			vals[i] = reflect.New(c.ScanType()).Interface()
		}
		if err := rows.Scan(vals...); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for i, c := range cols {
			row[c.Name()] = text(reflect.ValueOf(vals[i]).Elem())
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func text(v reflect.Value) string {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "NULL"
		}
		v = v.Elem()
	}
	if tm, ok := v.Interface().(time.Time); ok {
		u := tm.UTC()
		if u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 && u.Nanosecond() == 0 {
			return u.Format("2006-01-02")
		}
		return u.Format(time.RFC3339Nano)
	}
	return fmt.Sprint(v.Interface())
}

// pick returns the rows whose columns have the given values.
func pick(rows []map[string]string, where map[string]string) []map[string]string {
	var out []map[string]string
	for _, r := range rows {
		ok := true
		for k, v := range where {
			if r[k] != v {
				ok = false
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

// expectRow checks that exactly one row matches where and has want.
func expectRow(t *testing.T, card string, rows []map[string]string, where, want map[string]string) {
	t.Helper()
	got := pick(rows, where)
	if len(got) != 1 {
		t.Errorf("%s: %d rows for %v, want 1 (all: %v)", card, len(got), where, rows)
		return
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("%s %v: %s = %s, want %s (row %v)", card, where, k, got[0][k], v, got[0])
		}
	}
}

// Every ops event is stored once, whatever is re-sent (lost responses,
// reconnects), on both insert paths; only ops tables are written; the
// ReplacingMergeTree merges what slips past the lookup.
func TestAPM12_OpsEventsExactlyOnce(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM12(t, async)
			ctx := context.Background()
			p := opsPlayer{"p0123456789abcdef0123456789abcd", "de", "release"}
			at := day(1, 10, 0, 0)
			e.setNow(at.Add(time.Minute))
			batch := []string{
				e.ev("o-1", p, "s1", "apm_session_start", at, nil, map[string]string{"runId": "r1", "masterSeed": "18446", "revivesUsed": "1"}),
				e.ev("o-2", p, "s1", "ad_show", at.Add(time.Second), map[string]interface{}{"placement": "revive", "network": "admob", "revenue": 0.0123, "currency": "USD", "rewarded": true}, nil),
				e.ev("o-3", p, "s1", "run_start", at.Add(2*time.Second), map[string]interface{}{"mode": `hard "x"`}, nil),
			}
			e.post(t, batch...)
			// The same batch three more times (one of them the next day, after
			// offline play), and an overlapping one.
			e.post(t, batch...)
			e.setNow(at.Add(26 * time.Hour))
			e.post(t, batch...)
			e.post(t, batch[1], e.ev("o-4", p, "s2", "run_end", at.Add(25*time.Hour), nil, nil))

			if n := queryUint(t, e.client, "SELECT count() FROM ops_events"); n != 4 {
				t.Errorf("ops_events has %d rows, want 4 (re-sent events dropped at ingest)", n)
			}
			if n := queryUint(t, e.client, "SELECT count() FROM ops_events FINAL"); n != 4 {
				t.Errorf("FINAL %d", n)
			}
			for _, tech := range []string{"apm_custom_events", "apm_perf_samples", "apm_exceptions"} {
				if n := queryUint(t, e.client, "SELECT count() FROM "+tech); n != 0 {
					t.Errorf("%s has %d rows: ops events go to ops_events only", tech, n)
				}
			}
			rows := queryRows(t, e.client.Conn(), `SELECT name, player_id, session_id, country, channel, build_type, platform, app_version,
				toString(day) AS d, toUnixTimestamp64Milli(client_time) AS ct, toUnixTimestamp64Milli(event_time) AS et,
				toUnixTimestamp64Milli(server_time) AS st, seq, props['placement'] AS placement, num_props['revenue'] AS revenue,
				prop_types['rewarded'] AS rewarded_type, props['mode'] AS mode, length(ctx) AS ctx_keys, ctx['runId'] AS run, ctx['revivesUsed'] AS revives
				FROM ops_events ORDER BY client_time`)
			if len(rows) != 4 {
				t.Fatalf("rows %v", rows)
			}
			first := rows[0]
			if first["name"] != "apm_session_start" || first["player_id"] != p.id || first["country"] != "DE" || first["channel"] != "googleplay" ||
				first["build_type"] != "release" || first["platform"] != "Android" || first["d"] != dayStr(1) ||
				first["ct"] != fmt.Sprint(at.UnixMilli()) || first["et"] != first["ct"] || first["st"] != fmt.Sprint(at.Add(time.Minute).UnixMilli()) ||
				first["ctx_keys"] != "2" || first["run"] != "r1" || first["revives"] != "1" {
				t.Errorf("session start %v", first)
			}
			if r := rows[1]; r["placement"] != "revive" || r["revenue"] != "0.0123" || r["rewarded_type"] != "2" {
				t.Errorf("ad_show %v", r)
			}
			if r := rows[2]; r["mode"] != `hard "x"` {
				t.Errorf("run_start %v", r)
			}
			if r := rows[3]; r["name"] != "run_end" || r["d"] != dayStr(0) {
				t.Errorf("run_end %v", r)
			}
			// Derived rows: two days, one player first seen at the first event.
			if n := queryUint(t, e.client, "SELECT uniqExact(day, player_id) FROM ops_player_days"); n != 2 {
				t.Errorf("player days %d", n)
			}
			var first64, last64 int64
			var channel string
			must(t, e.client.Conn().QueryRow(ctx, "SELECT toUnixTimestamp64Milli(min(first_seen)), toUnixTimestamp64Milli(max(last_seen)), max(install_channel) FROM ops_players WHERE player_id = ?", p.id).Scan(&first64, &last64, &channel))
			if first64 != at.UnixMilli() || last64 != at.Add(25*time.Hour).UnixMilli() || channel != "googleplay" {
				t.Errorf("player first %d last %d channel %q", first64, last64, channel)
			}

			// A copy inserted past the lookup (two uploads at the same moment)
			// is merged by the ReplacingMergeTree.
			ev := models.OpsEvent{EventID: "o-2", Name: "ad_show", PlayerID: p.id, AppVersion: "1.0.0", Platform: "Android",
				ClientTime: at.Add(time.Second), EventTime: at.Add(time.Second), ServerTime: at, Day: day(1, 0, 0, 0)}
			must(t, e.repo.InsertOpsEvents(ctx, []models.OpsEvent{ev}))
			if n := queryUint(t, e.client, "SELECT count() FROM ops_events FINAL"); n != 4 {
				t.Errorf("FINAL with a copy: %d", n)
			}
			must(t, e.client.Exec(ctx, "OPTIMIZE TABLE ops_events FINAL"))
			if n := queryUint(t, e.client, "SELECT count() FROM ops_events"); n != 4 {
				t.Errorf("after OPTIMIZE FINAL: %d", n)
			}
		})
	}
}

// A device clock a day ahead counts at the receive time; an event played
// offline two days ago and uploaded now counts two days ago.
func TestAPM12_DaysFollowTheClientClockWithinReason(t *testing.T) {
	e := setupAPM12(t, false)
	p := opsPlayer{"p-clock", "US", "release"}
	now := day(0, 1, 0, 0)
	e.setNow(now)
	e.post(t,
		e.ev("c-ahead", p, "s", "x", now.Add(30*time.Hour), nil, nil),
		e.ev("c-offline", p, "s", "y", day(2, 20, 0, 0), nil, nil))
	rows := queryRows(t, e.client.Conn(), "SELECT name, toString(day) AS d, event_time = server_time AS at_server FROM ops_events ORDER BY name")
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	if rows[0]["d"] != dayStr(0) || rows[0]["at_server"] != "1" || rows[1]["d"] != dayStr(2) || rows[1]["at_server"] != "0" {
		t.Errorf("%v", rows)
	}
	if n := queryUint(t, e.client, "SELECT uniqExact(day) FROM ops_player_days WHERE player_id = 'p-clock'"); n != 2 {
		t.Errorf("player days %d", n)
	}
}

// Acceptance (2): three devices each launch once on two consecutive days:
// DAU 3 on both days, 3 new players on the first and none on the second,
// D1 retention 100 %. The second day's uploads also re-send the first day's
// events (a device that was offline): nothing counts twice.
func TestAPM12_Acceptance_DAUAndD1Retention(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM12(t, async)
			players := []opsPlayer{{"dev-a", "DE", "release"}, {"dev-b", "US", "release"}, {"dev-c", "BR", "release"}}
			launch := func(p opsPlayer, n int) string {
				return e.ev(fmt.Sprintf("%s-d%d", p.id, n), p, fmt.Sprintf("s-%s-%d", p.id, n), opsbi.SessionStart, day(n, 9, 0, 0), nil, nil)
			}
			var firstDay []string
			e.setNow(day(3, 21, 0, 0))
			for _, p := range players {
				firstDay = append(firstDay, launch(p, 3))
			}
			e.post(t, firstDay...)
			e.setNow(day(2, 21, 0, 0))
			for _, p := range players {
				e.post(t, launch(p, 2))
			}
			e.post(t, firstDay...) // re-sent the next day

			conn := e.client.Conn()
			dau := cardRows(t, conn, "dau", nil)
			expectRow(t, "dau", dau, map[string]string{"day": dayStr(3)}, map[string]string{"dau": "3"})
			expectRow(t, "dau", dau, map[string]string{"day": dayStr(2)}, map[string]string{"dau": "3"})
			if len(dau) != 2 {
				t.Errorf("dau rows %v", dau)
			}
			np := cardRows(t, conn, "new_players", nil)
			if len(np) != 1 {
				t.Errorf("new players %v", np)
			}
			expectRow(t, "new_players", np, map[string]string{"day": dayStr(3)}, map[string]string{"new_players": "3"})
			ret := cardRows(t, conn, "retention", nil)
			expectRow(t, "retention", ret, map[string]string{"cohort_day": dayStr(3)},
				map[string]string{"new_players": "3", "d1_pct": "100", "d7_pct": "NULL", "d30_pct": "NULL"})
			if n := queryUint(t, e.client, "SELECT count() FROM ops_events FINAL"); n != 6 {
				t.Errorf("events %d, want 6", n)
			}
		})
	}
}

// apm12Dataset: four players over two days, with sessions, a run, ad events
// in two placements and three currencies, exchange rates and a network
// report. Day 5 and day 4 are 5 and 4 days ago.
func apm12Dataset(t *testing.T, e *apm12Env) {
	pA := opsPlayer{"pA", "DE", "release"}
	pB := opsPlayer{"pB", "BR", "release"}
	pC := opsPlayer{"pC", "DE", "release"}
	pD := opsPlayer{"pD", "DE", "dev"}
	num := func(kv ...interface{}) map[string]interface{} {
		m := map[string]interface{}{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	e.setNow(day(5, 23, 0, 0))
	e.post(t,
		e.ev("A1", pA, "sA1", "apm_session_start", day(5, 10, 0, 0), nil, nil),
		e.ev("A2", pA, "sA1", "run_start", day(5, 10, 1, 0), nil, nil),
		e.ev("A3", pA, "sA1", "run_end", day(5, 10, 20, 0), nil, nil),
		e.ev("A4", pA, "sA1", "apm_session_end", day(5, 10, 30, 0), num("foreground_ms", 1800000), nil),
		e.ev("B1", pB, "sB1", "apm_session_start", day(5, 12, 0, 0), nil, nil),
		e.ev("B2", pB, "sB1", "run_start", day(5, 12, 5, 0), nil, nil),
		e.ev("B3", pB, "sB1", "ad_show", day(5, 12, 10, 0), num("placement", "revive", "network", "AppLovin", "revenue", 0.01, "currency", "eur"), nil),
		e.ev("B4", pB, "sB1", "ad_show", day(5, 12, 20, 0), num("placement", "shop", "network", "applovin", "revenue", 0.03, "currency", "EUR"), nil),
		e.ev("B5", pB, "sB1", "apm_session_end", day(5, 12, 30, 0), num("foreground_ms", 2400000), nil),
	)
	e.setNow(day(4, 23, 0, 0))
	e.post(t,
		e.ev("A5", pA, "sA2", "apm_session_start", day(4, 9, 0, 0), nil, nil),
		e.ev("A6", pA, "sA2", "ad_entry_show", day(4, 9, 5, 0), num("placement", "revive"), nil),
		e.ev("A7", pA, "sA2", "ad_click", day(4, 9, 5, 10), num("placement", "revive"), nil),
		e.ev("A8", pA, "sA2", "ad_request", day(4, 9, 5, 15), num("placement", "revive", "network", "admob"), nil),
		e.ev("A9", pA, "sA2", "ad_show", day(4, 9, 5, 20), num("placement", "revive", "network", "admob", "revenue", 0.02, "currency", "USD"), nil),
		e.ev("A10", pA, "sA2", "ad_complete", day(4, 9, 5, 50), num("placement", "revive", "network", "admob"), nil),
		e.ev("A11", pA, "sA2", "ad_reward", day(4, 9, 5, 51), num("placement", "revive", "network", "admob"), nil),
		e.ev("C1", pC, "sC1", "apm_session_start", day(4, 15, 0, 0), nil, nil),
		e.ev("C2", pC, "sC1", "ad_show", day(4, 15, 1, 0), num("placement", "revive", "network", "admob", "revenue", 5, "currency", "JPY"), nil),
		e.ev("C3", pC, "sC1", "ad_entry_show", day(4, 15, 2, 0), num("placement", "shop"), nil),
		e.ev("D1", pD, "sD1", "apm_session_start", day(4, 16, 0, 0), nil, nil),
		e.ev("D2", pD, "sD1", "ad_show", day(4, 16, 1, 0), num("placement", "revive", "network", "admob", "revenue", 1, "currency", "USD"), nil),
	)
	// Rates: EUR on day 6 (applies to days 5 and 4) and day 3 (later); no JPY.
	e.importCSV(t, "fx-rates", "?source=rates.csv", "date,currency,usd_per_unit\n"+dayStr(6)+",EUR,1.10\n"+dayStr(3)+",EUR,1.20\n")
	// Network reports: applovin's matches within 10 %, admob's does not,
	// unity's has no tracked counterpart.
	e.importCSV(t, "ad-report", "", "date,network,country,placement,impressions,revenue,currency\n"+
		dayStr(5)+",AppLovin,BR,revive,1,0.012,USD\n"+
		dayStr(5)+",applovin,br,shop,1,0.036,USD\n"+
		dayStr(4)+",admob,DE,revive,2,0.05,EUR\n"+
		dayStr(3)+",unity,US,revive,1,0.01,USD\n")
}

// Every preset question on known data, as Metabase runs it (defaults:
// 30 days, release builds).
func TestAPM12_PresetCards(t *testing.T) {
	e := setupAPM12(t, false)
	apm12Dataset(t, e)
	conn := e.client.Conn()
	d5, d4 := dayStr(5), dayStr(4)

	np := cardRows(t, conn, "new_players", nil)
	expectRow(t, "new_players", np, map[string]string{"day": d5}, map[string]string{"new_players": "2"})
	expectRow(t, "new_players", np, map[string]string{"day": d4}, map[string]string{"new_players": "1"})
	if len(np) != 2 {
		t.Errorf("new_players %v (the dev build's player must not count)", np)
	}
	if all := cardRows(t, conn, "new_players", map[string]string{"build": "all"}); len(pick(all, map[string]string{"day": d4, "new_players": "2"})) != 1 {
		t.Errorf("new_players, all builds: %v", all)
	}

	dau := cardRows(t, conn, "dau", nil)
	expectRow(t, "dau", dau, map[string]string{"day": d5}, map[string]string{"dau": "2"})
	expectRow(t, "dau", dau, map[string]string{"day": d4}, map[string]string{"dau": "2"})

	ret := cardRows(t, conn, "retention", nil)
	expectRow(t, "retention", ret, map[string]string{"cohort_day": d5}, map[string]string{"new_players": "2", "d1_pct": "50", "d7_pct": "NULL"})
	expectRow(t, "retention", ret, map[string]string{"cohort_day": d4}, map[string]string{"new_players": "1", "d1_pct": "0", "d7_pct": "NULL"})

	ses := cardRows(t, conn, "sessions", nil)
	expectRow(t, "sessions", ses, map[string]string{"day": d5}, map[string]string{"sessions": "2", "players": "2", "sessions_per_player": "1", "avg_session_s": "2100"})
	expectRow(t, "sessions", ses, map[string]string{"day": d4}, map[string]string{"sessions": "2", "players": "2", "avg_session_s": "235.5"})
	if m := pick(ses, map[string]string{"day": d5}); len(m) == 1 && m[0]["median_session_s"] != "1800" && m[0]["median_session_s"] != "2400" {
		t.Errorf("median %v", m[0])
	}

	evs := cardRows(t, conn, "events", nil)
	expectRow(t, "events", evs, map[string]string{"day": d5, "name": "ad_show"}, map[string]string{"events": "2", "players": "1"})
	expectRow(t, "events", evs, map[string]string{"day": d4, "name": "ad_entry_show"}, map[string]string{"events": "2", "players": "2"})
	expectRow(t, "events", evs, map[string]string{"day": d4, "name": "ad_show"}, map[string]string{"events": "2", "players": "2"})

	fun := cardRows(t, conn, "funnel", nil)
	if len(fun) != 3 {
		t.Errorf("funnel %v (empty steps left out)", fun)
	}
	expectRow(t, "funnel", fun, map[string]string{"step": "1"}, map[string]string{"event": "apm_session_start", "players": "3", "pct_of_first": "100"})
	expectRow(t, "funnel", fun, map[string]string{"step": "2"}, map[string]string{"event": "run_start", "players": "2", "pct_of_first": "66.7"})
	expectRow(t, "funnel", fun, map[string]string{"step": "3"}, map[string]string{"event": "run_end", "players": "1", "pct_of_first": "33.3"})
	custom := cardRows(t, conn, "funnel", map[string]string{"step1": "ad_entry_show", "step2": "ad_show", "step3": "", "window_s": "3600"})
	expectRow(t, "funnel", custom, map[string]string{"step": "1"}, map[string]string{"players": "2"})
	expectRow(t, "funnel", custom, map[string]string{"step": "2"}, map[string]string{"players": "1"})

	imp := cardRows(t, conn, "ad_impressions_per_dau", nil)
	expectRow(t, "ad_impressions_per_dau", imp, map[string]string{"day": d5}, map[string]string{"dau": "2", "impressions": "2", "impressions_per_dau": "1", "viewers": "1", "viewer_pct": "50"})
	expectRow(t, "ad_impressions_per_dau", imp, map[string]string{"day": d4}, map[string]string{"dau": "2", "impressions": "2", "impressions_per_dau": "1", "viewers": "2", "viewer_pct": "100"})

	rev := cardRows(t, conn, "ad_revenue", nil)
	expectRow(t, "ad_revenue", rev, map[string]string{"day": d5, "network": "applovin"}, map[string]string{"revenue_usd": "0.044", "impressions": "2", "impressions_without_rate": "0"})
	expectRow(t, "ad_revenue", rev, map[string]string{"day": d4, "network": "admob"}, map[string]string{"revenue_usd": "0.02", "impressions": "2", "impressions_without_rate": "1"})
	if len(rev) != 2 {
		t.Errorf("ad_revenue %v", rev)
	}

	ac := cardRows(t, conn, "arpdau_country", nil)
	expectRow(t, "arpdau_country", ac, map[string]string{"day": d5, "country": "BR"}, map[string]string{"dau": "1", "revenue_usd": "0.044", "arpdau_usd": "0.044"})
	expectRow(t, "arpdau_country", ac, map[string]string{"day": d5, "country": "DE"}, map[string]string{"dau": "1", "revenue_usd": "0"})
	expectRow(t, "arpdau_country", ac, map[string]string{"day": d4, "country": "DE"}, map[string]string{"dau": "2", "revenue_usd": "0.02", "arpdau_usd": "0.01"})

	ap := cardRows(t, conn, "arpdau_placement", nil)
	expectRow(t, "arpdau_placement", ap, map[string]string{"day": d5, "placement": "revive"}, map[string]string{"impressions": "1", "revenue_usd": "0.011", "dau": "2", "arpdau_usd": "0.0055"})
	expectRow(t, "arpdau_placement", ap, map[string]string{"day": d5, "placement": "shop"}, map[string]string{"impressions": "1", "revenue_usd": "0.033", "arpdau_usd": "0.0165"})
	expectRow(t, "arpdau_placement", ap, map[string]string{"day": d4, "placement": "revive"}, map[string]string{"impressions": "2", "revenue_usd": "0.02", "arpdau_usd": "0.01"})

	ae := cardRows(t, conn, "ad_events_placement", nil)
	expectRow(t, "ad_events_placement", ae, map[string]string{"placement": "revive", "name": "ad_show"}, map[string]string{"events": "3", "players": "3"})
	expectRow(t, "ad_events_placement", ae, map[string]string{"placement": "shop", "name": "ad_entry_show"}, map[string]string{"events": "1"})
	expectRow(t, "ad_events_placement", ae, map[string]string{"placement": "revive", "name": "ad_reward"}, map[string]string{"events": "1"})

	af := cardRows(t, conn, "ad_funnel_placement", nil)
	for step, want := range []string{"1", "1", "1", "1", "1"} {
		expectRow(t, "ad_funnel_placement", af, map[string]string{"placement": "revive", "step": fmt.Sprint(step + 1)}, map[string]string{"players": want, "pct_of_first": "100"})
	}
	expectRow(t, "ad_funnel_placement", af, map[string]string{"placement": "shop", "step": "1"}, map[string]string{"event": "ad_entry_show", "players": "1"})
	expectRow(t, "ad_funnel_placement", af, map[string]string{"placement": "shop", "step": "3"}, map[string]string{"event": "ad_show", "players": "0", "pct_of_first": "0"})

	ar := cardRows(t, conn, "ad_retention", nil)
	expectRow(t, "ad_retention", ar, map[string]string{"bucket": "0"}, map[string]string{"new_players": "1", "d1_pct": "100", "d7_pct": "NULL"})
	expectRow(t, "ad_retention", ar, map[string]string{"bucket": "1-2"}, map[string]string{"new_players": "2", "d1_pct": "0", "d7_pct": "NULL"})

	rc := cardRows(t, conn, "ad_reconciliation", nil)
	expectRow(t, "ad_reconciliation", rc, map[string]string{"day": d5, "network": "applovin", "country": "BR"},
		map[string]string{"tracked_impressions": "2", "reported_impressions": "2", "tracked_usd": "0.044", "reported_usd": "0.048", "diff_pct": "-8.3", "over_threshold": "0"})
	expectRow(t, "ad_reconciliation", rc, map[string]string{"day": d4, "network": "admob", "country": "DE"},
		map[string]string{"tracked_usd": "0.02", "reported_usd": "0.055", "over_threshold": "1"})
	expectRow(t, "ad_reconciliation", rc, map[string]string{"day": dayStr(3), "network": "unity", "country": "US"},
		map[string]string{"tracked_usd": "0", "reported_usd": "0.01", "diff_pct": "-100", "over_threshold": "1"})
	strict := cardRows(t, conn, "ad_reconciliation", map[string]string{"threshold_pct": "5"})
	expectRow(t, "ad_reconciliation", strict, map[string]string{"day": d5, "network": "applovin"}, map[string]string{"over_threshold": "1"})

	// Re-importing a report replaces its rows (latest import wins).
	time.Sleep(1100 * time.Millisecond) // imported_at has second precision
	e.importCSV(t, "ad-report", "", "date,network,country,placement,impressions,revenue,currency\n"+dayStr(5)+",applovin,BR,revive,1,0.008,USD\n")
	rc = cardRows(t, conn, "ad_reconciliation", nil)
	expectRow(t, "ad_reconciliation", rc, map[string]string{"day": d5, "network": "applovin", "country": "BR"}, map[string]string{"reported_usd": "0.044", "over_threshold": "0"})
}

// Deletion by player id (the SDK's GetPlayerId) removes the player from
// every ops table and nothing else.
func TestAPM12_PrivacyDeletionByPlayerID(t *testing.T) {
	e := setupAPM12(t, false)
	apm12Dataset(t, e)
	ctx := context.Background()
	seedDevice(t, e.client, "dev-x", 1)
	req := httptest.NewRequest(http.MethodPost, "/api/privacy/delete", strings.NewReader(`{"id_type":"player_id","id":"pA"}`))
	req.Header.Set("Authorization", "Bearer "+privacyTestToken)
	req.Header.Set(adminapi.ConfirmDeleteHeader, "pA")
	w := httptest.NewRecorder()
	e.admin.ServeHTTP(w, req)
	var d models.PrivacyDeletion
	decodeInto(t, w.Body.Bytes(), &d)
	if w.Code != http.StatusOK || d.Status != "done" || d.IDType != "player_id" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var tables []string
	for _, r := range d.Tables {
		tables = append(tables, r.Table)
	}
	sort.Strings(tables)
	if strings.Join(tables, ",") != "ops_events,ops_player_days,ops_players" {
		t.Errorf("tables %v", tables)
	}
	for _, tb := range []string{"ops_events", "ops_player_days", "ops_players"} {
		if n := queryUint(t, e.client, "SELECT count() FROM "+tb+" FINAL WHERE player_id = 'pA'"); n != 0 {
			t.Errorf("%s still has %d rows of pA", tb, n)
		}
		if n := queryUint(t, e.client, "SELECT count() FROM "+tb+" FINAL WHERE player_id = 'pB'"); n == 0 {
			t.Errorf("%s lost pB", tb)
		}
	}
	if n := queryUint(t, e.client, "SELECT count() FROM apm_perf_samples WHERE device_id = 'dev-x'"); n == 0 {
		t.Error("technical rows touched")
	}
	_ = ctx
}

// The BI account of deployments/clickhouse/users.d/ozx_ops_readonly.xml (as
// SQL: same grants and read-only profile): every preset question runs, a
// technical table and any write are refused.
func TestAPM12_ReadonlyBIAccount(t *testing.T) {
	e := setupAPM12(t, false)
	apm12Dataset(t, e)
	admin := adminClient(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano() % 1e9)
	user, profile := "apm12_ro_"+suffix, "apm12_ro_profile_"+suffix
	if err := admin.Exec(ctx, "CREATE SETTINGS PROFILE "+profile+" SETTINGS readonly = 1, max_execution_time = 60, max_memory_usage = 2000000000, max_rows_to_read = 2000000000"); err != nil {
		t.Skipf("cannot create a settings profile on this ClickHouse (access management off?): %v", err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP SETTINGS PROFILE IF EXISTS "+profile) })
	must(t, admin.Exec(ctx, "CREATE USER "+user+" IDENTIFIED WITH no_password SETTINGS PROFILE '"+profile+"'"))
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP USER IF EXISTS "+user) })
	for _, tb := range storage.OpsTableNames() {
		must(t, admin.Exec(ctx, "GRANT SELECT ON "+e.db+"."+tb+" TO "+user))
	}
	cfg := testutil.TestClickHouseConfig()
	// No session settings: a read-only (1) account may not change any.
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)},
		Auth: clickhouse.Auth{Database: e.db, Username: user}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, c := range opsbi.Cards {
		cardRows(t, conn, c.Key, nil)
	}
	if err := conn.Exec(ctx, "SELECT count() FROM apm_custom_events"); err == nil {
		t.Error("the BI account read a technical table")
	}
	if err := conn.Exec(ctx, "INSERT INTO ops_fx_rates (day, currency, usd_per_unit, source, imported_at) VALUES ('2026-01-01', 'EUR', 1, 'x', now())"); err == nil {
		t.Error("the BI account wrote")
	}
	if err := conn.Exec(ctx, "ALTER TABLE ops_events DELETE WHERE 1"); err == nil {
		t.Error("the BI account deleted")
	}
}

// As a copy of the production service account (SELECT, INSERT, CREATE
// TABLE, ALTER ADD COLUMN, ALTER DELETE): Migrate creates the ops tables
// without a warning, and ops events are stored.
func TestAPM12_ServiceAccountCreatesTheOpsTables(t *testing.T) {
	admin := adminClient(t)
	ctx := context.Background()
	user := fmt.Sprintf("apm12_svc_%d", time.Now().UnixNano()%1e9)
	if err := admin.Exec(ctx, "CREATE USER "+user+" IDENTIFIED WITH no_password"); err != nil {
		t.Skipf("cannot create a user on this ClickHouse (access management off?): %v", err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP USER IF EXISTS "+user) })
	db := schemaTestDB(t, "apm12_svc")
	createDB(t, admin, db)
	must(t, admin.Exec(ctx, "GRANT SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN, ALTER DELETE ON "+db+".* TO "+user))
	svc, logs := connect(t, db, user)
	must(t, svc.Migrate(ctx))
	if got := warnings(logs, "could not create an ops table"); len(got) != 0 {
		t.Fatalf("ops table warnings %v", got)
	}
	for _, tb := range storage.OpsTableNames() {
		if n := queryUint(t, svc, "SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?", tb); n != 1 {
			t.Errorf("%s not created", tb)
		}
	}
	must(t, svc.Migrate(ctx)) // idempotent
	router := api.NewRouter(&config.Config{}, storage.NewRepository(svc, zap.NewNop()), zap.NewNop())
	e := &apm12Env{}
	resp := postIngest(t, router, e.ev("svc-1", opsPlayer{"p-svc", "US", "release"}, "s", "apm_session_start", time.Now().Add(-time.Minute), nil, nil))
	if !resp.Stored || resp.Accepted != 1 {
		t.Fatalf("%+v", resp)
	}
	if n := queryUint(t, svc, "SELECT count() FROM ops_players WHERE player_id = 'p-svc'"); n != 1 {
		t.Errorf("ops_players %d", n)
	}
}

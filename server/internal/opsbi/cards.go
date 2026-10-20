// Package opsbi defines the preset BI questions and dashboards of the
// operations data (APM-12): the SQL of every card, its variables, and how the
// cards are laid out on the dashboards. cmd/metabase-provision creates them
// in Metabase (native SQL questions on the ClickHouse connection);
// tests/integration runs the same SQL on ClickHouse 23.8, 24.1 and 26.1.
//
// The SQL is Metabase native SQL: {{name}} is a variable (a template tag) of
// the card, substituted by Metabase (a number as is, a text as a quoted
// string literal). Render does the same for tests and for ad-hoc use.
//
// Conventions of every card:
//   - days are UTC days (ops_events.day, ops_player_days.day); "today" is
//     toDate(now('UTC')), so a server in another time zone shows the same;
//   - {{days}}: how many days back (default 30); {{build}}: "release"
//     (default), "dev", "editor" or "all";
//   - ops_events is read with FINAL (one row per event id); the derived
//     tables are counted with uniqExact / GROUP BY player_id, which a
//     duplicate row cannot change;
//   - revenue is converted to US dollars with the latest imported rate on
//     or before the impression's day (ops_fx_rates, ASOF JOIN); revenue in
//     USD (or without a currency) needs no rate; impressions in a currency
//     without any rate count 0 USD and are shown as "without rate".
package opsbi

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// VarType is a card variable's type.
type VarType string

const (
	VarNumber VarType = "number"
	VarText   VarType = "text"
)

// Var is a variable of a card (a Metabase template tag).
type Var struct {
	Name    string
	Label   string
	Type    VarType
	Default string
}

// Card is one preset question.
type Card struct {
	// Key is a stable identifier (the Metabase question is found by Name,
	// which is "OZX Ops: " + Title).
	Key         string
	Title       string
	Description string
	// Display: "line", "bar", "table", "row".
	Display string
	// Dimensions and Metrics are the chart's columns (visualization).
	Dimensions []string
	Metrics    []string
	SQL        string
	Vars       []Var
}

// Name is the Metabase question name.
func (c Card) Name() string { return "OZX Ops: " + c.Title }

// Common variables.
var (
	varDays      = Var{"days", "Days back", VarNumber, "30"}
	varBuild     = Var{"build", "Build (release, dev, editor, all)", VarText, "release"}
	varWindow    = Var{"window_s", "Funnel window (seconds)", VarNumber, "86400"}
	varAdWindow  = Var{"window_s", "Funnel window (seconds)", VarNumber, "3600"}
	varThreshold = Var{"threshold_pct", "Flag differences above (%)", VarNumber, "10"}
)

func stepVar(n int, def string) Var {
	return Var{"step" + strconv.Itoa(n), "Step " + strconv.Itoa(n) + " (event name; empty: none)", VarText, def}
}

// SQL fragments.
const (
	utcToday = "toDate(now('UTC'))"
	since    = "day >= " + utcToday + " - {{days}}"
	build    = "({{build}} = 'all' OR build_type = {{build}})"
)

// Ad event names (the SDK's ApmAds, README; OZX-1280).
const (
	AdEntryShow = "ad_entry_show"
	AdClick     = "ad_click"
	AdRequest   = "ad_request"
	AdShow      = "ad_show"
	AdComplete  = "ad_complete"
	AdReward    = "ad_reward"
	AdFail      = "ad_fail"
)

// The SDK's automatic session events (ApmClient, README).
const (
	SessionStart = "apm_session_start"
	SessionEnd   = "apm_session_end"
)

// adRevenue is one row per ad impression (ad_show) in the range with its
// revenue in US dollars.
const adRevenue = `SELECT e.d AS day, e.pid AS player_id, e.cc AS country, e.pl AS placement,
        e.nw AS network, e.cur AS currency,
        e.rev * if(e.cur IN ('USD', ''), 1, r.rate) AS revenue_usd,
        e.cur NOT IN ('USD', '') AND r.rate = 0 AS missing_rate
    FROM (
        SELECT day AS d, player_id AS pid, toString(country) AS cc, props['placement'] AS pl, lower(props['network']) AS nw,
            num_props['revenue'] AS rev, upper(props['currency']) AS cur
        FROM ops_events FINAL
        WHERE name = '` + AdShow + `' AND ` + since + ` AND ` + build + `
    ) AS e
    ASOF LEFT JOIN (SELECT toString(currency) AS cur, day AS d, usd_per_unit AS rate FROM ops_fx_rates FINAL) AS r
    ON e.cur = r.cur AND e.d >= r.d`

// dau is the daily active players.
const dau = `SELECT day, uniqExact(player_id) AS dau
        FROM ops_player_days
        WHERE ` + since + ` AND ` + build + `
        GROUP BY day`

// dauCountry is the daily active players per country (as text, like the
// revenue rows it is joined with).
const dauCountry = `SELECT day, toString(country) AS cc, uniqExact(player_id) AS dau
        FROM ops_player_days
        WHERE ` + since + ` AND ` + build + `
        GROUP BY day, cc`

// cohort is the players first seen in the range (one row per player).
const cohort = `SELECT player_id, toDate(toTimeZone(min(first_seen), 'UTC')) AS cohort_day
            FROM ops_players
            GROUP BY player_id
            HAVING cohort_day >= ` + utcToday + ` - {{days}} AND ({{build}} = 'all' OR max(build_type) = {{build}})`

// activity is the (player, day) pairs of activity from the cohorts' start.
const activity = `SELECT DISTINCT player_id, day FROM ops_player_days WHERE ` + since

// funnel returns a funnel over the given step conditions (SQL expressions on
// ops_events): one row per step (and group) with the players who reached it,
// in order, each step within {{window_s}} seconds of the first. group is the
// result column's name and groupExpr its expression on ops_events ("" for
// none); a player is counted per group.
func funnel(group, groupExpr string, steps []string, labels []string, where string) string {
	counts := make([]string, len(steps))
	nums := make([]string, len(steps))
	for i := range steps {
		counts[i] = "countIf(level >= " + strconv.Itoa(i+1) + ")"
		nums[i] = strconv.Itoa(i + 1)
	}
	inner, innerGroup, mid, midGroup, outer, order := "", "", "", "", "", "step"
	if group != "" {
		inner = groupExpr + " AS " + group + ", "
		innerGroup = ", " + group
		mid = group + ", "
		midGroup = "\n    GROUP BY " + group
		outer = group + ", "
		order = group + ", step"
	}
	return `SELECT ` + outer + `step, event, players, round(100 * players / greatest(first_n, 1), 1) AS pct_of_first
FROM (
    SELECT ` + mid + `[` + strings.Join(nums, ", ") + `] AS steps,
        [` + strings.Join(labels, ", ") + `] AS events,
        [` + strings.Join(counts, ", ") + `] AS counts,
        countIf(level >= 1) AS first_n
    FROM (
        SELECT player_id, ` + inner + `windowFunnel({{window_s}})(toDateTime(event_time), ` + strings.Join(steps, ", ") + `) AS level
        FROM ops_events FINAL
        WHERE ` + since + ` AND ` + build + ` AND ` + where + `
        GROUP BY player_id` + innerGroup + `
    )` + midGroup + `
)
ARRAY JOIN steps AS step, events AS event, counts AS players
WHERE event != ''
ORDER BY ` + order
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "'" + n + "'"
	}
	return strings.Join(q, ", ")
}

var adFunnelSteps = []string{AdEntryShow, AdClick, AdShow, AdComplete, AdReward}

// Cards are the preset questions.
var Cards = []Card{
	{
		Key: "new_players", Title: "New players per day", Display: "bar",
		Description: "Players whose first ops event (of any kind; the SDK sends apm_session_start at every launch) was on the day (UTC). A reinstall is a new player.",
		Dimensions:  []string{"day"}, Metrics: []string{"new_players"},
		SQL: `SELECT cohort_day AS day, count() AS new_players
FROM (
    ` + cohort + `
)
GROUP BY day
ORDER BY day`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "dau", Title: "Daily active players", Display: "line",
		Description: "Players with at least one ops event on the day (UTC).",
		Dimensions:  []string{"day"}, Metrics: []string{"dau"},
		SQL: dau + `
ORDER BY day`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "retention", Title: "Retention D1 / D7 / D30 by first day", Display: "table",
		Description: "Of the players first seen on a day, the share active again exactly 1, 7 and 30 days later (empty until that day has come).",
		SQL: `SELECT cday AS cohort_day,
    count() AS new_players,
    if(cday + 1 <= ` + utcToday + `, round(100 * countIf(d1) / count(), 1), NULL) AS d1_pct,
    if(cday + 7 <= ` + utcToday + `, round(100 * countIf(d7) / count(), 1), NULL) AS d7_pct,
    if(cday + 30 <= ` + utcToday + `, round(100 * countIf(d30) / count(), 1), NULL) AS d30_pct
FROM (
    SELECT c.player_id AS pid, c.cohort_day AS cday,
        max(a.day = c.cohort_day + 1) AS d1,
        max(a.day = c.cohort_day + 7) AS d7,
        max(a.day = c.cohort_day + 30) AS d30
    FROM (
        ` + cohort + `
    ) AS c
    LEFT JOIN (` + activity + `) AS a ON a.player_id = c.player_id
    GROUP BY c.player_id, c.cohort_day
)
GROUP BY cday
ORDER BY cday`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "sessions", Title: "Sessions per player and session length", Display: "table",
		Description: "Sessions of the SDK (a launch, or a return after more than 30 s in the background). Length: the foreground time apm_session_end reports, or the time between the session's first and last ops event if the app was killed before it could send one.",
		SQL: `SELECT first_day AS day,
    count() AS sessions,
    uniqExact(pid) AS players,
    round(count() / uniqExact(pid), 2) AS sessions_per_player,
    round(avg(length_s), 1) AS avg_session_s,
    round(quantileExact(0.5)(length_s), 1) AS median_session_s
FROM (
    SELECT session_id,
        any(player_id) AS pid,
        min(day) AS first_day,
        greatest(maxIf(num_props['foreground_ms'], name = '` + SessionEnd + `') / 1000,
            (toUnixTimestamp64Milli(max(event_time)) - toUnixTimestamp64Milli(min(event_time))) / 1000) AS length_s
    FROM ops_events FINAL
    WHERE day >= ` + utcToday + ` - {{days}} - 1 AND session_id != '' AND ` + build + `
    GROUP BY session_id
)
WHERE first_day >= ` + utcToday + ` - {{days}}
GROUP BY first_day
ORDER BY first_day`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "events", Title: "Ops events per day", Display: "table",
		Description: "Every ops event name per day: events and players.",
		SQL: `SELECT day, name, count() AS events, uniqExact(player_id) AS players
FROM ops_events FINAL
WHERE ` + since + ` AND ` + build + `
GROUP BY day, name
ORDER BY day, events DESC`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "funnel", Title: "Custom funnel", Display: "bar",
		Description: "Players who did the steps in order (event names; up to five, an empty step is left out), each within the window of the first step.",
		Dimensions:  []string{"event"}, Metrics: []string{"players"},
		SQL: funnel("", "", []string{"name = {{step1}}", "name = {{step2}}", "name = {{step3}}", "name = {{step4}}", "name = {{step5}}"},
			[]string{"{{step1}}", "{{step2}}", "{{step3}}", "{{step4}}", "{{step5}}"},
			"name IN ({{step1}}, {{step2}}, {{step3}}, {{step4}}, {{step5}})"),
		Vars: []Var{varDays, varBuild, stepVar(1, "apm_session_start"), stepVar(2, "run_start"), stepVar(3, "run_end"), stepVar(4, ""), stepVar(5, ""), varWindow},
	},
	{
		Key: "ad_impressions_per_dau", Title: "Ad impressions per DAU", Display: "line",
		Description: "Ad impressions (ad_show) per daily active player, and the share of players who saw an ad.",
		Dimensions:  []string{"day"}, Metrics: []string{"impressions_per_dau"},
		SQL: `SELECT d.day AS day, d.dau AS dau, a.imps AS impressions,
    round(a.imps / d.dau, 3) AS impressions_per_dau,
    a.viewing AS viewers, round(100 * a.viewing / d.dau, 1) AS viewer_pct
FROM (` + dau + `) AS d
LEFT JOIN (
    SELECT day, count() AS imps, uniqExact(player_id) AS viewing
    FROM ops_events FINAL
    WHERE name = '` + AdShow + `' AND ` + since + ` AND ` + build + `
    GROUP BY day
) AS a ON a.day = d.day
ORDER BY day`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "ad_revenue", Title: "Ad revenue per day and network (USD, estimated)", Display: "bar",
		Description: "The revenue the ad SDKs estimated per impression, in US dollars (imported daily rates). impressions_without_rate: impressions in a currency with no imported rate (counted as 0).",
		Dimensions:  []string{"day", "network"}, Metrics: []string{"revenue_usd"},
		SQL: `SELECT day, network, round(sum(revenue_usd), 4) AS revenue_usd, count() AS impressions,
    countIf(missing_rate) AS impressions_without_rate
FROM (
    ` + adRevenue + `
)
GROUP BY day, network
ORDER BY day, network`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "arpdau_country", Title: "Ad ARPDAU by country (USD)", Display: "table",
		Description: "Estimated ad revenue per daily active player, per country (coarse country of the player: CDN header or device region).",
		SQL: `SELECT d.day AS day, d.cc AS country, d.dau AS dau,
    round(r.rev, 4) AS revenue_usd,
    round(r.rev / d.dau, 5) AS arpdau_usd
FROM (` + dauCountry + `) AS d
LEFT JOIN (
    SELECT day, country, sum(revenue_usd) AS rev
    FROM (
        ` + adRevenue + `
    )
    GROUP BY day, country
) AS r ON r.day = d.day AND r.country = d.cc
ORDER BY day, revenue_usd DESC, country`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "arpdau_placement", Title: "Ad ARPDAU by placement (USD)", Display: "table",
		Description: "Estimated ad revenue of each placement per daily active player (all players of the day).",
		SQL: `SELECT r.day AS day, r.pl AS placement, r.imps AS impressions,
    round(r.rev, 4) AS revenue_usd, d.dau AS dau,
    if(d.dau = 0, NULL, round(r.rev / d.dau, 5)) AS arpdau_usd
FROM (
    SELECT day, placement AS pl, sum(revenue_usd) AS rev, count() AS imps
    FROM (
        ` + adRevenue + `
    )
    GROUP BY day, pl
) AS r
LEFT JOIN (` + dau + `) AS d ON d.day = r.day
ORDER BY day, placement`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "ad_events_placement", Title: "Ad events by placement", Display: "table",
		Description: "Every ad event per placement: events and players (no order required; see the funnel for the order).",
		SQL: `SELECT props['placement'] AS placement, name, count() AS events, uniqExact(player_id) AS players
FROM ops_events FINAL
WHERE name IN (` + quoteList([]string{AdEntryShow, AdClick, AdRequest, AdShow, AdComplete, AdReward, AdFail}) + `) AND ` + since + ` AND ` + build + `
GROUP BY placement, name
ORDER BY placement, events DESC`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "ad_funnel_placement", Title: "Ad funnel by placement", Display: "table",
		Description: "Per placement, players who went entry shown -> clicked -> ad shown -> completed -> rewarded in order, within the window.",
		SQL: funnel("placement", "props['placement']", []string{"name = '" + AdEntryShow + "'", "name = '" + AdClick + "'", "name = '" + AdShow + "'", "name = '" + AdComplete + "'", "name = '" + AdReward + "'"},
			strings.Split(quoteList(adFunnelSteps), ", "),
			"name IN ("+quoteList(adFunnelSteps)+") AND props['placement'] != ''"),
		Vars: []Var{varDays, varBuild, varAdWindow},
	},
	{
		Key: "ad_retention", Title: "Retention by ad views on the first day", Display: "table",
		Description: "New players grouped by the ads they saw on their first day (0, 1-2, 3-5, 6+): D1 and D7 retention of each group (of the players whose day 1 / day 7 has come).",
		SQL: `SELECT bucket,
    count() AS new_players,
    round(100 * countIf(d1) / nullIf(countIf(cday + 1 <= ` + utcToday + `), 0), 1) AS d1_pct,
    round(100 * countIf(d7) / nullIf(countIf(cday + 7 <= ` + utcToday + `), 0), 1) AS d7_pct
FROM (
    SELECT c.pid AS pid, c.cday AS cday,
        multiIf(c.views = 0, '0', c.views <= 2, '1-2', c.views <= 5, '3-5', '6+') AS bucket,
        max(a.day = c.cday + 1) AS d1,
        max(a.day = c.cday + 7) AS d7
    FROM (
        SELECT p.player_id AS pid, p.cohort_day AS cday, v.n AS views
        FROM (
            ` + cohort + `
        ) AS p
        LEFT JOIN (
            SELECT player_id, day, count() AS n
            FROM ops_events FINAL
            WHERE name = '` + AdShow + `' AND ` + since + `
            GROUP BY player_id, day
        ) AS v ON v.player_id = p.player_id AND v.day = p.cohort_day
    ) AS c
    LEFT JOIN (` + activity + `) AS a ON a.player_id = c.pid
    GROUP BY c.pid, c.cday, c.views
)
GROUP BY bucket
ORDER BY indexOf(['0', '1-2', '3-5', '6+'], bucket)`,
		Vars: []Var{varDays, varBuild},
	},
	{
		Key: "ad_reconciliation", Title: "Ad revenue: tracked vs network reports", Display: "table",
		Description: "Per day, network and country: the revenue the SDK tracked (estimated, USD) against the network's own report (imported, USD). over_threshold = 1 where they differ by more than the threshold, or only one side has revenue.",
		SQL: `SELECT day, network, country,
    t_imps AS tracked_impressions, r_imps AS reported_impressions,
    round(t_usd, 4) AS tracked_usd, round(r_usd, 4) AS reported_usd,
    if(r_usd = 0, NULL, round(100 * (t_usd - r_usd) / r_usd, 1)) AS diff_pct,
    multiIf(t_usd = 0 AND r_usd = 0, 0,
        t_usd = 0 OR r_usd = 0, 1,
        abs(t_usd - r_usd) * 100 > {{threshold_pct}} * r_usd, 1, 0) AS over_threshold
FROM (
    SELECT day, network, country, count() AS t_imps, sum(revenue_usd) AS t_usd
    FROM (
        ` + adRevenue + `
    )
    GROUP BY day, network, country
) AS t
FULL OUTER JOIN (
    SELECT rep.d AS day, rep.nw AS network, rep.cc AS country,
        sum(rep.imps) AS r_imps,
        sum(rep.rev * if(rep.cur = 'USD', 1, fx.rate)) AS r_usd
    FROM (
        SELECT day AS d, toString(network) AS nw, toString(country) AS cc, impressions AS imps, revenue AS rev, toString(currency) AS cur
        FROM ops_ad_reports FINAL WHERE ` + since + `
    ) AS rep
    ASOF LEFT JOIN (SELECT toString(currency) AS cur, day AS d, usd_per_unit AS rate FROM ops_fx_rates FINAL) AS fx
    ON rep.cur = fx.cur AND rep.d >= fx.d
    GROUP BY day, network, country
) AS r USING (day, network, country)
ORDER BY over_threshold DESC, day DESC, network, country`,
		Vars: []Var{varDays, varBuild, varThreshold},
	},
}

// Dashboard is a preset dashboard: its cards in order, laid out two per row.
type Dashboard struct {
	Key         string
	Title       string
	Description string
	Cards       []string // Card keys
}

// Name is the Metabase dashboard name.
func (d Dashboard) Name() string { return "OZX Ops: " + d.Title }

// Dashboards are the preset dashboards.
var Dashboards = []Dashboard{
	{Key: "players", Title: "Players", Description: "New players, DAU, retention, sessions (ops events; UTC days).",
		Cards: []string{"new_players", "dau", "retention", "sessions", "events"}},
	{Key: "funnels", Title: "Funnels", Description: "A funnel of up to five event names (set the steps in the filters).",
		Cards: []string{"funnel"}},
	{Key: "ads", Title: "Ads", Description: "Rewarded ads: impressions per DAU, estimated revenue in USD, ARPDAU by country and placement, funnels, retention by ad views, reconciliation with the networks' reports.",
		Cards: []string{"ad_impressions_per_dau", "ad_revenue", "arpdau_country", "arpdau_placement", "ad_events_placement", "ad_funnel_placement", "ad_retention", "ad_reconciliation"}},
}

// CardByKey returns the card with the key.
func CardByKey(key string) (Card, bool) {
	for _, c := range Cards {
		if c.Key == key {
			return c, true
		}
	}
	return Card{}, false
}

// VarsOf returns the variables of the cards of a dashboard (each name once,
// first definition wins): the dashboard's filters.
func (d Dashboard) VarsOf() []Var {
	seen := map[string]bool{}
	var out []Var
	for _, k := range d.Cards {
		c, _ := CardByKey(k)
		for _, v := range c.Vars {
			if !seen[v.Name] {
				seen[v.Name] = true
				out = append(out, v)
			}
		}
	}
	return out
}

var tagRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// Tags returns the variable names used in the card's SQL, sorted.
func (c Card) Tags() []string {
	seen := map[string]bool{}
	for _, m := range tagRE.FindAllStringSubmatch(c.SQL, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Render substitutes the card's variables as Metabase does: values from
// the map, or the defaults. A number must parse as one; a text becomes a
// quoted SQL string literal.
func (c Card) Render(values map[string]string) (string, error) {
	vars := map[string]Var{}
	for _, v := range c.Vars {
		vars[v.Name] = v
	}
	var err error
	out := tagRE.ReplaceAllStringFunc(c.SQL, func(m string) string {
		name := tagRE.FindStringSubmatch(m)[1]
		v, ok := vars[name]
		if !ok {
			err = fmt.Errorf("card %s: variable %s is not declared", c.Key, name)
			return m
		}
		val, set := values[name]
		if !set {
			val = v.Default
		}
		if v.Type == VarNumber {
			if _, perr := strconv.ParseFloat(val, 64); perr != nil {
				err = fmt.Errorf("card %s: %s must be a number, got %q", c.Key, name, val)
				return m
			}
			return val
		}
		return "'" + strings.ReplaceAll(strings.ReplaceAll(val, `\`, `\\`), "'", `\'`) + "'"
	})
	return out, err
}

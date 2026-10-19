package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-9: crash-free sessions and users, ANR and exception rates (session
// health), and funnels over custom events.

// DefaultHealthCrashTypes are the crash types that make a session "crashed"
// for the crash-free rates: the process died in the foreground (a native or
// Java crash, a low-memory kill, another foreground kill). ANRs are counted
// separately (Play Console's ANR rate); "unknown" (a foreground process that
// ended without an exit record, Android 10 and older) is not counted unless
// asked for: it may be a kill the player caused.
var DefaultHealthCrashTypes = []string{fingerprint.CrashTypeNative, fingerprint.CrashTypeJava, fingerprint.CrashTypeOOM, fingerprint.CrashTypeKilled}

// healthCrashTypes are the crash types counted per type in CrashesByType.
var healthCrashTypes = []string{fingerprint.CrashTypeNative, fingerprint.CrashTypeJava, fingerprint.CrashTypeOOM,
	fingerprint.CrashTypeKilled, fingerprint.CrashTypeANR, fingerprint.CrashTypeUnknown}

// healthSessionTables are the tables whose rows say a session existed (every
// event table that has os_version; scene loads come with perf samples or a
// startup anyway). A session counts if any of its rows is in the range.
var healthSessionTables = []EventTable{TablePerfSamples, TableStartups, TableJanks, TableCustomEvents, TableCustomMetrics, TableRuns}

// sessionHealthQuery builds the query: per table, one row per (group,
// build type, session) with what the session had (crash flags, exception
// count, crashes per type), then the groups over the sessions.
func sessionHealthQuery(f models.AdminFilter, g CustomGroup, crashTypes []string) (string, []interface{}, error) {
	expr, exprArgs, err := groupValue(g)
	if err != nil {
		return "", nil, err
	}
	// Aliases never equal a column name (ClickHouse would substitute the
	// aggregate for the column inside it): bt, sid, dev, crashed, anr, exc,
	// t0..tN per table part; d, sc, sa, se, s0..sN per session.
	var zeroTypes, typeCols, sessionSums, groupSums strings.Builder
	for i, t := range healthCrashTypes {
		fmt.Fprintf(&zeroTypes, ", toUInt64(0) AS t%d", i)
		fmt.Fprintf(&typeCols, ", toUInt64(countIf(crash_type = '%s')) AS t%d", t, i) // constants, not input
		fmt.Fprintf(&sessionSums, ", sum(t%d) AS s%d", i, i)
		fmt.Fprintf(&groupSums, ", toInt64(sum(s%d))", i)
	}
	var parts []string
	var args []interface{}
	part := func(table, crashed, anr, exc, types string, extraArgs ...interface{}) {
		where, whereArgs := tableWhere(table, f)
		parts = append(parts, fmt.Sprintf(`SELECT %s AS value, build_type AS bt, session_id AS sid, any(device_id) AS dev,
				%s AS crashed, %s AS anr, %s AS exc%s
			FROM %s %s AND session_id != ''
			GROUP BY value, bt, sid`, expr, crashed, anr, exc, types, table, where))
		args = append(args, exprArgs...)
		args = append(args, extraArgs...)
		args = append(args, whereArgs...)
	}
	for _, t := range healthSessionTables {
		part(t.Name, "toUInt8(0)", "toUInt8(0)", "toUInt64(0)", zeroTypes.String())
	}
	part(TableExceptions.Name, "toUInt8(0)", "toUInt8(0)", "toUInt64(sum(count))", zeroTypes.String())
	// The crash types that count are bound; the per-type counts are constants.
	part(TableCrashes.Name, "toUInt8(countIf(crash_type IN ?) > 0)", "toUInt8(countIf(crash_type = 'anr') > 0)", "toUInt64(0)",
		typeCols.String(), groupSet(crashTypes))

	inner := strings.Join(parts, "\n\t\tUNION ALL\n\t\t")
	query := fmt.Sprintf(`SELECT value, bt,
			toInt64(uniqExact(sid)) AS sessions, toInt64(uniqExact(d)),
			toInt64(uniqExactIf(sid, sc = 1)), toInt64(uniqExactIf(d, sc = 1)),
			toInt64(uniqExactIf(sid, sa = 1)),
			toInt64(uniqExactIf(sid, se > 0)), toInt64(sum(se))%s
		FROM (
			SELECT value, bt, sid, any(dev) AS d, max(crashed) AS sc, max(anr) AS sa, sum(exc) AS se%s
			FROM (%s)
			GROUP BY value, bt, sid
		)
		GROUP BY value, bt
		ORDER BY sessions DESC, value, bt
		LIMIT %d`, groupSums.String(), sessionSums.String(), inner, analysisGroupRows+1)
	return query, args, nil
}

// GetSessionHealth returns, per group and build type, the sessions and
// users, the crash-free rates (crashTypes count as crashes), the ANR rate
// and the exception rates, most sessions first (at most 50 groups).
func (r *Repository) GetSessionHealth(ctx context.Context, f models.AdminFilter, g CustomGroup, crashTypes []string) (*models.SessionHealthResponse, error) {
	if len(crashTypes) == 0 {
		crashTypes = DefaultHealthCrashTypes
	}
	query, args, err := sessionHealthQuery(f, g, crashTypes)
	if err != nil {
		return nil, err
	}
	resp := &models.SessionHealthResponse{GroupBy: g.String(), CrashTypes: crashTypes, Rows: []models.SessionHealthRow{}}
	err = withAnalysisSlot(ctx, func() error {
		return r.queryRows(ctx, query, args, func(rows driver.Rows) error {
			var row models.SessionHealthRow
			types := make([]int64, len(healthCrashTypes))
			dest := []any{&row.Value, &row.BuildType, &row.Sessions, &row.Users, &row.CrashedSessions, &row.CrashedUsers,
				&row.ANRSessions, &row.ExceptionSessions, &row.Exceptions}
			for i := range types {
				dest = append(dest, &types[i])
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			if len(resp.Rows) == analysisGroupRows {
				resp.Truncated = true
				return nil
			}
			row.CrashesByType = make(map[string]int64, len(types))
			for i, t := range healthCrashTypes {
				row.CrashesByType[t] = types[i]
			}
			fillHealthRates(&row)
			resp.Rows = append(resp.Rows, row)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// fillHealthRates computes the rates of a row from its counts.
func fillHealthRates(row *models.SessionHealthRow) {
	if row.Sessions > 0 {
		s := float64(row.Sessions)
		row.CrashFreeSessions = 1 - float64(row.CrashedSessions)/s
		row.ANRRate = float64(row.ANRSessions) / s
		row.ExceptionSessionRate = float64(row.ExceptionSessions) / s
		row.ExceptionsPerSession = float64(row.Exceptions) / s
	}
	if row.Users > 0 {
		row.CrashFreeUsers = 1 - float64(row.CrashedUsers)/float64(row.Users)
	}
}

// Funnel limits.
const (
	MaxFunnelSteps         = 8
	MaxFunnelPropsPerStep  = 4
	DefaultFunnelWindowSec = 2 * 60 * 60
	MaxFunnelWindowSec     = 24 * 60 * 60
	funnelGroupRows        = 20
)

// funnelColumns are the columns a funnel may be split by (a session's value:
// its first row's).
var funnelColumns = map[string]bool{"app_version": true, "build_type": true, "platform": true}

// ValidFunnelGroup reports whether a funnel may be split by column ("" = not split).
func ValidFunnelGroup(column string) bool { return column == "" || funnelColumns[column] }

// funnelQuery builds the funnel: per session, the deepest step reached in
// order within the window (ClickHouse windowFunnel over the session's custom
// events of the steps' names; timestamps in ms), then sessions per level.
func funnelQuery(f models.AdminFilter, steps []models.FunnelStep, windowSec int64, by string) (string, []interface{}, error) {
	if len(steps) == 0 || len(steps) > MaxFunnelSteps {
		return "", nil, fmt.Errorf("1 to %d steps", MaxFunnelSteps)
	}
	if windowSec <= 0 || windowSec > MaxFunnelWindowSec {
		return "", nil, fmt.Errorf("window out of range")
	}
	if !ValidFunnelGroup(by) {
		return "", nil, fmt.Errorf("cannot split by %q", by)
	}
	var conds []string
	var condArgs []interface{}
	names := map[string]bool{}
	var nameList []string
	for _, s := range steps {
		if !models.ValidContextKey(s.Name) || len(s.Props) > MaxFunnelPropsPerStep {
			return "", nil, fmt.Errorf("invalid step")
		}
		c := "name = ?"
		condArgs = append(condArgs, s.Name)
		keys := make([]string, 0, len(s.Props))
		for k := range s.Props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !models.ValidContextKey(k) {
				return "", nil, fmt.Errorf("invalid property key")
			}
			c += " AND props[?] = ?"
			condArgs = append(condArgs, k, s.Props[k])
		}
		conds = append(conds, "("+c+")")
		if !names[s.Name] {
			names[s.Name] = true
			nameList = append(nameList, s.Name)
		}
	}
	value := "''"
	if by != "" {
		value = "argMin(" + by + ", timestamp)"
	}
	where, whereArgs := tableWhere(TableCustomEvents.Name, f)
	query := fmt.Sprintf(`SELECT value, level, toInt64(count())
		FROM (
			SELECT session_id, %s AS value,
				windowFunnel(%d)(toUInt64(toUnixTimestamp64Milli(timestamp)), %s) AS level
			FROM apm_custom_events %s AND session_id != '' AND name IN ?
			GROUP BY session_id
		)
		WHERE level > 0
		GROUP BY value, level`, value, windowSec*1000, strings.Join(conds, ", "), where)
	args := append(append(append([]interface{}{}, condArgs...), whereArgs...), groupSet(nameList))
	return query, args, nil
}

// GetFunnel returns the sessions reaching each step of the funnel in order
// within windowSec, overall or per value of by (the 20 values with the most
// sessions at the first step).
func (r *Repository) GetFunnel(ctx context.Context, f models.AdminFilter, steps []models.FunnelStep, windowSec int64, by string) (*models.FunnelResponse, error) {
	query, args, err := funnelQuery(f, steps, windowSec, by)
	if err != nil {
		return nil, err
	}
	// sessions[value][level-1]: sessions whose deepest step is level.
	levels := map[string][]int64{}
	err = withAnalysisSlot(ctx, func() error {
		return r.queryRows(ctx, query, args, func(rows driver.Rows) error {
			var value string
			var level uint8
			var n int64
			if err := rows.Scan(&value, &level, &n); err != nil {
				return err
			}
			if level < 1 || int(level) > len(steps) {
				return nil
			}
			l := levels[value]
			if l == nil {
				l = make([]int64, len(steps))
				levels[value] = l
			}
			l[level-1] += n
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	resp := &models.FunnelResponse{StartTime: f.StartTime.UTC(), EndTime: f.EndTime.UTC(), WindowSeconds: windowSec,
		GroupBy: by, Steps: steps, Groups: []models.FunnelGroup{}}
	if by == "" && len(levels) == 0 {
		levels[""] = make([]int64, len(steps))
	}
	for value, l := range levels {
		resp.Groups = append(resp.Groups, models.FunnelGroup{Value: value, Steps: FunnelSteps(steps, l)})
	}
	sort.Slice(resp.Groups, func(a, b int) bool {
		sa, sb := resp.Groups[a].Steps[0].Sessions, resp.Groups[b].Steps[0].Sessions
		if sa != sb {
			return sa > sb
		}
		return resp.Groups[a].Value < resp.Groups[b].Value
	})
	if len(resp.Groups) > funnelGroupRows {
		resp.Groups = resp.Groups[:funnelGroupRows]
		resp.Truncated = true
	}
	return resp, nil
}

// FunnelSteps turns the sessions per deepest level (deepest[i]: sessions
// whose deepest step is i+1) into sessions reaching each step and the
// conversions.
func FunnelSteps(steps []models.FunnelStep, deepest []int64) []models.FunnelStepResult {
	out := make([]models.FunnelStepResult, len(steps))
	var reached int64
	for i := len(steps) - 1; i >= 0; i-- {
		reached += deepest[i]
		out[i] = models.FunnelStepResult{Name: steps[i].Name, Props: steps[i].Props, Sessions: reached}
	}
	for i := range out {
		if out[0].Sessions > 0 {
			out[i].Conversion = float64(out[i].Sessions) / float64(out[0].Sessions)
		}
		if i == 0 {
			if out[0].Sessions > 0 {
				out[0].FromPrevious = 1
			}
		} else if out[i-1].Sessions > 0 {
			out[i].FromPrevious = float64(out[i].Sessions) / float64(out[i-1].Sessions)
		}
	}
	return out
}

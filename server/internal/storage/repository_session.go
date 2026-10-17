package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/timeline"
)

// Session time line (APM-15) and the sessions of a device.
//
// Cost: session_id and device_id are in no sort key. Every query is bounded
// by the time range (daily partitions) and, for session_id, by the
// idx_session_id bloom filter skip index (schema.go) where it exists; the
// first query finds the session's first and last row, and the others read
// only that span. Results are bounded per type; Truncated says which types
// had more.

// SessionQuery selects a session's rows.
type SessionQuery struct {
	SessionID string
	Start     time.Time
	End       time.Time
	// At (optional): when the per-type limits cut, the rows nearest to it
	// are kept (the occurrence the time line was opened from).
	At   *time.Time
	Perf bool // add the sparse performance track
}

// Per-type limits of a session's time line.
const (
	SessionRowsPerType     = 1000
	SessionExceptionRows   = 500 // each carries up to ~50 breadcrumb lines
	SessionBreadcrumbItems = 2000
	SessionContextChanges  = 1000
	MaxPerfPoints          = 240
	maxCrashStackChars     = 300 // the time line shows the first line; the crash page has the stack
	maxSessionMessageChars = 2000
	DeviceSessionsLimit    = 100
	// SessionRangeMax bounds the range of a session or device query.
	SessionRangeMax = 31 * 24 * time.Hour
)

var perfIntervals = []int{1, 5, 10, 30, 60, 300, 900, 1800, 3600, 3 * 3600, 6 * 3600, 12 * 3600, 86400}

// sessionWhere selects one session's rows in [start, end] (milliseconds).
func sessionWhere(sessionID string, start, end time.Time) (string, []interface{}) {
	return "WHERE session_id = ? AND timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?))",
		[]interface{}{sessionID, start.UnixMilli(), end.UnixMilli()}
}

// sessionOrder orders a per-type query: by time, or by distance to at so
// that the limit keeps the rows nearest to it. It returns extra arguments.
func sessionOrder(at *time.Time) (string, []interface{}) {
	if at == nil {
		return "ORDER BY timestamp, seq", nil
	}
	return "ORDER BY abs(toUnixTimestamp64Milli(timestamp) - toInt64(?)), timestamp, seq", []interface{}{at.UnixMilli()}
}

// sessionSource is one table of the time line: the columns its items need.
type sessionSource struct {
	table   string
	src     string // the item type
	columns string // after timestamp, event_id, seq
	limit   int
	scan    func(rows driver.Rows, it *models.TimelineItem, crumbs *[]string) error
}

// ctxMap normalizes a scanned Map column (nil for empty).
func ctxMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

var sessionSources = []sessionSource{
	{TableCustomEvents.Name, models.TimelineCustomEvent, "scene, name, toInt64(count), aggregated = 1, ctx_merged = 1, props, ctx", SessionRowsPerType,
		func(rows driver.Rows, it *models.TimelineItem, _ *[]string) error {
			var props, ctx map[string]string
			if err := rows.Scan(&it.Time, &it.EventID, &it.Seq, &it.Scene, &it.Name, &it.Count, &it.Aggregated, &it.CtxMerged, &props, &ctx); err != nil {
				return err
			}
			it.Props, it.Context = ctxMap(props), ctxMap(ctx)
			return nil
		}},
	{TableCustomMetrics.Name, models.TimelineCustomMetric, "scene, name, unit, toInt64(count), " + finiteFloat("value_sum") + ", " + finiteFloat("value_min") + ", " + finiteFloat("value_max") + ", toInt64(window_ms), ctx_merged = 1, ctx", SessionRowsPerType,
		func(rows driver.Rows, it *models.TimelineItem, _ *[]string) error {
			var ctx map[string]string
			if err := rows.Scan(&it.Time, &it.EventID, &it.Seq, &it.Scene, &it.Name, &it.Unit, &it.Count, &it.Sum, &it.Min, &it.Max, &it.WindowMs, &it.CtxMerged, &ctx); err != nil {
				return err
			}
			it.Context = ctxMap(ctx)
			return nil
		}},
	{TableExceptions.Name, models.TimelineException, fmt.Sprintf("scene, fingerprint, substringUTF8(message, 1, %d), exception_type, log_type, throw_site, call_site, toInt64(count), toInt64(error_seq), toInt64(session_ms), unhandled, ctx_merged = 1, ctx, breadcrumbs", maxSessionMessageChars), SessionExceptionRows,
		func(rows driver.Rows, it *models.TimelineItem, crumbs *[]string) error {
			var ctx map[string]string
			if err := rows.Scan(&it.Time, &it.EventID, &it.Seq, &it.Scene, &it.Fingerprint, &it.Message, &it.ExceptionType, &it.LogType, &it.ThrowSite, &it.CallSite,
				&it.Count, &it.ErrorSeq, &it.SessionMs, &it.Unhandled, &it.CtxMerged, &ctx, crumbs); err != nil {
				return err
			}
			it.Context = ctxMap(ctx)
			return nil
		}},
	{TableCrashes.Name, models.TimelineCrash, fmt.Sprintf("scene, fingerprint, crash_type, substringUTF8(stack, 1, %d), ctx, breadcrumbs", maxCrashStackChars), SessionRowsPerType,
		func(rows driver.Rows, it *models.TimelineItem, crumbs *[]string) error {
			var ctx map[string]string
			if err := rows.Scan(&it.Time, &it.EventID, &it.Seq, &it.Scene, &it.Fingerprint, &it.CrashType, &it.Message, &ctx, crumbs); err != nil {
				return err
			}
			it.Context = ctxMap(ctx)
			return nil
		}},
	{TableJanks.Name, models.TimelineJank, "scene, " + finiteFloat("duration_ms") + ", " + finiteFloat("max_frame_ms") + ", toInt64(recent_gc_count), " + finiteFloat("recent_gc_alloc_kb") + ", ctx, recent_events", SessionRowsPerType,
		func(rows driver.Rows, it *models.TimelineItem, crumbs *[]string) error {
			var ctx map[string]string
			if err := rows.Scan(&it.Time, &it.EventID, &it.Seq, &it.Scene, &it.DurationMs, &it.MaxFrameMs, &it.RecentGCCount, &it.RecentGCAllocKB, &ctx, crumbs); err != nil {
				return err
			}
			it.Context = ctxMap(ctx)
			if len(*crumbs) > 0 {
				it.RecentEvents = append([]string(nil), *crumbs...)
			}
			return nil
		}},
	{TableSceneLoads.Name, models.TimelineSceneLoad, "scene_name, " + finiteFloat("load_ms") + ", " + finiteFloat("activate_ms") + ", ctx", SessionRowsPerType,
		func(rows driver.Rows, it *models.TimelineItem, _ *[]string) error {
			var ctx map[string]string
			if err := rows.Scan(&it.Time, &it.EventID, &it.Seq, &it.SceneName, &it.LoadMs, &it.ActivateMs, &ctx); err != nil {
				return err
			}
			it.Context = ctxMap(ctx)
			return nil
		}},
	{TableStartups.Name, models.TimelineStartup, finiteFloat("phase1_ms") + ", " + finiteFloat("phase2_ms") + ", " + finiteFloat("tti_ms") + ", ctx", SessionRowsPerType,
		func(rows driver.Rows, it *models.TimelineItem, _ *[]string) error {
			var ctx map[string]string
			if err := rows.Scan(&it.Time, &it.EventID, &it.Seq, &it.Phase1Ms, &it.Phase2Ms, &it.TTIMs, &ctx); err != nil {
				return err
			}
			it.Context = ctxMap(ctx)
			return nil
		}},
}

// ctxSources are the tables whose rows say which context was current at
// their time: every row except counts merged across contexts (ctx_merged).
var ctxSources = []struct{ table, src, cond string }{
	{TablePerfSamples.Name, "perf", ""},
	{TableCustomEvents.Name, models.TimelineCustomEvent, " AND ctx_merged = 0"},
	{TableCustomMetrics.Name, models.TimelineCustomMetric, " AND ctx_merged = 0"},
	{TableExceptions.Name, models.TimelineException, " AND ctx_merged = 0"},
	{TableCrashes.Name, models.TimelineCrash, ""},
	{TableJanks.Name, models.TimelineJank, ""},
	{TableSceneLoads.Name, models.TimelineSceneLoad, ""},
	{TableStartups.Name, models.TimelineStartup, ""},
}

// sessionInfoQuery finds the session's span and its device and build (of
// its latest row) across every event table.
func sessionInfoQuery(q SessionQuery) (string, []interface{}) {
	where, whereArgs := sessionWhere(q.SessionID, q.Start, q.End)
	parts := make([]string, 0, len(EventTables))
	var args []interface{}
	for _, t := range EventTables {
		osVersion, sessionMs := "os_version", "toUInt64(0)"
		if t == TableSceneLoads {
			osVersion = "''"
		}
		if t == TableExceptions {
			sessionMs = "session_ms"
		}
		parts = append(parts, fmt.Sprintf("SELECT timestamp, '%s' AS src, device_id, app_version, platform, device_model, %s AS os_version, build_type, %s AS session_ms FROM %s %s",
			t.Name, osVersion, sessionMs, t.Name, where))
		args = append(args, whereArgs...)
	}
	return `SELECT toInt64(count()), min(timestamp), max(timestamp),
			argMax(device_id, timestamp), argMax(app_version, timestamp), argMax(platform, timestamp),
			argMax(device_model, timestamp), argMax(os_version, timestamp), argMax(build_type, timestamp),
			toInt64(countIf(session_ms > 0)), minIf(toUnixTimestamp64Milli(timestamp) - toInt64(session_ms), session_ms > 0),
			toInt64(countIf(src = '` + TablePerfSamples.Name + `')), minIf(timestamp, src = '` + TablePerfSamples.Name + `'), maxIf(timestamp, src = '` + TablePerfSamples.Name + `')
		FROM (` + strings.Join(parts, "\n\t\tUNION ALL\n\t\t") + `)`, args
}

// ctxHash is the hash of a context's sorted entries (Map keys are
// LowCardinality; each entry becomes one plain string "key=value" (keys
// cannot contain "="), so the hash does not
// depend on nested LowCardinality support).
const ctxHash = `cityHash64(arraySort(arrayMap((k, v) -> concat(toString(k), '=', v), mapKeys(ctx), mapValues(ctx))))`

// ctxUnion renders, for every context source, cols (with %s for the source
// name) over the session's rows, combined with UNION ALL.
func ctxUnion(q SessionQuery, cols string) (string, []interface{}) {
	where, whereArgs := sessionWhere(q.SessionID, q.Start, q.End)
	parts := make([]string, 0, len(ctxSources))
	var args []interface{}
	for _, s := range ctxSources {
		parts = append(parts, fmt.Sprintf("SELECT "+cols+" FROM %s %s%s", s.src, s.table, where, s.cond))
		args = append(args, whereArgs...)
	}
	return strings.Join(parts, "\n\t\t\tUNION ALL\n\t\t\t"), args
}

// contextChangesQuery returns the rows at which the session's context
// differs from the previous row's (by time, then the SDK's sequence number):
// a window over the hash of the sorted entries. Only (time, seq, source,
// hash) go through the sort; the contexts are fetched afterwards by hash
// (contextsByHashQuery).
func contextChangesQuery(q SessionQuery, limit int) (string, []interface{}) {
	union, args := ctxUnion(q, "timestamp, seq, '%s' AS src, "+ctxHash+" AS h")
	return fmt.Sprintf(`SELECT timestamp, src, h FROM (
			SELECT timestamp, seq, src, h,
				lagInFrame(h, 1, toUInt64(0)) OVER (ORDER BY timestamp, seq, src ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) AS prev
			FROM (%s)
		)
		WHERE h != prev
		ORDER BY timestamp, seq, src
		LIMIT %d`, union, limit+1), args
}

// contextsByHashQuery returns one context per hash among the session's rows.
func contextsByHashQuery(q SessionQuery, hashes []uint64) (string, []interface{}) {
	union, args := ctxUnion(q, ctxHash+" AS h, ctx, '%s' AS src")
	return `SELECT h, any(ctx) FROM (` + union + `) WHERE h IN ? GROUP BY h`, append(args, hashes)
}

// PerfInterval returns the bucket size (seconds) of the performance track
// over [first, last]: the smallest of perfIntervals giving at most
// MaxPerfPoints buckets.
func PerfInterval(first, last time.Time) int {
	span := last.Sub(first).Seconds()
	for _, s := range perfIntervals {
		if span/float64(s) < MaxPerfPoints {
			return s
		}
	}
	return perfIntervals[len(perfIntervals)-1]
}

// sessionTimeout bounds one time line request below the admin server's write
// timeout (30 s by default), so that its queries are cancelled rather than
// left running after the client got a cut-off answer.
const sessionTimeout = 25 * time.Second

type sourceResult struct {
	items  []models.TimelineItem
	crumbs []models.TimelineItem
	more   bool
	err    error
}

// GetSessionTimeline returns everything stored for one session in the range,
// merged by time. After the first query (the session's span), the per-type,
// context and perf queries run in parallel.
func (r *Repository) GetSessionTimeline(ctx context.Context, q SessionQuery) (*models.SessionTimeline, error) {
	ctx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()
	tl := &models.SessionTimeline{
		SessionID: q.SessionID, StartTime: q.Start.UTC(), EndTime: q.End.UTC(),
		Items: []models.TimelineItem{}, Counts: map[string]int{}, Truncated: []string{}, Perf: []models.PerfPoint{},
	}
	for _, t := range models.TimelineTypes {
		tl.Counts[t] = 0
	}

	// 1. The session's span, device and build.
	infoQuery, infoArgs := sessionInfoQuery(q)
	var withSessionMs, startedAtMs, perfRows int64
	var perfFirst, perfLast time.Time
	s := &tl.Session
	if err := r.client.conn.QueryRow(ctx, infoQuery, infoArgs...).Scan(&s.Rows, &s.FirstSeen, &s.LastSeen,
		&s.DeviceID, &s.AppVersion, &s.Platform, &s.DeviceModel, &s.OSVersion, &s.BuildType,
		&withSessionMs, &startedAtMs, &perfRows, &perfFirst, &perfLast); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	s.SessionID = q.SessionID
	if s.Rows == 0 {
		s.FirstSeen, s.LastSeen = time.Time{}, time.Time{}
		return tl, nil
	}
	tl.Found = true
	s.FirstSeen, s.LastSeen = s.FirstSeen.UTC(), s.LastSeen.UTC()
	if withSessionMs > 0 {
		started := time.UnixMilli(startedAtMs).UTC()
		s.StartedAt = &started
	}
	// Only the session's own span from here on.
	span := q
	span.Start, span.End = s.FirstSeen, s.LastSeen
	where, whereArgs := sessionWhere(span.SessionID, span.Start, span.End)
	order, orderArgs := sessionOrder(q.At)

	// 2. The rows of each table, the context changes and the perf track, in
	// parallel (each query is small with the session index).
	results := make([]sourceResult, len(sessionSources))
	var changes []models.TimelineItem
	var changesTruncated bool
	var changesErr, perfErr error
	var wg sync.WaitGroup
	for i, src := range sessionSources {
		wg.Add(1)
		go func(i int, src sessionSource) {
			defer wg.Done()
			res := &results[i]
			query := fmt.Sprintf("SELECT timestamp, event_id, seq, %s FROM %s %s %s LIMIT %d", src.columns, src.table, where, order, src.limit+1)
			args := append(append([]interface{}{}, whereArgs...), orderArgs...)
			res.err = r.queryRows(ctx, query, args, func(dr driver.Rows) error {
				it := models.TimelineItem{Type: src.src}
				var lines []string
				if err := src.scan(dr, &it, &lines); err != nil {
					return err
				}
				if len(res.items) == src.limit {
					res.more = true // the extra row only says there were more
					return nil
				}
				it.Time = it.Time.UTC()
				for _, line := range lines {
					res.crumbs = append(res.crumbs, timeline.ParseBreadcrumb(line, it.Time, src.src))
				}
				res.items = append(res.items, it)
				return nil
			})
			if res.err != nil {
				res.err = fmt.Errorf("%s: %w", src.table, res.err)
			}
		}(i, src)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		changes, changesTruncated, changesErr = r.contextChanges(ctx, span)
	}()
	perfTruncated := false
	if q.Perf && perfRows > 0 {
		tl.PerfIntervalSeconds = PerfInterval(perfFirst, perfLast)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tl.Perf, perfTruncated, perfErr = r.perfTrack(ctx, span, tl.PerfIntervalSeconds)
		}()
	}
	wg.Wait()

	var rows, crumbs []models.TimelineItem
	for i, src := range sessionSources {
		res := &results[i]
		if res.err != nil {
			return nil, res.err
		}
		if res.more {
			tl.Truncated = append(tl.Truncated, src.src)
		}
		tl.Counts[src.src] = len(res.items)
		rows = append(rows, res.items...)
		crumbs = append(crumbs, res.crumbs...)
	}
	if changesErr != nil {
		return nil, fmt.Errorf("context changes: %w", changesErr)
	}
	if perfErr != nil {
		return nil, fmt.Errorf("perf: %w", perfErr)
	}

	// 3. Breadcrumbs: once per line, not when the row they stand for is on
	// the time line.
	crumbs, dup := timeline.DedupeBreadcrumbs(crumbs)
	crumbs, covered := timeline.DropCovered(crumbs, rows)
	tl.DuplicateBreadcrumbs = dup + covered
	crumbs, cut := timeline.Nearest(crumbs, SessionBreadcrumbItems, q.At)
	if cut {
		tl.Truncated = append(tl.Truncated, models.TimelineBreadcrumb)
	}
	tl.Counts[models.TimelineBreadcrumb] = len(crumbs)

	// 4. Context changes (the earliest SessionContextChanges).
	if changesTruncated {
		tl.Truncated = append(tl.Truncated, models.TimelineContext)
	}
	tl.Counts[models.TimelineContext] = len(changes)
	if perfTruncated {
		tl.Truncated = append(tl.Truncated, "perf")
	}

	tl.Items = append(append(append(tl.Items, rows...), crumbs...), changes...)
	timeline.Sort(tl.Items)
	return tl, nil
}

func (r *Repository) contextChanges(ctx context.Context, q SessionQuery) ([]models.TimelineItem, bool, error) {
	type change struct {
		ts  time.Time
		src string
		h   uint64
	}
	query, args := contextChangesQuery(q, SessionContextChanges)
	var found []change
	truncated := false
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var c change
		if err := rows.Scan(&c.ts, &c.src, &c.h); err != nil {
			return err
		}
		if len(found) == SessionContextChanges {
			truncated = true
			return nil
		}
		found = append(found, c)
		return nil
	})
	if err != nil || len(found) == 0 {
		return nil, truncated, err
	}
	seen := map[uint64]bool{}
	var hashes []uint64
	for _, c := range found {
		if !seen[c.h] {
			seen[c.h] = true
			hashes = append(hashes, c.h)
		}
	}
	byHash := map[uint64]map[string]string{}
	query, args = contextsByHashQuery(q, hashes)
	err = r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var h uint64
		var m map[string]string
		if err := rows.Scan(&h, &m); err != nil {
			return err
		}
		byHash[h] = m
		return nil
	})
	if err != nil {
		return nil, truncated, err
	}
	var items []models.TimelineItem
	var prev map[string]string
	for _, c := range found {
		cur := byHash[c.h]
		if changes := timeline.Diff(prev, cur); len(changes) > 0 {
			items = append(items, models.TimelineItem{Time: c.ts.UTC(), Type: models.TimelineContext, Source: c.src, Changes: changes, Context: ctxMap(cur)})
		}
		prev = cur
	}
	return items, truncated, nil
}

func (r *Repository) perfTrack(ctx context.Context, q SessionQuery, interval int) ([]models.PerfPoint, bool, error) {
	where, args := sessionWhere(q.SessionID, q.Start, q.End)
	query := fmt.Sprintf(`
		SELECT toStartOfInterval(timestamp, INTERVAL %d SECOND) AS t, toInt64(count()),
			%s, %s, %s, %s
		FROM apm_perf_samples %s
		GROUP BY t
		ORDER BY t
		LIMIT %d
	`, interval, finiteFloat("avg(fps)"), finiteFloat("min(fps)"), finiteFloat("max(frame_time_ms)"), finiteFloat("max(mem_mb)"), where, MaxPerfPoints+1)
	points := []models.PerfPoint{}
	truncated := false
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var p models.PerfPoint
		if err := rows.Scan(&p.Time, &p.Samples, &p.FPSAvg, &p.FPSMin, &p.FrameMsMax, &p.MemMBMax); err != nil {
			return err
		}
		if len(points) == MaxPerfPoints {
			truncated = true
			return nil
		}
		p.Time = p.Time.UTC()
		points = append(points, p)
		return nil
	})
	return points, truncated, err
}

// GetDeviceSessions lists the sessions of one device in [start, end], most
// recent first (at most DeviceSessionsLimit).
func (r *Repository) GetDeviceSessions(ctx context.Context, deviceID string, start, end time.Time) (*models.DeviceSessionsResponse, error) {
	resp := &models.DeviceSessionsResponse{DeviceID: deviceID, StartTime: start.UTC(), EndTime: end.UTC(), Sessions: []models.SessionSummary{}}
	where := "WHERE device_id = ? AND session_id != '' AND timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?))"
	whereArgs := []interface{}{deviceID, start.UnixMilli(), end.UnixMilli()}
	parts := make([]string, 0, len(EventTables))
	var args []interface{}
	for _, t := range EventTables {
		exc, crash := "toUInt64(0)", "toUInt64(0)"
		if t == TableExceptions {
			exc = "toUInt64(sum(count))"
		}
		if t == TableCrashes {
			crash = "toUInt64(count())"
		}
		parts = append(parts, fmt.Sprintf(`SELECT session_id, min(timestamp) AS first, max(timestamp) AS last, toUInt64(count()) AS c, %s AS exc, %s AS crashes,
				argMax(app_version, timestamp) AS app_version, argMax(platform, timestamp) AS platform,
				argMax(device_model, timestamp) AS device_model, argMax(build_type, timestamp) AS build_type
			FROM %s %s GROUP BY session_id`, exc, crash, t.Name, where))
		args = append(args, whereArgs...)
	}
	query := fmt.Sprintf(`
		SELECT session_id, min(first), max(last) AS last_seen, toInt64(sum(c)), toInt64(sum(exc)), toInt64(sum(crashes)),
			argMax(app_version, last), argMax(platform, last), argMax(device_model, last), argMax(build_type, last)
		FROM (%s)
		GROUP BY session_id
		ORDER BY last_seen DESC, session_id
		LIMIT %d
	`, strings.Join(parts, "\n\t\tUNION ALL\n\t\t"), DeviceSessionsLimit+1)
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var s models.SessionSummary
		if err := rows.Scan(&s.SessionID, &s.FirstSeen, &s.LastSeen, &s.Rows, &s.Exceptions, &s.Crashes,
			&s.AppVersion, &s.Platform, &s.DeviceModel, &s.BuildType); err != nil {
			return err
		}
		if len(resp.Sessions) == DeviceSessionsLimit {
			resp.Truncated = true
			return nil
		}
		s.FirstSeen, s.LastSeen = s.FirstSeen.UTC(), s.LastSeen.UTC()
		resp.Sessions = append(resp.Sessions, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

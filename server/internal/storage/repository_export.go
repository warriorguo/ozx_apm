package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/sessionexport"
)

// Session export (APM-10): every stored row of one session, for
// sessionexport.Build (OZX's AiTelemetry session directory format).
//
// Based on the time line's queries (APM-15): the session's span first
// (sessionInfoQuery), then one query per table over that span only, by the
// session_id index where it exists, each with a row limit (the earliest
// rows are kept; a table with more is listed in Truncated). Cost bounds:
//   - one export at a time per server process (exportSlots; a second
//     request waits for it, or for its own deadline), and every query takes
//     one of the time line's shared slots (at most 4 time line or export
//     queries at once on the pool that ingest shares);
//   - the queries run one after another, cancelled with the request or
//     after exportTimeout;
//   - the rows held are bounded by the limits below (about 40 MB at worst,
//     exceptions with long stacks), and the files by sessionexport.MaxBytes.

// Row limits per table of one export.
const (
	ExportCustomEventRows = 20000
	ExportPerfRows        = 10800 // 3 hours at one sample a second
	ExportExceptionRows   = 2000
	ExportRowsPerType     = 5000
	ExportCrashRows       = 200
	exportMessageChars    = 2000
	exportStackChars      = 4000
	exportTimeout         = 25 * time.Second
)

var exportSlots = make(chan struct{}, 1)

// exportSource is one table of the export: its columns after timestamp, seq,
// game_t, frame and the run id, and how to scan them into a row.
type exportSource struct {
	table   string
	source  string
	runID   string // expression of the row's run id
	columns string
	limit   int
	scan    func(rows driver.Rows, base []any, r *sessionexport.Row) error
}

func f64(name string) string { return finiteFloat(name) }

func f(key string, value any) sessionexport.Field {
	return sessionexport.Field{Key: key, Value: value}
}

var exportSources = []exportSource{
	{TableCustomEvents.Name, sessionexport.SourceCustomEvent, "ctx['runId']",
		"name, toInt64(count), aggregated = 1, ctx_merged = 1, props, prop_types", ExportCustomEventRows,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var props map[string]string
			var types map[string]uint8
			if err := rows.Scan(append(base, &r.Name, &r.Count, &r.Aggregated, &r.CtxMerged, &props, &types)...); err != nil {
				return err
			}
			r.Props, r.PropTypes = props, types
			return nil
		}},
	{TableExceptions.Name, sessionexport.SourceException, "ctx['runId']",
		fmt.Sprintf("scene, fingerprint, exception_type, log_type, substringUTF8(message, 1, %d), throw_site, call_site, toInt64(count), toInt64(error_seq), unhandled, ctx_merged = 1, substringUTF8(stack, 1, %d)",
			exportMessageChars, exportStackChars), ExportExceptionRows,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var scene, fp, typ, logType, msg, throwSite, callSite, unhandled, stack string
			var count, errorSeq int64
			var merged bool
			if err := rows.Scan(append(base, &scene, &fp, &typ, &logType, &msg, &throwSite, &callSite, &count, &errorSeq, &unhandled, &merged, &stack)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("scene", scene), f("fingerprint", fp), f("exceptionType", typ), f("logType", logType),
				f("message", msg), f("throwSite", throwSite), f("callSite", callSite), f("count", count), f("errorSeq", errorSeq),
				f("unhandled", unhandled), f("ctxMerged", merged), f("stack", stack)}
			return nil
		}},
	{TableCrashes.Name, sessionexport.SourceCrash, "ctx['runId']",
		fmt.Sprintf("scene, crash_type, fingerprint, summary, exit_reason, foreground = 1, substringUTF8(stack, 1, %d)", exportStackChars), ExportCrashRows,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var scene, typ, fp, summary, exitReason, stack string
			var fg bool
			if err := rows.Scan(append(base, &scene, &typ, &fp, &summary, &exitReason, &fg, &stack)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("scene", scene), f("crashType", typ), f("fingerprint", fp), f("summary", summary),
				f("exitReason", exitReason), f("foreground", fg), f("stack", stack)}
			return nil
		}},
	{TableJanks.Name, sessionexport.SourceJank, "ctx['runId']",
		"scene, " + f64("duration_ms") + ", " + f64("max_frame_ms") + ", toInt64(recent_gc_count), " + f64("recent_gc_alloc_kb"), ExportRowsPerType,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var scene string
			var dur, maxFrame, gcKB float64
			var gc int64
			if err := rows.Scan(append(base, &scene, &dur, &maxFrame, &gc, &gcKB)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("scene", scene), f("durationMs", dur), f("maxFrameMs", maxFrame), f("recentGcCount", gc), f("recentGcAllocKb", gcKB)}
			return nil
		}},
	{TableSceneLoads.Name, sessionexport.SourceSceneLoad, "ctx['runId']",
		"scene_name, " + f64("load_ms") + ", " + f64("activate_ms"), ExportRowsPerType,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var scene string
			var load, activate float64
			if err := rows.Scan(append(base, &scene, &load, &activate)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("sceneName", scene), f("loadMs", load), f("activateMs", activate)}
			return nil
		}},
	{TableStartups.Name, sessionexport.SourceStartup, "ctx['runId']",
		f64("phase1_ms") + ", " + f64("phase2_ms") + ", " + f64("tti_ms"), ExportRowsPerType,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var p1, p2, tti float64
			if err := rows.Scan(append(base, &p1, &p2, &tti)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("phase1Ms", p1), f("phase2Ms", p2), f("ttiMs", tti)}
			return nil
		}},
	// A run's own id (the context may already have the next run's).
	{TableRuns.Name, sessionexport.SourceRun, "run_id",
		"scene, run_id, end_reason, result, crash_type, toInt64(duration_ms), toInt64(samples), " + f64("peak_mem_mb") + ", " + f64("peak_alloc_mb") + ", " +
			f64("peak_reserved_mb") + ", " + f64("peak_gfx_mb") + ", " + f64("peak_rss_mb"), ExportRowsPerType,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var scene, runID, end, result, crashType string
			var dur, samples int64
			var mem, alloc, reserved, gfx, rss float64
			if err := rows.Scan(append(base, &scene, &runID, &end, &result, &crashType, &dur, &samples, &mem, &alloc, &reserved, &gfx, &rss)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("scene", scene), f("runId", runID), f("endReason", end), f("result", result), f("crashType", crashType),
				f("durationMs", dur), f("samples", samples), f("peakMemMb", mem), f("peakAllocMb", alloc), f("peakReservedMb", reserved),
				f("peakGfxMb", gfx), f("peakRssMb", rss)}
			return nil
		}},
	{TableCustomMetrics.Name, sessionexport.SourceCustomMetric, "ctx['runId']",
		"scene, name, unit, toInt64(count), " + f64("value_sum") + ", " + f64("value_min") + ", " + f64("value_max") + ", toInt64(window_ms), ctx_merged = 1", ExportRowsPerType,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var scene, name, unit string
			var count, window int64
			var sum, min, max float64
			var merged bool
			if err := rows.Scan(append(base, &scene, &name, &unit, &count, &sum, &min, &max, &window, &merged)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("scene", scene), f("name", name), f("unit", unit), f("count", count), f("sum", sum),
				f("min", min), f("max", max), f("windowMs", window), f("ctxMerged", merged)}
			return nil
		}},
	{TablePerfSamples.Name, sessionexport.SourcePerf, "ctx['runId']",
		"scene, " + f64("fps") + ", " + f64("frame_time_ms") + ", " + f64("main_thread_ms") + ", " + f64("gc_alloc_kb") + ", " + f64("mem_mb") + ", " +
			f64("mem_alloc_mb") + ", " + f64("mem_rss_mb"), ExportPerfRows,
		func(rows driver.Rows, base []any, r *sessionexport.Row) error {
			var scene string
			var fps, frameMs, mainMs, gcKB, mem, alloc, rss float64
			if err := rows.Scan(append(base, &scene, &fps, &frameMs, &mainMs, &gcKB, &mem, &alloc, &rss)...); err != nil {
				return err
			}
			r.Fields = []sessionexport.Field{f("scene", scene), f("fps", fps), f("frameMs", frameMs), f("mainMs", mainMs), f("gcAllocKb", gcKB),
				f("memMb", mem), f("memAllocMb", alloc), f("memRssMb", rss)}
			return nil
		}},
}

// exportQuery selects one table's rows of the session's span, earliest first.
func exportQuery(src exportSource, q SessionQuery) (string, []interface{}) {
	where, args := sessionWhere(q.SessionID, q.Start, q.End)
	return fmt.Sprintf("SELECT timestamp, seq, game_t, frame, %s, %s FROM %s %s ORDER BY timestamp, seq LIMIT %d",
		src.runID, src.columns, src.table, where, src.limit+1), args
}

// exportMetaKeys are the game context keys copied into session.json (their
// first non-empty value in the session).
var exportMetaKeys = []string{"gitCommit", "dataSource", "dataPackVersion", "dataPackSha256"}

// exportMetaQuery returns, over the session's rows of every table, the first
// non-empty value of each exportMetaKeys key and the first non-empty scene.
func exportMetaQuery(q SessionQuery) (string, []interface{}) {
	cols := "timestamp, '%s' AS src"
	for i, k := range exportMetaKeys {
		cols += fmt.Sprintf(", ctx['%s'] AS m%d", k, i)
	}
	where, whereArgs := sessionWhere(q.SessionID, q.Start, q.End)
	parts := ""
	var args []interface{}
	for i, t := range EventTables {
		scene := "scene"
		if t == TableSceneLoads {
			scene = "scene_name"
		}
		if t == TableStartups {
			scene = "''"
		}
		if i > 0 {
			parts += "\n\t\tUNION ALL\n\t\t"
		}
		parts += fmt.Sprintf("SELECT "+cols+", %s AS sc FROM %s %s", t.Name, scene, t.Name, where)
		args = append(args, whereArgs...)
	}
	sel := "argMinIf(sc, timestamp, sc != '')"
	for i := range exportMetaKeys {
		sel += fmt.Sprintf(", argMinIf(m%d, timestamp, m%d != '')", i, i)
	}
	return "SELECT " + sel + " FROM (" + parts + ")", args
}

// ExportSession reads one session for the export. Found is false (and the
// input empty) when the session has no row in the range.
func (r *Repository) ExportSession(parent context.Context, q SessionQuery) (*sessionexport.Input, bool, error) {
	select {
	case exportSlots <- struct{}{}:
	case <-parent.Done():
		return nil, false, parent.Err()
	}
	defer func() { <-exportSlots }()
	ctx, cancel := parallelQueryContext(parent, exportTimeout)
	defer cancel()

	// 1. The session's span, device and build (the time line's query).
	infoQuery, infoArgs := sessionInfoQuery(q)
	var s models.SessionInfo
	var withSessionMs, startedAtMs, perfRows int64
	var perfFirst, perfLast time.Time
	if err := withTimelineSlot(ctx, func() error {
		return r.client.conn.QueryRow(ctx, infoQuery, infoArgs...).Scan(&s.Rows, &s.FirstSeen, &s.LastSeen,
			&s.DeviceID, &s.AppVersion, &s.Platform, &s.DeviceModel, &s.OSVersion, &s.BuildType,
			&withSessionMs, &startedAtMs, &perfRows, &perfFirst, &perfLast)
	}); err != nil {
		return nil, false, fmt.Errorf("session: %w", err)
	}
	if s.Rows == 0 {
		return &sessionexport.Input{}, false, nil
	}
	in := &sessionexport.Input{Session: sessionexport.Session{
		SessionID: q.SessionID, DeviceID: s.DeviceID, AppVersion: s.AppVersion, Platform: s.Platform,
		DeviceModel: s.DeviceModel, OSVersion: s.OSVersion, BuildType: s.BuildType,
		FirstSeen: s.FirstSeen.UTC(), LastSeen: s.LastSeen.UTC(), Meta: map[string]string{},
	}}
	if withSessionMs > 0 {
		started := time.UnixMilli(startedAtMs).UTC()
		in.Session.StartedAt = &started
	}
	span := q
	span.Start, span.End = s.FirstSeen, s.LastSeen

	// 2. session.json's game context keys and scene.
	metaQuery, metaArgs := exportMetaQuery(span)
	meta := make([]string, len(exportMetaKeys)+1)
	dest := make([]any, len(meta))
	for i := range meta {
		dest[i] = &meta[i]
	}
	if err := withTimelineSlot(ctx, func() error {
		return r.client.conn.QueryRow(ctx, metaQuery, metaArgs...).Scan(dest...)
	}); err != nil {
		return nil, false, fmt.Errorf("session meta: %w", err)
	}
	in.Session.Meta["scene"] = meta[0]
	for i, k := range exportMetaKeys {
		in.Session.Meta[k] = meta[i+1]
	}

	// 3. The rows of each table, one query at a time.
	for _, src := range exportSources {
		query, args := exportQuery(src, span)
		n := 0
		more := false
		err := withTimelineSlot(ctx, func() error {
			return r.queryRows(ctx, query, args, func(rows driver.Rows) error {
				if n == src.limit {
					more = true // the extra row only says there were more
					return nil
				}
				row := sessionexport.Row{Source: src.source}
				base := []any{&row.Time, &row.Seq, &row.GameT, &row.Frame, &row.RunID}
				if err := src.scan(rows, base, &row); err != nil {
					return err
				}
				row.Time = row.Time.UTC()
				in.Rows = append(in.Rows, row)
				n++
				return nil
			})
		})
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", src.table, err)
		}
		if more {
			in.Truncated = append(in.Truncated, src.source)
		}
	}
	return in, true, nil
}

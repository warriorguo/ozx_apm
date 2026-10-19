package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-9: game runs, the perf samples' memory breakdown and the run peaks.

// InsertRuns batch inserts run events (bounded by processor.NormalizeRun).
func (r *Repository) InsertRuns(ctx context.Context, runs []models.Run) error {
	if len(runs) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO apm_runs (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, "+
		"run_id, end_reason, result, crash_type, duration_ms, samples, peak_mem_mb, peak_mono_heap_mb, peak_alloc_mb, peak_reserved_mb, peak_gfx_mb, peak_rss_mb, "+
		"start_alloc_mb, start_rss_mb, end_alloc_mb, end_rss_mb, event_id, seq, build_type, gpu, ram_class, net_type, ctx)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, e := range runs {
		err := batch.Append(
			e.Timestamp,
			e.AppVersion,
			e.Platform,
			e.DeviceModel,
			e.OSVersion,
			e.SessionID,
			e.DeviceID,
			e.Scene,
			e.RunID,
			e.EndReason,
			e.Result,
			e.CrashType,
			uint64(e.DurationMs), // clamped to [0, a week]
			uint32(e.Samples),
			float32(e.PeakMemMB),
			float32(e.PeakMonoHeapMB),
			float32(e.PeakAllocMB),
			float32(e.PeakReservedMB),
			float32(e.PeakGfxMB),
			float32(e.PeakRssMB),
			float32(e.StartAllocMB),
			float32(e.StartRssMB),
			float32(e.EndAllocMB),
			float32(e.EndRssMB),
			e.EventID,
			e.Seq,
			e.BuildType,
			e.GPU,
			e.RAMClass,
			e.NetType,
			contextColumn(e.Context),
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}
	return batch.Send()
}

// MaxAnalysisRange bounds the time range of the APM-9 analysis endpoints
// (memory, health, funnel): they read every row of the range.
const MaxAnalysisRange = 31 * 24 * time.Hour

// analysisSlots bounds the APM-9 analysis requests running at once across
// all requests. Each runs its queries one after the other (one connection),
// so at most 2 of the 10 pooled connections, which ingest shares, are
// theirs (APM-15's time line has its own 4).
var analysisSlots = make(chan struct{}, 2)

// withAnalysisSlot runs f once a slot is free, or returns ctx's error.
func withAnalysisSlot(ctx context.Context, f func() error) error {
	select {
	case analysisSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-analysisSlots }()
	return f()
}

// analysisGroupRows bounds the groups an analysis endpoint returns.
const analysisGroupRows = 50

// groupValue is the SQL of an analysis grouping (a column of the table or a
// context key) and its arguments. Unlike CustomGroup.groupExpr it never
// treats merged counts specially: the tables it is used on have none, or
// (exceptions) a merged count is grouped under the context it kept.
func groupValue(g CustomGroup) (string, []interface{}, error) {
	switch g.Kind {
	case "ctx":
		if !models.ValidContextKey(g.Key) {
			return "", nil, fmt.Errorf("invalid context key")
		}
		return "ctx[?]", []interface{}{g.Key}, nil
	case "column":
		if !customColumns[g.Key] {
			return "", nil, fmt.Errorf("cannot group by %q", g.Key)
		}
		return g.Key, nil, nil
	}
	return "", nil, fmt.Errorf("unknown grouping %q", g.Kind)
}

// MemoryFigures are the perf sample's memory columns, in display order.
var MemoryFigures = []string{"mem_mb", "mem_mono_heap_mb", "mem_alloc_mb", "mem_reserved_mb", "mem_gfx_mb", "mem_rss_mb"}

// GetMemoryBreakdown returns, per group, the perf samples' memory figures:
// for each, the samples that have it (> 0) and their p50/p95/max (MB).
func (r *Repository) GetMemoryBreakdown(ctx context.Context, f models.AdminFilter, g CustomGroup) (*models.MemoryBreakdownResponse, error) {
	expr, exprArgs, err := groupValue(g)
	if err != nil {
		return nil, err
	}
	where, whereArgs := tableWhere(TablePerfSamples.Name, f)
	var cols strings.Builder
	for _, fig := range MemoryFigures {
		fmt.Fprintf(&cols, ", toInt64(countIf(%[1]s > 0)), %[2]s, %[3]s, %[4]s", fig,
			finiteFloat("quantileIf(0.5)("+fig+", "+fig+" > 0)"),
			finiteFloat("quantileIf(0.95)("+fig+", "+fig+" > 0)"),
			finiteFloat("maxIf("+fig+", "+fig+" > 0)"))
	}
	query := fmt.Sprintf(`SELECT %s AS value, toInt64(count()) AS samples, toInt64(uniqExact(session_id))%s
		FROM apm_perf_samples %s
		GROUP BY value
		ORDER BY samples DESC, value
		LIMIT %d`, expr, cols.String(), where, analysisGroupRows+1)
	args := append(append([]interface{}{}, exprArgs...), whereArgs...)

	resp := &models.MemoryBreakdownResponse{GroupBy: g.String(), Rows: []models.MemoryBreakdownRow{}}
	err = withAnalysisSlot(ctx, func() error {
		return r.queryRows(ctx, query, args, func(rows driver.Rows) error {
			var row models.MemoryBreakdownRow
			figs := make([]models.MemoryFigure, len(MemoryFigures))
			dest := []any{&row.Value, &row.Samples, &row.Sessions}
			for i := range figs {
				dest = append(dest, &figs[i].Rows, &figs[i].P50, &figs[i].P95, &figs[i].Max)
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			if len(resp.Rows) == analysisGroupRows {
				resp.Truncated = true
				return nil
			}
			row.Figures = make(map[string]models.MemoryFigure, len(figs))
			for i, name := range MemoryFigures {
				row.Figures[name] = figs[i]
			}
			resp.Rows = append(resp.Rows, row)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// runPeakMetric is one peak column of apm_runs and, for growth, the start
// and end columns of the same figure ("" for none).
type runPeakMetric struct{ peak, start, end string }

var runPeakMetrics = map[string]runPeakMetric{
	"peak_rss":       {"peak_rss_mb", "start_rss_mb", "end_rss_mb"},
	"peak_alloc":     {"peak_alloc_mb", "start_alloc_mb", "end_alloc_mb"},
	"peak_reserved":  {"peak_reserved_mb", "", ""},
	"peak_gfx":       {"peak_gfx_mb", "", ""},
	"peak_mem":       {"peak_mem_mb", "", ""},
	"peak_mono_heap": {"peak_mono_heap_mb", "", ""},
}

// ValidRunPeakMetric reports whether metric names a run peak.
func ValidRunPeakMetric(metric string) bool {
	_, ok := runPeakMetrics[metric]
	return ok
}

// RunPeakBuckets are the bounds (MB) of the run peak histogram.
var RunPeakBuckets = []float64{256, 512, 768, 1024, 1536, 2048, 3072, 4096}

// GetRunPeaks returns, per group, the distribution of one peak memory
// figure over the runs that have it, and how the runs ended (crashed, OOM,
// killed); plus a histogram of the peak over every filtered run.
func (r *Repository) GetRunPeaks(ctx context.Context, f models.AdminFilter, metric string, g CustomGroup) (*models.RunPeaksResponse, error) {
	m, ok := runPeakMetrics[metric]
	if !ok {
		return nil, fmt.Errorf("unknown metric: %s", metric)
	}
	expr, exprArgs, err := groupValue(g)
	if err != nil {
		return nil, err
	}
	where, whereArgs := tableWhere(TableRuns.Name, f)
	growth := "toFloat64(0)"
	if m.start != "" {
		growth = finiteFloat(fmt.Sprintf("avgIf(%[2]s - %[1]s, %[1]s > 0 AND %[2]s > 0 AND end_reason IN ('end', 'new_run', 'shutdown'))", m.start, m.end))
	}
	p := m.peak
	query := fmt.Sprintf(`SELECT %[1]s AS value, toInt64(count()) AS runs, toInt64(countIf(%[2]s > 0)),
			toInt64(uniqExact(session_id)), toInt64(uniqExact(device_id)),
			toInt64(countIf(end_reason = 'crash')), toInt64(countIf(crash_type = 'oom')), toInt64(countIf(crash_type = 'killed')),
			%[3]s, %[4]s, %[5]s, %[6]s, %[7]s, %[8]s
		FROM apm_runs %[9]s
		GROUP BY value
		ORDER BY runs DESC, value
		LIMIT %[10]d`, expr, p,
		finiteFloat("quantileExactIf(0.5)("+p+", "+p+" > 0)"),
		finiteFloat("quantileExactIf(0.9)("+p+", "+p+" > 0)"),
		finiteFloat("quantileExactIf(0.99)("+p+", "+p+" > 0)"),
		finiteFloat("maxIf("+p+", "+p+" > 0)"),
		finiteFloat("avg(duration_ms)"), growth, where, analysisGroupRows+1)
	args := append(append([]interface{}{}, exprArgs...), whereArgs...)

	var bucket strings.Builder
	bucket.WriteString("multiIf(")
	for i, b := range RunPeakBuckets {
		fmt.Fprintf(&bucket, "%s < %g, %d, ", p, b, i)
	}
	fmt.Fprintf(&bucket, "%d)", len(RunPeakBuckets))
	histQuery := fmt.Sprintf(`SELECT toInt64(%s) AS b, toInt64(count()), toInt64(countIf(crash_type = 'oom'))
		FROM apm_runs %s AND %s > 0
		GROUP BY b ORDER BY b`, bucket.String(), where, p)

	resp := &models.RunPeaksResponse{Metric: metric, GroupBy: g.String(), Rows: []models.RunPeakRow{}, Buckets: []models.RunPeakBucket{}}
	err = withAnalysisSlot(ctx, func() error {
		err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
			var row models.RunPeakRow
			if err := rows.Scan(&row.Value, &row.Runs, &row.WithValue, &row.Sessions, &row.Devices,
				&row.CrashedRuns, &row.OOMRuns, &row.KilledRuns, &row.P50, &row.P90, &row.P99, &row.Max,
				&row.AvgDuration, &row.AvgGrowthMB); err != nil {
				return err
			}
			if len(resp.Rows) == analysisGroupRows {
				resp.Truncated = true
				return nil
			}
			resp.Rows = append(resp.Rows, row)
			return nil
		})
		if err != nil {
			return fmt.Errorf("rows: %w", err)
		}
		counts := map[int64][2]int64{}
		err = r.queryRows(ctx, histQuery, whereArgs, func(rows driver.Rows) error {
			var b, n, oom int64
			if err := rows.Scan(&b, &n, &oom); err != nil {
				return err
			}
			counts[b] = [2]int64{n, oom}
			return nil
		})
		if err != nil {
			return fmt.Errorf("histogram: %w", err)
		}
		low := 0.0
		for i := 0; i <= len(RunPeakBuckets); i++ {
			high := 0.0
			if i < len(RunPeakBuckets) {
				high = RunPeakBuckets[i]
			}
			c := counts[int64(i)]
			resp.Buckets = append(resp.Buckets, models.RunPeakBucket{Low: low, High: high, Runs: c[0], OOM: c[1]})
			low = high
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

package storage

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/metrics"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Custom events and metrics (APM-6).

// InsertCustomEvents batch inserts custom events.
func (r *Repository) InsertCustomEvents(ctx context.Context, events []models.CustomEvent) error {
	if len(events) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO apm_custom_events (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, "+
		"name, count, aggregated, ctx_merged, props, event_id, seq, build_type, gpu, ram_class, net_type, ctx)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, e := range events {
		err := batch.Append(
			e.Timestamp,
			e.AppVersion,
			e.Platform,
			e.DeviceModel,
			e.OSVersion,
			e.SessionID,
			e.DeviceID,
			e.Scene,
			e.Name,
			uint32(e.Count), // bounded by processor.NormalizeCustomEvent
			boolColumn(bool(e.Aggregated)),
			boolColumn(bool(e.CtxMerged)),
			contextColumn(e.Props),
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

// InsertCustomMetrics batch inserts custom metric rows (validated by
// processor.ValidateCustomMetric: finite numbers, valid keys).
func (r *Repository) InsertCustomMetrics(ctx context.Context, rows []models.CustomMetric) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO apm_custom_metrics (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, "+
		"name, unit, count, value_sum, value_min, value_max, window_ms, hist_version, hist_keys, hist_counts, ctx_merged, "+
		"event_id, seq, build_type, gpu, ram_class, net_type, ctx)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, m := range rows {
		keys := make([]int16, len(m.HistKeys))
		for i, k := range m.HistKeys {
			keys[i] = int16(k)
		}
		counts := m.HistCounts
		if counts == nil {
			counts = []uint64{}
		}
		err := batch.Append(
			m.Timestamp,
			m.AppVersion,
			m.Platform,
			m.DeviceModel,
			m.OSVersion,
			m.SessionID,
			m.DeviceID,
			m.Scene,
			m.Name,
			m.Unit,
			m.Count,
			*m.Sum,
			*m.Min,
			*m.Max,
			uint32(m.WindowMs),
			uint8(m.HistVersion),
			keys,
			counts,
			boolColumn(bool(m.CtxMerged)),
			m.EventID,
			m.Seq,
			m.BuildType,
			m.GPU,
			m.RAMClass,
			m.NetType,
			contextColumn(m.Context),
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}
	return batch.Send()
}

// customListRows bounds the names listed; Truncated says there were more.
const customListRows = 200

// customDistributionRows bounds the groups of a distribution.
const customDistributionRows = 50

// metricColumns aggregates metric rows: samples, sessions, sum, min, max and
// the merged histogram of version 1 (keys ascending, counts).
const metricColumns = `toInt64(sum(count)) AS samples,
			toInt64(uniqExact(session_id)) AS sessions,
			sum(value_sum) AS total,
			min(value_min) AS lo,
			max(value_max) AS hi,
			tupleElement(sumMapIf(hist_keys, hist_counts, hist_version = 1) AS h, 1) AS hk,
			tupleElement(h, 2) AS hc`

// percentiles fills the statistics of one metric group from its aggregates.
func percentiles(s *models.CustomMetricStat, total, lo, hi float64, keys []int16, counts []uint64) {
	if s.Count > 0 {
		s.Avg = total / float64(s.Count)
		s.Min, s.Max = lo, hi
	}
	q := metrics.Quantiles(keys, counts, lo, hi, 0.5, 0.9, 0.95, 0.99)
	s.P50, s.P90, s.P95, s.P99 = q[0], q[1], q[2], q[3]
	for _, p := range []*float64{&s.Avg, &s.Min, &s.Max, &s.P50, &s.P90, &s.P95, &s.P99} {
		if !finiteValue(*p) {
			*p = 0
		}
	}
}

func finiteValue(f float64) bool { return f == f && f-f == 0 }

// GetCustomEvents lists the event names in the filter, most frequent first.
func (r *Repository) GetCustomEvents(ctx context.Context, f models.AdminFilter) (*models.CustomEventsResponse, error) {
	where, args := tableWhere(TableCustomEvents.Name, f)
	query := fmt.Sprintf(`
		SELECT name, toInt64(sum(count)) AS cnt, toInt64(uniqExact(session_id)), toInt64(uniqExact(device_id)), max(timestamp)
		FROM apm_custom_events %s
		GROUP BY name
		ORDER BY cnt DESC, name
		LIMIT %d
	`, where, customListRows+1)
	resp := &models.CustomEventsResponse{Events: []models.CustomEventStat{}}
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var e models.CustomEventStat
		if err := rows.Scan(&e.Name, &e.Count, &e.Sessions, &e.Devices, &e.LastSeen); err != nil {
			return err
		}
		if len(resp.Events) == customListRows {
			resp.Truncated = true
			return nil
		}
		resp.Events = append(resp.Events, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetCustomMetrics lists the metric names in the filter with their
// statistics over all its rows, most samples first.
func (r *Repository) GetCustomMetrics(ctx context.Context, f models.AdminFilter) (*models.CustomMetricsResponse, error) {
	where, args := tableWhere(TableCustomMetrics.Name, f)
	query := fmt.Sprintf(`
		SELECT name, argMax(unit, timestamp), %s
		FROM apm_custom_metrics %s
		GROUP BY name
		ORDER BY samples DESC, name
		LIMIT %d
	`, metricColumns, where, customListRows+1)
	resp := &models.CustomMetricsResponse{Metrics: []models.CustomMetricStat{}}
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var s models.CustomMetricStat
		var total, lo, hi float64
		var keys []int16
		var counts []uint64
		if err := rows.Scan(&s.Name, &s.Unit, &s.Count, &s.Sessions, &total, &lo, &hi, &keys, &counts); err != nil {
			return err
		}
		if len(resp.Metrics) == customListRows {
			resp.Truncated = true
			return nil
		}
		percentiles(&s, total, lo, hi, keys, counts)
		resp.Metrics = append(resp.Metrics, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// CustomGroup says how a distribution groups: by a context key, a property
// (events only) or a column.
type CustomGroup struct {
	Kind string // "ctx", "prop" or "column"
	Key  string // the context or property key, or the column name
}

// customColumns are the columns a distribution may group by (never user
// input in the SQL text: only these names are).
var customColumns = map[string]bool{"app_version": true, "platform": true, "device_model": true, "os_version": true, "build_type": true}

// ValidCustomColumn reports whether column may be grouped by.
func ValidCustomColumn(column string) bool { return customColumns[column] }

// groupExpr is the SQL of the grouping value, its arguments, and the
// condition of the rows whose value is not known (merged); "" for none.
func (g CustomGroup) groupExpr(table string) (expr string, args []interface{}, merged string, mergedArgs []interface{}, err error) {
	switch g.Kind {
	case "ctx":
		return "ctx[?]", []interface{}{g.Key}, "ctx_merged = 1 AND NOT mapContains(ctx, ?)", []interface{}{g.Key}, nil
	case "prop":
		if table != TableCustomEvents.Name {
			return "", nil, "", nil, fmt.Errorf("metrics have no properties")
		}
		return "props[?]", []interface{}{g.Key}, "aggregated = 1", nil, nil
	case "column":
		if !customColumns[g.Key] {
			return "", nil, "", nil, fmt.Errorf("cannot group by %q", g.Key)
		}
		return g.Key, nil, "", nil, nil
	}
	return "", nil, "", nil, fmt.Errorf("unknown grouping %q", g.Kind)
}

func (g CustomGroup) String() string {
	if g.Kind == "column" {
		return g.Key
	}
	return g.Kind + ":" + g.Key
}

// GetCustomDistribution groups one event or metric name (metric selects the
// table) by g. Events: calls per value; metrics: samples and percentiles per
// value. Rows whose value is not known are counted in Merged.
func (r *Repository) GetCustomDistribution(ctx context.Context, f models.AdminFilter, metric bool, name string, g CustomGroup) (*models.CustomDistribution, error) {
	table := TableCustomEvents.Name
	if metric {
		table = TableCustomMetrics.Name
	}
	expr, exprArgs, merged, mergedArgs, err := g.groupExpr(table)
	if err != nil {
		return nil, err
	}
	where, whereArgs := tableWhere(table, f)
	where += " AND name = ?"
	whereArgs = append(whereArgs, name)

	dist := &models.CustomDistribution{Name: name, GroupBy: g.String(), Rows: []models.CustomMetricStat{}}
	totalQuery := fmt.Sprintf(`SELECT toInt64(sum(count)) FROM %s %s`, table, where)
	if err := r.client.conn.QueryRow(ctx, totalQuery, whereArgs...).Scan(&dist.Total); err != nil {
		return nil, fmt.Errorf("total: %w", err)
	}
	rowsWhere, rowsArgs := where, whereArgs
	if merged != "" {
		mArgs := append(append([]interface{}{}, whereArgs...), mergedArgs...)
		mergedQuery := fmt.Sprintf(`SELECT toInt64(sum(count)) FROM %s %s AND %s`, table, where, merged)
		if err := r.client.conn.QueryRow(ctx, mergedQuery, mArgs...).Scan(&dist.Merged); err != nil {
			return nil, fmt.Errorf("merged: %w", err)
		}
		rowsWhere += " AND NOT (" + merged + ")"
		rowsArgs = mArgs
	}

	var query string
	if metric {
		query = fmt.Sprintf(`
			SELECT %s AS value, argMax(unit, timestamp), %s
			FROM %s %s
			GROUP BY value
			ORDER BY samples DESC, value
			LIMIT %d
		`, expr, metricColumns, table, rowsWhere, customDistributionRows+1)
	} else {
		query = fmt.Sprintf(`
			SELECT %s AS value, toInt64(sum(count)) AS cnt, toInt64(uniqExact(session_id))
			FROM %s %s
			GROUP BY value
			ORDER BY cnt DESC, value
			LIMIT %d
		`, expr, table, rowsWhere, customDistributionRows+1)
	}
	args := append(append([]interface{}{}, exprArgs...), rowsArgs...)
	err = r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var s models.CustomMetricStat
		if metric {
			var total, lo, hi float64
			var keys []int16
			var counts []uint64
			if err := rows.Scan(&s.Value, &s.Unit, &s.Count, &s.Sessions, &total, &lo, &hi, &keys, &counts); err != nil {
				return err
			}
			percentiles(&s, total, lo, hi, keys, counts)
			if dist.Unit == "" {
				dist.Unit = s.Unit
			}
			s.Unit = ""
		} else if err := rows.Scan(&s.Value, &s.Count, &s.Sessions); err != nil {
			return err
		}
		if len(dist.Rows) == customDistributionRows {
			dist.Truncated = true
			return nil
		}
		dist.Rows = append(dist.Rows, s)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return dist, nil
}

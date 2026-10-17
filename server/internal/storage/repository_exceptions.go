package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// The exception detail page (APM-11): occurrences page by page, the
// distribution of a group over a column or a context key, and its trend.
// Everything counts occurrences (sum of count, APM-3), not rows.

// exceptionSorts maps the list's sort parameter to its ORDER BY (the column
// aliases of exceptionGroupColumns; never user input in the SQL text).
var exceptionSorts = map[string]string{
	"":           "cnt DESC",
	"count":      "cnt DESC",
	"sessions":   "session_count DESC",
	"first_seen": "first_seen DESC",
	"last_seen":  "last_seen DESC",
}

// ValidExceptionSort reports whether sort is a sort of GetExceptionGroups.
func ValidExceptionSort(sort string) bool {
	_, ok := exceptionSorts[sort]
	return ok
}

// occurrenceColumns are the columns of one occurrence (scanOccurrence).
const occurrenceColumns = `timestamp, event_id, toInt64(count), app_version, build_type, platform, device_model, os_version,
			scene, session_id, device_id, ctx, breadcrumbs, toInt64(error_seq), toInt64(session_ms), unhandled, ctx_merged = 1`

func scanOccurrence(rows interface{ Scan(dest ...any) error }) (models.ExceptionOccurrence, error) {
	var o models.ExceptionOccurrence
	if err := rows.Scan(&o.Timestamp, &o.EventID, &o.Count, &o.AppVersion, &o.BuildType, &o.Platform, &o.DeviceModel,
		&o.OSVersion, &o.Scene, &o.SessionID, &o.DeviceID, &o.Context, &o.Breadcrumbs,
		&o.ErrorSeq, &o.SessionMs, &o.Unhandled, &o.CtxMerged); err != nil {
		return o, err
	}
	if o.Context == nil {
		o.Context = map[string]string{}
	}
	if o.Breadcrumbs == nil {
		o.Breadcrumbs = []string{}
	}
	return o, nil
}

// MaxOccurrencesPageSize bounds one page of GetExceptionOccurrences.
const MaxOccurrencesPageSize = 50

// GetExceptionOccurrences returns one page of the group's rows matching the
// filter, most recent first, and how many rows match in all.
func (r *Repository) GetExceptionOccurrences(ctx context.Context, fp string, f models.AdminFilter, page, pageSize int) (*models.ExceptionOccurrencesResponse, error) {
	if page < 1 || pageSize < 1 || pageSize > MaxOccurrencesPageSize {
		return nil, fmt.Errorf("page %d, page size %d", page, pageSize)
	}
	where, args := tableWhere(TableExceptions.Name, f)
	where += " AND fingerprint = ?"
	args = append(args, fp)

	resp := &models.ExceptionOccurrencesResponse{Fingerprint: fp, Occurrences: []models.ExceptionOccurrence{}, Page: page, PageSize: pageSize}
	if err := r.client.conn.QueryRow(ctx, "SELECT toInt64(count()) FROM apm_exceptions "+where, args...).Scan(&resp.TotalCount); err != nil {
		return nil, fmt.Errorf("occurrence count: %w", err)
	}
	if resp.TotalCount == 0 || int64(page-1)*int64(pageSize) >= resp.TotalCount {
		return resp, nil
	}
	query := fmt.Sprintf(`
		SELECT %s
		FROM apm_exceptions
		%s
		ORDER BY timestamp DESC, event_id
		LIMIT %d OFFSET %d
	`, occurrenceColumns, where, pageSize, (page-1)*pageSize)
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		o, err := scanOccurrence(rows)
		if err != nil {
			return err
		}
		resp.Occurrences = append(resp.Occurrences, o)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("occurrences: %w", err)
	}
	return resp, nil
}

// exceptionDistributionRows bounds the values of a distribution.
const exceptionDistributionRows = 50

// GetExceptionDistribution groups the group's occurrences by g: a column
// (app_version, platform, device_model, os_version, build_type) or a context
// key. For a context key, occurrences the SDK merged across contexts that
// lack the key (ctx_merged, APM-3) are reported as Merged, not under "".
func (r *Repository) GetExceptionDistribution(ctx context.Context, f models.AdminFilter, fp string, g CustomGroup) (*models.ExceptionDistribution, error) {
	if g.Kind == "prop" {
		return nil, fmt.Errorf("exceptions have no properties")
	}
	expr, exprArgs, merged, mergedArgs, err := g.groupExpr(TableExceptions.Name)
	if err != nil {
		return nil, err
	}
	where, whereArgs := tableWhere(TableExceptions.Name, f)
	where += " AND fingerprint = ?"
	whereArgs = append(whereArgs, fp)

	dist := &models.ExceptionDistribution{Fingerprint: fp, GroupBy: g.String(), Rows: []models.ExceptionDistributionRow{}}
	if err := r.client.conn.QueryRow(ctx, "SELECT toInt64(sum(count)) FROM apm_exceptions "+where, whereArgs...).Scan(&dist.Total); err != nil {
		return nil, fmt.Errorf("total: %w", err)
	}
	rowsWhere, rowsArgs := where, whereArgs
	if merged != "" {
		mArgs := append(append([]interface{}{}, whereArgs...), mergedArgs...)
		if err := r.client.conn.QueryRow(ctx, "SELECT toInt64(sum(count)) FROM apm_exceptions "+where+" AND "+merged, mArgs...).Scan(&dist.Merged); err != nil {
			return nil, fmt.Errorf("merged: %w", err)
		}
		rowsWhere += " AND NOT (" + merged + ")"
		rowsArgs = mArgs
	}
	query := fmt.Sprintf(`
		SELECT %s AS value, toInt64(sum(count)) AS cnt, toInt64(uniqExact(session_id))
		FROM apm_exceptions %s
		GROUP BY value
		ORDER BY cnt DESC, value
		LIMIT %d
	`, expr, rowsWhere, exceptionDistributionRows+1)
	args := append(append([]interface{}{}, exprArgs...), rowsArgs...)
	err = r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var row models.ExceptionDistributionRow
		if err := rows.Scan(&row.Value, &row.Count, &row.Sessions); err != nil {
			return err
		}
		if len(dist.Rows) == exceptionDistributionRows {
			dist.Truncated = true
			return nil
		}
		dist.Rows = append(dist.Rows, row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return dist, nil
}

// trendIntervals are the bucket sizes of a trend, in seconds; the smallest
// that gives at most maxTrendPoints buckets over the range is used.
var trendIntervals = []int{60, 300, 900, 3600, 3 * 3600, 6 * 3600, 86400}

const maxTrendPoints = 200

// TrendInterval returns the bucket size (seconds) of a trend over [start, end].
func TrendInterval(start, end time.Time) int {
	span := end.Sub(start).Seconds()
	for _, s := range trendIntervals {
		if span/float64(s) <= maxTrendPoints {
			return s
		}
	}
	return trendIntervals[len(trendIntervals)-1]
}

// exceptionTrend counts the occurrences of the rows matching where per
// bucket. The interval is one of trendIntervals (never user input).
func (r *Repository) exceptionTrend(ctx context.Context, where string, args []interface{}, f models.AdminFilter) (models.ExceptionTrend, error) {
	trend := models.ExceptionTrend{IntervalSeconds: TrendInterval(f.StartTime, f.EndTime), Points: []models.ExceptionTrendPoint{}}
	query := fmt.Sprintf(`
		SELECT toStartOfInterval(timestamp, INTERVAL %d SECOND) AS t, toInt64(sum(count)), toInt64(uniqExact(session_id))
		FROM apm_exceptions %s
		GROUP BY t
		ORDER BY t
		LIMIT %d
	`, trend.IntervalSeconds, where, 2*maxTrendPoints+2)
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var p models.ExceptionTrendPoint
		if err := rows.Scan(&p.Timestamp, &p.Count, &p.Sessions); err != nil {
			return err
		}
		p.Timestamp = p.Timestamp.UTC()
		trend.Points = append(trend.Points, p)
		return nil
	})
	return trend, err
}

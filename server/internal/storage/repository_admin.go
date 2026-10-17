package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// adminWhere renders the filter as a WHERE clause with "?" placeholders and
// returns its arguments. Every user-supplied value (versions, context keys
// and values) is a bound argument, quoted and escaped by the driver; nothing
// from the request is written into the SQL text.
func adminWhere(f models.AdminFilter) (string, []interface{}) {
	conds := []string{"timestamp >= ?", "timestamp <= ?"}
	args := []interface{}{f.StartTime, f.EndTime}

	if f.AppVersion != "" {
		conds = append(conds, "app_version = ?")
		args = append(args, f.AppVersion)
	}
	if f.Platform != "" {
		conds = append(conds, "platform = ?")
		args = append(args, f.Platform)
	}
	switch f.BuildType {
	case models.BuildFilterAll:
	case models.BuildFilterDev, models.BuildFilterEditor:
		conds = append(conds, "build_type = ?")
		args = append(args, f.BuildType)
	case models.BuildFilterUnknown:
		conds = append(conds, "build_type = ''")
	default: // BuildFilterRelease, and the zero value
		conds = append(conds, "build_type IN ('release', '')")
	}
	for _, c := range f.Context {
		conds = append(conds, "ctx[?] IN ?")
		args = append(args, c.Key, groupSet(c.Values))
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// GetDashboardSummary returns aggregated metrics for the dashboard
func (r *Repository) GetDashboardSummary(ctx context.Context, f models.AdminFilter) (*models.DashboardSummary, error) {
	summary := &models.DashboardSummary{
		TopVersions:  []models.VersionStats{},
		TopPlatforms: []models.PlatformStats{},
	}

	whereClause, args := adminWhere(f)

	// Get session count from perf_samples (unique sessions)
	sessionQuery := fmt.Sprintf(`
		SELECT toInt64(uniqExact(session_id)) as sessions, toInt64(count()) as events, %s as avg_fps
		FROM apm_perf_samples %s
	`, finiteFloat("avg(fps)"), whereClause)

	if err := r.client.conn.QueryRow(ctx, sessionQuery, args...).Scan(&summary.TotalSessions, &summary.TotalEvents, &summary.AvgFPS); err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}

	crashQuery := fmt.Sprintf(`SELECT toInt64(count()) FROM apm_crashes %s`, whereClause)
	if err := r.client.conn.QueryRow(ctx, crashQuery, args...).Scan(&summary.CrashCount); err != nil {
		return nil, fmt.Errorf("crash count: %w", err)
	}

	excQuery := fmt.Sprintf(`SELECT toInt64(sum(count)) FROM apm_exceptions %s`, whereClause)
	if err := r.client.conn.QueryRow(ctx, excQuery, args...).Scan(&summary.ExceptionCount); err != nil {
		return nil, fmt.Errorf("exception count: %w", err)
	}

	jankQuery := fmt.Sprintf(`SELECT toInt64(count()) FROM apm_janks %s`, whereClause)
	if err := r.client.conn.QueryRow(ctx, jankQuery, args...).Scan(&summary.JankCount); err != nil {
		return nil, fmt.Errorf("jank count: %w", err)
	}

	startupQuery := fmt.Sprintf(`SELECT %s FROM apm_startups %s`, finiteFloat("avg(phase1_ms + phase2_ms + tti_ms)"), whereClause)
	if err := r.client.conn.QueryRow(ctx, startupQuery, args...).Scan(&summary.AvgStartupMs); err != nil {
		return nil, fmt.Errorf("avg startup: %w", err)
	}

	// Calculate crash rate
	if summary.TotalSessions > 0 {
		summary.CrashRate = float64(summary.CrashCount) / float64(summary.TotalSessions) * 1000
	}

	// Get top versions
	versionQuery := fmt.Sprintf(`
		SELECT
			app_version,
			toInt64(uniqExact(session_id)) as session_count
		FROM apm_perf_samples %s
		GROUP BY app_version
		ORDER BY session_count DESC, app_version
		LIMIT 5
	`, whereClause)

	err := r.queryRows(ctx, versionQuery, args, func(rows driver.Rows) error {
		var vs models.VersionStats
		if err := rows.Scan(&vs.Version, &vs.SessionCount); err != nil {
			return err
		}
		summary.TopVersions = append(summary.TopVersions, vs)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("top versions: %w", err)
	}

	// Get top platforms
	platformQuery := fmt.Sprintf(`
		SELECT
			platform,
			toInt64(uniqExact(session_id)) as session_count,
			%s as avg_fps
		FROM apm_perf_samples %s
		GROUP BY platform
		ORDER BY session_count DESC, platform
		LIMIT 5
	`, finiteFloat("avg(fps)"), whereClause)

	err = r.queryRows(ctx, platformQuery, args, func(rows driver.Rows) error {
		var ps models.PlatformStats
		if err := rows.Scan(&ps.Platform, &ps.SessionCount, &ps.AvgFPS); err != nil {
			return err
		}
		summary.TopPlatforms = append(summary.TopPlatforms, ps)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("top platforms: %w", err)
	}

	return summary, nil
}

// GetTimeSeries returns time series data for a metric. interval is one of a
// fixed set chosen by the handler, never user input.
func (r *Repository) GetTimeSeries(ctx context.Context, metric, interval string, f models.AdminFilter) ([]models.TimeSeriesPoint, error) {
	whereClause, args := adminWhere(f)

	// The value is always scanned into a float64, so every expression is
	// converted to Float64 (count() is UInt64, avg(Float32) can be NaN).
	var table, value string
	switch metric {
	case "fps":
		table, value = "apm_perf_samples", "avg(fps)"
	case "frame_time":
		table, value = "apm_perf_samples", "avg(frame_time_ms)"
	case "crashes", "crash_count":
		table, value = "apm_crashes", "count()"
	case "exceptions":
		table, value = "apm_exceptions", "sum(count)"
	case "janks", "jank_count":
		table, value = "apm_janks", "count()"
	case "sessions":
		table, value = "apm_perf_samples", "uniqExact(session_id)"
	case "startup", "startup_time":
		table, value = "apm_startups", "avg(phase1_ms + phase2_ms + tti_ms)"
	default:
		return nil, fmt.Errorf("unknown metric: %s", metric)
	}
	query := fmt.Sprintf(`
		SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, %s
		FROM %s %s
		GROUP BY t ORDER BY t
	`, interval, finiteFloat(value), table, whereClause)

	points := []models.TimeSeriesPoint{}
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var p models.TimeSeriesPoint
		if err := rows.Scan(&p.Timestamp, &p.Value); err != nil {
			return err
		}
		points = append(points, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return points, nil
}

// GetDistribution returns distribution data for a metric
func (r *Repository) GetDistribution(ctx context.Context, metric, scene string, f models.AdminFilter) (*models.DistributionResponse, error) {
	whereClause, args := adminWhere(f)

	var valueColumn, table string
	var buckets []string

	switch metric {
	case "fps":
		table = "apm_perf_samples"
		valueColumn = "fps"
		buckets = []string{"0-15", "15-30", "30-45", "45-60", "60+"}
	case "frame_time":
		table = "apm_perf_samples"
		valueColumn = "frame_time_ms"
		buckets = []string{"0-16", "16-33", "33-50", "50-100", "100+"}
	case "startup":
		table = "apm_startups"
		valueColumn = "phase1_ms + phase2_ms + tti_ms"
		buckets = []string{"0-1000", "1000-2000", "2000-3000", "3000-5000", "5000+"}
	default:
		return nil, fmt.Errorf("unknown metric: %s", metric)
	}
	// apm_startups has no scene column; the scene filter applies to perf samples only.
	if scene != "" && table == "apm_perf_samples" {
		whereClause += " AND scene = ?"
		args = append(args, scene)
	}

	// Get percentiles
	pctQuery := fmt.Sprintf(`
		SELECT %s as p50, %s as p90, %s as p95, %s as p99
		FROM %s %s
	`, finiteFloat("quantile(0.5)("+valueColumn+")"), finiteFloat("quantile(0.9)("+valueColumn+")"),
		finiteFloat("quantile(0.95)("+valueColumn+")"), finiteFloat("quantile(0.99)("+valueColumn+")"), table, whereClause)

	resp := &models.DistributionResponse{Metric: metric, Buckets: []models.DistributionBucket{}}
	if err := r.client.conn.QueryRow(ctx, pctQuery, args...).Scan(&resp.P50, &resp.P90, &resp.P95, &resp.P99); err != nil {
		return nil, fmt.Errorf("percentiles: %w", err)
	}

	// Build bucket query based on metric
	var bucketQuery string
	switch metric {
	case "fps":
		bucketQuery = fmt.Sprintf(`
			SELECT
				multiIf(fps < 15, '0-15', fps < 30, '15-30', fps < 45, '30-45', fps < 60, '45-60', '60+') as bucket,
				toInt64(count()) as cnt
			FROM %s %s
			GROUP BY bucket
		`, table, whereClause)
	case "frame_time":
		bucketQuery = fmt.Sprintf(`
			SELECT
				multiIf(frame_time_ms < 16, '0-16', frame_time_ms < 33, '16-33', frame_time_ms < 50, '33-50', frame_time_ms < 100, '50-100', '100+') as bucket,
				toInt64(count()) as cnt
			FROM %s %s
			GROUP BY bucket
		`, table, whereClause)
	case "startup":
		bucketQuery = fmt.Sprintf(`
			SELECT
				multiIf(%s < 1000, '0-1000', %s < 2000, '1000-2000', %s < 3000, '2000-3000', %s < 5000, '3000-5000', '5000+') as bucket,
				toInt64(count()) as cnt
			FROM %s %s
			GROUP BY bucket
		`, valueColumn, valueColumn, valueColumn, valueColumn, table, whereClause)
	}

	bucketCounts := make(map[string]int64)
	var total int64
	err := r.queryRows(ctx, bucketQuery, args, func(rows driver.Rows) error {
		var bucket string
		var count int64
		if err := rows.Scan(&bucket, &count); err != nil {
			return err
		}
		bucketCounts[bucket] = count
		total += count
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("buckets: %w", err)
	}

	// Build response in order
	for _, b := range buckets {
		count := bucketCounts[b]
		pct := float64(0)
		if total > 0 {
			pct = float64(count) / float64(total) * 100
		}
		resp.Buckets = append(resp.Buckets, models.DistributionBucket{
			Bucket: b,
			Count:  count,
			Pct:    pct,
		})
	}

	return resp, nil
}

// GetAppVersions returns list of app versions
func (r *Repository) GetAppVersions(ctx context.Context) ([]string, error) {
	query := `
		SELECT DISTINCT app_version
		FROM apm_perf_samples
		WHERE timestamp >= now() - INTERVAL 30 DAY
		ORDER BY app_version DESC
		LIMIT 50
	`

	versions := []string{}
	err := r.queryRows(ctx, query, nil, func(rows driver.Rows) error {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		versions = append(versions, v)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return versions, nil
}

// GetScenes returns list of scenes
func (r *Repository) GetScenes(ctx context.Context, appVersion string) ([]string, error) {
	whereClause := "WHERE timestamp >= now() - INTERVAL 30 DAY"
	var args []interface{}

	if appVersion != "" {
		whereClause += " AND app_version = ?"
		args = append(args, appVersion)
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT scene
		FROM apm_perf_samples
		%s AND scene != ''
		ORDER BY scene
		LIMIT 100
	`, whereClause)

	scenes := []string{}
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		scenes = append(scenes, s)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return scenes, nil
}

// GetCrashGroups returns grouped crash data
func (r *Repository) GetCrashGroups(ctx context.Context, f models.AdminFilter, page, pageSize int) ([]models.CrashGroup, int64, error) {
	whereClause, args := adminWhere(f)

	// Get total count
	countQuery := fmt.Sprintf(`SELECT toInt64(uniqExact(fingerprint)) FROM apm_crashes %s`, whereClause)
	var totalCount int64
	if err := r.client.conn.QueryRow(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("crash group count: %w", err)
	}

	// Get crash groups
	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT
			fingerprint,
			any(crash_type) as crash_type,
			toInt64(count()) as cnt,
			toInt64(uniqExact(session_id)) as session_count,
			min(timestamp) as first_seen,
			max(timestamp) as last_seen,
			groupUniqArray(app_version) as versions,
			topK(5)(device_model) as devices
		FROM apm_crashes
		%s
		GROUP BY fingerprint
		ORDER BY cnt DESC, fingerprint
		LIMIT %d OFFSET %d
	`, whereClause, pageSize, offset)

	crashes := []models.CrashGroup{}
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var c models.CrashGroup
		var versions, devices []string
		if err := rows.Scan(&c.Fingerprint, &c.CrashType, &c.Count, &c.SessionCount, &c.FirstSeen, &c.LastSeen, &versions, &devices); err != nil {
			return err
		}
		if versions == nil {
			versions = []string{}
		}
		if devices == nil {
			devices = []string{}
		}
		c.AffectedVersions = versions
		c.TopDevices = devices
		crashes = append(crashes, c)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return crashes, totalCount, nil
}

// GetCrashDetail returns detailed crash information
func (r *Repository) GetCrashDetail(ctx context.Context, fingerprint string, f models.AdminFilter) (*models.CrashDetail, error) {
	whereClause, args := adminWhere(f)
	whereClause += " AND fingerprint = ?"
	detailArgs := append(args, fingerprint)

	// Get basic info and sample stack
	query := fmt.Sprintf(`
		SELECT
			fingerprint,
			any(crash_type),
			any(stack),
			toInt64(count()),
			toInt64(uniqExact(session_id)),
			min(timestamp),
			max(timestamp)
		FROM apm_crashes
		%s
		GROUP BY fingerprint
	`, whereClause)

	detail := &models.CrashDetail{
		Occurrences: []models.CrashOccurrence{},
		VersionDist: []models.VersionDist{},
		DeviceDist:  []models.DeviceDist{},
	}
	row := r.client.conn.QueryRow(ctx, query, detailArgs...)
	if err := row.Scan(&detail.Fingerprint, &detail.CrashType, &detail.Stack, &detail.Count, &detail.SessionCount, &detail.FirstSeen, &detail.LastSeen); err != nil {
		return nil, err
	}

	// Get recent occurrences
	occQuery := fmt.Sprintf(`
		SELECT timestamp, app_version, platform, device_model, os_version, scene, breadcrumbs, build_type, ctx
		FROM apm_crashes
		%s
		ORDER BY timestamp DESC
		LIMIT 10
	`, whereClause)

	err := r.queryRows(ctx, occQuery, detailArgs, func(rows driver.Rows) error {
		var occ models.CrashOccurrence
		if err := rows.Scan(&occ.Timestamp, &occ.AppVersion, &occ.Platform, &occ.DeviceModel, &occ.OSVersion, &occ.Scene, &occ.Breadcrumbs, &occ.BuildType, &occ.Context); err != nil {
			return err
		}
		if occ.Context == nil {
			occ.Context = map[string]string{}
		}
		detail.Occurrences = append(detail.Occurrences, occ)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("occurrences: %w", err)
	}

	// Get version distribution
	versionQuery := fmt.Sprintf(`
		SELECT app_version, toInt64(count()) as cnt
		FROM apm_crashes
		%s
		GROUP BY app_version
		ORDER BY cnt DESC, app_version
	`, whereClause)
	err = r.queryRows(ctx, versionQuery, detailArgs, func(rows driver.Rows) error {
		var vd models.VersionDist
		if err := rows.Scan(&vd.Version, &vd.Count); err != nil {
			return err
		}
		detail.VersionDist = append(detail.VersionDist, vd)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("version distribution: %w", err)
	}

	// Get device distribution
	deviceQuery := fmt.Sprintf(`
		SELECT device_model, toInt64(count()) as cnt
		FROM apm_crashes
		%s
		GROUP BY device_model
		ORDER BY cnt DESC, device_model
		LIMIT 10
	`, whereClause)
	err = r.queryRows(ctx, deviceQuery, detailArgs, func(rows driver.Rows) error {
		var dd models.DeviceDist
		if err := rows.Scan(&dd.Device, &dd.Count); err != nil {
			return err
		}
		detail.DeviceDist = append(detail.DeviceDist, dd)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("device distribution: %w", err)
	}

	return detail, nil
}

// GetExceptionGroups returns grouped exception data
func (r *Repository) GetExceptionGroups(ctx context.Context, f models.AdminFilter, page, pageSize int) ([]models.ExceptionGroup, int64, error) {
	whereClause, args := adminWhere(f)

	// Get total count
	countQuery := fmt.Sprintf(`SELECT toInt64(uniqExact(fingerprint)) FROM apm_exceptions %s`, whereClause)
	var totalCount int64
	if err := r.client.conn.QueryRow(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("exception group count: %w", err)
	}

	// Get exception groups
	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT
			fingerprint,
			any(message) as message,
			toInt64(sum(count)) as cnt,
			toInt64(uniqExact(session_id)) as session_count,
			min(timestamp) as first_seen,
			max(timestamp) as last_seen
		FROM apm_exceptions
		%s
		GROUP BY fingerprint
		ORDER BY cnt DESC, fingerprint
		LIMIT %d OFFSET %d
	`, whereClause, pageSize, offset)

	exceptions := []models.ExceptionGroup{}
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var e models.ExceptionGroup
		if err := rows.Scan(&e.Fingerprint, &e.Message, &e.Count, &e.SessionCount, &e.FirstSeen, &e.LastSeen); err != nil {
			return err
		}
		exceptions = append(exceptions, e)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return exceptions, totalCount, nil
}

// contextSources maps the "source" parameter of the context endpoints to
// tables; "all" (or empty) means every event table.
var contextSources = map[string]string{
	"perf":        "apm_perf_samples",
	"janks":       "apm_janks",
	"startups":    "apm_startups",
	"scene_loads": "apm_scene_loads",
	"exceptions":  "apm_exceptions",
	"crashes":     "apm_crashes",
}

// ValidContextSource reports whether source names a context source.
func ValidContextSource(source string) bool {
	_, ok := contextSources[source]
	return ok || source == "" || source == "all"
}

func contextSourceTables(source string) ([]string, error) {
	if t, ok := contextSources[source]; ok {
		return []string{t}, nil
	}
	if source == "" || source == "all" {
		tables := make([]string, len(EventTables))
		for i, t := range EventTables {
			tables[i] = t.Name
		}
		return tables, nil
	}
	return nil, fmt.Errorf("unknown source: %s", source)
}

// unionOverTables runs inner (one SELECT with %s for the table and %s for the
// WHERE clause) on every table and combines the rows with UNION ALL. Each
// part gets the full argument list: before (arguments that precede the
// WHERE clause), the filter's, then after.
func unionOverTables(tables []string, inner string, f models.AdminFilter, before, after []interface{}) (string, []interface{}) {
	whereClause, whereArgs := adminWhere(f)
	parts := make([]string, len(tables))
	var args []interface{}
	for i, t := range tables {
		parts[i] = fmt.Sprintf(inner, t, whereClause)
		args = append(args, before...)
		args = append(args, whereArgs...)
		args = append(args, after...)
	}
	return strings.Join(parts, "\n\t\tUNION ALL\n\t\t"), args
}

// GetContextKeys lists the context keys seen in the filtered events, with
// the number of events that carry each key.
func (r *Repository) GetContextKeys(ctx context.Context, f models.AdminFilter, source string) ([]models.ContextKeyStat, error) {
	tables, err := contextSourceTables(source)
	if err != nil {
		return nil, err
	}
	union, args := unionOverTables(tables,
		`SELECT arrayJoin(mapKeys(ctx)) AS key, count() AS c FROM %s %s GROUP BY key`, f, nil, nil)
	query := `SELECT key, toInt64(sum(c)) AS cnt FROM (` + union + `) GROUP BY key ORDER BY cnt DESC, key LIMIT 200`

	keys := []models.ContextKeyStat{}
	err = r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var k models.ContextKeyStat
		if err := rows.Scan(&k.Key, &k.Count); err != nil {
			return err
		}
		keys = append(keys, k)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// GetContextValues returns the most frequent values of one context key among
// the filtered events that carry it.
func (r *Repository) GetContextValues(ctx context.Context, f models.AdminFilter, source, key string, limit int) ([]models.ContextValueStat, error) {
	tables, err := contextSourceTables(source)
	if err != nil {
		return nil, err
	}
	union, args := unionOverTables(tables,
		`SELECT ctx[?] AS value, count() AS c FROM %s %s AND mapContains(ctx, ?) GROUP BY value`,
		f, []interface{}{key}, []interface{}{key})
	query := fmt.Sprintf(`SELECT value, toInt64(sum(c)) AS cnt FROM (%s) GROUP BY value ORDER BY cnt DESC, value LIMIT %d`, union, limit)

	values := []models.ContextValueStat{}
	err = r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var v models.ContextValueStat
		if err := rows.Scan(&v.Value, &v.Count); err != nil {
			return err
		}
		values = append(values, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return values, nil
}

// contextMetric describes one metric of GetContextDistribution: the table,
// the count expression and the value whose p50/p95 are reported ("" for none).
type contextMetric struct {
	table, count, value, unit string
}

var contextMetrics = map[string]contextMetric{
	"fps":        {"apm_perf_samples", "count()", "fps", "fps"},
	"frame_time": {"apm_perf_samples", "count()", "frame_time_ms", "ms"},
	"janks":      {"apm_janks", "count()", "duration_ms", "ms"},
	"startup":    {"apm_startups", "count()", "phase1_ms + phase2_ms + tti_ms", "ms"},
	"scene_load": {"apm_scene_loads", "count()", "load_ms", "ms"},
	"exceptions": {"apm_exceptions", "sum(count)", "", ""},
	"crashes":    {"apm_crashes", "count()", "", ""},
}

// ValidContextMetric reports whether metric is a GetContextDistribution metric.
func ValidContextMetric(metric string) bool {
	_, ok := contextMetrics[metric]
	return ok
}

// contextDistributionRows is the number of values returned; the rest is
// reported as Truncated.
const contextDistributionRows = 50

// GetContextDistribution groups the filtered events of one metric by the
// value of a context key. fingerprint, if set, narrows exceptions/crashes to
// one group (ignored for other metrics).
func (r *Repository) GetContextDistribution(ctx context.Context, f models.AdminFilter, metric, key, fingerprint string) (*models.ContextDistribution, error) {
	m, ok := contextMetrics[metric]
	if !ok {
		return nil, fmt.Errorf("unknown metric: %s", metric)
	}
	whereClause, whereArgs := adminWhere(f)
	if fingerprint != "" && m.value == "" {
		whereClause += " AND fingerprint = ?"
		whereArgs = append(whereArgs, fingerprint)
	}
	p50, p95 := "toFloat64(0)", "toFloat64(0)"
	if m.value != "" {
		p50, p95 = finiteFloat("quantile(0.5)("+m.value+")"), finiteFloat("quantile(0.95)("+m.value+")")
	}

	dist := &models.ContextDistribution{Key: key, Metric: metric, Unit: m.unit, Rows: []models.ContextDistributionRow{}}
	totalQuery := fmt.Sprintf(`SELECT toInt64(%s) FROM %s %s`, m.count, m.table, whereClause)
	if err := r.client.conn.QueryRow(ctx, totalQuery, whereArgs...).Scan(&dist.Total); err != nil {
		return nil, fmt.Errorf("total: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT ctx[?] AS value, toInt64(%s) AS cnt, toInt64(uniqExact(session_id)) AS sessions, %s AS p50, %s AS p95
		FROM %s %s
		GROUP BY value
		ORDER BY cnt DESC, value
		LIMIT %d
	`, m.count, p50, p95, m.table, whereClause, contextDistributionRows+1)
	args := append([]interface{}{key}, whereArgs...)
	err := r.queryRows(ctx, query, args, func(rows driver.Rows) error {
		var row models.ContextDistributionRow
		if err := rows.Scan(&row.Value, &row.Count, &row.Sessions, &row.P50, &row.P95); err != nil {
			return err
		}
		if len(dist.Rows) == contextDistributionRows {
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

// finiteFloat converts an aggregate to Float64 and replaces NaN/Inf with 0:
// avg() and quantile() of no rows are NaN, which encoding/json cannot encode.
func finiteFloat(expr string) string {
	return "ifNotFinite(toFloat64(" + expr + "), 0)"
}

// queryRows runs query and calls scan for every row, returning the first
// error from the query, a scan, or the row iteration.
func (r *Repository) queryRows(ctx context.Context, query string, args []interface{}, scan func(driver.Rows) error) error {
	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

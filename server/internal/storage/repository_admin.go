package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// GetDashboardSummary returns aggregated metrics for the dashboard
func (r *Repository) GetDashboardSummary(ctx context.Context, startTime, endTime time.Time, appVersion, platform string) (*models.DashboardSummary, error) {
	summary := &models.DashboardSummary{}

	// Build WHERE clause
	whereClause := "WHERE timestamp >= ? AND timestamp <= ?"
	args := []interface{}{startTime, endTime}

	if appVersion != "" {
		whereClause += " AND app_version = ?"
		args = append(args, appVersion)
	}
	if platform != "" {
		whereClause += " AND platform = ?"
		args = append(args, platform)
	}

	// Get session count from perf_samples (unique sessions)
	sessionQuery := fmt.Sprintf(`
		SELECT uniqExact(session_id) as sessions, count() as events, avg(fps) as avg_fps
		FROM apm_perf_samples %s
	`, whereClause)

	row := r.client.conn.QueryRow(ctx, sessionQuery, args...)
	var events int64
	var avgFPS float64
	if err := row.Scan(&summary.TotalSessions, &events, &avgFPS); err == nil {
		summary.TotalEvents = events
		summary.AvgFPS = avgFPS
	}

	// Get crash count
	crashQuery := fmt.Sprintf(`SELECT count() FROM apm_crashes %s`, whereClause)
	row = r.client.conn.QueryRow(ctx, crashQuery, args...)
	row.Scan(&summary.CrashCount)

	// Get exception count
	excQuery := fmt.Sprintf(`SELECT sum(count) FROM apm_exceptions %s`, whereClause)
	row = r.client.conn.QueryRow(ctx, excQuery, args...)
	row.Scan(&summary.ExceptionCount)

	// Get jank count
	jankQuery := fmt.Sprintf(`SELECT count() FROM apm_janks %s`, whereClause)
	row = r.client.conn.QueryRow(ctx, jankQuery, args...)
	row.Scan(&summary.JankCount)

	// Get avg startup time
	startupQuery := fmt.Sprintf(`SELECT avg(phase1_ms + phase2_ms + tti_ms) FROM apm_startups %s`, whereClause)
	row = r.client.conn.QueryRow(ctx, startupQuery, args...)
	row.Scan(&summary.AvgStartupMs)

	// Calculate crash rate
	if summary.TotalSessions > 0 {
		summary.CrashRate = float64(summary.CrashCount) / float64(summary.TotalSessions) * 1000
	}

	// Get top versions
	versionQuery := fmt.Sprintf(`
		SELECT
			app_version,
			uniqExact(session_id) as session_count
		FROM apm_perf_samples %s
		GROUP BY app_version
		ORDER BY session_count DESC
		LIMIT 5
	`, whereClause)

	rows, err := r.client.conn.Query(ctx, versionQuery, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var vs models.VersionStats
			rows.Scan(&vs.Version, &vs.SessionCount)
			summary.TopVersions = append(summary.TopVersions, vs)
		}
	}

	// Get top platforms
	platformQuery := fmt.Sprintf(`
		SELECT
			platform,
			uniqExact(session_id) as session_count,
			avg(fps) as avg_fps
		FROM apm_perf_samples %s
		GROUP BY platform
		ORDER BY session_count DESC
		LIMIT 5
	`, whereClause)

	rows, err = r.client.conn.Query(ctx, platformQuery, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ps models.PlatformStats
			rows.Scan(&ps.Platform, &ps.SessionCount, &ps.AvgFPS)
			summary.TopPlatforms = append(summary.TopPlatforms, ps)
		}
	}

	return summary, nil
}

// GetTimeSeries returns time series data for a metric
func (r *Repository) GetTimeSeries(ctx context.Context, metric string, startTime, endTime time.Time, interval, appVersion, platform string) ([]models.TimeSeriesPoint, error) {
	var query string
	whereClause := "WHERE timestamp >= ? AND timestamp <= ?"
	args := []interface{}{startTime, endTime}

	if appVersion != "" {
		whereClause += " AND app_version = ?"
		args = append(args, appVersion)
	}
	if platform != "" {
		whereClause += " AND platform = ?"
		args = append(args, platform)
	}

	switch metric {
	case "fps":
		query = fmt.Sprintf(`
			SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, avg(fps)
			FROM apm_perf_samples %s
			GROUP BY t ORDER BY t
		`, interval, whereClause)
	case "frame_time":
		query = fmt.Sprintf(`
			SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, avg(frame_time_ms)
			FROM apm_perf_samples %s
			GROUP BY t ORDER BY t
		`, interval, whereClause)
	case "crashes":
		query = fmt.Sprintf(`
			SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, count()
			FROM apm_crashes %s
			GROUP BY t ORDER BY t
		`, interval, whereClause)
	case "exceptions":
		query = fmt.Sprintf(`
			SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, sum(count)
			FROM apm_exceptions %s
			GROUP BY t ORDER BY t
		`, interval, whereClause)
	case "janks":
		query = fmt.Sprintf(`
			SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, count()
			FROM apm_janks %s
			GROUP BY t ORDER BY t
		`, interval, whereClause)
	case "sessions":
		query = fmt.Sprintf(`
			SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, uniqExact(session_id)
			FROM apm_perf_samples %s
			GROUP BY t ORDER BY t
		`, interval, whereClause)
	case "startup":
		query = fmt.Sprintf(`
			SELECT toStartOfInterval(timestamp, INTERVAL %s) as t, avg(phase1_ms + phase2_ms + tti_ms)
			FROM apm_startups %s
			GROUP BY t ORDER BY t
		`, interval, whereClause)
	default:
		return nil, fmt.Errorf("unknown metric: %s", metric)
	}

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []models.TimeSeriesPoint
	for rows.Next() {
		var p models.TimeSeriesPoint
		if err := rows.Scan(&p.Timestamp, &p.Value); err != nil {
			continue
		}
		points = append(points, p)
	}

	return points, nil
}

// GetDistribution returns distribution data for a metric
func (r *Repository) GetDistribution(ctx context.Context, metric string, startTime, endTime time.Time, appVersion, platform, scene string) (*models.DistributionResponse, error) {
	whereClause := "WHERE timestamp >= ? AND timestamp <= ?"
	args := []interface{}{startTime, endTime}

	if appVersion != "" {
		whereClause += " AND app_version = ?"
		args = append(args, appVersion)
	}
	if platform != "" {
		whereClause += " AND platform = ?"
		args = append(args, platform)
	}
	if scene != "" {
		whereClause += " AND scene = ?"
		args = append(args, scene)
	}

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

	// Get percentiles
	pctQuery := fmt.Sprintf(`
		SELECT
			quantile(0.5)(%s) as p50,
			quantile(0.9)(%s) as p90,
			quantile(0.95)(%s) as p95,
			quantile(0.99)(%s) as p99
		FROM %s %s
	`, valueColumn, valueColumn, valueColumn, valueColumn, table, whereClause)

	resp := &models.DistributionResponse{Metric: metric}
	row := r.client.conn.QueryRow(ctx, pctQuery, args...)
	row.Scan(&resp.P50, &resp.P90, &resp.P95, &resp.P99)

	// Build bucket query based on metric
	var bucketQuery string
	switch metric {
	case "fps":
		bucketQuery = fmt.Sprintf(`
			SELECT
				multiIf(fps < 15, '0-15', fps < 30, '15-30', fps < 45, '30-45', fps < 60, '45-60', '60+') as bucket,
				count() as cnt
			FROM %s %s
			GROUP BY bucket
		`, table, whereClause)
	case "frame_time":
		bucketQuery = fmt.Sprintf(`
			SELECT
				multiIf(frame_time_ms < 16, '0-16', frame_time_ms < 33, '16-33', frame_time_ms < 50, '33-50', frame_time_ms < 100, '50-100', '100+') as bucket,
				count() as cnt
			FROM %s %s
			GROUP BY bucket
		`, table, whereClause)
	case "startup":
		bucketQuery = fmt.Sprintf(`
			SELECT
				multiIf(%s < 1000, '0-1000', %s < 2000, '1000-2000', %s < 3000, '2000-3000', %s < 5000, '3000-5000', '5000+') as bucket,
				count() as cnt
			FROM %s %s
			GROUP BY bucket
		`, valueColumn, valueColumn, valueColumn, valueColumn, table, whereClause)
	}

	rows, err := r.client.conn.Query(ctx, bucketQuery, args...)
	if err != nil {
		return resp, nil
	}
	defer rows.Close()

	bucketCounts := make(map[string]int64)
	var total int64
	for rows.Next() {
		var bucket string
		var count int64
		rows.Scan(&bucket, &count)
		bucketCounts[bucket] = count
		total += count
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

	rows, err := r.client.conn.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		versions = append(versions, v)
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

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scenes []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		scenes = append(scenes, s)
	}

	return scenes, nil
}

// GetCrashGroups returns grouped crash data
func (r *Repository) GetCrashGroups(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.CrashGroup, int64, error) {
	whereClause := "WHERE timestamp >= ? AND timestamp <= ?"
	args := []interface{}{startTime, endTime}

	if appVersion != "" {
		whereClause += " AND app_version = ?"
		args = append(args, appVersion)
	}
	if platform != "" {
		whereClause += " AND platform = ?"
		args = append(args, platform)
	}

	// Get total count
	countQuery := fmt.Sprintf(`SELECT count(DISTINCT fingerprint) FROM apm_crashes %s`, whereClause)
	var totalCount int64
	r.client.conn.QueryRow(ctx, countQuery, args...).Scan(&totalCount)

	// Get crash groups
	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT
			fingerprint,
			any(crash_type) as crash_type,
			count() as cnt,
			uniqExact(session_id) as session_count,
			min(timestamp) as first_seen,
			max(timestamp) as last_seen,
			groupArray(DISTINCT app_version) as versions,
			topK(5)(device_model) as devices
		FROM apm_crashes
		%s
		GROUP BY fingerprint
		ORDER BY cnt DESC
		LIMIT %d OFFSET %d
	`, whereClause, pageSize, offset)

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var crashes []models.CrashGroup
	for rows.Next() {
		var c models.CrashGroup
		var versions, devices []string
		if err := rows.Scan(&c.Fingerprint, &c.CrashType, &c.Count, &c.SessionCount, &c.FirstSeen, &c.LastSeen, &versions, &devices); err != nil {
			continue
		}
		c.AffectedVersions = versions
		c.TopDevices = devices
		crashes = append(crashes, c)
	}

	return crashes, totalCount, nil
}

// GetCrashDetail returns detailed crash information
func (r *Repository) GetCrashDetail(ctx context.Context, fingerprint string, startTime, endTime time.Time) (*models.CrashDetail, error) {
	// Get basic info and sample stack
	query := `
		SELECT
			fingerprint,
			any(crash_type),
			any(stack),
			count(),
			uniqExact(session_id),
			min(timestamp),
			max(timestamp)
		FROM apm_crashes
		WHERE fingerprint = ? AND timestamp >= ? AND timestamp <= ?
		GROUP BY fingerprint
	`

	detail := &models.CrashDetail{}
	row := r.client.conn.QueryRow(ctx, query, fingerprint, startTime, endTime)
	if err := row.Scan(&detail.Fingerprint, &detail.CrashType, &detail.Stack, &detail.Count, &detail.SessionCount, &detail.FirstSeen, &detail.LastSeen); err != nil {
		return nil, err
	}

	// Get recent occurrences
	occQuery := `
		SELECT timestamp, app_version, platform, device_model, os_version, scene, breadcrumbs
		FROM apm_crashes
		WHERE fingerprint = ? AND timestamp >= ? AND timestamp <= ?
		ORDER BY timestamp DESC
		LIMIT 10
	`

	rows, err := r.client.conn.Query(ctx, occQuery, fingerprint, startTime, endTime)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var occ models.CrashOccurrence
			rows.Scan(&occ.Timestamp, &occ.AppVersion, &occ.Platform, &occ.DeviceModel, &occ.OSVersion, &occ.Scene, &occ.Breadcrumbs)
			detail.Occurrences = append(detail.Occurrences, occ)
		}
	}

	// Get version distribution
	versionQuery := `
		SELECT app_version, count() as cnt
		FROM apm_crashes
		WHERE fingerprint = ? AND timestamp >= ? AND timestamp <= ?
		GROUP BY app_version
		ORDER BY cnt DESC
	`
	rows, err = r.client.conn.Query(ctx, versionQuery, fingerprint, startTime, endTime)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var vd models.VersionDist
			rows.Scan(&vd.Version, &vd.Count)
			detail.VersionDist = append(detail.VersionDist, vd)
		}
	}

	// Get device distribution
	deviceQuery := `
		SELECT device_model, count() as cnt
		FROM apm_crashes
		WHERE fingerprint = ? AND timestamp >= ? AND timestamp <= ?
		GROUP BY device_model
		ORDER BY cnt DESC
		LIMIT 10
	`
	rows, err = r.client.conn.Query(ctx, deviceQuery, fingerprint, startTime, endTime)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var dd models.DeviceDist
			rows.Scan(&dd.Device, &dd.Count)
			detail.DeviceDist = append(detail.DeviceDist, dd)
		}
	}

	return detail, nil
}

// GetExceptionGroups returns grouped exception data
func (r *Repository) GetExceptionGroups(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.ExceptionGroup, int64, error) {
	whereClause := "WHERE timestamp >= ? AND timestamp <= ?"
	args := []interface{}{startTime, endTime}

	if appVersion != "" {
		whereClause += " AND app_version = ?"
		args = append(args, appVersion)
	}
	if platform != "" {
		whereClause += " AND platform = ?"
		args = append(args, platform)
	}

	// Get total count
	countQuery := fmt.Sprintf(`SELECT count(DISTINCT fingerprint) FROM apm_exceptions %s`, whereClause)
	var totalCount int64
	r.client.conn.QueryRow(ctx, countQuery, args...).Scan(&totalCount)

	// Get exception groups
	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT
			fingerprint,
			any(message) as message,
			sum(count) as cnt,
			uniqExact(session_id) as session_count,
			min(timestamp) as first_seen,
			max(timestamp) as last_seen
		FROM apm_exceptions
		%s
		GROUP BY fingerprint
		ORDER BY cnt DESC
		LIMIT %d OFFSET %d
	`, whereClause, pageSize, offset)

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var exceptions []models.ExceptionGroup
	for rows.Next() {
		var e models.ExceptionGroup
		if err := rows.Scan(&e.Fingerprint, &e.Message, &e.Count, &e.SessionCount, &e.FirstSeen, &e.LastSeen); err != nil {
			continue
		}
		exceptions = append(exceptions, e)
	}

	return exceptions, totalCount, nil
}

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

type Repository struct {
	client *ClickHouseClient
	logger *zap.Logger
}

func NewRepository(client *ClickHouseClient, logger *zap.Logger) *Repository {
	return &Repository{
		client: client,
		logger: logger,
	}
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.client.Ping(ctx)
}

func (r *Repository) Migrate(ctx context.Context) error {
	return r.client.Migrate(ctx)
}

// InsertPerfSamples batch inserts performance samples
func (r *Repository) InsertPerfSamples(ctx context.Context, samples []models.PerfSample) error {
	if len(samples) == 0 {
		return nil
	}

	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_perf_samples")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, s := range samples {
		err := batch.Append(
			s.Timestamp,
			s.AppVersion,
			s.Platform,
			s.DeviceModel,
			s.OSVersion,
			s.SessionID,
			s.DeviceID,
			s.Scene,
			s.FPS,
			s.FrameTimeMs,
			s.MainThreadMs,
			s.GCAllocKB,
			s.MemMB,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// InsertJanks batch inserts jank events
func (r *Repository) InsertJanks(ctx context.Context, janks []models.Jank) error {
	if len(janks) == 0 {
		return nil
	}

	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_janks")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, j := range janks {
		err := batch.Append(
			j.Timestamp,
			j.AppVersion,
			j.Platform,
			j.DeviceModel,
			j.OSVersion,
			j.SessionID,
			j.DeviceID,
			j.Scene,
			j.DurationMs,
			j.MaxFrameMs,
			j.RecentGCCount,
			j.RecentGCAllocKB,
			j.RecentEvents,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// InsertStartups batch inserts startup events
func (r *Repository) InsertStartups(ctx context.Context, startups []models.Startup) error {
	if len(startups) == 0 {
		return nil
	}

	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_startups")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, s := range startups {
		err := batch.Append(
			s.Timestamp,
			s.AppVersion,
			s.Platform,
			s.DeviceModel,
			s.OSVersion,
			s.SessionID,
			s.DeviceID,
			s.Phase1Ms,
			s.Phase2Ms,
			s.TTIMs,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// InsertSceneLoads batch inserts scene load events
func (r *Repository) InsertSceneLoads(ctx context.Context, loads []models.SceneLoad) error {
	if len(loads) == 0 {
		return nil
	}

	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_scene_loads")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, l := range loads {
		err := batch.Append(
			l.Timestamp,
			l.AppVersion,
			l.Platform,
			l.DeviceModel,
			l.SessionID,
			l.DeviceID,
			l.SceneName,
			l.LoadMs,
			l.ActivateMs,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// InsertExceptions batch inserts exception events
func (r *Repository) InsertExceptions(ctx context.Context, exceptions []models.Exception) error {
	if len(exceptions) == 0 {
		return nil
	}

	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_exceptions")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, e := range exceptions {
		err := batch.Append(
			e.Timestamp,
			e.AppVersion,
			e.Platform,
			e.DeviceModel,
			e.OSVersion,
			e.SessionID,
			e.DeviceID,
			e.Scene,
			e.Fingerprint,
			e.Message,
			e.Stack,
			e.Count,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// InsertCrashes batch inserts crash events
func (r *Repository) InsertCrashes(ctx context.Context, crashes []models.Crash) error {
	if len(crashes) == 0 {
		return nil
	}

	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_crashes")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, c := range crashes {
		err := batch.Append(
			c.Timestamp,
			c.AppVersion,
			c.Platform,
			c.DeviceModel,
			c.OSVersion,
			c.SessionID,
			c.DeviceID,
			c.Scene,
			c.CrashType,
			c.Fingerprint,
			c.Stack,
			c.Breadcrumbs,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// Query methods

func (r *Repository) QueryFPSMetrics(ctx context.Context, filter models.QueryFilter) ([]models.FPSMetrics, error) {
	query := `
		SELECT
			app_version,
			platform,
			scene,
			count() as count,
			avg(fps) as avg_fps,
			quantile(0.5)(fps) as p50_fps,
			quantile(0.9)(fps) as p90_fps,
			quantile(0.95)(fps) as p95_fps,
			quantile(0.99)(fps) as p99_fps
		FROM apm_perf_samples
		WHERE timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{filter.StartTime, filter.EndTime}

	if filter.AppVersion != "" {
		query += " AND app_version = ?"
		args = append(args, filter.AppVersion)
	}
	if filter.Platform != "" {
		query += " AND platform = ?"
		args = append(args, filter.Platform)
	}
	if filter.Scene != "" {
		query += " AND scene = ?"
		args = append(args, filter.Scene)
	}

	query += " GROUP BY app_version, platform, scene ORDER BY count DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.FPSMetrics
	for rows.Next() {
		var m models.FPSMetrics
		if err := rows.Scan(
			&m.AppVersion,
			&m.Platform,
			&m.Scene,
			&m.Count,
			&m.AvgFPS,
			&m.P50FPS,
			&m.P90FPS,
			&m.P95FPS,
			&m.P99FPS,
		); err != nil {
			return nil, err
		}
		results = append(results, m)
	}

	return results, nil
}

func (r *Repository) QueryStartupMetrics(ctx context.Context, filter models.QueryFilter) ([]models.StartupMetrics, error) {
	query := `
		SELECT
			app_version,
			platform,
			count() as count,
			avg(phase1_ms) as avg_phase1,
			avg(phase2_ms) as avg_phase2,
			avg(tti_ms) as avg_tti,
			quantile(0.5)(phase1_ms + phase2_ms + tti_ms) as p50_total,
			quantile(0.95)(phase1_ms + phase2_ms + tti_ms) as p95_total,
			quantile(0.99)(phase1_ms + phase2_ms + tti_ms) as p99_total
		FROM apm_startups
		WHERE timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{filter.StartTime, filter.EndTime}

	if filter.AppVersion != "" {
		query += " AND app_version = ?"
		args = append(args, filter.AppVersion)
	}
	if filter.Platform != "" {
		query += " AND platform = ?"
		args = append(args, filter.Platform)
	}

	query += " GROUP BY app_version, platform ORDER BY count DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.StartupMetrics
	for rows.Next() {
		var m models.StartupMetrics
		if err := rows.Scan(
			&m.AppVersion,
			&m.Platform,
			&m.Count,
			&m.AvgPhase1,
			&m.AvgPhase2,
			&m.AvgTTI,
			&m.P50Total,
			&m.P95Total,
			&m.P99Total,
		); err != nil {
			return nil, err
		}
		results = append(results, m)
	}

	return results, nil
}

func (r *Repository) QueryJankMetrics(ctx context.Context, filter models.QueryFilter) ([]models.JankMetrics, error) {
	query := `
		SELECT
			app_version,
			platform,
			scene,
			count() as count,
			avg(duration_ms) as avg_duration,
			max(max_frame_ms) as max_duration,
			uniqExact(session_id) as session_count
		FROM apm_janks
		WHERE timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{filter.StartTime, filter.EndTime}

	if filter.AppVersion != "" {
		query += " AND app_version = ?"
		args = append(args, filter.AppVersion)
	}
	if filter.Platform != "" {
		query += " AND platform = ?"
		args = append(args, filter.Platform)
	}
	if filter.Scene != "" {
		query += " AND scene = ?"
		args = append(args, filter.Scene)
	}

	query += " GROUP BY app_version, platform, scene ORDER BY count DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.JankMetrics
	for rows.Next() {
		var m models.JankMetrics
		if err := rows.Scan(
			&m.AppVersion,
			&m.Platform,
			&m.Scene,
			&m.Count,
			&m.AvgDuration,
			&m.MaxDuration,
			&m.SessionCount,
		); err != nil {
			return nil, err
		}
		results = append(results, m)
	}

	return results, nil
}

func (r *Repository) QueryExceptions(ctx context.Context, filter models.QueryFilter) ([]models.ExceptionSummary, error) {
	query := `
		SELECT
			fingerprint,
			any(message) as message,
			app_version,
			platform,
			sum(count) as count,
			uniqExact(session_id) as session_count,
			min(timestamp) as first_seen,
			max(timestamp) as last_seen,
			any(stack) as sample_stack
		FROM apm_exceptions
		WHERE timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{filter.StartTime, filter.EndTime}

	if filter.AppVersion != "" {
		query += " AND app_version = ?"
		args = append(args, filter.AppVersion)
	}
	if filter.Platform != "" {
		query += " AND platform = ?"
		args = append(args, filter.Platform)
	}

	query += " GROUP BY fingerprint, app_version, platform ORDER BY count DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.ExceptionSummary
	for rows.Next() {
		var e models.ExceptionSummary
		if err := rows.Scan(
			&e.Fingerprint,
			&e.Message,
			&e.AppVersion,
			&e.Platform,
			&e.Count,
			&e.SessionCount,
			&e.FirstSeen,
			&e.LastSeen,
			&e.SampleStack,
		); err != nil {
			return nil, err
		}
		results = append(results, e)
	}

	return results, nil
}

func (r *Repository) QueryCrashes(ctx context.Context, filter models.QueryFilter) ([]models.CrashSummary, error) {
	query := `
		SELECT
			fingerprint,
			any(crash_type) as crash_type,
			app_version,
			platform,
			count() as count,
			uniqExact(session_id) as session_count,
			min(timestamp) as first_seen,
			max(timestamp) as last_seen,
			any(stack) as sample_stack
		FROM apm_crashes
		WHERE timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{filter.StartTime, filter.EndTime}

	if filter.AppVersion != "" {
		query += " AND app_version = ?"
		args = append(args, filter.AppVersion)
	}
	if filter.Platform != "" {
		query += " AND platform = ?"
		args = append(args, filter.Platform)
	}

	query += " GROUP BY fingerprint, app_version, platform ORDER BY count DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.CrashSummary
	for rows.Next() {
		var c models.CrashSummary
		if err := rows.Scan(
			&c.Fingerprint,
			&c.CrashType,
			&c.AppVersion,
			&c.Platform,
			&c.Count,
			&c.SessionCount,
			&c.FirstSeen,
			&c.LastSeen,
			&c.SampleStack,
		); err != nil {
			return nil, err
		}
		results = append(results, c)
	}

	return results, nil
}

// buildWhereClause is a helper to build WHERE clauses with filters
func buildWhereClause(filter models.QueryFilter) (string, []interface{}) {
	var conditions []string
	var args []interface{}

	conditions = append(conditions, "timestamp >= ?")
	args = append(args, filter.StartTime)
	conditions = append(conditions, "timestamp <= ?")
	args = append(args, filter.EndTime)

	if filter.AppVersion != "" {
		conditions = append(conditions, "app_version = ?")
		args = append(args, filter.AppVersion)
	}
	if filter.Platform != "" {
		conditions = append(conditions, "platform = ?")
		args = append(args, filter.Platform)
	}
	if filter.DeviceModel != "" {
		conditions = append(conditions, "device_model = ?")
		args = append(args, filter.DeviceModel)
	}
	if filter.Scene != "" {
		conditions = append(conditions, "scene = ?")
		args = append(args, filter.Scene)
	}

	return "WHERE " + strings.Join(conditions, " AND "), args
}

// Ensure time import is used
var _ = time.Now

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

	batch, err := r.newInsert(ctx, "INSERT INTO apm_perf_samples (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, fps, frame_time_ms, main_thread_ms, gc_alloc_kb, mem_mb, event_id, seq, build_type, gpu, ram_class, net_type, ctx, "+
		"interval_ms, mem_mono_heap_mb, mem_alloc_mb, mem_reserved_mb, mem_gfx_mb, mem_rss_mb, game_t, frame)")
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
			s.EventID,
			s.Seq,
			s.BuildType,
			s.GPU,
			s.RAMClass,
			s.NetType,
			contextColumn(s.Context),
			uint32(s.IntervalMs), // bounded by processor.NormalizePerfMemory
			float32(s.MemMonoHeapMB),
			float32(s.MemAllocMB),
			float32(s.MemReservedMB),
			float32(s.MemGfxMB),
			float32(s.MemRssMB),
			s.GameT, // APM-10: nil (NULL) without the client's clock
			s.Frame,
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

	batch, err := r.newInsert(ctx, "INSERT INTO apm_janks (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, duration_ms, max_frame_ms, recent_gc_count, recent_gc_alloc_kb, recent_events, event_id, seq, build_type, gpu, ram_class, net_type, ctx, game_t, frame)")
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
			j.EventID,
			j.Seq,
			j.BuildType,
			j.GPU,
			j.RAMClass,
			j.NetType,
			contextColumn(j.Context),
			j.GameT, // APM-10: nil (NULL) without the client's clock
			j.Frame,
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

	batch, err := r.newInsert(ctx, "INSERT INTO apm_startups (timestamp, app_version, platform, device_model, os_version, session_id, device_id, phase1_ms, phase2_ms, tti_ms, event_id, seq, build_type, gpu, ram_class, net_type, ctx, game_t, frame)")
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
			s.EventID,
			s.Seq,
			s.BuildType,
			s.GPU,
			s.RAMClass,
			s.NetType,
			contextColumn(s.Context),
			s.GameT, // APM-10: nil (NULL) without the client's clock
			s.Frame,
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

	batch, err := r.newInsert(ctx, "INSERT INTO apm_scene_loads (timestamp, app_version, platform, device_model, session_id, device_id, scene_name, load_ms, activate_ms, event_id, seq, build_type, gpu, ram_class, net_type, ctx, game_t, frame)")
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
			l.EventID,
			l.Seq,
			l.BuildType,
			l.GPU,
			l.RAMClass,
			l.NetType,
			contextColumn(l.Context),
			l.GameT, // APM-10: nil (NULL) without the client's clock
			l.Frame,
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

	batch, err := r.newInsert(ctx, "INSERT INTO apm_exceptions (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, fingerprint, message, stack, count, event_id, seq, build_type, gpu, ram_class, net_type, ctx, "+
		"client_fingerprint, fingerprint_version, log_type, exception_type, message_template, embedded_exception, inner_exceptions, throw_site, call_site, breadcrumbs, "+
		"error_seq, session_ms, unhandled, ctx_merged, game_t, frame)")
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
			e.EventID,
			e.Seq,
			e.BuildType,
			e.GPU,
			e.RAMClass,
			e.NetType,
			contextColumn(e.Context),
			e.ClientFingerprint,
			e.FingerprintVersion,
			e.LogType,
			e.ExceptionType,
			e.MessageTemplate,
			e.EmbeddedException,
			stringsColumn(e.InnerExceptions),
			e.ThrowSite,
			e.CallSite,
			stringsColumn(e.Breadcrumbs),
			// APM-3; bounded by processor.NormalizeOccurrenceInfo.
			uint32(e.ErrorSeq),
			uint64(e.SessionMs),
			string(e.Unhandled),
			boolColumn(bool(e.CtxMerged)),
			e.GameT, // APM-10: nil (NULL) without the client's clock
			e.Frame,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// stringsColumn returns a non-nil slice for an Array(String) column.
func stringsColumn(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// boolColumn is a UInt8 column's value for a flag.
func boolColumn(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// InsertCrashes batch inserts crash events
func (r *Repository) InsertCrashes(ctx context.Context, crashes []models.Crash) error {
	if len(crashes) == 0 {
		return nil
	}

	batch, err := r.newInsert(ctx, "INSERT INTO apm_crashes (timestamp, app_version, platform, device_model, os_version, session_id, device_id, scene, crash_type, fingerprint, stack, breadcrumbs, event_id, seq, build_type, gpu, ram_class, net_type, ctx, "+
		"client_fingerprint, fingerprint_version, summary, crash_site, exit_reason, exit_status, exit_description, importance, pss_kb, rss_kb, "+
		"signal, signal_code, fault_addr, abort_message, abi, session_ms, foreground, details, game_t, frame)")
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
			stringsColumn(c.Breadcrumbs),
			c.EventID,
			c.Seq,
			c.BuildType,
			c.GPU,
			c.RAMClass,
			c.NetType,
			contextColumn(c.Context),
			// APM-7; bounded by processor.NormalizeCrash.
			c.ClientFingerprint,
			c.FingerprintVersion,
			c.Summary,
			c.CrashSite,
			c.ExitReason,
			int32(c.ExitStatus),
			c.ExitDescription,
			uint16(c.Importance),
			uint64(c.PssKB),
			uint64(c.RssKB),
			c.Signal,
			c.SignalCode,
			c.FaultAddr,
			c.AbortMessage,
			c.ABI,
			uint64(c.SessionMs),
			boolColumn(bool(c.Foreground)),
			contextColumn(c.Details),
			c.GameT, // APM-10: nil (NULL) without the client's clock
			c.Frame,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	return batch.Send()
}

// contextColumn is the value inserted into the ctx Map column; never nil.
func contextColumn(ctx map[string]string) map[string]string {
	if ctx == nil {
		return map[string]string{}
	}
	return ctx
}

// propTypesColumn is the value inserted into apm_custom_events.prop_types
// (APM-10); never nil.
func propTypesColumn(types map[string]uint8) map[string]uint8 {
	if types == nil {
		return map[string]uint8{}
	}
	return types
}

// Query methods

func (r *Repository) QueryFPSMetrics(ctx context.Context, filter models.QueryFilter) ([]models.FPSMetrics, error) {
	query := `
		SELECT
			app_version,
			platform,
			scene,
			count() as count,
			toFloat64(avg(fps)) as avg_fps,
			toFloat64(quantile(0.5)(fps)) as p50_fps,
			toFloat64(quantile(0.9)(fps)) as p90_fps,
			toFloat64(quantile(0.95)(fps)) as p95_fps,
			toFloat64(quantile(0.99)(fps)) as p99_fps
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
			toFloat64(avg(phase1_ms)) as avg_phase1,
			toFloat64(avg(phase2_ms)) as avg_phase2,
			toFloat64(avg(tti_ms)) as avg_tti,
			toFloat64(quantile(0.5)(phase1_ms + phase2_ms + tti_ms)) as p50_total,
			toFloat64(quantile(0.95)(phase1_ms + phase2_ms + tti_ms)) as p95_total,
			toFloat64(quantile(0.99)(phase1_ms + phase2_ms + tti_ms)) as p99_total
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
			toFloat64(avg(duration_ms)) as avg_duration,
			toFloat64(max(max_frame_ms)) as max_duration,
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

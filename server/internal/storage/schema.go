package storage

import (
	"context"
)

const schemaSQL = `
-- Performance samples (sampled data)
CREATE TABLE IF NOT EXISTS apm_perf_samples (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    fps Float32,
    frame_time_ms Float32,
    main_thread_ms Float32,
    gc_alloc_kb Float32,
    mem_mb Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp);

-- Jank events
CREATE TABLE IF NOT EXISTS apm_janks (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    duration_ms Float32,
    max_frame_ms Float32,
    recent_gc_count UInt32,
    recent_gc_alloc_kb Float32,
    recent_events Array(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene, timestamp);

-- Startup events
CREATE TABLE IF NOT EXISTS apm_startups (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    phase1_ms Float32,
    phase2_ms Float32,
    tti_ms Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp);

-- Scene loads
CREATE TABLE IF NOT EXISTS apm_scene_loads (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    session_id String,
    device_id String,
    scene_name String,
    load_ms Float32,
    activate_ms Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene_name, timestamp);

-- Exceptions (non-fatal)
CREATE TABLE IF NOT EXISTS apm_exceptions (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    fingerprint String,
    message String,
    stack String,
    count UInt32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp);

-- Crashes
CREATE TABLE IF NOT EXISTS apm_crashes (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    crash_type String,
    fingerprint String,
    stack String,
    breadcrumbs Array(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp);
`

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS apm_perf_samples (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    fps Float32,
    frame_time_ms Float32,
    main_thread_ms Float32,
    gc_alloc_kb Float32,
    mem_mb Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp)`,

	`CREATE TABLE IF NOT EXISTS apm_janks (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    duration_ms Float32,
    max_frame_ms Float32,
    recent_gc_count UInt32,
    recent_gc_alloc_kb Float32,
    recent_events Array(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene, timestamp)`,

	`CREATE TABLE IF NOT EXISTS apm_startups (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    phase1_ms Float32,
    phase2_ms Float32,
    tti_ms Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp)`,

	`CREATE TABLE IF NOT EXISTS apm_scene_loads (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    session_id String,
    device_id String,
    scene_name String,
    load_ms Float32,
    activate_ms Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene_name, timestamp)`,

	`CREATE TABLE IF NOT EXISTS apm_exceptions (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    fingerprint String,
    message String,
    stack String,
    count UInt32
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp)`,

	`CREATE TABLE IF NOT EXISTS apm_crashes (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    crash_type String,
    fingerprint String,
    stack String,
    breadcrumbs Array(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp)`,
}

func (c *ClickHouseClient) Migrate(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if err := c.conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

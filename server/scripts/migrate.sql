-- OZX APM ClickHouse Schema Migration
-- Run this script to initialize the database schema

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
ORDER BY (app_version, platform, timestamp)
TTL timestamp + INTERVAL 30 DAY;

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
ORDER BY (app_version, scene, timestamp)
TTL timestamp + INTERVAL 30 DAY;

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
ORDER BY (app_version, platform, timestamp)
TTL timestamp + INTERVAL 30 DAY;

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
ORDER BY (app_version, scene_name, timestamp)
TTL timestamp + INTERVAL 30 DAY;

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
ORDER BY (app_version, fingerprint, timestamp)
TTL timestamp + INTERVAL 30 DAY;

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
ORDER BY (app_version, fingerprint, timestamp)
TTL timestamp + INTERVAL 30 DAY;

-- Materialized views for aggregations (optional, for better query performance)

-- Daily FPS aggregation
CREATE MATERIALIZED VIEW IF NOT EXISTS apm_fps_daily
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, app_version, platform, scene)
AS SELECT
    toDate(timestamp) as date,
    app_version,
    platform,
    scene,
    count() as sample_count,
    sum(fps) as fps_sum,
    sum(fps * fps) as fps_sum_sq
FROM apm_perf_samples
GROUP BY date, app_version, platform, scene;

-- Daily startup aggregation
CREATE MATERIALIZED VIEW IF NOT EXISTS apm_startup_daily
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, app_version, platform)
AS SELECT
    toDate(timestamp) as date,
    app_version,
    platform,
    count() as sample_count,
    sum(phase1_ms) as phase1_sum,
    sum(phase2_ms) as phase2_sum,
    sum(tti_ms) as tti_sum
FROM apm_startups
GROUP BY date, app_version, platform;

-- Daily exception count
CREATE MATERIALIZED VIEW IF NOT EXISTS apm_exception_daily
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, app_version, platform, fingerprint)
AS SELECT
    toDate(timestamp) as date,
    app_version,
    platform,
    fingerprint,
    sum(count) as total_count
FROM apm_exceptions
GROUP BY date, app_version, platform, fingerprint;

-- Daily crash count
CREATE MATERIALIZED VIEW IF NOT EXISTS apm_crash_daily
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, app_version, platform, fingerprint)
AS SELECT
    toDate(timestamp) as date,
    app_version,
    platform,
    fingerprint,
    count() as crash_count
FROM apm_crashes
GROUP BY date, app_version, platform, fingerprint;

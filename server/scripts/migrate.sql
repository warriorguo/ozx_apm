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
    mem_mb Float32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1
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
    recent_events Array(String),
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1
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
    tti_ms Float32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1
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
    activate_ms Float32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1
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
    count UInt32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1
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
    breadcrumbs Array(String),
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1
) ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp)
TTL timestamp + INTERVAL 30 DAY;

-- APM-2: client event id + per-install sequence number.
-- Additive and idempotent so it is safe to re-run on existing deployments.
-- De-duplication by event_id: APM-19 (below).
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;

-- APM-19: bloom_filter skip index on event_id for the ingest-time duplicate
-- lookup. Additive, idempotent, metadata only: new parts carry the index,
-- existing parts get it when merged. Needs the ALTER ADD INDEX privilege (the
-- `apm` service account does not have it; run as an admin, see RELEASE.md).
-- To index existing parts right away (a background mutation per table):
--   ALTER TABLE <table> MATERIALIZE INDEX idx_event_id;
ALTER TABLE apm_perf_samples ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
ALTER TABLE apm_janks ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
ALTER TABLE apm_startups ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
ALTER TABLE apm_scene_loads ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
ALTER TABLE apm_exceptions ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
ALTER TABLE apm_crashes ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;

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

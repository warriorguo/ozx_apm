-- OZX APM ClickHouse schema.
--
-- GENERATED from server/internal/storage/schema.go, which is the only source
-- of truth (the server runs the same statements at every start). Do not edit
-- by hand; regenerate with:
--   cd server && go run ./cmd/schemasql > scripts/migrate.sql
--
-- Idempotent: safe to re-run on an existing deployment. It never changes the
-- TTL of an existing table (CREATE TABLE IF NOT EXISTS leaves it as it is); see
-- RELEASE.md (APM-17) and scripts/apm17_fix_schema.sh for adding a missing TTL.

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
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    interval_ms UInt32 DEFAULT 0,
    mem_mono_heap_mb Float32 DEFAULT 0,
    mem_alloc_mb Float32 DEFAULT 0,
    mem_reserved_mb Float32 DEFAULT 0,
    mem_gfx_mb Float32 DEFAULT 0,
    mem_rss_mb Float32 DEFAULT 0,
    game_t Nullable(Float64),
    frame Nullable(Int64),
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

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
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    game_t Nullable(Float64),
    frame Nullable(Int64),
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

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
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    game_t Nullable(Float64),
    frame Nullable(Int64),
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

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
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    game_t Nullable(Float64),
    frame Nullable(Int64),
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene_name, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

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
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    client_fingerprint String DEFAULT '',
    fingerprint_version UInt8 DEFAULT 0,
    log_type LowCardinality(String) DEFAULT '',
    exception_type String DEFAULT '',
    message_template String DEFAULT '',
    embedded_exception String DEFAULT '',
    inner_exceptions Array(String),
    throw_site String DEFAULT '',
    call_site String DEFAULT '',
    breadcrumbs Array(String),
    error_seq UInt32 DEFAULT 0,
    session_ms UInt64 DEFAULT 0,
    unhandled LowCardinality(String) DEFAULT '',
    ctx_merged UInt8 DEFAULT 0,
    game_t Nullable(Float64),
    frame Nullable(Int64),
    recovered UInt8 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

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
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    client_fingerprint String DEFAULT '',
    fingerprint_version UInt8 DEFAULT 0,
    summary String DEFAULT '',
    crash_site String DEFAULT '',
    exit_reason LowCardinality(String) DEFAULT '',
    exit_status Int32 DEFAULT 0,
    exit_description String DEFAULT '',
    importance UInt16 DEFAULT 0,
    pss_kb UInt64 DEFAULT 0,
    rss_kb UInt64 DEFAULT 0,
    signal LowCardinality(String) DEFAULT '',
    signal_code LowCardinality(String) DEFAULT '',
    fault_addr String DEFAULT '',
    abort_message String DEFAULT '',
    abi LowCardinality(String) DEFAULT '',
    session_ms UInt64 DEFAULT 0,
    foreground UInt8 DEFAULT 0,
    details Map(LowCardinality(String), String),
    game_t Nullable(Float64),
    frame Nullable(Int64),
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS apm_custom_events (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    name String,
    count UInt32,
    aggregated UInt8,
    ctx_merged UInt8,
    props Map(LowCardinality(String), String),
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    prop_types Map(LowCardinality(String), UInt8),
    game_t Nullable(Float64),
    frame Nullable(Int64),
    recovered UInt8 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, name, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS apm_custom_metrics (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    name String,
    unit LowCardinality(String),
    count UInt64,
    value_sum Float64,
    value_min Float64,
    value_max Float64,
    window_ms UInt32,
    hist_version UInt8,
    hist_keys Array(Int16),
    hist_counts Array(UInt64),
    ctx_merged UInt8,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    game_t Nullable(Float64),
    frame Nullable(Int64),
    recovered UInt8 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, name, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS apm_runs (
    timestamp DateTime64(3),
    app_version String,
    platform String,
    device_model String,
    os_version String,
    session_id String,
    device_id String,
    scene String,
    run_id String,
    end_reason LowCardinality(String),
    result String,
    crash_type LowCardinality(String),
    duration_ms UInt64,
    samples UInt32,
    peak_mem_mb Float32,
    peak_mono_heap_mb Float32,
    peak_alloc_mb Float32,
    peak_reserved_mb Float32,
    peak_gfx_mb Float32,
    peak_rss_mb Float32,
    start_alloc_mb Float32,
    start_rss_mb Float32,
    end_alloc_mb Float32,
    end_rss_mb Float32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    build_type LowCardinality(String) DEFAULT '',
    gpu LowCardinality(String) DEFAULT '',
    ram_class LowCardinality(String) DEFAULT '',
    net_type LowCardinality(String) DEFAULT '',
    ctx Map(LowCardinality(String), String),
    game_t Nullable(Float64),
    frame Nullable(Int64),
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, end_reason, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;

-- Additive column migrations for tables created by earlier releases
-- (APM-2: event_id, seq; APM-5: build_type, gpu, ram_class, net_type, ctx;
-- APM-4: the exception columns from client_fingerprint to breadcrumbs;
-- APM-3: error_seq, session_ms, unhandled, ctx_merged; APM-7: the crash
-- columns from client_fingerprint to details; APM-9: the perf sample's
-- memory columns from interval_ms to mem_rss_mb; APM-10: prop_types on
-- apm_custom_events, then game_t and frame on every event table; APM-24:
-- recovered on apm_exceptions, apm_custom_events and apm_custom_metrics). The APM-6 tables
-- (apm_custom_events, apm_custom_metrics) and APM-9's apm_runs have had all
-- their columns from the start.
-- Metadata only (existing parts are not rewritten), idempotent.
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS interval_ms UInt32 DEFAULT 0;
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS mem_mono_heap_mb Float32 DEFAULT 0;
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS mem_alloc_mb Float32 DEFAULT 0;
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS mem_reserved_mb Float32 DEFAULT 0;
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS mem_gfx_mb Float32 DEFAULT 0;
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS mem_rss_mb Float32 DEFAULT 0;
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS client_fingerprint String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS fingerprint_version UInt8 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS log_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS exception_type String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS message_template String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS embedded_exception String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS inner_exceptions Array(String);
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS throw_site String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS call_site String DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS breadcrumbs Array(String);
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS error_seq UInt32 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS session_ms UInt64 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS unhandled LowCardinality(String) DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS ctx_merged UInt8 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS recovered UInt8 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS client_fingerprint String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS fingerprint_version UInt8 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS summary String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS crash_site String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS exit_reason LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS exit_status Int32 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS exit_description String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS importance UInt16 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS pss_kb UInt64 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS rss_kb UInt64 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS signal LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS signal_code LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS fault_addr String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS abort_message String DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS abi LowCardinality(String) DEFAULT '';
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS session_ms UInt64 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS foreground UInt8 DEFAULT 0;
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS details Map(LowCardinality(String), String);
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS prop_types Map(LowCardinality(String), UInt8);
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_custom_events ADD COLUMN IF NOT EXISTS recovered UInt8 DEFAULT 0;
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS frame Nullable(Int64);
ALTER TABLE apm_custom_metrics ADD COLUMN IF NOT EXISTS recovered UInt8 DEFAULT 0;
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS game_t Nullable(Float64);
ALTER TABLE apm_runs ADD COLUMN IF NOT EXISTS frame Nullable(Int64);

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
ALTER TABLE apm_custom_events ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
ALTER TABLE apm_custom_metrics ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
ALTER TABLE apm_runs ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;

-- APM-15: bloom_filter skip index on session_id for the session time line.
-- Additive, idempotent, metadata only, same privilege as above. To index
-- existing parts right away (optional; a background mutation per table):
--   ALTER TABLE <table> MATERIALIZE INDEX idx_session_id;
ALTER TABLE apm_perf_samples ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_janks ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_startups ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_scene_loads ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_exceptions ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_crashes ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_custom_events ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_custom_metrics ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_runs ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;

-- Daily aggregations. They only see rows inserted after they were created.
-- Need the CREATE VIEW privilege (not granted to the `apm` service account).

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

CREATE MATERIALIZED VIEW IF NOT EXISTS apm_custom_event_daily
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, app_version, platform, build_type, name)
TTL date + INTERVAL 365 DAY
AS SELECT
    toDate(timestamp) as date,
    app_version,
    platform,
    build_type,
    name,
    sum(count) as total_count
FROM apm_custom_events
GROUP BY date, app_version, platform, build_type, name;

CREATE MATERIALIZED VIEW IF NOT EXISTS apm_custom_metric_daily
ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, app_version, platform, build_type, name)
TTL date + INTERVAL 365 DAY
AS SELECT
    toDate(timestamp) as date,
    app_version,
    platform,
    build_type,
    name,
    sumState(count) as samples,
    sumState(value_sum) as total,
    minState(value_min) as min_value,
    maxState(value_max) as max_value,
    sumMapState(hist_keys, hist_counts) as hist
FROM apm_custom_metrics
WHERE hist_version = 1
GROUP BY date, app_version, platform, build_type, name;

-- APM-14: audit record of every privacy deletion request (subject id stored
-- as a SHA-256 hash only). See internal/storage/privacy.go.

CREATE TABLE IF NOT EXISTS apm_privacy_deletions (
    request_id String,
    at DateTime64(3),
    id_type LowCardinality(String),
    id_hash String,
    status LowCardinality(String),
    tables Array(String),
    rows_before UInt64,
    rows_after UInt64,
    error String,
    requested_by String
) ENGINE = MergeTree()
ORDER BY (id_hash, at);

-- APM-8: issue state (resolved / unresolved, ReplacingMergeTree: the latest
-- row per issue wins, read with FINAL) and the alert outbox (append-only; the
-- rows of an alert key make alerting idempotent across restarts and
-- replicas). See internal/storage/repository_alerts.go.

CREATE TABLE IF NOT EXISTS apm_issue_states (
    kind LowCardinality(String),
    fingerprint String,
    status LowCardinality(String),
    resolved_in_version String,
    last_seen_version String,
    resolved_at DateTime64(3),
    regressed_in_version String,
    regressed_at DateTime64(3),
    updated_at DateTime64(3),
    updated_by LowCardinality(String),
    comment String,
    version UInt64
) ENGINE = ReplacingMergeTree(version)
ORDER BY (kind, fingerprint);

CREATE TABLE IF NOT EXISTS apm_alerts (
    alert_key String,
    at DateTime64(3),
    status LowCardinality(String),
    attempt UInt16,
    instance String,
    type LowCardinality(String),
    kind LowCardinality(String),
    fingerprint String,
    app_version String,
    payload String,
    error String,
    inserted_at DateTime64(3) DEFAULT now64(3)
) ENGINE = MergeTree()
ORDER BY (alert_key, at);

-- APM-7: uploaded symbols (cmd/apmsym) used to symbolicate native crash
-- stacks. See internal/storage/schema.go (symbolTables).

CREATE TABLE IF NOT EXISTS apm_symbol_files (
    build_id String,
    upload_id String,
    module String,
    arch LowCardinality(String),
    app_version String,
    app_build String,
    functions UInt64,
    lines UInt64,
    line_map UInt8,
    method_map UInt8,
    source String,
    tool String,
    uploaded_at DateTime64(3)
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMM(uploaded_at)
ORDER BY (build_id, upload_id)
TTL toDateTime(uploaded_at) + INTERVAL 400 DAY;

CREATE TABLE IF NOT EXISTS apm_symbol_funcs (
    build_id String,
    upload_id String,
    addr UInt64,
    size UInt64,
    name String,
    raw_name String,
    uploaded_at DateTime64(3)
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMM(uploaded_at)
ORDER BY (build_id, upload_id, addr)
TTL toDateTime(uploaded_at) + INTERVAL 400 DAY;

CREATE TABLE IF NOT EXISTS apm_symbol_lines (
    build_id String,
    upload_id String,
    addr UInt64,
    file LowCardinality(String),
    line UInt32,
    uploaded_at DateTime64(3)
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMM(uploaded_at)
ORDER BY (build_id, upload_id, addr)
TTL toDateTime(uploaded_at) + INTERVAL 400 DAY;

-- APM-12: operations data (one year, keyed by an anonymous player id; no
-- device id, device details or IP). ops_player_days and ops_players are
-- written by the ingest server, the rates and ad reports by imports. See
-- internal/storage/schema.go (opsTables) and docs/ops-schema.md.

CREATE TABLE IF NOT EXISTS ops_events (
    event_id String,
    name LowCardinality(String),
    day Date,
    event_time DateTime64(3),
    client_time DateTime64(3),
    server_time DateTime64(3),
    seq UInt64,
    player_id String,
    session_id String,
    app_version LowCardinality(String),
    platform LowCardinality(String),
    build_type LowCardinality(String),
    channel LowCardinality(String),
    country LowCardinality(String),
    props Map(LowCardinality(String), String),
    prop_types Map(LowCardinality(String), UInt8),
    num_props Map(LowCardinality(String), Float64),
    ctx Map(LowCardinality(String), String),
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_player_id player_id TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_client_time client_time TYPE minmax GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (name, day, event_id)
TTL day + INTERVAL 365 DAY;

CREATE TABLE IF NOT EXISTS ops_player_days (
    day Date,
    player_id String,
    app_version LowCardinality(String),
    platform LowCardinality(String),
    build_type LowCardinality(String),
    channel LowCardinality(String),
    country LowCardinality(String),
    INDEX idx_player_id player_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, player_id)
TTL day + INTERVAL 365 DAY;

CREATE TABLE IF NOT EXISTS ops_players (
    player_id String,
    first_seen SimpleAggregateFunction(min, DateTime64(3)),
    last_seen SimpleAggregateFunction(max, DateTime64(3)),
    install_channel SimpleAggregateFunction(max, String),
    platform SimpleAggregateFunction(max, String),
    build_type SimpleAggregateFunction(max, String)
) ENGINE = AggregatingMergeTree()
ORDER BY player_id
TTL toDateTime(last_seen) + INTERVAL 365 DAY;

CREATE TABLE IF NOT EXISTS ops_fx_rates (
    day Date,
    currency LowCardinality(String),
    usd_per_unit Float64,
    source String,
    imported_at DateTime
) ENGINE = ReplacingMergeTree(imported_at)
ORDER BY (currency, day);

CREATE TABLE IF NOT EXISTS ops_ad_reports (
    day Date,
    network LowCardinality(String),
    country LowCardinality(String),
    placement String,
    ad_unit String,
    impressions UInt64,
    revenue Float64,
    currency LowCardinality(String),
    source String,
    imported_at DateTime
) ENGINE = ReplacingMergeTree(imported_at)
PARTITION BY toYYYYMM(day)
ORDER BY (day, network, country, placement, ad_unit)
TTL day + INTERVAL 365 DAY;

-- APM-13: index of the session files (AiTelemetry session directories of
-- development builds) kept in object storage. See internal/storage/schema.go
-- (sessionFilesTableDDL) and internal/sessionfiles.

CREATE TABLE IF NOT EXISTS apm_session_files (
    upload_id String,
    version UInt64,
    status LowCardinality(String),
    created_at DateTime64(3),
    updated_at DateTime64(3),
    expires_at DateTime,
    device_id String,
    session_id String,
    recording_name String,
    app_version LowCardinality(String),
    app_build String,
    build_type LowCardinality(String),
    platform LowCardinality(String),
    device_model String,
    os_version String,
    unity_version String,
    sdk_version String,
    git_commit String,
    started_at String,
    scene String,
    data_source String,
    data_pack_version String,
    data_pack_sha256 String,
    size_bytes UInt64,
    part_bytes UInt32,
    part_count UInt32,
    sha256 String,
    file_count UInt32,
    raw_bytes UInt64,
    object_key String,
    session_json String,
    error String,
    INDEX idx_git_commit git_commit TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_device_id device_id TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(created_at)
ORDER BY upload_id
TTL expires_at + INTERVAL 30 DAY;

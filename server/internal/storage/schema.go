package storage

import (
	"context"

	"go.uber.org/zap"
)

// schemaStatements create the event tables on a fresh install and apply the
// additive migrations; Migrate runs them on every start. Keep
// scripts/migrate.sql in step.
//
// The event tables are ReplacingMergeTree with event_id appended to the sort
// key (APM-19 phase B): a re-sent event has the same timestamp, app_version
// and key column, so its full sort key matches the stored copy and merges
// keep only one. The ingest handler drops most re-sent events before insert
// (phase A); this catches the rest, eventually. Events without event_id get a
// server-generated "srv-" id at ingest so that they never collapse together.
// Existing MergeTree tables are converted by scripts/apm19_rmt_migrate.sh,
// never by Migrate.
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
    mem_mb Float32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX ` + EventIDIndexDDL + `
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp, event_id)`,

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
    recent_events Array(String),
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX ` + EventIDIndexDDL + `
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene, timestamp, event_id)`,

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
    tti_ms Float32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX ` + EventIDIndexDDL + `
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp, event_id)`,

	`CREATE TABLE IF NOT EXISTS apm_scene_loads (
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
    INDEX ` + EventIDIndexDDL + `
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, scene_name, timestamp, event_id)`,

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
    count UInt32,
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX ` + EventIDIndexDDL + `
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp, event_id)`,

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
    breadcrumbs Array(String),
    event_id String DEFAULT '',
    seq UInt64 DEFAULT 0,
    INDEX ` + EventIDIndexDDL + `
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, fingerprint, timestamp, event_id)`,

	// Additive migrations for deployments created before APM-2. Must stay
	// idempotent: Migrate runs on every server start.
	`ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS event_id String DEFAULT ''`,
	`ALTER TABLE apm_perf_samples ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0`,
	`ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS event_id String DEFAULT ''`,
	`ALTER TABLE apm_janks ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0`,
	`ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS event_id String DEFAULT ''`,
	`ALTER TABLE apm_startups ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0`,
	`ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS event_id String DEFAULT ''`,
	`ALTER TABLE apm_scene_loads ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0`,
	`ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS event_id String DEFAULT ''`,
	`ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0`,
	`ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS event_id String DEFAULT ''`,
	`ALTER TABLE apm_crashes ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0`,
}

// EventIDIndexName is the bloom_filter skip index on event_id (APM-19). The
// ingest-time duplicate lookup filters on event_id IN (...); the index lets
// ClickHouse skip granules that cannot contain any of the ids.
//
// bloom_filter(0.001), one filter per granule: a lookup probes ~60 ids, so
// with 0.01 about half of the candidate granules were false positives in the
// APM-19 benchmark (5M rows/day: 72 of 164 granules read), with 0.001 about
// 8% (9-16 of 164), for an index of ~3% of the table instead of ~2%.
const EventIDIndexName = "idx_event_id"

// EventIDIndexDDL is the index definition used by CREATE TABLE and ADD INDEX.
const EventIDIndexDDL = EventIDIndexName + " event_id TYPE bloom_filter(0.001) GRANULARITY 1"

func addEventIDIndexStatement(table string) string {
	return "ALTER TABLE " + table + " ADD INDEX IF NOT EXISTS " + EventIDIndexDDL
}

// migrateEventIDIndexes adds the event_id index to existing tables that lack
// it. It only speeds up the duplicate lookup, so a failure is logged and
// startup continues: the production service account has no ALTER ADD INDEX
// privilege (RELEASE.md has the admin commands), and ClickHouse checks the
// privilege even when the index already exists, which is why tables that
// have it are skipped. Adding an index only changes metadata: parts written
// afterwards carry it, existing parts get it on merge or through an explicit
// MATERIALIZE INDEX (a background mutation, deliberately not run here).
func (c *ClickHouseClient) migrateEventIDIndexes(ctx context.Context) {
	// system.tables, not system.data_skipping_indices: the service account
	// may read the former (rows filtered to its tables) but not the latter.
	have := map[string]bool{}
	rows, err := c.conn.Query(ctx, "SELECT name FROM system.tables WHERE database = currentDatabase() AND position(create_table_query, ?) > 0", "INDEX "+EventIDIndexName+" ")
	if err == nil {
		for rows.Next() {
			var table string
			if rows.Scan(&table) == nil {
				have[table] = true
			}
		}
		rows.Close()
	}
	for _, t := range EventTables {
		if have[t.Name] {
			continue
		}
		stmt := addEventIDIndexStatement(t.Name)
		if err := c.conn.Exec(ctx, stmt); err != nil {
			c.logger.Warn("could not add the event_id index; duplicate lookups will read more data until an admin adds it (RELEASE.md, APM-19)",
				zap.String("statement", stmt), zap.Error(err))
		}
	}
}

func (c *ClickHouseClient) Migrate(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if err := c.conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	c.migrateEventIDIndexes(ctx)
	return nil
}

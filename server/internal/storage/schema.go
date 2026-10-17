package storage

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

// This file is the one authoritative definition of the ClickHouse schema.
// Migrate runs it on every server start, and scripts/migrate.sql is generated
// from it (go run ./cmd/schemasql > scripts/migrate.sql; a unit test fails
// when the two differ), so a database set up either way ends up the same:
// the six event tables with TTL, the event_id skip index and the four
// apm_*_daily materialized views (APM-17), and the privacy deletion audit
// table (APM-14, privacy.go).
//
// The event tables are ReplacingMergeTree with event_id appended to the sort
// key (APM-19 phase B): a re-sent event has the same timestamp, app_version
// and key column, so its full sort key matches the stored copy and merges
// keep only one. The ingest handler drops most re-sent events before insert
// (phase A); this catches the rest, eventually. Events without event_id get a
// server-generated "srv-" id at ingest so that they never collapse together.
// Existing MergeTree tables are converted by scripts/apm19_rmt_migrate.sh,
// never by Migrate.
//
// Everything Migrate must do needs only the privileges of the production
// service account (SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN). Steps that
// need more (ADD INDEX, CREATE VIEW, MODIFY TTL) only log a warning when they
// are refused or cannot be done; RELEASE.md has the admin commands.

// EventTTL is the retention of the event tables: technical detail data is
// kept for 30 days (CLAUDE.md privacy rules). processor.MaxEventAge matches it.
//
// toDateTime(): ClickHouse before 24.x (verified: 23.8 and 24.1) rejects a
// TTL on a DateTime64 expression ("TTL expression result column should have
// DateTime or Date type"); 26.1 accepts both. Tables created by the previous
// migrate.sql on 26.1 (production) have "timestamp + INTERVAL 30 DAY", which
// is the same retention; Migrate never changes an existing TTL.
const EventTTL = "toDateTime(timestamp) + INTERVAL 30 DAY"

// eventColumns lists the columns every event table has after its own ones,
// in the order ALTER TABLE ADD COLUMN appended them to existing tables, so a
// fresh table and a migrated one have the same column order.
var eventColumns = []string{
	// APM-2: client event id and per-install sequence number.
	"event_id String DEFAULT ''",
	"seq UInt64 DEFAULT 0",
	// APM-5: build type ('release', 'dev', 'editor'; '' for events from SDKs
	// that predate it), device details, and the game context: the key/value
	// pairs the game had set with ApmClient.SetContext when the event was
	// recorded (models.EventContext has the limits).
	"build_type LowCardinality(String) DEFAULT ''",
	"gpu LowCardinality(String) DEFAULT ''",
	"ram_class LowCardinality(String) DEFAULT ''",
	"net_type LowCardinality(String) DEFAULT ''",
	"ctx Map(LowCardinality(String), String)",
}

type eventTableSchema struct {
	table   EventTable
	columns []string // table-specific columns, before eventColumns
}

// addedColumns are table-specific columns added after eventColumns, in the
// order they were added; CREATE TABLE puts them last, as the ALTERs do.
var addedColumns = map[string][]string{
	TableExceptions.Name: append(append([]string{}, exceptionColumnsAPM4...), exceptionColumnsAPM3...),
}

// exceptionColumnsAPM3 describe the occurrence(s) an exception row reports
// (models.ExceptionOccurrenceInfo): error_seq is which error of its session
// the row's first occurrence was (1 = the session's first error; 0 on rows of
// older SDKs), session_ms the time since the session started, unhandled
// empty or 'domain' / 'domain_terminating' / 'task', and ctx_merged 1 when the
// row's count merges occurrences in different game contexts (ctx then holds
// only the entries they shared).
var exceptionColumnsAPM3 = []string{
	"error_seq UInt32 DEFAULT 0",
	"session_ms UInt64 DEFAULT 0",
	"unhandled LowCardinality(String) DEFAULT ''",
	"ctx_merged UInt8 DEFAULT 0",
}

// exceptionColumnsAPM4 are the exception columns of APM-4. fingerprint is
// now computed by the server (internal/fingerprint) and fingerprint_version
// says by which version of the algorithm; 0 marks rows stored before APM-4,
// whose fingerprint is the SDK's. client_fingerprint is what the SDK sent.
// The rest is parsed from message, stack and embedded_exception (the
// exception the SDK found in an error log's message) and is empty on older
// rows.
var exceptionColumnsAPM4 = []string{
	"client_fingerprint String DEFAULT ''",
	"fingerprint_version UInt8 DEFAULT 0",
	"log_type LowCardinality(String) DEFAULT ''",
	"exception_type String DEFAULT ''",
	"message_template String DEFAULT ''",
	"embedded_exception String DEFAULT ''",
	"inner_exceptions Array(String)",
	"throw_site String DEFAULT ''",
	"call_site String DEFAULT ''",
	"breadcrumbs Array(String)",
}

var eventTableSchemas = []eventTableSchema{
	{TablePerfSamples, []string{
		"timestamp DateTime64(3)",
		"app_version String",
		"platform String",
		"device_model String",
		"os_version String",
		"session_id String",
		"device_id String",
		"scene String",
		"fps Float32",
		"frame_time_ms Float32",
		"main_thread_ms Float32",
		"gc_alloc_kb Float32",
		"mem_mb Float32",
	}},
	{TableJanks, []string{
		"timestamp DateTime64(3)",
		"app_version String",
		"platform String",
		"device_model String",
		"os_version String",
		"session_id String",
		"device_id String",
		"scene String",
		"duration_ms Float32",
		"max_frame_ms Float32",
		"recent_gc_count UInt32",
		"recent_gc_alloc_kb Float32",
		"recent_events Array(String)",
	}},
	{TableStartups, []string{
		"timestamp DateTime64(3)",
		"app_version String",
		"platform String",
		"device_model String",
		"os_version String",
		"session_id String",
		"device_id String",
		"phase1_ms Float32",
		"phase2_ms Float32",
		"tti_ms Float32",
	}},
	{TableSceneLoads, []string{
		"timestamp DateTime64(3)",
		"app_version String",
		"platform String",
		"device_model String",
		"session_id String",
		"device_id String",
		"scene_name String",
		"load_ms Float32",
		"activate_ms Float32",
	}},
	{TableExceptions, []string{
		"timestamp DateTime64(3)",
		"app_version String",
		"platform String",
		"device_model String",
		"os_version String",
		"session_id String",
		"device_id String",
		"scene String",
		"fingerprint String",
		"message String",
		"stack String",
		"count UInt32",
	}},
	{TableCrashes, []string{
		"timestamp DateTime64(3)",
		"app_version String",
		"platform String",
		"device_model String",
		"os_version String",
		"session_id String",
		"device_id String",
		"scene String",
		"crash_type String",
		"fingerprint String",
		"stack String",
		"breadcrumbs Array(String)",
	}},
}

func createEventTableStatement(s eventTableSchema) string {
	var sb strings.Builder
	sb.WriteString("CREATE TABLE IF NOT EXISTS " + s.table.Name + " (\n")
	for _, col := range s.columns {
		sb.WriteString("    " + col + ",\n")
	}
	for _, col := range eventColumns {
		sb.WriteString("    " + col + ",\n")
	}
	for _, col := range addedColumns[s.table.Name] {
		sb.WriteString("    " + col + ",\n")
	}
	sb.WriteString("    INDEX " + EventIDIndexDDL + "\n")
	sb.WriteString(") ENGINE = ReplacingMergeTree()\n")
	sb.WriteString("PARTITION BY toYYYYMMDD(timestamp)\n")
	sb.WriteString("ORDER BY (app_version, " + s.table.KeyColumn + ", timestamp, event_id)\n")
	sb.WriteString("TTL " + EventTTL)
	return sb.String()
}

func addColumnStatement(table, column string) string {
	return "ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS " + column
}

// schemaStatements create the event tables on a fresh install and apply the
// additive column migrations to existing ones. Each must succeed (Migrate
// fails otherwise) and stay idempotent: Migrate runs them on every start.
// Columns are only ever added (ALTER TABLE ADD COLUMN IF NOT EXISTS), which
// changes metadata only and keeps older binaries working, because every
// insert names its columns.
var schemaStatements = func() []string {
	var stmts []string
	for _, s := range eventTableSchemas {
		stmts = append(stmts, createEventTableStatement(s))
	}
	for _, s := range eventTableSchemas {
		for _, col := range eventColumns {
			stmts = append(stmts, addColumnStatement(s.table.Name, col))
		}
		for _, col := range addedColumns[s.table.Name] {
			stmts = append(stmts, addColumnStatement(s.table.Name, col))
		}
	}
	return stmts
}()

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

// materializedView is one of the apm_*_daily aggregations. They are fed at
// insert time and only see rows inserted after they were created (no
// POPULATE: back-filling would race with concurrent inserts). Nothing in the
// server reads them today; they are kept for ad-hoc queries and dashboards.
type materializedView struct {
	Name string
	DDL  string
}

var materializedViews = []materializedView{
	{"apm_fps_daily", `CREATE MATERIALIZED VIEW IF NOT EXISTS apm_fps_daily
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
GROUP BY date, app_version, platform, scene`},
	{"apm_startup_daily", `CREATE MATERIALIZED VIEW IF NOT EXISTS apm_startup_daily
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
GROUP BY date, app_version, platform`},
	{"apm_exception_daily", `CREATE MATERIALIZED VIEW IF NOT EXISTS apm_exception_daily
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
GROUP BY date, app_version, platform, fingerprint`},
	{"apm_crash_daily", `CREATE MATERIALIZED VIEW IF NOT EXISTS apm_crash_daily
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
GROUP BY date, app_version, platform, fingerprint`},
}

// MaterializedViewNames lists the apm_*_daily views Migrate creates.
func MaterializedViewNames() []string {
	names := make([]string, len(materializedViews))
	for i, v := range materializedViews {
		names[i] = v.Name
	}
	return names
}

// existingTables returns name -> create_table_query for the tables and views
// of the current database. The service account may read system.tables (rows
// are filtered to what it has grants on) but not system.data_skipping_indices.
func (c *ClickHouseClient) existingTables(ctx context.Context) (map[string]string, error) {
	rows, err := c.conn.Query(ctx, "SELECT name, create_table_query FROM system.tables WHERE database = currentDatabase()")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := map[string]string{}
	for rows.Next() {
		var name, create string
		if err := rows.Scan(&name, &create); err != nil {
			return nil, err
		}
		tables[name] = create
	}
	return tables, rows.Err()
}

// migrateEventIDIndexes adds the event_id index to existing tables that lack
// it. It only speeds up the duplicate lookup, so a failure is logged and
// startup continues: the production service account has no ALTER ADD INDEX
// privilege (RELEASE.md has the admin commands), and ClickHouse checks the
// privilege even when the index already exists, which is why tables that
// have it are skipped. Adding an index only changes metadata: parts written
// afterwards carry it, existing parts get it on merge or through an explicit
// MATERIALIZE INDEX (a background mutation, deliberately not run here).
func (c *ClickHouseClient) migrateEventIDIndexes(ctx context.Context, tables map[string]string) {
	for _, t := range EventTables {
		if strings.Contains(tables[t.Name], "INDEX "+EventIDIndexName+" ") {
			continue
		}
		stmt := addEventIDIndexStatement(t.Name)
		if err := c.conn.Exec(ctx, stmt); err != nil {
			c.logger.Warn("could not add the event_id index; duplicate lookups will read more data until an admin adds it (RELEASE.md, APM-19)",
				zap.String("statement", stmt), zap.Error(err))
		}
	}
}

// migrateMaterializedViews creates the apm_*_daily views that do not exist
// yet. Existing views are skipped without a statement (the service account
// has no CREATE VIEW privilege, and they are never redefined here). A refused
// or failed CREATE is logged and startup continues: no server code reads the
// views, and an admin can create them (RELEASE.md, APM-17).
func (c *ClickHouseClient) migrateMaterializedViews(ctx context.Context, tables map[string]string) {
	for _, v := range materializedViews {
		if _, ok := tables[v.Name]; ok {
			continue
		}
		if err := c.conn.Exec(ctx, v.DDL); err != nil {
			c.logger.Warn("could not create a materialized view; ask an admin to create it (RELEASE.md, APM-17)",
				zap.String("view", v.Name), zap.Error(err))
		}
	}
}

// checkEventTTL warns about event tables without a TTL (created by Migrate
// before APM-17). Adding one is not done here: ALTER TABLE ... MODIFY TTL
// needs a privilege the service account does not have, and it immediately
// deletes every row older than the TTL, which must be an admin's decision
// (RELEASE.md, scripts/apm17_fix_schema.sh).
func (c *ClickHouseClient) checkEventTTL(tables map[string]string) {
	for _, t := range EventTables {
		create, ok := tables[t.Name]
		if !ok {
			continue
		}
		if !strings.Contains(create, " TTL ") {
			c.logger.Warn("event table has no TTL: its data never expires; an admin must add it (RELEASE.md, APM-17)",
				zap.String("table", t.Name), zap.String("ttl", EventTTL))
		}
	}
}

// Migrate creates or upgrades the schema. Run on every server start.
func (c *ClickHouseClient) Migrate(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if err := c.conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	tables, err := c.existingTables(ctx)
	if err != nil {
		// Only the optional steps below depend on it.
		c.logger.Warn("could not list tables; trying every optional migration", zap.Error(err))
		tables = map[string]string{}
	}
	c.migrateEventIDIndexes(ctx, tables)
	c.migrateMaterializedViews(ctx, tables)
	c.checkEventTTL(tables)
	c.migratePrivacyAudit(ctx)
	return nil
}

// SchemaSQL renders the schema as a ClickHouse script. scripts/migrate.sql
// is this output; it is for setting up a database by hand (make migrate) and
// does what Migrate does, plus the index and views without the fallbacks.
func SchemaSQL() string {
	var sb strings.Builder
	sb.WriteString(`-- OZX APM ClickHouse schema.
--
-- GENERATED from server/internal/storage/schema.go, which is the only source
-- of truth (the server runs the same statements at every start). Do not edit
-- by hand; regenerate with:
--   cd server && go run ./cmd/schemasql > scripts/migrate.sql
--
-- Idempotent: safe to re-run on an existing deployment. It never changes the
-- TTL of an existing table (CREATE TABLE IF NOT EXISTS leaves it as it is); see
-- RELEASE.md (APM-17) and scripts/apm17_fix_schema.sh for adding a missing TTL.

`)
	for i, s := range eventTableSchemas {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(createEventTableStatement(s) + ";\n")
	}
	sb.WriteString(`
-- Additive column migrations for tables created by earlier releases
-- (APM-2: event_id, seq; APM-5: build_type, gpu, ram_class, net_type, ctx;
-- APM-4: the exception columns from client_fingerprint to breadcrumbs;
-- APM-3: error_seq, session_ms, unhandled, ctx_merged).
-- Metadata only (existing parts are not rewritten), idempotent.
`)
	for _, s := range eventTableSchemas {
		for _, col := range eventColumns {
			sb.WriteString(addColumnStatement(s.table.Name, col) + ";\n")
		}
		for _, col := range addedColumns[s.table.Name] {
			sb.WriteString(addColumnStatement(s.table.Name, col) + ";\n")
		}
	}
	sb.WriteString(`
-- APM-19: bloom_filter skip index on event_id for the ingest-time duplicate
-- lookup. Additive, idempotent, metadata only: new parts carry the index,
-- existing parts get it when merged. Needs the ALTER ADD INDEX privilege (the
-- ` + "`apm`" + ` service account does not have it; run as an admin, see RELEASE.md).
-- To index existing parts right away (a background mutation per table):
--   ALTER TABLE <table> MATERIALIZE INDEX idx_event_id;
`)
	for _, t := range EventTables {
		sb.WriteString(addEventIDIndexStatement(t.Name) + ";\n")
	}
	sb.WriteString(`
-- Daily aggregations. They only see rows inserted after they were created.
-- Need the CREATE VIEW privilege (not granted to the ` + "`apm`" + ` service account).
`)
	for _, v := range materializedViews {
		sb.WriteString("\n" + v.DDL + ";\n")
	}
	sb.WriteString(`
-- APM-14: audit record of every privacy deletion request (subject id stored
-- as a SHA-256 hash only). See internal/storage/privacy.go.
`)
	sb.WriteString("\n" + privacyAuditTableDDL + ";\n")
	return sb.String()
}

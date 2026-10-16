-- APM-19 phase B: MergeTree -> ReplacingMergeTree(ORDER BY ..., event_id).
--
-- REFERENCE ONLY. Run scripts/apm19_rmt_migrate.sh, which executes exactly
-- these statements for all six tables, derives each new table from the live
-- table's own DDL (so TTL, codecs and settings are kept), loops over the
-- partitions, and checks every step. This file shows the statements for one
-- table, apm_perf_samples, on the production schema (scripts/migrate.sql:
-- TTL 30 days, materialized view apm_fps_daily), so they can be reviewed.
--
-- Needs: an Atomic database (EXCHANGE TABLES), and on ozx_apm.*: SELECT,
-- INSERT, CREATE TABLE, CREATE VIEW (ATTACH), DROP TABLE, DROP VIEW (EXCHANGE,
-- RENAME, DETACH), TRUNCATE, ALTER ADD INDEX and ALTER ATTACH PARTITION.
-- On n192 use the local `default` account (full rights).
-- The phase B binary must already serve all traffic (see RELEASE.md).

-- 1. New table: the live DDL with the engine and ORDER BY changed.
CREATE TABLE ozx_apm.apm_perf_samples_rmt
(
    `timestamp` DateTime64(3), `app_version` String, `platform` String, `device_model` String,
    `os_version` String, `session_id` String, `device_id` String, `scene` String, `fps` Float32,
    `frame_time_ms` Float32, `main_thread_ms` Float32, `gc_alloc_kb` Float32, `mem_mb` Float32,
    `event_id` String DEFAULT '', `seq` UInt64 DEFAULT 0,
    INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (app_version, platform, timestamp, event_id)
TTL timestamp + toIntervalDay(30)
SETTINGS index_granularity = 8192;

-- 2. Swap. The views are detached first: an insert running while its target
--    is renamed can push the whole source table into a view (seen on 26.1).
--    Wait until no insert that started before the DETACH is running.
DETACH TABLE ozx_apm.apm_fps_daily;
EXCHANGE TABLES ozx_apm.apm_perf_samples AND ozx_apm.apm_perf_samples_rmt;
RENAME TABLE ozx_apm.apm_perf_samples_rmt TO ozx_apm.apm_perf_samples_pre_apm19;
ATTACH TABLE ozx_apm.apm_fps_daily;
--    New inserts now go to the ReplacingMergeTree. Wait until no insert that
--    started before the EXCHANGE is running (it still writes to the old table):
SELECT count() FROM system.processes WHERE query_kind = 'Insert' AND elapsed >= <seconds since the EXCHANGE>;

-- 3. Copy, per partition, through a staging table and ATTACH PARTITION FROM
--    (attaching parts does not feed the materialized views; INSERT INTO the
--    live table would add every old row to apm_fps_daily a second time).
CREATE TABLE ozx_apm.apm_perf_samples_apm19_stage AS ozx_apm.apm_perf_samples;
--    for each partition_id of apm_perf_samples_pre_apm19 ('20261015' here):
INSERT INTO ozx_apm.apm_perf_samples_apm19_stage
SELECT * REPLACE (if(event_id = '', concat('srv-apm19-', toString(generateUUIDv4())), event_id) AS event_id)
FROM ozx_apm.apm_perf_samples_pre_apm19 WHERE _partition_id = '20261015'
SETTINGS optimize_on_insert = 0;
--    check: count() of the stage = count() of the source partition, then
ALTER TABLE ozx_apm.apm_perf_samples ATTACH PARTITION ID '20261015' FROM ozx_apm.apm_perf_samples_apm19_stage;
TRUNCATE TABLE ozx_apm.apm_perf_samples_apm19_stage;
--    after the last partition:
DROP TABLE ozx_apm.apm_perf_samples_apm19_stage;

-- 4. Verify: rows copied = rows in _pre_apm19 (unchanged since step 2), as many
--    'srv-apm19-' ids as rows without event_id in _pre_apm19, and no event_id
--    of _pre_apm19 missing from the new table:
SELECT count() FROM ozx_apm.apm_perf_samples_pre_apm19;
SELECT countIf(event_id = '') FROM ozx_apm.apm_perf_samples_pre_apm19;
SELECT countIf(startsWith(event_id, 'srv-apm19-')) FROM ozx_apm.apm_perf_samples;
SELECT count() FROM (SELECT event_id FROM ozx_apm.apm_perf_samples_pre_apm19 WHERE _partition_id = '20261015' AND event_id != ''
                     EXCEPT SELECT event_id FROM ozx_apm.apm_perf_samples WHERE _partition_id = '20261015');

-- 5. Keep apm_perf_samples_pre_apm19 for rollback; later (drop-backup):
-- DROP TABLE ozx_apm.apm_perf_samples_pre_apm19;

-- ROLLBACK (the script's `rollback`):
-- DETACH TABLE ozx_apm.apm_fps_daily;
-- EXCHANGE TABLES ozx_apm.apm_perf_samples AND ozx_apm.apm_perf_samples_pre_apm19;
-- RENAME TABLE ozx_apm.apm_perf_samples_pre_apm19 TO ozx_apm.apm_perf_samples_apm19_rolledback;
-- ATTACH TABLE ozx_apm.apm_fps_daily;
-- (wait for inserts that started before the EXCHANGE)
-- CREATE TABLE ozx_apm.apm_perf_samples_apm19_stage AS ozx_apm.apm_perf_samples;
-- for each partition p of apm_perf_samples_apm19_rolledback: copy the rows inserted since the migration
-- INSERT INTO ozx_apm.apm_perf_samples_apm19_stage
-- SELECT * FROM ozx_apm.apm_perf_samples_apm19_rolledback FINAL
-- WHERE _partition_id = 'p' AND NOT startsWith(event_id, 'srv-apm19-')
--   AND event_id NOT IN (SELECT event_id FROM ozx_apm.apm_perf_samples WHERE _partition_id = 'p' AND event_id != '')
-- SETTINGS optimize_on_insert = 0;
-- ALTER TABLE ozx_apm.apm_perf_samples ATTACH PARTITION ID 'p' FROM ozx_apm.apm_perf_samples_apm19_stage;
-- TRUNCATE TABLE ozx_apm.apm_perf_samples_apm19_stage;
-- DROP TABLE ozx_apm.apm_perf_samples_apm19_stage;

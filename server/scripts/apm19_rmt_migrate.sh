#!/usr/bin/env bash
# APM-19 phase B: convert the six event tables from MergeTree to
# ReplacingMergeTree(ORDER BY ..., event_id), and back (rollback).
#
# One-off, run by a human with ClickHouse admin rights (on n192: the local
# `default` account). Never run automatically. See RELEASE.md, "APM-19, phase B".
# scripts/apm19_rmt_migrate.sql shows the statements for one table.
#
# Usage:
#   scripts/apm19_rmt_migrate.sh check      read-only preflight
#   scripts/apm19_rmt_migrate.sh migrate    MergeTree -> ReplacingMergeTree
#   scripts/apm19_rmt_migrate.sh verify     re-check row counts after migrate
#   scripts/apm19_rmt_migrate.sh rollback   ReplacingMergeTree -> MergeTree
#   scripts/apm19_rmt_migrate.sh drop-backup   drop the <table>_pre_apm19 copies
#
# Environment:
#   CH      clickhouse-client command (default: clickhouse-client). Word-split,
#           so extra arguments work: CH="clickhouse-client --host h --user u --password p"
#   DB      database (default: ozx_apm)
#   TABLES  space-separated subset of the six tables (default: all six)
#   ALLOW_RENAME=1  allow the non-atomic RENAME fallback when the database
#           engine is not Atomic (EXCHANGE TABLES needs Atomic)
#   INFLIGHT_WAIT_MAX  seconds to wait for inserts that started before the
#           swap (default 120; the server uses max_execution_time=60)
#
# Per table, migrate does:
#   1. CREATE TABLE <t>_rmt: the live table's own DDL (columns, codecs, TTL,
#      settings, indexes) with ENGINE = ReplacingMergeTree and event_id
#      appended to ORDER BY; adds the event_id index if missing.
#   2. EXCHANGE TABLES <t> AND <t>_rmt, then RENAME <t>_rmt TO <t>_pre_apm19.
#      From here new inserts land in the new (empty) table: nothing is lost.
#      Materialized views follow the table name, so they keep being fed. They
#      are detached for the few seconds of the swap (see swap_all), so rows
#      inserted in that window are missing from the apm_*_daily views only.
#   3. Wait until no insert that started before the swap is still running
#      (such an insert still writes into the old table).
#   4. Copy <t>_pre_apm19 into <t>, one partition at a time, through a staging
#      table and ALTER TABLE ... ATTACH PARTITION ... FROM: unlike
#      INSERT INTO <t> SELECT, attaching parts does not trigger the
#      materialized views, so the apm_*_daily aggregates are not doubled.
#      Rows without event_id (old SDKs) get 'srv-apm19-' || UUIDv4, so they
#      never collapse together.
#   5. Verify: rows copied == rows in <t>_pre_apm19, per partition and in
#      total, and <t>_pre_apm19 did not change during the copy.
#   6. Keep <t>_pre_apm19 for rollback; drop it later with drop-backup.
#
# Rollback (per table): EXCHANGE <t> back with <t>_pre_apm19 (the old
# MergeTree is live again, the ReplacingMergeTree becomes
# <t>_apm19_rolledback), wait for in-flight inserts, then copy the rows that
# were inserted into the ReplacingMergeTree since the migration (event_id not
# in the old table and not a migration id), again through ATTACH PARTITION.

set -euo pipefail

CH=${CH:-clickhouse-client}
DB=${DB:-ozx_apm}
TABLES=${TABLES:-"apm_perf_samples apm_janks apm_startups apm_scene_loads apm_exceptions apm_crashes"}
INFLIGHT_WAIT_MAX=${INFLIGHT_WAIT_MAX:-120}
MIGRATION_ID_PREFIX="srv-apm19-"

# Expected sort key of each table before the migration.
old_key() {
  case $1 in
    apm_perf_samples|apm_startups) echo "app_version, platform, timestamp" ;;
    apm_janks) echo "app_version, scene, timestamp" ;;
    apm_scene_loads) echo "app_version, scene_name, timestamp" ;;
    apm_exceptions|apm_crashes) echo "app_version, fingerprint, timestamp" ;;
    *) echo "unknown table $1" >&2; exit 2 ;;
  esac
}

q() { $CH -d "$DB" -q "$1"; }
log() { echo "[$(date '+%F %T')] $*"; }
die() { echo "ERROR: $*" >&2; exit 1; }
exists() { [ "$(q "SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = '$1'")" = 1 ]; }
engine() { q "SELECT engine FROM system.tables WHERE database = currentDatabase() AND name = '$1'"; }
sortkey() { q "SELECT sorting_key FROM system.tables WHERE database = currentDatabase() AND name = '$1'"; }
rows() { q "SELECT count() FROM $1"; }
columns_of() { q "SELECT arrayStringConcat(groupArray(concat(name, ' ', type)), ', ') FROM (SELECT name, type FROM system.columns WHERE database = currentDatabase() AND table = '$1' ORDER BY position)"; }
partitions_of() { q "SELECT DISTINCT partition_id FROM system.parts WHERE database = currentDatabase() AND table = '$1' AND active ORDER BY partition_id"; }
db_engine() { q "SELECT engine FROM system.databases WHERE name = currentDatabase()"; }

swap() { # swap <a> <b>: exchange two tables atomically
  if [ "$(db_engine)" = Atomic ]; then
    q "EXCHANGE TABLES $1 AND $2"
  else
    [ "${ALLOW_RENAME:-0}" = 1 ] || die "database $DB is $(db_engine), not Atomic: EXCHANGE TABLES is unavailable. Re-run with ALLOW_RENAME=1 to use RENAME (inserts fail for a moment, see RELEASE.md)."
    q "RENAME TABLE $1 TO $1_apm19_swap, $2 TO $1, $1_apm19_swap TO $2"
  fi
}

# Inserts that started before the swap still write into the old table object.
# Wait until none is running and no async insert queued before the swap is
# pending (async inserts are flushed within async_insert_busy_timeout_ms).
wait_inflight() {
  local t0=$1 waited=0 n
  while :; do
    n=$(q "SELECT
      (SELECT count() FROM system.processes WHERE query_kind = 'Insert' AND elapsed >= toFloat64(toUnixTimestamp(now()) - $t0) - 1)
    + (SELECT count() FROM system.asynchronous_inserts WHERE database = currentDatabase() AND toUnixTimestamp(first_update) <= $t0)")
    [ "$n" = 0 ] && break
    [ "$waited" -ge "$INFLIGHT_WAIT_MAX" ] && die "$n inserts that started before the swap are still running after ${waited}s"
    sleep 1; waited=$((waited + 1))
  done
  sleep 2 # margin for an insert between its last block and commit
  log "no insert from before the swap is running (waited ${waited}s)"
}

# Materialized views fed by $TABLES, as db.name.
dependent_mvs() {
  q "SELECT arrayStringConcat(groupArray(v), ' ') FROM (
       SELECT arrayJoin(arrayMap((d, n) -> concat(d, '.', n), dependencies_database, dependencies_table)) AS v
       FROM system.tables WHERE database = currentDatabase() AND name IN ('$(echo $TABLES | sed "s/ /','/g")'))"
}

# swap_all <other suffix> <new suffix for the old table>
# Swaps every table with <table><other>, then renames the table that was live
# to <table><new suffix>. The materialized views fed by the tables are
# detached around the swap: an insert that runs while its target table is
# renamed can push the WHOLE source table into a materialized view instead of
# the inserted block (seen on ClickHouse 26.1: apm_fps_daily doubled in 2 of
# 4 rehearsals; never with the views detached). While detached (a few
# seconds), inserted rows do not reach the views. DETACH without PERMANENTLY:
# a server restart attaches them again.
swap_all() {
  local other=$1 renamed=$2 t td mv
  MVS=$(dependent_mvs)
  if [ -n "$MVS" ]; then
    td=$(q "SELECT toUnixTimestamp(now())")
    STATE_MV=detached
    for mv in $MVS; do q "DETACH TABLE $mv"; done
    log "detached materialized views: $MVS"
    wait_inflight "$td"   # inserts that started before the detach still push to the views
  fi
  SWAP_T0=$(q "SELECT toUnixTimestamp(now())")
  for t in $TABLES; do
    swap "$t" "${t}${other}"
    q "RENAME TABLE ${t}${other} TO ${t}${renamed}"
    log "swapped: $t is now $(engine "$t"), the table that was live is ${t}${renamed}"
  done
  if [ -n "$MVS" ]; then
    for mv in $MVS; do q "ATTACH TABLE $mv"; done
    STATE_MV=""
    log "attached materialized views again; they missed the rows inserted in the last $(( $(q "SELECT toUnixTimestamp(now())") - td ))s"
  fi
  wait_inflight "$SWAP_T0"   # inserts that started before the swap still write into the old table
}

preflight_migrate() {
  local t=$1 k
  exists "$t" || die "$t does not exist"
  for s in "${t}_rmt" "${t}_pre_apm19" "${t}_apm19_stage" "${t}_apm19_rolledback"; do
    exists "$s" && die "$s already exists: an earlier run did not finish. Inspect it (RELEASE.md) before retrying."
  done
  [ "$(engine "$t")" = MergeTree ] || die "$t is $(engine "$t"), expected MergeTree (already migrated?)"
  k=$(sortkey "$t")
  [ "$k" = "$(old_key "$t")" ] || die "$t has ORDER BY ($k), expected ($(old_key "$t"))"
  q "SELECT count() FROM system.columns WHERE database = currentDatabase() AND table = '$t' AND name = 'event_id' AND type = 'String'" | grep -qx 1 \
    || die "$t has no event_id String column (APM-2 phase 2 not applied)"
}

create_rmt() {
  local t=$1 k ddl
  k=$(old_key "$t")
  # The live table's own DDL with the engine and sort key changed. The checks
  # below make sure every replacement happened.
  ddl=$($CH -d "$DB" --format TSVRaw -q "
    SELECT replaceOne(replaceOne(replaceOne(create_table_query,
        'CREATE TABLE $DB.$t ', 'CREATE TABLE $DB.${t}_rmt '),
        'ENGINE = MergeTree ', 'ENGINE = ReplacingMergeTree '),
        'ORDER BY ($k)', 'ORDER BY ($k, event_id)')
    FROM system.tables WHERE database = currentDatabase() AND name = '$t'")
  echo "$ddl" | grep -q "CREATE TABLE $DB.${t}_rmt " || die "could not rewrite the table name in: $ddl"
  echo "$ddl" | grep -q "ENGINE = ReplacingMergeTree " || die "could not rewrite the engine in: $ddl"
  echo "$ddl" | grep -qF "ORDER BY ($k, event_id)" || die "could not rewrite ORDER BY in: $ddl"
  log "CREATE ${t}_rmt: $ddl"
  q "$ddl"
  if ! q "SELECT create_table_query FROM system.tables WHERE database = currentDatabase() AND name = '${t}_rmt'" | grep -q "INDEX idx_event_id "; then
    q "ALTER TABLE ${t}_rmt ADD INDEX idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1"
  fi
  [ "$(engine "${t}_rmt")" = ReplacingMergeTree ] || die "${t}_rmt engine is $(engine "${t}_rmt")"
  [ "$(sortkey "${t}_rmt")" = "$k, event_id" ] || die "${t}_rmt ORDER BY is ($(sortkey "${t}_rmt"))"
  [ "$(columns_of "${t}_rmt")" = "$(columns_of "$t")" ] || die "${t}_rmt columns differ from $t"
}

# copy_partitions <from> <FINAL or ""> <to> <select list> <extra WHERE or ""> <label>
# Copies <from> into <to> partition by partition through <to>_apm19_stage
# (same structure as <to>) and ATTACH PARTITION FROM, which does not fire
# materialized views. __P__ in the WHERE is replaced by the partition id.
# Writes the total number of rows copied to $TMPF.
copy_partitions() {
  local from=$1 final=$2 to=$3 sel=$4 where=$5 stage="${3}_apm19_stage" total=0 p n_src n_stage w
  q "CREATE TABLE $stage AS $to"
  for p in $(partitions_of "$from"); do
    w="_partition_id = '$p'${where:+ AND ${where//__P__/$p}}"
    n_src=$(q "SELECT count() FROM $from $final WHERE $w")
    [ "$n_src" = 0 ] && continue
    # optimize_on_insert=0: a ReplacingMergeTree would otherwise collapse rows
    # with the same sort key inside the inserted block, and the row counts
    # below could not prove that nothing was lost. Merges collapse them later.
    q "INSERT INTO $stage SELECT $sel FROM $from $final WHERE $w SETTINGS optimize_on_insert = 0"
    n_stage=$(q "SELECT count() FROM $stage")
    [ "$n_stage" = "$n_src" ] || die "partition $p: staged $n_stage rows, source has $n_src"
    q "ALTER TABLE $to ATTACH PARTITION ID '$p' FROM $stage"
    q "TRUNCATE TABLE $stage"
    total=$((total + n_src))
    log "  $6 partition $p: $n_src rows"
  done
  q "DROP TABLE $stage"
  echo "$total" > "$TMPF"
}

cmd_check() {
  log "ClickHouse $(q 'SELECT version()'), database $DB engine $(db_engine)"
  [ "$(db_engine)" = Atomic ] || echo "WARNING: database is not Atomic; migrate needs ALLOW_RENAME=1 (see RELEASE.md)"
  q "SELECT name, engine, sorting_key, total_rows, formatReadableSize(total_bytes) AS size,
            position(create_table_query, ' TTL ') > 0 AS has_ttl,
            position(create_table_query, 'INDEX idx_event_id ') > 0 AS has_event_id_index
     FROM system.tables WHERE database = currentDatabase()
       AND (name IN ('$(echo $TABLES | sed "s/ /','/g")') OR name LIKE '%\\_rmt' OR name LIKE '%\\_pre\\_apm19' OR name LIKE '%\\_apm19\\_%')
     ORDER BY name FORMAT PrettyCompactMonoBlock"
  log "materialized views fed by these tables (they follow the table name; the copy does not feed them):"
  q "SELECT name, dependencies_table AS materialized_views FROM system.tables
     WHERE database = currentDatabase() AND name IN ('$(echo $TABLES | sed "s/ /','/g")') AND notEmpty(dependencies_table)
     FORMAT PrettyCompactMonoBlock"
  for t in $TABLES; do
    q "SELECT '$t' AS table, countIf(event_id = '') AS rows_without_event_id, count() AS rows FROM $t FORMAT PrettyCompactMonoBlock"
  done
  log "free disk (the migration needs roughly the size of the six tables once more, until drop-backup):"
  q "SELECT name, path, formatReadableSize(free_space) AS free FROM system.disks FORMAT PrettyCompactMonoBlock"
  q "SELECT count() AS running_mutations FROM system.mutations WHERE database = currentDatabase() AND NOT is_done"
}

cmd_migrate() {
  local t n_pre n_pre_after copied legacy migrated empty missing
  for t in $TABLES; do preflight_migrate "$t"; done
  log "preflight ok for: $TABLES"
  for t in $TABLES; do create_rmt "$t"; done

  STATE=swapping
  swap_all _rmt _pre_apm19

  for t in $TABLES; do
    n_pre=$(rows "${t}_pre_apm19")
    legacy=$(q "SELECT countIf(event_id = '') FROM ${t}_pre_apm19")
    log "$t: copying $n_pre rows ($legacy without event_id) from ${t}_pre_apm19"
    copy_partitions "${t}_pre_apm19" "" "$t" \
      "* REPLACE (if(event_id = '', concat('$MIGRATION_ID_PREFIX', toString(generateUUIDv4())), event_id) AS event_id)" \
      "" "$t"
    copied=$(cat "$TMPF")
    n_pre_after=$(rows "${t}_pre_apm19")
    migrated=$(q "SELECT count() FROM $t WHERE startsWith(event_id, '$MIGRATION_ID_PREFIX')")
    [ "$n_pre_after" = "$n_pre" ] || die "$t: ${t}_pre_apm19 changed during the copy ($n_pre -> $n_pre_after rows): an insert reached the old table late. Rows are not lost (they are in ${t}_pre_apm19); see RELEASE.md before continuing."
    [ "$copied" = "$n_pre" ] || die "$t: copied $copied rows, ${t}_pre_apm19 has $n_pre"
    [ "$migrated" = "$legacy" ] || die "$t: $migrated rows carry a migration id, expected $legacy"
    missing=$(missing_ids "$t")
    [ "$missing" = 0 ] || die "$t: $missing event_ids of ${t}_pre_apm19 are not in $t"
    empty=$(q "SELECT count() FROM $t WHERE event_id = ''")
    log "$t: OK, copied $copied = $n_pre rows; now $(rows "$t") rows in $t ($((copied)) copied + live inserts since the swap)"
    [ "$empty" = 0 ] || echo "WARNING: $t has $empty rows with an empty event_id: a binary without APM-19 phase B is still writing. Old-SDK rows with the same (app_version, key, timestamp) can collapse; deploy the phase B binary everywhere."
  done
  STATE=done
  log "migration done. Keep the *_pre_apm19 tables until you are sure; drop them with: $0 drop-backup"
}

# missing_ids <t>: event_ids of <t>_pre_apm19 not present in <t>, counted per
# partition to bound memory (a copy keeps its timestamp, so its partition).
missing_ids() {
  local t=$1 p n total=0
  for p in $(partitions_of "${t}_pre_apm19"); do
    n=$(q "SELECT count() FROM (SELECT event_id FROM ${t}_pre_apm19 WHERE _partition_id = '$p' AND event_id != ''
                                EXCEPT SELECT event_id FROM $t WHERE _partition_id = '$p')")
    total=$((total + n))
  done
  echo "$total"
}

cmd_verify() {
  local t n_pre n_mig legacy
  for t in $TABLES; do
    exists "${t}_pre_apm19" || die "${t}_pre_apm19 does not exist"
    n_pre=$(rows "${t}_pre_apm19"); legacy=$(q "SELECT countIf(event_id = '') FROM ${t}_pre_apm19")
    n_mig=$(q "SELECT count() FROM $t WHERE startsWith(event_id, '$MIGRATION_ID_PREFIX')")
    q "SELECT '$t' AS table, '$(engine "$t")' AS engine, '$(sortkey "$t")' AS sorting_key, $n_pre AS rows_pre_apm19,
              (SELECT count() FROM $t) AS rows_now, $legacy AS legacy_in_pre, $n_mig AS migration_ids_now,
              (SELECT count() FROM $t WHERE event_id = '') AS empty_ids_now,
              $(missing_ids "$t") AS ids_missing_now
       FORMAT Vertical"
  done
}

cmd_rollback() {
  local t copied
  for t in $TABLES; do
    exists "${t}_pre_apm19" || die "${t}_pre_apm19 does not exist: nothing to roll back to"
    [ "$(engine "${t}_pre_apm19")" = MergeTree ] || die "${t}_pre_apm19 is $(engine "${t}_pre_apm19")"
    [ "$(engine "$t")" = ReplacingMergeTree ] || die "$t is $(engine "$t"), expected ReplacingMergeTree"
    exists "${t}_apm19_rolledback" && die "${t}_apm19_rolledback already exists"
  done
  for t in $TABLES; do
    # Left over by a migrate that stopped half-way; it only holds staged copies.
    exists "${t}_apm19_stage" && { q "DROP TABLE ${t}_apm19_stage"; log "dropped leftover ${t}_apm19_stage"; }
  done
  swap_all _pre_apm19 _apm19_rolledback
  for t in $TABLES; do
    # Rows inserted after the migration: not a migration id and not already
    # in the old table (same partition: a re-sent event has the same
    # timestamp). FINAL: one row per event even if a duplicate was not merged.
    log "$t: copying back the rows inserted since the migration"
    copy_partitions "${t}_apm19_rolledback" FINAL "$t" "*" \
      "NOT startsWith(event_id, '$MIGRATION_ID_PREFIX') AND event_id NOT IN (SELECT event_id FROM $t WHERE _partition_id = '__P__' AND event_id != '')" "$t"
    copied=$(cat "$TMPF")
    log "$t: OK, rolled back; copied back $copied rows, $t now has $(rows "$t") rows. ${t}_apm19_rolledback is kept for inspection."
  done
}

# Drops the old tables, but only when every row was copied: every event_id of
# <t>_pre_apm19 is in <t>, and <t> has as many migration ids as <t>_pre_apm19
# has rows without event_id. FORCE=1 skips the check (e.g. after TTL removed
# old rows from one table but not the other).
cmd_drop_backup() {
  local t missing legacy migrated
  for t in $TABLES; do
    exists "${t}_pre_apm19" || continue
    [ "$(engine "$t")" = ReplacingMergeTree ] || die "$t is not ReplacingMergeTree; refusing to drop ${t}_pre_apm19"
    exists "${t}_apm19_stage" && die "${t}_apm19_stage exists: a copy did not finish; refusing to drop ${t}_pre_apm19"
    if [ "${FORCE:-0}" != 1 ]; then
      missing=$(missing_ids "$t")
      legacy=$(q "SELECT countIf(event_id = '') FROM ${t}_pre_apm19")
      migrated=$(q "SELECT count() FROM $t WHERE startsWith(event_id, '$MIGRATION_ID_PREFIX')")
      [ "$missing" = 0 ] && [ "$migrated" -ge "$legacy" ] || die "$t: the copy is incomplete ($missing event_ids missing, $migrated of $legacy old-SDK rows copied); refusing to drop ${t}_pre_apm19 (FORCE=1 overrides)"
    fi
  done
  for t in $TABLES; do
    exists "${t}_pre_apm19" && { q "DROP TABLE ${t}_pre_apm19"; log "dropped ${t}_pre_apm19"; }
  done
}

STATE=""; STATE_MV=""; MVS=""
on_exit() {
  rm -f "$TMPF"
  if [ "$STATE_MV" = detached ]; then
    echo "STOPPED while materialized views were detached. Attach them again:" >&2
    for mv in $MVS; do echo "  $CH -q 'ATTACH TABLE $mv'" >&2; done
  fi
  if [ "$STATE" = swapping ]; then
    echo "MIGRATION STOPPED after tables were swapped. New inserts are safe in the new tables and the old rows are in the *_pre_apm19 tables. Run '$0 rollback' (it restores the old tables and copies back every row inserted since), investigate, drop the *_apm19_rolledback tables, and run migrate again." >&2
  fi
}
TMPF=$(mktemp)
trap on_exit EXIT
case "${1:-}" in
  check) cmd_check ;;
  migrate) cmd_migrate ;;
  verify) cmd_verify ;;
  rollback) cmd_rollback ;;
  drop-backup) cmd_drop_backup ;;
  *) sed -n '2,20p' "$0"; exit 2 ;;
esac

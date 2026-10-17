#!/usr/bin/env bash
# APM-17: bring a database whose tables were created by the server's startup
# migration before APM-17 up to the schema in scripts/migrate.sql: add the
# 30-day TTL to event tables that have none and create the apm_*_daily
# materialized views that are missing.
#
# One-off, run by a human with ClickHouse admin rights (on n192: the local
# `default` account). The server's `apm` account cannot do either step (no
# ALTER MODIFY TTL, no CREATE VIEW); the server only warns at startup.
# See RELEASE.md, "APM-17".
#
# Usage:
#   scripts/apm17_fix_schema.sh check   read-only: what is missing, and how many
#                                       rows adding the TTL would delete
#   scripts/apm17_fix_schema.sh apply   add the missing TTLs and views
#                                       (asks for confirmation; YES=1 skips it)
#
# Environment:
#   CH   clickhouse-client command (default: clickhouse-client). Word-split,
#        so extra arguments work: CH="clickhouse-client --password"
#   DB   database (default: ozx_apm)
#   MIGRATE_SQL  the generated schema (default: migrate.sql next to this script)
#
# WARNING: ALTER TABLE ... MODIFY TTL immediately starts a background mutation
# (materialize_ttl_after_modify=1, the ClickHouse default) that rewrites the
# table's parts and DELETES every row older than 30 days. `check` prints how
# many rows that is. Tables that already have a TTL are never touched.
#
# The views only aggregate rows inserted after they are created (no POPULATE:
# back-filling would double rows inserted while it runs).

set -euo pipefail

CH=${CH:-clickhouse-client}
DB=${DB:-ozx_apm}
MIGRATE_SQL=${MIGRATE_SQL:-"$(cd "$(dirname "$0")" && pwd)/migrate.sql"}
TABLES="apm_perf_samples apm_janks apm_startups apm_scene_loads apm_exceptions apm_crashes"
VIEWS="apm_fps_daily apm_startup_daily apm_exception_daily apm_crash_daily"
# Same as storage.EventTTL. toDateTime(): 23.8 and 24.1 reject a TTL on DateTime64.
TTL_EXPR="toDateTime(timestamp) + INTERVAL 30 DAY"

q() { $CH -d "$DB" -q "$1"; }
log() { echo "[$(date '+%F %T')] $*"; }
die() { echo "ERROR: $*" >&2; exit 1; }
exists() { [ "$(q "SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = '$1'")" = 1 ]; }
has_ttl() { [ "$(q "SELECT position(engine_full, ' TTL ') > 0 FROM system.tables WHERE database = currentDatabase() AND name = '$1'")" = 1 ]; }
expired_rows() { q "SELECT count() FROM $1 WHERE timestamp < now() - INTERVAL 30 DAY"; }

# The CREATE MATERIALIZED VIEW statement for $1 from the generated schema.
view_ddl() {
  awk -v name="$1" '
    $0 == "CREATE MATERIALIZED VIEW IF NOT EXISTS " name { on = 1 }
    on { print; if ($0 ~ /;$/) exit }
  ' "$MIGRATE_SQL"
}

missing_ttl_tables() {
  local t
  for t in $TABLES; do
    exists "$t" || die "table $t does not exist in $DB (start the server once, or run migrate.sql)"
    has_ttl "$t" || echo "$t"
  done
}

missing_views() {
  local v
  for v in $VIEWS; do
    exists "$v" || echo "$v"
  done
}

check() {
  [ -f "$MIGRATE_SQL" ] || die "$MIGRATE_SQL not found"
  log "database $DB"
  local t v n
  for t in $TABLES; do
    exists "$t" || die "table $t does not exist in $DB"
    if has_ttl "$t"; then
      echo "  $t: TTL present: $(q "SELECT extract(engine_full, 'TTL (.*?)( SETTINGS|\$)') FROM system.tables WHERE database = currentDatabase() AND name = '$t'")"
    else
      n=$(expired_rows "$t")
      echo "  $t: NO TTL; apply deletes $n rows older than 30 days (of $(q "SELECT count() FROM $t"))"
    fi
  done
  for v in $VIEWS; do
    if exists "$v"; then
      echo "  $v: present"
    else
      [ -n "$(view_ddl "$v")" ] || die "no DDL for $v in $MIGRATE_SQL"
      echo "  $v: MISSING (apply creates it; it aggregates new rows only)"
    fi
  done
}

apply() {
  check
  local tables views
  tables=$(missing_ttl_tables)
  views=$(missing_views)
  if [ -z "$tables" ] && [ -z "$views" ]; then
    log "nothing to do"
    return
  fi
  if [ "${YES:-0}" != 1 ]; then
    [ -t 0 ] || die "not a terminal: re-run with YES=1 to confirm"
    read -r -p "Add the TTL to: ${tables:-none}; create: ${views:-none}. Rows older than 30 days are deleted. Type yes: " answer
    [ "$answer" = yes ] || die "aborted"
  fi
  local t v
  for t in $tables; do
    log "$t: MODIFY TTL $TTL_EXPR"
    q "ALTER TABLE $t MODIFY TTL $TTL_EXPR"
  done
  for v in $views; do
    log "$v: CREATE MATERIALIZED VIEW"
    q "$(view_ddl "$v")"
  done
  log "done; TTL mutations still running (if any):"
  q "SELECT table, mutation_id, command, is_done, latest_fail_reason FROM system.mutations WHERE database = currentDatabase() AND NOT is_done"
}

case "${1:-}" in
  check) check ;;
  apply) apply ;;
  *) sed -n '2,30p' "$0"; exit 2 ;;
esac

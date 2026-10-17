# Server release notes and rollback procedure

ozx_apm is a live service. Every release here must be deployable on its own,
safe under a rolling deploy (old and new binaries serving at the same time),
and must say exactly how to roll back.

## APM-2, phase 1: ingest correctness (no schema change)

Deploy this first. It changes no table, so rolling back to the previous
binary needs nothing but redeploying it.

Changes:

- Every insert names its columns (`INSERT INTO t (a, b, ...)`). The previous
  binary used positional inserts, which fail with
  `expected N arguments, got M` as soon as any column is added. This binary
  keeps working after an additive migration, which is what makes it the
  rollback target for phase 2.
- The ingest response is truthful. Only events that were actually inserted
  count as `accepted`; storage failures are counted in `failed`.
  - A failed table insert is retried server-side (2 extra attempts) before
    answering, so a transient ClickHouse error does not make the client
    resend the whole batch.
  - If it still fails and the batch comes from an SDK that can de-duplicate
    (events carry `event_id`, or the `X-Apm-Sdk-Version` header is present):
    HTTP 503, `stored:false`, `retry:true`, `Retry-After: 30`. The client keeps
    the batch and resends it.
  - If it still fails and the batch comes from an old SDK (no `event_id`,
    no header): HTTP 200 as before, with `stored:false` and an error log line
    "dropping events from a legacy client". A resend from an old SDK could
    never be de-duplicated, and old SDKs only look at the status code.
- Payloads that can never be accepted get HTTP 400/413 with a JSON body
  containing `reject_code` (`invalid_json`, `payload_too_large`). New SDKs
  discard (quarantine) a batch only on an explicit `reject_code`; a bare 400
  from a proxy or the gzip middleware is retried.
- A batch with one malformed event (e.g. `"fps":59,50` from an old SDK on a
  German-locale device) is no longer rejected as a whole: the well-formed
  events are salvaged and only the broken one is rejected.
- Events up to 30 days old are accepted (was 7). Offline players upload late
  and events keep their original time; 30 days matches the detail retention.
- Error messages show the event index in decimal (was `string(rune(i))`).

Response body:

```json
{"accepted": 18, "rejected": 1, "failed": 0, "stored": true, "retry": false,
 "reject_code": "", "errors": ["event 3: missing device_id"]}
```

Rollback: redeploy the previous binary. No schema action.

## APM-2, phase 2: store `event_id` and `seq` (additive schema change)

Deploy only after phase 1 runs everywhere: no instance of the pre-phase-1
binary may still serve traffic, including during the rolling deploy.

Changes:

- `Migrate` (run on every server start) adds two columns to all six event
  tables (`apm_perf_samples`, `apm_janks`, `apm_startups`, `apm_scene_loads`,
  `apm_exceptions`, `apm_crashes`):
  ```sql
  ALTER TABLE <table> ADD COLUMN IF NOT EXISTS event_id String DEFAULT '';
  ALTER TABLE <table> ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0;
  ```
  The same statements are in `scripts/migrate.sql`. In ClickHouse they only
  change metadata (existing parts are not rewritten), and they are idempotent.
- Inserts write `event_id` and `seq`. Events from old SDKs store `''` / `0`.
- No de-duplication yet (APM-12).

Rolling deploy: phase-1 and phase-2 binaries can serve side by side. Phase 1
names its columns, so it keeps inserting after the ALTER and its rows get the
defaults. Verified on ClickHouse 23.8 and 24.1.

### Rollback

- **To phase 1:** redeploy the phase-1 binary. No schema action is needed;
  the extra columns stay and get their defaults.
- **To anything older than phase 1** (the binary with positional
  `INSERT INTO t` and no column list): that binary fails every insert against
  the migrated tables with `expected 15 arguments, got 13`, and it still
  answers 200, so clients delete the data. **Before** starting it, stop every
  phase-2 instance (its `Migrate` would add the columns back on restart), then
  drop the columns on all six tables:
  ```sql
  ALTER TABLE apm_perf_samples DROP COLUMN IF EXISTS event_id, DROP COLUMN IF EXISTS seq;
  ALTER TABLE apm_janks        DROP COLUMN IF EXISTS event_id, DROP COLUMN IF EXISTS seq;
  ALTER TABLE apm_startups     DROP COLUMN IF EXISTS event_id, DROP COLUMN IF EXISTS seq;
  ALTER TABLE apm_scene_loads  DROP COLUMN IF EXISTS event_id, DROP COLUMN IF EXISTS seq;
  ALTER TABLE apm_exceptions   DROP COLUMN IF EXISTS event_id, DROP COLUMN IF EXISTS seq;
  ALTER TABLE apm_crashes      DROP COLUMN IF EXISTS event_id, DROP COLUMN IF EXISTS seq;
  ```
  This loses the stored `event_id`/`seq` values. Prefer rolling back to phase 1.

## APM-2: optional `clickhouse.async_insert` (no schema change)

Off by default. When `clickhouse.async_insert: true`, ingest inserts are sent
as `INSERT ... VALUES` with `async_insert=1, wait_for_async_insert=1`.
ClickHouse then buffers concurrent requests into one part instead of one part
per request, which caps part creation when many clients drain an offline
backlog at once. The request still returns only after the data is written, so
the ingest response stays truthful. Cost: up to `async_insert_busy_timeout_ms`
(200ms by default) of extra latency per request.

Measured (40-50 concurrent single-row inserts, merges stopped):

| path                                   | 23.8.16 | 24.1.8 |
|----------------------------------------|---------|--------|
| native batch (default)                 | 50 parts | 50 parts |
| native batch + async_insert settings   | 50 parts (setting ignored) | 5 parts |
| `INSERT ... VALUES` + async (this flag) | 4-5 parts | 4-5 parts |

The native-protocol batch silently ignores `async_insert` on 23.8, which is
why the flag uses the `VALUES` path. Both paths store identical values
(integration test `TestInsertPaths_RoundTripIdentically`, run on 23.8 and 24.1).

Suggested rollout: enable after phase 2 has settled, on one instance first,
and watch insert latency and `system.asynchronous_inserts`. Rollback: set it
back to `false` and restart; no schema action.

## APM-19, phase A: drop re-sent events at ingest (additive)

Delivery from the SDK is at least once. If the app is killed after the server
stored a batch but before the SDK deleted it locally, the next launch sends
the same events again (APM-18 on a Samsung A33: one batch of 60 rows stored
twice). Phase A stops these duplicates at ingest. Phase B (below) is the
eventual safety net for what phase A cannot catch.

Changes:

- Before inserting, for each table, the server drops events whose `event_id`
  appeared earlier in the same batch, then asks ClickHouse which of the
  remaining ids are already stored:
  ```sql
  SELECT DISTINCT event_id FROM <table>
  WHERE app_version IN (...) AND <platform|scene|scene_name|fingerprint> IN (...)
    AND timestamp >= <batch min> AND timestamp <= <batch max>
    AND event_id IN (...)          -- at most 500 ids per query
  ```
  The filters are the table's sort-key prefix and the exact time range of the
  looked-up events, so the primary key prunes the read to a few granules. A
  re-sent event has the same values as the stored copy, so they never hide it.
- Dropped events count as `accepted` (the server has them; the client must
  delete them) and are reported in a new response field `duplicates`.
  Logs: `dropped duplicate events` (in_batch / already_stored), and
  `duplicates` / `dedup_lookup_failed` on every `ingested events` line.
  There is no metrics endpoint in this server; alert on these log fields.
- Events without `event_id` (old SDKs) are never looked up or dropped.
- If a lookup fails or takes longer than 5 s, the events are **inserted
  anyway** and a warning `duplicate lookup failed` is logged. Rationale: a
  lost event is worse than a duplicate, the client would otherwise get a 503
  and resend the whole batch (more load on a struggling ClickHouse), and
  phase B removes the duplicate on merge.
- A `bloom_filter(0.001)` skip index `idx_event_id` on `event_id`, in every
  `CREATE TABLE` and added to existing tables by `Migrate`:
  ```sql
  ALTER TABLE <table> ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1;
  ```
  Additive, idempotent, metadata only. **The production `apm` account has no
  `ALTER ADD INDEX` privilege** (its grants are `SELECT, INSERT, CREATE TABLE,
  ALTER ADD COLUMN ON ozx_apm.*`). `Migrate` therefore treats the index as
  optional: it skips tables that already have it and only logs a warning
  (`could not add the event_id index`) when the ALTER is refused; the server
  starts and de-duplicates without the index. An admin adds it (below).
- `MATERIALIZE INDEX` is not run by `Migrate`: it is a mutation (it rewrites
  the index files of every existing part in the background) and must not be
  started on every server start. Without it, parts written before the ALTER
  have no index until they are merged; the lookup still works on them, it
  only reads more. Production holds little data today, so materializing is
  cheap and is part of the steps below; if phase B follows soon it is not
  needed, because the phase B copy writes the index for all rows anyway.

Not caught by phase A (phase B removes these on merge):

- two requests carrying the same events at the same moment (both lookups
  run before either insert);
- a lookup that failed or timed out;
- a duplicate written by a binary without phase A (e.g. during the rolling
  deploy, or after a rollback);
- the server's own insert retry (`insertWithRetry`) when ClickHouse wrote the
  rows but answered with an error (seen when a materialized view fails after
  the main table was written).

Lookup cost (ClickHouse 26.1.12 and 24.1.8 in Docker on an M-series Mac, 5M
`apm_perf_samples` rows in one day: 20,000 devices, 3 versions, 2 platforms;
one batch of 60 ids, 40 stored and 20 new; median of 6 warm runs):

| lookup                                  | index | rows read | time   |
|-----------------------------------------|-------|-----------|--------|
| batch from one session (60 s range)     | any   | 8,192 (1 granule) | 1-2 ms |
| same ids, range = whole day (backlog)   | none  | 1,343,488 | 7-8 ms |
| same ids, range = whole day             | bloom_filter(0.01)  | 540-614K | 6-7 ms |
| same ids, range = whole day             | bloom_filter(0.001) | 74-131K  | 4 ms   |

The index costs 9 MiB per 5M rows (3% of the table, event_id column itself
172 MiB). A typical request (perf samples + exceptions) runs two lookups,
adding a few milliseconds.

Verified on ClickHouse 26.1.12 (tables from `scripts/migrate.sql`, with TTL
and materialized views, as on n192), 24.1.8 and 23.8.16 (tables from
`Migrate`), with a copy of the production `apm` account:

- `0fcc972` stores a re-sent batch twice (the bug); phase A answers the same
  resend with `accepted 4, duplicates 4` and stores nothing;
- partial overlap (4 stored + 2 new): `accepted 6, duplicates 4`, only the
  new rows stored; a repeated id inside one batch is stored once;
- old-SDK events are stored on every upload, never looked up;
- same results with `async_insert: true`;
- `0fcc972` and phase A side by side: a batch stored by the old binary and
  re-sent to phase A is dropped; the reverse direction duplicates (expected);
- the `apm` account: the server starts, warns once per table, de-duplicates
  without the index; after the admin adds the index, no warning on restart;
- a lookup failure (account without SELECT on event_id): 200, events stored,
  `duplicate lookup failed` logged;
- rollback to `0fcc972` with the index present, and after dropping it.

### Deploy

1. Deploy the phase A binary (CI/CD app 36). A rolling deploy is safe: the
   binary needs no schema change, and old and new binaries can serve side by
   side (the old one simply does not drop duplicates).
2. As an admin on n192, add the index to the existing tables and materialize
   it. The `apm` account cannot do this; use the local `default` account
   (passwordless, localhost only, no sudo needed):
   ```sh
   ssh n192
   for t in apm_perf_samples apm_janks apm_startups apm_scene_loads apm_exceptions apm_crashes; do
     clickhouse-client -d ozx_apm -q "ALTER TABLE $t ADD INDEX IF NOT EXISTS idx_event_id event_id TYPE bloom_filter(0.001) GRANULARITY 1"
     clickhouse-client -d ozx_apm -q "ALTER TABLE $t MATERIALIZE INDEX idx_event_id"
   done
   clickhouse-client -q "SELECT table, name, type_full FROM system.data_skipping_indices WHERE database = 'ozx_apm'"
   clickhouse-client -q "SELECT table, mutation_id, is_done, latest_fail_reason FROM system.mutations WHERE database = 'ozx_apm' AND NOT is_done"
   ```
   Privileges: `ALTER ADD INDEX` and `ALTER MATERIALIZE INDEX` on
   `ozx_apm.*`. Alternatively grant `ALTER ADD INDEX` to `apm` in
   `users.d/apm.xml` and let `Migrate` add it on the next start (it still
   does not materialize).
3. Check: `/health` is ok; the server log has `ingested events` lines with a
   `duplicates` field and no `duplicate lookup failed` warnings.

### Rollback

Redeploy the previous binary. No schema action is needed: the index is
harmless to older binaries (verified with `0fcc972`). To remove it anyway (as
admin): `ALTER TABLE <table> DROP INDEX IF EXISTS idx_event_id` on the six
tables.

Once phase B is deployed, roll back the tables first (phase B rollback below),
then the binary.

## APM-19, phase B: ReplacingMergeTree as the safety net (table rebuild)

Deploy only after phase A runs everywhere. Phase B removes, on merge, the
duplicates phase A cannot catch.

Changes:

- **New installs**: `Migrate` creates the six event tables as
  `ReplacingMergeTree` with `event_id` appended to the existing sort key, e.g.
  `ORDER BY (app_version, platform, timestamp, event_id)`. The prefix is
  unchanged, so every query filters and prunes as before. A re-sent event has
  the same timestamp, app_version and key column as the stored copy, so the
  full sort key matches and merges keep one row.
- **Server ids for old-SDK events**: an event without `event_id` gets
  `srv-<UUIDv4>` at ingest (after the duplicate lookup, so it is never looked
  up). Without it, two old-SDK events with the same app_version, key column
  and millisecond (e.g. two devices on the same version) would collapse into
  one row. A server-side insert retry reuses the same ids. The 503-or-200
  decision for a failed insert still uses what the client sent, so old SDKs
  keep their old behaviour. Nothing else reads `event_id = ''` (admin and
  query APIs do not use event_id); old-SDK rows are now
  `startsWith(event_id, 'srv-')` (or `seq = 0`).
- **Existing tables are not converted by `Migrate`.** A one-off script,
  run by a human, rebuilds them: `scripts/apm19_rmt_migrate.sh`
  (`scripts/apm19_rmt_migrate.sql` shows its statements for one table). Per
  table it creates `<t>_rmt` from the live table's own DDL (TTL, codecs and
  settings kept; only the engine and ORDER BY change), swaps it in with
  `EXCHANGE TABLES`, keeps the old table as `<t>_pre_apm19`, and copies the old
  rows in, partition by partition, through a staging table and
  `ATTACH PARTITION ... FROM`. Old-SDK rows get `srv-apm19-<UUIDv4>`. Details
  and the reasons for each step are in the script header. Notably:
  - inserts that arrive during the copy land in the new live table, so
    nothing is lost; the script then waits until no insert that started before
    the swap is still running (such an insert writes to the old table);
  - the copy uses `ATTACH PARTITION`, not `INSERT INTO <t> SELECT`, because an
    insert would feed every old row into the `apm_*_daily` materialized views
    a second time;
  - the materialized views are **detached during the swap** (about 4 s): on
    26.1 an insert running while its target table is renamed pushed the whole
    source table into a view (doubling `apm_fps_daily` or
    `apm_exception_daily`) in 2 of 4 rehearsal runs and in 3 of 10 rounds of
    a minimal reproduction; with the views detached, 0 of 14 (10 rounds + 4
    rehearsals). Cost: rows inserted while they are detached are missing from
    the views (not from the tables). No code reads these views today.
- **Queries do not use `FINAL`.** Phase A keeps duplicates rare; the few
  that slip through count twice until a merge removes them (usually minutes,
  not guaranteed). `FINAL` on every admin query costs extra reading and
  merging at query time and was not measured, so it is not added. For an
  exact number, query with `FINAL`, or run `OPTIMIZE TABLE <t> FINAL` as
  maintenance (rewrites the table).
- The `apm_*_daily` materialized views are fed at insert time and are not
  de-duplicated by the ReplacingMergeTree; phase A keeps duplicates out of
  them.

### Deploy order (strict)

1. Phase A runs everywhere (and the admin added the index).
2. Deploy the phase B binary (CI/CD app 36). A rolling deploy is safe: phase
   A and phase B binaries can serve side by side on the MergeTree tables.
3. **From now on no binary older than phase B may serve traffic.** Such a
   binary stores old-SDK events with `event_id = ''`; in a ReplacingMergeTree
   two of them with the same app_version, key column and millisecond collapse
   into one (shown in the rehearsal). The migration script warns if it finds
   rows with an empty event_id in the new tables.
4. Run the migration on n192 (below).
5. Verify, keep the `*_pre_apm19` tables for a few days, then `drop-backup`.

### Production migration (run by a human on n192)

Privileges: the local `default` account on n192 (passwordless, localhost
only, full rights). No sudo is needed. The `apm` account cannot do this. The
script needs an Atomic database (default since ClickHouse 20.10; `check`
prints the engine), and disk space for about one more copy of the six tables
until `drop-backup` (`check` prints the sizes and free space).

```sh
# from a checkout of this commit
scp server/scripts/apm19_rmt_migrate.sh n192:~/apm19_rmt_migrate.sh
ssh n192
chmod +x ~/apm19_rmt_migrate.sh
~/apm19_rmt_migrate.sh check                       # read-only
~/apm19_rmt_migrate.sh migrate 2>&1 | tee ~/apm19-migrate.log
~/apm19_rmt_migrate.sh verify
# if the default account has a password: CH="clickhouse-client --password" ~/apm19_rmt_migrate.sh ...
```

`check` must show the six tables as `MergeTree` with the expected sort keys,
`has_event_id_index`, the four materialized views, and the database engine
`Atomic`. `migrate` prints, per table, `OK, copied N = N rows`; `verify` must
show `ids_missing_now: 0`, `migration_ids_now` equal to `legacy_in_pre`, and
`empty_ids_now: 0`. Expected duration: the rehearsal migrated 340k rows in
17-21 s; production holds far less.

Effects while it runs: the views miss ~4 s of rows (above); until a table's
copy has finished, its old rows are not visible to queries or to the phase A
lookup, so an old event re-sent in that window is stored again and removed
later by a merge.

After a few days, and once satisfied:

```sh
~/apm19_rmt_migrate.sh drop-backup   # refuses unless every old row is in the new table (FORCE=1 overrides)
```

If `migrate` stops after the swap (it prints so): run
`~/apm19_rmt_migrate.sh rollback`, investigate, drop the
`*_apm19_rolledback` tables, and run `migrate` again. If it stops before the
swap: drop the `*_rmt` tables it created and run it again. If the database is
not Atomic, `ALLOW_RENAME=1` uses a three-way `RENAME TABLE` instead of
`EXCHANGE`; that is not atomic on an Ordinary database (inserts fail for a
moment: new SDKs get 503 and resend, old SDKs lose that batch) and was not
rehearsed.

### Rollback

Tables first, binary second:

1. With traffic running, on n192:
   ```sh
   ~/apm19_rmt_migrate.sh rollback
   ```
   It swaps every table back with `<t>_pre_apm19` (the old MergeTree with all
   rows from before the migration is live again; the ReplacingMergeTree
   becomes `<t>_apm19_rolledback`), waits for inserts that started before the
   swap, then copies back every row inserted since the migration: rows of
   `<t>_apm19_rolledback FINAL` whose event_id is not a migration id and not
   already in the old table, per partition, through `ATTACH PARTITION` (the
   views already counted them). Duplicates that existed before the migration
   are back as well: this restores the old table.
2. Then the binary may go back to phase A or older.
3. Drop the `*_apm19_rolledback` tables when satisfied. Rows stored by the
   phase B binary keep their `srv-` ids; older binaries ignore them.

Rolling back only the binary to a pre-phase-B version while the tables are
ReplacingMergeTree is **not** safe (old-SDK collapse, above).

### Verified

Rehearsed on ClickHouse 26.1.12 (production schema with TTL and the four
materialized views), 24.1.8 and 23.8.16 (tables from `Migrate`), each with
300k perf / 30k exception / 6k crash rows over three days (10% without
event_id, 1,000 old duplicates), data from `0fcc972` including a re-sent
batch and two identical old-SDK events, and a load generator posting new
events, re-sends and old-SDK events (~16 batches/s) during every migrate and
rollback:

- `migrate`: every acknowledged event present, none stored twice, all
  old-SDK rows present; `verify` clean; admin numbers (the dashboard's SQL)
  for the old days identical before and after; materialized view totals for
  the old days unchanged;
- `OPTIMIZE FINAL` collapses the 997 duplicated ids (including those written
  by `0fcc972`) to 0; the two identical old-SDK rows stay two;
- a `0fcc972` binary writing two identical old-SDK events into the new tables:
  they collapse to one (why step 3 above is strict);
- a migrate killed at the 3rd `ATTACH PARTITION` (fault injection), then
  `rollback`: nothing lost, admin numbers unchanged; `migrate` again
  afterwards succeeds;
- `rollback` under load: every event acknowledged before and during the
  rollback present in the restored tables;
- `drop-backup` refuses when one copied row is missing;
- the phase B binary on the migrated tables, with and without
  `async_insert`: re-sent batches dropped, old-SDK events not collapsed.

### Follow-up: APM-17

APM-17 (TTL and materialized views missing from `Migrate`-created tables) is
not part of this change: `Migrate` still creates tables without TTL, and the
migration keeps whatever TTL the live table has (production: 30 days, from
`scripts/migrate.sql`). A deployment whose tables came from `Migrate` has no
TTL; adding it there means rewriting the tables, so APM-17 could reuse this
rebuild (create `<t>_rmt` with the TTL) instead of a separate
`ALTER ... MODIFY TTL` pass. (Done separately; see the APM-17 section.)

## APM-17: one schema definition; TTL and materialized views from `Migrate`

Before this change the server's startup migration (`Migrate`) created the
event tables **without** `TTL` and without the four `apm_*_daily`
materialized views that `scripts/migrate.sql` has. A deployment whose tables
were created by the server kept its data forever.

Changes:

- `internal/storage/schema.go` is the only definition of the schema.
  `scripts/migrate.sql` is generated from it (`make schema-sql`, i.e.
  `go run ./cmd/schemasql > scripts/migrate.sql`); a unit test fails if the
  file is out of date. The generated DDL is the same as the previous
  hand-written file except for the TTL expression (next point).
- New tables get `TTL toDateTime(timestamp) + INTERVAL 30 DAY`. The previous
  `TTL timestamp + INTERVAL 30 DAY` is rejected by ClickHouse 23.8 and 24.1
  on a `DateTime64` column ("TTL expression result column should have
  DateTime or Date type"; only 26.1 accepts it), so the old `migrate.sql` did
  not run there at all. Both expressions mean the same retention. `Migrate`
  never changes the TTL of an existing table.
- `Migrate` creates the four materialized views if they are missing (no
  `POPULATE`: a view only aggregates rows inserted after it was created;
  back-filling would double rows inserted while it runs). Existing views are
  skipped without any statement. With the production `apm` account
  (`SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN`) the views are created on
  ClickHouse 24.1 and 26.1 (a `CREATE TABLE` grant is enough there); if a
  version refuses, the server logs `could not create a materialized view`
  per view and starts anyway (no code reads the views).
- At startup, each event table without a TTL is logged as a warning
  `event table has no TTL: its data never expires` and the server starts.
  It does not add the TTL itself: `ALTER TABLE ... MODIFY TTL` needs the
  `ALTER TTL` privilege, which `apm` does not have, and it deletes data, which
  must be an admin's decision (below).

Production (n192) already has the TTL and the four views (tables from the
old `migrate.sql`), so this release changes nothing there and logs no new
warnings (integration test `TestMigrate_UpgradesProductionDatabase`, with the
111d9bf `migrate.sql` as fixture).

### For a deployment whose tables came from `Migrate` (no TTL)

Run as a ClickHouse admin (the `apm` account cannot):

```sh
scripts/apm17_fix_schema.sh check   # read-only: tables without TTL, rows it would delete, missing views
scripts/apm17_fix_schema.sh apply   # asks for confirmation (YES=1 to skip)
# CH="clickhouse-client --password" DB=ozx_apm scripts/apm17_fix_schema.sh ...
```

`apply` runs, for every event table **without** a TTL only,
`ALTER TABLE <t> MODIFY TTL toDateTime(timestamp) + INTERVAL 30 DAY`, and
creates the missing views from `scripts/migrate.sql`.

**This immediately deletes every row older than 30 days** from those tables:
with ClickHouse's default `materialize_ttl_after_modify = 1`, `MODIFY TTL`
starts a background mutation that rewrites the table's parts and drops
expired rows (I/O roughly the size of the table). `check` prints how many
rows that is per table. Export them first if they are needed. To only expire
data on future merges instead, run the `ALTER` by hand with
`SETTINGS materialize_ttl_after_modify = 0` (old rows then go away over time,
as parts are merged).

Rehearsed on ClickHouse 26.1.12, 24.1.8 and 23.8.16 (tables as `Migrate`
created them at 111d9bf, 5 rows of which 2 were 40 days old): `check` showed
"deletes 2 rows", `apply` without a terminal and without `YES=1` refused,
`apply` added the TTL to all six tables and created the four views, the two
old rows were gone and the three recent ones kept, and a second `apply` did
nothing.

### Rollback

Redeploy the previous binary; no schema action is needed. Views created by
this release stay and keep being fed by older binaries (harmless). A TTL
added by the admin step can be removed with `ALTER TABLE <t> REMOVE TTL`;
rows it already deleted are gone.

### Verified

Integration tests on ClickHouse 26.1.12, 24.1.8 and 23.8.16
(`tests/integration/schema_migrate_test.go`): a fresh install has the TTL on
all six tables, the four views (one inner table each) and logs nothing, also
on restart; a database created by the 111d9bf `Migrate` gets the views, six
TTL warnings, keeps all rows, and is quiet after the admin step; the
production schema is left as it is (views not doubled, view contents
unchanged, no warnings). With a copy of the `apm` account (26.1 and 24.1;
23.8 has no access management for `default`), `Migrate` succeeds on both
kinds of database and only warns about the TTL.

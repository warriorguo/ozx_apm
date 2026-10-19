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

## APM-5: game context, build type, device details (additive schema change)

Deploy order: **server, then admin UI, then SDK builds.** An older server
accepts events from the new SDK but drops the new fields, so ship game builds
with the new SDK only after this server runs everywhere.

### Wire format (SDK -> server)

Every event may carry, next to the existing fields:

```json
{"type":"perf_sample", ..., "build_type":"release", "gpu":"Adreno (TM) 642L",
 "ram_class":"8GB", "net_type":"WiFi",
 "ctx":{"levelId":"3","roomId":"r-12","masterSeed":"184467440737"}}
```

- `build_type`: `release`, `dev` or `editor`. Anything else, or absent, is
  stored as `''` (unknown). An event without `build_type` whose platform names
  an editor (`WindowsEditor`, `OSXEditor`, ...) is stored as `editor`, because
  the platform itself is folded into `Windows`/`macOS`.
- `ctx`: the game context (`ApmClient.SetContext`), a JSON object. Limits, the
  same in SDK and server: at most 64 keys; keys 1-64 characters from
  `[A-Za-z0-9_.-]`; values up to 256 characters. Server policy: an invalid key,
  an object/array value or a key beyond the 64th (in key order) is dropped;
  a longer value is truncated (on a character boundary); numbers/booleans are
  stored as their JSON text; `null`/`""` are not stored; a `ctx` that is not
  an object is dropped as a whole. **The event is never rejected** for its
  context. Counts are logged on the `ingested events` line (`ctx_dropped`,
  `ctx_truncated`) and reported as one entry in the response's `errors`.
- `gpu` (cut to 128 characters), `ram_class`, `net_type` (cut to 32).

### Schema

`Migrate` adds five columns to the six event tables (also in
`scripts/migrate.sql`):

```sql
ALTER TABLE <t> ADD COLUMN IF NOT EXISTS build_type LowCardinality(String) DEFAULT '';
ALTER TABLE <t> ADD COLUMN IF NOT EXISTS gpu LowCardinality(String) DEFAULT '';
ALTER TABLE <t> ADD COLUMN IF NOT EXISTS ram_class LowCardinality(String) DEFAULT '';
ALTER TABLE <t> ADD COLUMN IF NOT EXISTS net_type LowCardinality(String) DEFAULT '';
ALTER TABLE <t> ADD COLUMN IF NOT EXISTS ctx Map(LowCardinality(String), String);
```

Metadata only, idempotent, within the `apm` account's `ALTER ADD COLUMN`
grant. Existing rows read `''` and an empty map. The sort keys, TTL and the
daily views are unchanged (the views do not split by build type; nothing reads
them). Fresh and migrated tables have identical columns in the same order
(integration test `TestMigrate_UpgradedTablesMatchFreshInstall`).

The async-insert path (`clickhouse.async_insert: true`) renders the map as
`map(?, ?, ...)` with every key and value bound: clickhouse-go 2.17 formats a
bound Go map with unescaped keys.

### Admin API

Every endpoint takes:

- `build_type`: `release` (**default**), `dev`, `editor`, `unknown`, `all`.
  `release` also shows events with an empty build type, i.e. everything sent
  by SDKs before APM-5: they cannot be told apart, and hiding them would make
  all existing data vanish from the default view. `unknown` shows only those.
  Editor events from old SDKs that sent an editor platform are `editor`; old
  SDKs that folded the editor into Windows/macOS themselves (this SDK before
  APM-5) are `unknown`. Pre-APM-5 data expires within 30 days (TTL).
- `ctx=key:value`, repeatable (at most 10): the same key repeated is an OR,
  different keys an AND; `key:` (empty value) matches events without the key.
  Keys and values are bound query arguments only. A bad `build_type` or `ctx`
  is a 400.

New endpoints (same filters; default range 7 days):

- `GET /api/context/keys[?source=perf|janks|startups|scene_loads|exceptions|crashes|all]`
  -> `{"keys":[{"key":"levelId","count":123}]}` (events carrying the key, top 200)
- `GET /api/context/values?key=K[&source=..][&limit=N]`
  -> `{"key":"K","values":[{"value":"3","count":40}]}` (top 50, at most 500)
- `GET /api/context/distribution?key=K&metric=M[&fingerprint=F]`, M one of
  `fps`, `frame_time`, `janks`, `startup`, `scene_load`, `exceptions`,
  `crashes` -> `{"key","metric","unit","total","truncated","rows":[{"value",
  "count","sessions","p50","p95"}]}`; `value` `""` = key not set; p50/p95 of
  fps / ms (0 for exceptions and crashes); exceptions count occurrences
  (`sum(count)`); top 50 values.

`/api/crashes/detail` occurrences now include `build_type` and `context`.

The time range is now compared at millisecond precision (it was bound with
second precision, which dropped the events of the last second before
`end_time`).

Cost: `ctx[...]` filters and groupings read the `ctx` column (keys and
values) of the rows in the time range; there is no index on it. Fine at the
current volume; revisit with a materialized column for a hot key if needed.

### Rolling deploy and rollback

Old and new binaries can serve side by side: the old one names its insert
columns, so its rows get the defaults; its admin API ignores the new filters
(shows all build types). **Rollback:** redeploy the previous binary; no
schema action. The columns stay and keep their data; the next deploy of this
release uses it again. Dropping the columns is not needed and would lose the
context (`ALTER TABLE <t> DROP COLUMN ...`, admin privilege).

### Verified

- Unit tests (`go test ./...`), integration tests on ClickHouse 26.1.12,
  24.1.8 and 23.8.16: context normalization and limits, every event type
  carrying the fields, legacy events, the editor platform, both insert paths
  round-tripping keys/values with quotes and backslashes, the admin filters
  and endpoints end to end (ingest router -> ClickHouse -> admin router),
  defaults without time range, 400s, empty ranges, millisecond range.
- Migration rehearsal (a script outside the repo) on each version: production schema of 111d9bf (its `migrate.sql`
  with TTL and the four views; 23.8/24.1 with the `toDateTime()` TTL),
  servers as a copy of the `apm` account where possible (26.1, 24.1); the
  111d9bf binary ingesting while the new binary starts and migrates
  (20 batches/s old format + 30 batches/s new format, six event types each):
  every acknowledged event stored exactly once, old rows unchanged and
  reading the defaults, new rows with build type/device/context, views not
  doubled (`apm_fps_daily` = perf rows), TTL kept, no warnings or errors,
  admin numbers for default/dev/unknown/all and a context filter as expected;
  then the 111d9bf binary restarted on the migrated tables (ingest and admin
  work) and the new binary again (no warnings).

## APM-14: privacy consent (SDK) and deletion by device (admin API)

Two independent parts: the SDK's consent handling (game builds) and an admin
endpoint that deletes everything stored for one device. No change to the wire
format or to the event tables. Deploy order: **server, then the grant, then
SDK builds** (any order works, but deletion requests can only be honoured once
the first two are done).

### SDK: consent

Categories (`ConsentCategory`): `Technical` (everything the SDK collects today:
exceptions, crashes, perf samples, jank, startup, scene loads) and `Analytics`
(APM-12 operations events; none collected yet, the state is stored and
reported so the game can ask now). States (`ConsentState`): `Undecided`
(`Unknown` is the old name), `Granted`, `Denied`.

```csharp
var config = new ApmConfig { /* ... */ };
// Defaults until the player chooses (shown: the built-in ones, for Google Play):
config.DefaultTechnicalConsent = ConsentState.Granted;
config.DefaultAnalyticsConsent = ConsentState.Undecided;
// Opt-in markets (e.g. mainland China): collect nothing until the game asks.
// config.UseOptInConsent();          // both Undecided
// config.RememberConsent = false;    // the game stores the choice itself
ApmClient.Initialize(config);

ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
ApmClient.SetConsent(ConsentCategory.Analytics, ConsentState.Denied);
ApmClient.GetConsent(ConsentCategory.Technical);
string id = ApmClient.GetDeviceId();     // what support needs to delete server data
string newId = ApmClient.ResetDeviceId();
```

- Where a category's state comes from, in order: the game's `SetConsent`
  (also before `Initialize`), the choice stored in PlayerPrefs by an earlier
  `SetConsent` (keys `ozx_apm_consent_technical` / `ozx_apm_consent_analytics`,
  0/1/2; `ApmConfig.RememberConsent`, on by default), the config default.
  Defaults are never stored, only choices. `Initialize` only reads PlayerPrefs.
  A game that calls `PlayerPrefs.DeleteAll()` also deletes the stored choice.
- Technical not `Granted`: collectors are stopped (`ApmClient.Update` then only
  advances the startup tracker: no allocation, measured by an EditMode test),
  nothing is enqueued, written to disk (not even the sequence file) or uploaded,
  and the upload under way is cancelled (a request that was built is never
  started, one in progress is aborted; its batch goes back to storage).
  `RecordBreadcrumb` is ignored.
- `Undecided` keeps what is already stored and uploads it once consent is
  granted. `Denied` deletes, within milliseconds on the storage thread: queued
  and stored events, quarantined batches, the `seq` file and the
  `ozx_apm_offline` directory itself, the network debug log, and the device and
  session ids in PlayerPrefs (kept in memory for the process; a launch that
  stays denied gets new ones). It also deletes at `Initialize` if files were
  left behind (killed right after the choice, or an older SDK). `Granted`
  again resumes with the in-memory ids and stores them again.
- In-flight data: events queued, being written or being uploaded when consent
  is withdrawn are never sent. A request whose bytes had already reached the
  server before the abort may still be stored there; delete it with the admin
  API.
- `ResetDeviceId()`: a new random device id and a new session (session ids are
  random, never derived from the device id), local events deleted (they carry
  the old id), the upload under way cancelled, event `seq` restarted at 1.
  Stored only with technical consent. Works before `Initialize`. Server data
  keeps the old id: read `GetDeviceId()` first and delete it with the admin API.
- `ApmStats` has `Consent` (technical), `AnalyticsConsent` and `Collecting`.
- Compatibility: `ApmClient.SetConsent(state)`, `ApmClient.Consent` and
  `ApmConfig.Consent` keep working and cover both categories
  (`config.Consent = X` sets both defaults). **SDK rollback warning:** a build
  with an SDK older than this ignores the stored choice and collects with its
  config (Granted unless the game sets `config.Consent`), so a player who
  denied would be collected again by that build. Do not ship an older SDK to
  players once a consent dialog is live.
- Analytics (APM-12): operations events must check
  `GetConsent(ConsentCategory.Analytics) == Granted` before enqueue, be stored
  in their own segments so `Denied` can delete only them, and the upload gate
  (today: technical consent) must become per category.

### Server: `POST /api/privacy/delete` and `POST /api/privacy/verify`

```sh
TOKEN=...                             # the value of OZX_ADMIN_PRIVACY_TOKEN (from the secret store)
ID=0123456789abcdef0123456789abcdef   # the device id (ApmClient.GetDeviceId)
curl -sS -X POST http://<admin-host>/api/privacy/delete \
  -H "Authorization: Bearer $TOKEN" -H "X-Apm-Confirm-Delete: $ID" \
  -H 'Content-Type: application/json' \
  -d "{\"id_type\":\"device_id\",\"id\":\"$ID\"}"
curl -sS -X POST http://<admin-host>/api/privacy/verify \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"id_type\":\"device_id\",\"id\":\"$ID\"}"
```

- **Token (required).** Both endpoints need `Authorization: Bearer <token>`,
  compared in constant time with the server config `admin_server.privacy_token`,
  environment variable **`OZX_ADMIN_PRIVACY_TOKEN`**. Without a token configured
  they are disabled: every call answers 403 "privacy API disabled: no token
  configured", and the server logs `privacy API disabled: no token configured`
  (info) once at startup. A missing or wrong token answers 401 and logs a
  warning `privacy API: unauthorized request` with path, remote address and
  reason; the token itself is never logged or returned.
  - Set it on the admin deployment (CI/CD app 38, and app 36 only if it runs
    the admin server) as an environment variable stored as a secret, e.g.
    generated with `openssl rand -hex 32`; then redeploy (it is read at
    startup). Keep it out of `config.yaml`, the repo and tickets. To rotate:
    set a new value and redeploy; to disable the API: remove it and redeploy.
  - Check after deploy: the startup log has no "privacy API disabled" line;
    `verify` without the header answers 401, with it 200.
- `X-Apm-Confirm-Delete` must repeat the id (400 otherwise), in addition to
  the token. It guards against typos and, because it is not in the admin CORS
  allow-list, against requests from other web origins.
- **Open risk (pre-existing):** the rest of the admin API (dashboard, crashes,
  exceptions, context endpoints) still has **no authentication** and relies on
  being reachable only from the internal network; it exposes stack traces,
  device ids and game context to anyone who can reach it. Only the two privacy
  endpoints are protected by this change.
- Tables: every MergeTree-family table of the database that has the column
  (`device_id`): the six event tables, tables added later with the column
  (APM-12 `ops_events`) and leftover copies such as `*_pre_apm19`.
  `id_type: player_id` is rejected until APM-12 stores a player id; it then only
  needs an entry in `models.PrivacyIDTypes`.
- The `apm_*_daily` aggregates are **not** changed: they are sums per date,
  version, platform and scene/fingerprint, with no device or player column, and
  ClickHouse never propagates a delete to a materialized view's target. They
  keep counting the deleted device's past events anonymously.
- Mechanism: per table with rows of the device, `ALTER TABLE <t> DELETE WHERE
  device_id = ?` with `mutations_sync = 2` (waits for the mutation); tables
  without rows are skipped. Lightweight `DELETE FROM` is not used: on 24.1 and
  26.1 it also needs `ALTER UPDATE(_row_exists)`, which 26.1 can only grant per
  table and 24.1, granted on `ozx_apm.*`, turns into `ALTER UPDATE` on every
  column (verified). Cost: each part that holds a row of the device is
  rewritten; parts without one are not. Fine at the current volume; at much
  larger volume, schedule deletions off-peak.
- ReplacingMergeTree (APM-19): the mutation removes every copy present when it
  starts, duplicates included; merges never bring a row back (tested with
  `OPTIMIZE ... FINAL` and `FINAL` reads). A row inserted after the mutation
  started (an upload still in flight, an `async_insert` buffer) survives:
  `verify` shows it and running the deletion again removes it. Ask the player
  to deny consent or reset the id first, so nothing new arrives.
- Responses: `200` `status: done` (nothing left); `202` `status: running`
  (still going after 20 s; it continues in the background, one deletion at a
  time, 30 min deadline) or `incomplete` (rows were left: run it again); `503`
  `failed` when ClickHouse refuses (the error names the missing grant; logged
  as an error with `request_id` and `id_hash`); `500` other errors; `400` bad
  request.
  Bodies carry per-table `rows_before`/`rows_after` and the id's SHA-256
  (`id_hash`), never the id. Idempotent. The admin nginx proxy times out at
  30 s, above the 20 s wait.
- Audit: table `apm_privacy_deletions` (created by `Migrate` with the
  `CREATE TABLE` grant, also in `scripts/migrate.sql`): `request_id, at,
  id_type, id_hash, status, tables, rows_before, rows_after, error,
  requested_by`. A `running` row is written before anything is deleted (no
  record, no deletion: 500) and a final row at the end. No TTL (one small row
  per request, evidence that a request was honoured). A server restart during
  a deletion leaves only the `running` row: run it again. `verify` lists the
  records of the id (by hash).

### Privileges (admin, on n192)

The `apm` service account needs `ALTER DELETE` on `ozx_apm.*`. It is defined in
`/etc/clickhouse-server/users.d/apm.xml` (APM-16), so a SQL `GRANT` is refused
there; edit the grants line and ClickHouse reloads it by itself:

```xml
<grants>
    <query>GRANT SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN, ALTER DELETE ON ozx_apm.*</query>
</grants>
```

```sh
clickhouse-client -q "SHOW GRANTS FOR apm"   # must list ALTER DELETE ON ozx_apm.*
```

For an account created with SQL: `GRANT ALTER DELETE ON ozx_apm.* TO apm`.
Check afterwards: deleting a device that has rows answers 200, not 503.
Nothing else needs the grant; the server starts and runs without it (deletion
requests then answer 503).

### Rolling deploy and rollback

Old and new binaries serve side by side: the old one ignores the audit table
and has no privacy endpoints. Deploying without `OZX_ADMIN_PRIVACY_TOKEN` is
safe: the endpoints stay disabled (403) until it is set. **Rollback:** redeploy the previous binary; keep
`apm_privacy_deletions` (it is the record of honoured requests). The grant can
stay, or be removed from `apm.xml`. Deleted rows cannot be restored (that is
the point; no backup is taken).

### Verified

- Server: `go vet`, `go test ./...`, handler unit tests (`-race`): 200/202/400/
  500/503 paths, token (disabled 403 without one, 401 for missing, malformed,
  wrong or lower-case "bearer" values, never logged; 200 with it; the token does
  not replace the confirmation header), the env variable, confirmation header, validation, audit before deletion, one
  deletion at a time, background completion. Integration on ClickHouse
  26.1.12, 24.1.8 and 23.8.16 (34/34, 34/34 and 32/32 + 2 skipped: 23.8's local
  default account cannot create users): two devices in all six tables with a
  re-sent duplicate in another part, plus a `*_pre_apm19` copy; deletion leaves
  0 rows of the device (plain and `FINAL`, also after `OPTIMIZE FINAL`), the
  other device's rows and the four aggregates unchanged; a late insert is found
  by `verify` and removed by a second run; a third run is a no-op; the audit
  table holds no raw id. As a copy of the `apm` account (26.1, 24.1): 503 naming
  the grant, no row touched, audited as failed; after `GRANT ALTER DELETE` the
  same scenario passes.
- SDK: C# 8 compile with and without `UNITY_2021_2_OR_NEWER`; EditMode tests
  (Unity 2022.3.62) for every transition, persistence across a relaunch,
  `RememberConsent` off, deletion of files left at launch, `ResetDeviceId`
  (before and after `Initialize`), the exception collector off while denied,
  the upload/Clear races (one deterministic interleaving, two threaded stress
  tests) and no allocation per frame or per event while denied. Mutation
  checks: removing the Clear generation check or the reporter's consent
  cancel makes a test fail.
- Not verified: on a device (test plan written, not run yet), the XML-defined `apm`
  account with the new grant (the tests use a SQL-defined copy), deletion time
  at production volume, iOS.

## APM-4: exception grouping by a server-side fingerprint (additive schema change)

Deploy order: **server, then admin UI, then SDK builds.** Each step works
without the next one (below).

Before this change the fingerprint was computed by the SDK from the text
before the message's first colon and the first stack line. OZX's event bus
logs every handler exception as
`Debug.LogError("[GameEventBus] Error handling event {EventType}: {ex}")`, so
all handler failures of one event type were one group, whatever exception
was thrown where; the exception's stack was in the message, which the SDK
cut to 500 characters.

### Wire format (SDK -> server)

Exception events may carry, next to the existing fields:

```json
{"type":"exception", ..., "fingerprint":"fad75585e1523085",
 "log_type":"error",
 "embedded_exception":"System.InvalidOperationException: Room template not found for roomId=7\n  at Ozx.Rooms.RoomLoader.LoadTemplate (System.Int32 roomId) [0x00000] in /Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs:71 \n  ...",
 "breadcrumbs":["[10:00:00.000] Scene: Main", "[10:00:01.512] Exception: ..."]}
```

- `log_type`: `exception` (Debug.LogException / unhandled), `error` or
  `assert`. Absent from older SDKs.
- `embedded_exception`: an `Exception.ToString()` the SDK found in an error or
  assert message (a type name ending in `Exception` followed by stack frames
  or an inner exception), extracted from the **full** message before it is
  cut to 500 characters. Up to 8000 characters: when longer, each exception
  keeps its header and its first 32/16/8/4/2/1 frames until it fits, the rest
  replaced by `  ... (N frames omitted)`. The `stack` limit is now 8000 too
  (was 4000).
- `breadcrumbs`: on the first occurrence in a de-duplication window only, the
  last 20 breadcrumbs (`[HH:mm:ss.fff] text`, UTC, cut to 200 characters).
  Repeat counts (count > 1) carry the first occurrence's message, stack and
  embedded exception, no breadcrumbs.
- `fingerprint`: still sent and still required; the SDK computes it with the
  server's algorithm (below) to de-duplicate on the device.

Server limits: `embedded_exception` 16000 characters, at most the last 50
breadcrumbs of 512 characters; an unknown `log_type` is stored as `''`.

### Fingerprint, version 1

Computed by the server at ingest from the stored raw fields
(`internal/fingerprint`; its package comment is the full specification):

1. Classification: a logged exception (`log_type` `exception`: Unity's
   message is the innermost `Type: message`, the stack has `Rethrow as
   Outer: message` before each outer exception's frames), an exception
   wrapped in an error log (`embedded_exception`; its call site is the first
   in-app frame of Unity's stack of the log call), or a plain error/assert.
   Events of older SDKs: a message starting with `SomethingException:` is a
   logged exception; otherwise the (500-character) message is searched for
   an embedded exception, whose cut-off last line is ignored.
2. Frames in every format (Mono and IL2CPP `Exception.ToString()` with and
   without file/line, .NET, Unity `Type:Method (args) (at File:Line)`) become
   a method identity: no arguments, location, generic arguments, arity or
   compiler-generated numbers (`<Start>b__5_0` -> `<Start>b`), `:` -> `.`,
   `/` -> `+`. Frames whose method starts with `System.`, `Mono.`,
   `Microsoft.`, `UnityEngine.`, `UnityEditor.`, `Unity.`, `TMPro.`,
   `Newtonsoft.` or `OzxApm.` are not in-app.
3. Input of the hash:
   - exceptions: `v1`, `exception`, the outermost exception's type without
     namespace, the inner exceptions' types (`A>B`), and the first two
     distinct in-app frames of the outermost exception (any frames if none is
     in-app); without frames, its message template;
   - plain errors and asserts: `v1`, the log type, the template of the
     message's first line, and the first in-app frame of the log call.
4. FNV-1a 64 of that text, 16 hex digits.

The message template replaces quoted text, GUIDs, hex numbers and hashes,
numbers (also inside identifiers: `room_12` -> `room_<n>`) and values after
`=` (`roomId=forest` -> `roomId=<v>`), and is cut to 200 characters.

Line numbers, file paths, the LogError call site and the message (when there
is a stack) do not take part, so the same bug groups together across Mono and
IL2CPP, across versions, and with any roomId; two bus handlers throwing are
two groups. Only two frames are used because IL2CPP release builds inline
small methods (frames Mono shows are missing); a bug whose top frames are
inlined differently on IL2CPP than on Mono can still form two groups. In
Unity's form of a logged exception the frames of the logging call follow the
exception's own; they count only when the exception has fewer than two
in-app frames.

`testdata/vectors.json` pins the output (22 cases); the SDK's port
(`sdk/Runtime/Collectors/ExceptionParser.cs`) is checked against it, and
`internal/processor/testdata/sdk_events.jsonl` holds events serialized by
the SDK whose fingerprint the server must reproduce.

### Schema

`Migrate` adds ten columns to `apm_exceptions` (also in `scripts/migrate.sql`):

```sql
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
```

Metadata only, idempotent, within the `apm` account's `ALTER ADD COLUMN`
grant; fresh and migrated tables have the same columns in the same order.
`fingerprint` (in the sort key) now holds the server's fingerprint and
`fingerprint_version` the algorithm version; **0 marks rows stored before
APM-4**, whose `fingerprint` is the SDK's and whose new columns are empty.
`client_fingerprint` is what the SDK sent. `inner_exceptions` is
`Type: first line of message` per inner exception, outermost first;
`throw_site`/`call_site` are `Method (at File:Line)`.

### Admin API

- `/api/exceptions` groups by the server's fingerprint as before and adds
  `fingerprint_version`, `exception_type`, `message_template`, `throw_site`,
  `call_site` (of the group's latest occurrence; `message` is now the latest
  too). Old groups (version 0) have only `message` and `fingerprint`.
- New `GET /api/exceptions/detail?fingerprint=F` (same filters, default 30
  days, 404 if nothing matches): the group summary with first/last version,
  `sample` (the latest occurrence: raw message, stack, embedded exception,
  client fingerprint, inner exceptions, and `parsed`: the exception chain
  with method/file/line/in_app per frame, the log call's frames, throw and
  call site, as parsed now), the latest 20 `occurrences` (session, count,
  version, build type, device, context, breadcrumbs), and version/device
  distributions (occurrences). Distribution by any context key:
  `/api/context/distribution?metric=exceptions&fingerprint=F&key=K`. This is
  the data APM-11's detail page needs; the page is APM-11.

The admin UI's exception list shows the type, throw site and call site.

### Rolling deploy and rollback

Old and new binaries can serve side by side: the old binary names its insert
columns, so its rows get the defaults (version 0, its stored fingerprint is
the SDK's). For events of the new SDK the SDK's fingerprint equals the
server's, so they group the same whichever binary stored them, and a batch
re-sent across binaries is still recognised as a duplicate (rehearsed in both
directions). Events of older SDKs group differently from the deploy on: rows
stored before keep the SDK's fingerprint (a version-0 group), rows stored
after get the server's (a new group); the old groups age out with the
30-day TTL.

**Rollback:** redeploy the previous binary; no schema action. The columns
stay; its rows read the defaults. An SDK with APM-4 against an older server:
accepted, the new fields are ignored (no embedded exception, no breadcrumbs
stored), and the stored fingerprint is the SDK's, i.e. v1 grouping.

### Changing the algorithm later (without an SDK release)

Everything the fingerprint is computed from is stored (`log_type`,
`message`, `stack`, `embedded_exception`). To change it:

1. Change `internal/fingerprint`, bump `Version`, regenerate the vectors
   (`go test ./internal/fingerprint -run TestVectors -update`) and review
   the diff: every changed fingerprint is a regrouping.
2. Deploy. New events get the new fingerprint and `fingerprint_version`.
   Stored rows keep theirs (the column says which version made them) and
   expire with the TTL, so for up to 30 days a bug can show as two groups,
   one per version.
3. Port the change to `ExceptionParser.cs` in the next SDK release. Until
   then the device de-duplicates with the old algorithm, which is harmless
   as long as it does not group *more* coarsely than the server (a repeat
   count would be attributed to the first occurrence's group).

An event stored before the bump and re-sent after it (APM-19: the app was
killed between the server's answer and the local delete) gets the new
fingerprint, so neither the duplicate lookup nor the ReplacingMergeTree
(fingerprint is in the sort key) finds the stored copy: it is stored twice.
This needs the rare re-send to straddle the deploy.

Regrouping stored rows at once is not provided: `fingerprint` is in the sort
key and cannot be updated in place. It would mean re-inserting the rows with
the new fingerprint (keeping their `event_id`) and deleting the old ones as
an admin (`ALTER TABLE ... DELETE`, a mutation), which also double-counts in
the `apm_exception_daily` view.

### SDK

- The repeat path (the same text again within the 60-second window, where
  numbers in the message's first line may differ: an error logged every
  frame with a changing roomId is a repeat) is a hash lookup of the message
  and stack: no parsing and **no allocation** (before: Regex + MD5 + string
  formatting, about 2.4 KB per call). Measured on Unity's Mono 2022.3
  outside Unity (M-series Mac): 0.7 us per repeat of a 700-character error
  (0.8 us with a changing roomId), 8 us for an 8 KB one, 0 bytes; the code
  before APM-4: 2.3 us and 2400 bytes per repeat. The only texts this
  merges although their fingerprints differ are exception types whose names
  differ only in digits, in the same place.
- Parsing happens once per distinct text and window: 13-14 us and 18 KB for
  a typical bus error (3 frames), about 70 us and 138 KB for an 8 KB message
  with a 60-frame inner chain (compacted to 4.4 KB), event and breadcrumbs
  included. Not measured on a device.
- Breadcrumbs are recorded without formatting (ring of 50) and formatted
  only when an exception is first seen; an exception records one breadcrumb
  per group and window (it was one per log call).
- `ExceptionParser` is plain C# (no Regex, no UnityEngine, no reflection),
  IL2CPP and stripping safe; the log callback still swallows everything.

### Verified

- `go test ./...`: unit tests incl. a stack corpus (real output of Unity
  2022.3's Mono and `UnityEngine.StackTraceUtility`, IL2CPP with and without
  line numbers, .NET, Unity LogError/LogException formats, the GameEventBus
  wrapper, inner chains of 2 and 3, AggregateException, async rethrow,
  ids/GUIDs/numbers), the golden vectors, a fuzz test (33M inputs), hostile
  input, and ingest of SDK-serialized events. Mono and IL2CPP stacks of the
  same code, and changed line numbers, give the same fingerprint.
- Integration tests on ClickHouse 26.1.12, 24.1.8 and 23.8.16: both insert
  paths round-trip every new column; ingest -> ClickHouse -> admin API for
  two handlers (two groups with type and throw site), five roomIds (one
  group, distribution by roomId), an inner chain, an old-SDK event; rows
  written with the previous binary's INSERT open as version-0 groups.
- Migration rehearsal on each version (a program outside the repo): the
  4f0bf21 schema (its `migrate.sql`: ReplacingMergeTree, TTL, four views,
  APM-5 columns), servers as a copy of the `apm` account (26.1, 24.1; 23.8
  has no access management for `default`); the 4f0bf21 binary ingesting 25
  batches/s (new-SDK and old-SDK exceptions, perf samples) while the new
  binary starts and migrates (about 0.2 s), then both serving: 225 batches,
  1362 events with ids and 225 old-SDK events each stored exactly once;
  `apm_exception_daily` equal to the table's `sum(count)`; TTL and engine
  kept; old-binary rows at version 0 with empty new columns, new-binary rows
  at version 1 with type, sites, breadcrumbs and embedded exception; the
  server fingerprint equal to the SDK's for every new-SDK event; the bus
  errors of two handlers and many roomIds in 2 groups; re-sends old -> new
  and new -> old dropped as duplicates; admin list and detail of both
  binaries on the migrated table; then the old binary alone (rollback: 20
  batches, all stored once) and the new binary again (no migration, no
  warnings); no warning or error in any server log.
- SDK: compiles at C# 8 with and without `UNITY_2021_2_OR_NEWER`; the
  parser matches all 22 golden vectors on .NET 6; on Unity's Mono (outside
  Unity) 16 of the 23 new EditMode tests run and pass, the 7 that need the
  Unity runtime (Unity's stack capture, the allocation constraint) were
  compiled but not run here.

## APM-3: complete exception capture, per-context counts, session error sequence, idle GC (additive schema change)

Deploy order: **server, then SDK builds.** The admin UI needs no change (the
new fields are additive; APM-11 builds the UI on them). An SDK with APM-3
against an older server works: the new fields are ignored, and the
per-context repeat counts are still separate rows, so `sum(count)` and the
distribution by a context key are right there too.

### SDK: what is captured

- **Every thread.** The SDK subscribes to `Application.logMessageReceivedThreaded`
  only (one callback, called on the logging thread, the main thread
  included; subscribing to `logMessageReceived` as well would report
  main-thread logs twice). Unity's own log output is unchanged.
- **Unhandled exceptions.** `AppDomain.CurrentDomain.UnhandledException`
  (`unhandled: "domain"`, or `"domain_terminating"` when `IsTerminating`) and
  `TaskScheduler.UnobservedTaskException` (`"task"`; a single inner exception
  is reported as itself, so it groups with the same exception logged by the
  game; the task is not marked observed, the default behaviour stays). The
  exception's `ToString()` is sent as the embedded exception of an `error`
  whose message is its first line, so it is parsed and grouped like any
  other.
  - Unity's `UnhandledExceptionHandler` may also `Debug.LogException` it, on
    the same thread, before or after the SDK's handler. Both are one
    occurrence: a logged exception followed (within 2 s, same thread, same
    fingerprint) by the unhandled report is marked unhandled (a first
    occurrence logged as an exception off the main thread waits 100 ms
    before it is enqueued for that; a repeat moves to an unhandled count);
    an unhandled report followed by the log drops the log.
  - Terminating: the handler records the exception, closes the
    de-duplication window and waits up to 500 ms for the storage worker to
    write everything to disk (`OfflineStorage.Flush` + `WaitIdle`, as on
    pause) before returning, so the next launch uploads it.
  - Whether an unhandled exception in a managed thread ends the process
    depends on the runtime and the player; both cases are handled. On
    Android IL2CPP (Unity 2022.3, A33, device test U1) the event arrives with
    `IsTerminating = true` but the process keeps running: such rows read
    `domain_terminating` although the app did not end. Unity's log of the
    same exception was recognised as its echo there (one row, count 1).
  - `"task"` is best effort: .NET raises `UnobservedTaskException` only when
    the faulted task is finalized. On IL2CPP's conservative GC a task can stay
    uncollected for a long time; in device test U2, 3 of 5 faulted tasks were
    reported (correct type and throw site), 2 were never finalized in the
    test's 50 s.
    Exceptions on the main thread are caught by Unity and arrive as logged
    exceptions (as before).
- The SDK never captures its own output (`[OzxApm]` prefix), never throws
  from a callback, and never logs or enqueues while holding its lock.

### Wire format (SDK -> server)

Exception events may carry, next to the APM-4 fields:

```json
{"type":"exception", ..., "count":40, "error_seq":3, "session_ms":846,
 "unhandled":"task", "ctx_merged":true, "ctx":{"levelId":"3"}}
```

- `error_seq`: which error of its session the event's (first) occurrence
  was, counting every occurrence of every error, repeats included: 1 is the
  session's first error. Restarts with every new session. `session_ms`: the
  milliseconds from the session start to that occurrence. A first-occurrence
  event carries its own; a repeat count carries those of its first repeat.
- `unhandled`: absent for logged errors; `domain`, `domain_terminating`,
  `task`.
- `ctx_merged`: the count merges occurrences in different game contexts;
  `ctx` holds only the entries they all had.
- `ctx` of every exception event is now the context **when it happened**
  (before: the first occurrence's context at enqueue, which was the same
  moment, and the repeat count's at its first repeat).

### SDK: de-duplication and limits

Per window of 60 s (also closed by `Flush`, going to background, shutdown,
and when a new session starts, so no count spans two sessions and every
count carries the id of the session it happened in):

| What | Limit | Beyond it |
|---|---|---|
| First occurrences sent at once (with breadcrumbs) | 30 fingerprints | reported at the end of the window, still with their own time, `error_seq`, context and count, without breadcrumbs |
| Repeat counts per fingerprint | one event per (context, unhandled kind), at most 8 | one merged count (`ctx_merged`) per fingerprint, and one for its unhandled occurrences |
| Repeat counts in all | 64 | merged counts |
| Fingerprints tracked | 100 | counted in one summary event per window, `log_type` `error`, message `[OzxApm] N errors of M kinds not reported one by one: ...` (its own group; its context is what all of them shared, `ctx_merged` if they differed); tracked normally in the next window |
| Distinct texts remembered for the cheap repeat path | 512 | parsed every time (correct, slower) |

So no occurrence is lost from `sum(count)`, and the first occurrence of a
fingerprint is always sent unless more than 100 different errors occur within
a minute. At most 30 + 64 + 100 + 1 = 195 exception events per window (about
30 first occurrences of 3-25 KB, the rest 1-17 KB each, mostly the error's
text: the server recomputes the fingerprint from it). In practice an error
logged every frame in one room is one event per minute.

First occurrences are persisted within milliseconds; the counts of the
current window live in memory until it closes (60 s, `Flush`, going to
background, shutdown, an unhandled exception that ends the process). If the
process is killed in the foreground (OOM, native crash, force-stop), the
counts of that last window are lost, as before APM-3.

`ApmStats` has `ErrorsSeen`, `ErrorEvents` and `ErrorsInSummary`.

The repeat key counts every run of digits in the first line as one `#`. The
server's message template treats some digit-bearing tokens differently
(hex numbers, GUIDs), so two texts differing only there share a device-side
count under the first text's fingerprint: a misgrouping of the count, never
a loss.

APM-6 plugs its recent-logs ring into the same callback (`ILogSink` on the
collector: called for every log type, on the logging thread, before the
error path).

### SDK: idle GC and serialization

The storage worker serialized every event into a string, counted its UTF-8
bytes and encoded it again: per perf sample (once a second) about 1.8 KB,
6 KB with a 30-key context (measured on the A33 in APM-5: 65 frames with
allocations and 192 KB per idle minute, 68 frames / 444 KB with 30 keys).
Events are now written straight into a reused UTF-8 buffer (`JsonLine`):
the context as UTF-8 bytes built once per context change, numbers and the
32-hex-digit event id without strings (the id is 128 bits from two SplitMix64
streams seeded once from a Guid, instead of a Guid string per event), floats
digit for digit as `float.ToString("F2", InvariantCulture)` on Unity's class
library (checked against it for every float below 0.03125 and every 13th up
to 1e15: 0 differences in 290 million). **The disk and wire format is
unchanged.**

Measured on Unity's Mono outside Unity (M-series Mac), steady state: writing
a perf sample to its segment allocates 4.5 bytes/event on average (the seq
file is rewritten every 1000 events), before 1839 bytes (no context) / 5979
bytes (30 keys). The main thread does not allocate at idle (pooled sample,
cached context; `ExceptionCollector.Update` 0 bytes). What still allocates,
on the storage worker: opening and sealing a segment every `BatchSize` (20)
events, about 6 KB, i.e. 3 times a minute at idle, and the uploads (one per
`LowValueUploadIntervalSeconds` = 120 s at idle).

Measured on the A33 (dev arm64 build, 60 s idle, 30 fps; APM-4 build and
APM-3 build alternated twice under the same conditions):

| Build | Frames with allocations | Bytes per minute | `Update` p50 | `Update` p99 |
|---|---|---|---|---|
| APM-4, no context | 65-67 | 206 KB | 0.035-0.036 ms | 0.20-0.30 ms |
| APM-3, no context | 12-13 | 87 KB | 0.039-0.041 ms | 0.18-0.21 ms |
| APM-4, 30-key context | 65 | 397 KB | 0.036-0.037 ms | 0.14-0.27 ms |
| APM-3, 30-key context | 11-13 | 120 KB | 0.039-0.041 ms | 0.24-0.37 ms |

The remaining allocations are about 12 frames a minute; the largest single
frame (47-80 KB) is an upload. `Update` p99 varies as much between two runs
of the same build as between builds; p50 is about 3 us higher.

### SDK: cost of the error path

Unity's Mono outside Unity (M-series Mac), 645 + 110 character bus error:

| Path | Time | Allocated |
|---|---|---|
| Repeat, same text | 0.92 us | 0 |
| Repeat, roomId changing every call | 0.92 us | 0 |
| Repeat with a 30-key context | 0.93 us | 0 |
| Repeat on another thread | 1.8 us | 0 |
| A warning (not captured) | 0.01 us | 0 |
| `Update` when nothing is due | 0.02 us | 0 |
| First occurrence (parse, event, breadcrumbs) | 10.5 us | 7.8 KB |

On the A33 these are a few microseconds; the APM-4 measurement (T6) put the
SDK's share of a per-frame bus error at 0.01-0.07 ms/frame (p50).

**The 1.4-1.7 KB per frame measured in APM-4 T6** (SDK on minus SDK off,
14229 - 12825 bytes/frame for `busspam`, 18445 - 16735 for `busspamvar`) is
not SDK code. Unity calls `Application.CallLogCallback(string logString,
string stackTrace, ...)` with both strings converted from native memory, and
only when a log callback is registered (`SetLogCallbackDefined`); the
handlers then get those same strings. Their managed size (20 + 2 x length
bytes each) is 1272 bytes for `busspam` (521-character message +
92-character stack) and 1576 for `busspamvar` (673 + 92); the rest, 132 and
134 bytes/frame, is the idle serialization above (192 KB/min = 107-115
bytes/frame at 30 fps), which this change removes. The repeat path itself
allocates 0 bytes. Once any log callback is registered (the game's own, a
crash reporter's or the SDK's), every log line costs this conversion; it
does not grow with the number of handlers. Measured after APM-3 (A33, dev
build, same test as APM-4 T6): SDK on minus off = 14157 - 12825 = 1332
bytes/frame for `busspam` (APM-4: 1404) and 18372 - 16735 = 1637 for
`busspamvar` (APM-4: 1710); bus p50 on minus off 0.08-0.10 ms (Unity's
string conversion and callback dispatch included); 0 SDK warnings. A
background thread logging 2776 errors in 10 s arrived as sum(count) = 2776
in 2 events.

### Schema

`Migrate` adds four columns to `apm_exceptions` (also in `scripts/migrate.sql`):

```sql
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS error_seq UInt32 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS session_ms UInt64 DEFAULT 0;
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS unhandled LowCardinality(String) DEFAULT '';
ALTER TABLE apm_exceptions ADD COLUMN IF NOT EXISTS ctx_merged UInt8 DEFAULT 0;
```

Metadata only, idempotent, within the `apm` account's `ALTER ADD COLUMN`
grant; fresh and migrated tables have the same columns in the same order.
Rows of older SDKs and of the previous binary read 0 / `''`. Ingest clamps
`error_seq` to [0, 2^32-1] and `session_ms` to [0, 366 days], stores an
unknown `unhandled` as `''`, and reads a value of the wrong JSON type as
0 / `''` / false; it never rejects an event for these fields.

### Admin API

- `first_error=1` (or `true`; `0`/`false`/absent: all; anything else is a
  400) keeps only exception rows whose occurrence was the first error of its
  session (`error_seq = 1`), wherever `apm_exceptions` is read: the list, the
  detail, `/api/context/keys|values|distribution` (exceptions), the
  exceptions time series and the dashboard's exception count. Other tables
  are not filtered. Rows of older SDKs never match. With it, a group's
  `count` is the number of sessions it started.
- Exception groups (list and detail) add `first_error_sessions` (sessions in
  which the group was the first error) and `unhandled_count` (its
  occurrences no code caught).
- Occurrences in the detail add `error_seq`, `session_ms`, `unhandled`,
  `ctx_merged`.
- `/api/context/distribution?metric=exceptions` counts occurrences
  (`sum(count)`, as before) and adds `merged`: occurrences in merged counts
  that lack the key, i.e. whose value is not known. They are not reported
  under `""` ("key not set"); `total` includes them.

### Rolling deploy and rollback

Old and new binaries can serve side by side: the old binary names its insert
columns, so its rows get the defaults; its admin API ignores `first_error`.
**Rollback:** redeploy the previous binary; no schema action. The columns
stay; the previous binary does not read them.

### Verified

- `go vet`, `go test ./...`: 369 unit tests (215 top level) pass, among them
  the bounds of the new fields, ingest of APM-3/APM-4/older events, the
  `first_error` parameter, and events serialized by the APM-3 SDK on Unity's
  Mono (`processor/testdata/sdk_events_apm3.jsonl`: unhandled with an inner
  chain, task, a logged exception marked unhandled, per-room and merged
  counts, the summary) whose fingerprints the server reproduces.
- Integration tests on ClickHouse 26.1.12, 24.1.8 and 23.8.16 (38 / 38 / 36
  pass, 0 fail; 23.8 skips 2 that need access management): ingest router ->
  ClickHouse -> admin router on both insert paths for an Awake root cause
  and cascading NREs in three rooms with a merged count, a second session, an
  unhandled task exception and an older SDK's event: group counts,
  `first_error=1` (also with a context filter), detail fields, the roomId
  distribution weighted by count with `merged`, context values and time
  series with `first_error`, a 400; rows of the previous binary's INSERT.
- Migration rehearsal (a program outside the repo) on each version, twice:
  from the schema after APM-4 (c8c05c2's `migrate.sql`) with the c8c05c2
  binary, and from today's production schema (c392f9f) with the c392f9f
  binary (APM-4 and APM-3 deployed together). Servers as a copy of the `apm`
  account (26.1, 24.1; 23.8 has no access management for `default`). The old
  binary ingests 25 batches/s (APM-3 SDK sessions: root cause, first NRE,
  three per-room counts, a merged count, an unhandled task; an APM-4 SDK
  event; an old-SDK event; perf samples) while the new binary starts and
  migrates (0.2-0.9 s), then both serve: every acknowledged event stored
  exactly once (about 2600 per run), `sum(count)` equal to what was sent
  (about 23000), `apm_exception_daily` equal to the table, TTL and engine
  kept; old-binary rows with the APM-3 columns at defaults, new-binary rows
  with them (error_seq on 7 events per batch, one first error, one merged,
  one task); re-sends across binaries dropped as duplicates; the new admin
  API's `first_error=1` shows only the root cause (one per new-binary
  session) and the roomId distribution adds up (rows + merged = total); the
  old admin API works on the migrated table; then the old binary alone
  (rollback) and the new binary again; no warning or error in any log.
- SDK: compiles at C# 8 with and without `UNITY_2021_2_OR_NEWER`. Unity
  EditMode could not run here (the sandbox blocks the licensing client); on
  Unity's Mono outside Unity 139 runtime tests pass (all new logic tests of
  `ExceptionCaptureTests`, `JsonSerializerTests`, the storage and context
  tests), 2 fail for the environment (no `JSONSerializeModule`, no native
  gzip), 82 need the Unity runtime (allocation constraints, Unity's stack
  capture, MonoBehaviour); a 15 s stress run of 6 threads (repeats, 2 million
  occurrences beyond the fingerprint limit, unhandled reports, task
  exceptions, context changes) against window flushes counted all 10 million
  occurrences exactly once with unique sequence numbers. A code review
  (another agent) found five defects (a session starting without a pause
  shared a window with the previous one; the summary's context; echoes of
  untracked exceptions counted twice; the overflow lost the unhandled kind;
  a moved first occurrence); all fixed with tests before the numbers above.
- Not yet run on a device (A33 IL2CPP): background-thread errors, unhandled
  thread and task exceptions, per-frame spam with and without the SDK, three
  rooms in one window, the first-error filter after an Awake failure, idle
  GC with and without 30 context keys, offline/kill.

## APM-6: custom events, custom metrics and timers, recent warnings on the breadcrumb time line (additive schema change)

Deploy order: **server (it creates the two tables), then admin UI, then SDK
builds.** An older server rejects `custom_event` / `custom_metric` events
("unknown type", counted in `rejected`, batch `stored: true`), and the SDK
deletes a stored batch once the server answers: game builds with this SDK
must only ship once this server runs everywhere, or their custom events and
metrics are lost (everything else in the batch is stored).

### SDK: API

```csharp
ApmClient.TrackEvent("room_enter");                              // + overloads: one/two properties,
ApmClient.TrackEvent("room_enter", "roomId", roomId);            //   IDictionary<string,string>
ApmClient.RecordMetric("gold_delta", -15);                       // or (name, value, unit)
ApmTimer t = ApmClient.StartTimer("room_switch_ms");             // a readonly struct
ApmClient.StopTimer(t);                                          // or StopTimer("room_switch_ms")
ApmClient.CancelTimer("room_switch_ms");
```

- All of them: any thread, never throw, return false / -1 when nothing was
  recorded (before Initialize, without technical consent,
  `ApmConfig.EnableCustomEvents = false`, invalid name). Technical data
  (APM-14 `TechnicalAllowed`): nothing is recorded or kept without technical
  consent; Denied also clears the in-memory time line. Business/operations
  events (retention, funnels, purchases) are APM-12, not this API.
- Names and property keys: `[A-Za-z0-9_.-]{1,64}`. Properties: the game
  context's limits (at most 64, values cut to 256 characters, about 4 KB);
  invalid keys and empty values are left out, the event is kept. Refusals and
  cuts log a warning (first 10) and count in `ApmStats.CustomRejected`.
  **Names, properties and units are stored as sent: they must never contain
  personal data** (player names, e-mail, account or payment ids, chat or other
  input text, precise location).
- Timers: `StopTimer` records the elapsed milliseconds (Stopwatch) as a
  sample of the metric, unit `ms`, **with the game context at the time it is
  stopped**: for a room switch, set the new `roomId` first, then stop. A timer
  that spans a pause of the app (`OnApplicationPause(true)`) is not recorded
  (`ApmStats.TimersSpanningPause`); `StopTimer` still returns the time. Named
  timers: one pending per name (a second `StartTimer(name)` replaces it; at
  most 256 pending names; `ApmStats.TimersUntracked` counts starts beyond that).

### SDK: what is sent

- **Custom events**: every call goes onto the breadcrumb time line (below).
  Per minute (its own clock: `Flush` and pauses do not renew it), the first
  `ApmConfig.MaxCustomEventsPerMinute` (60) calls, at most 10 of one name (so
  one per-frame event cannot use up the budget of the others), are each sent
  with their properties at once; further calls are counted per (name, game
  context) and sent as one event per count at the end of the window
  (`aggregated`, no properties). Every call reaches
  `sum(count)`, except calls of names beyond 64 distinct names per window
  (dropped, `ApmStats.CustomEventsDropped`; still on the time line).
- **Custom metrics**: never one row per sample. Per window, the samples of a
  (name, game context) series are aggregated into count, sum, min, max and a
  histogram, sent as one event when the window ends.
  - Histogram: logarithmic buckets of relative width gamma = 1.02/0.98 (the
    DDSketch construction): bucket i holds (gamma^(i-1), gamma^i] and reads back
    as 2 gamma^i / (gamma + 1). Counts are exact and add up across rows and
    devices (ClickHouse `sumMap`), so **p50/p95/p99 have the exact rank
    (nearest rank) and a value within 2% of the true sample at that rank**, for
    magnitudes in [1e-9, 4e15] (0 and negatives work; below 1e-9 reads as 0),
    clamped to the exact min/max. Chosen over a t-digest: exact and simple to
    merge server-side in SQL, a fixed error bound, no allocation per sample.
    Version 1 (`hist_v`); SDK `Utils/MetricHistogram.cs` and server
    `internal/metrics/buckets.go` must change together (a vector of SDK keys is
    in the server's tests).
  - Bucket keys per series and window: arrays start at 64 and grow to 512 (a
    spread of 8e8 x at 2%; a series reused in later windows keeps its size);
    beyond that the two lowest keys merge (only the lowest quantiles lose
    accuracy). A sample that would make the sum infinite is dropped and counted.
  - Per window: 16 contexts per name and 128 series in all; further contexts
    go to the name's overflow series, whose context is what its samples shared
    (`ctx_merged`); names beyond 64 are dropped and counted
    (`ApmStats.MetricSamplesDropped`).
- Window: 60 s, also ended by `Flush`, going to background, shutdown, and a
  new session (counts keep the session they happened in). Custom events and
  metrics are low-value data (held up to `LowValueUploadIntervalSeconds` like
  perf samples). An unhandled exception that ends the process flushes the
  window before the process dies (with APM-3's exception counts); a process
  killed in the foreground (OOM, native crash, force-stop) loses the counts of
  its current window; individual custom events are on disk within milliseconds.

### SDK: the breadcrumb time line (exceptions, crashes, janks)

`Core/Timeline.cs`, shared by the collectors: three rings merged in the order
they were recorded when an event takes a snapshot:

- Breadcrumbs (50): custom events (`Event: room_enter roomId=12`), scene loads
  (`Scene: Main`), exceptions (`Exception: <first line>`, once per group and
  window), `ApmClient.RecordBreadcrumb` texts.
- Recent log lines, 24 warnings and 12 errors/asserts in separate rings (warning
  spam cannot push errors out, nor the other way round): every **Warning, Error
  and Assert** line the game logs
  on any thread (`Warning: Asset not found: anim/arack_red`); not plain `Log`
  lines, not exceptions (those are the breadcrumbs above), never the SDK's own
  `[OzxApm]` lines. Fed by APM-3's single log callback (`ILogSink`): the line is
  stored by reference (Unity hands the string over anyway) with its time, under
  a leaf lock: **no allocation, no formatting per log line** (0 bytes, 0.09 us on
  Unity's Mono on a Mac). A new line longer than 4096 characters is stored cut
  (only it allocates; a repeat of it is only counted). A line of the same shape (equal up to digits, e.g. another
  enemy id) as one of the 8 most recent ones is counted on that one instead of
  stored (`Warning: Pool not found for enemy 12 (x300 until 10:00:05.120)`), so
  a warning logged every frame does not push the cause out. The two rings are
  separate so log spam cannot evict custom events and vice versa.
- Privacy: log lines are the game's own text and now reach the server inside
  breadcrumbs. When a snapshot is formatted, the query string and fragment of
  every http(s) URL are replaced by `?...` (tokens in UnityWebRequest errors);
  other personal data must not be logged at Warning/Error level by the game.
- Exceptions carry the 20 most recent breadcrumbs (200 characters each) and the
  10 most recent log lines (300 characters each), merged, oldest first; the
  error being reported is not among its own lines, and an error or assert
  line on the time line is not added again as an `Exception:` breadcrumb. At
  most ~7.5 KB per first occurrence (was ~4.5 KB).
- Janks: `recent_events` (empty before APM-6: `JankDetector.RecordEvent` had no
  caller) now holds the breadcrumbs (10) and log lines (5) of the 10 s before the
  jank, 200 characters each.
- Crashes: the SDK sends no crash events yet (native crash capture is not part
  of this); an unhandled managed exception that ends the process is an
  exception event (APM-3) and carries the time line.
- Wire format of breadcrumbs unchanged (`Array(String)`, `[HH:mm:ss.fff] text`, UTC).

### SDK: cost

Unity's Mono outside Unity (M-series Mac), steady state, 30-key context
(`/tmp/claude-501/apm6/monoharness/Bench6.cs`):

| Path | Time | Allocated |
|---|---|---|
| `RecordMetric`, known name and context | 0.10 us | 0 |
| ... with a window closed every 1000 samples | 0.09 us | 0.7 B/sample (the window's events) |
| `StartTimer` + `StopTimer` (handle or name), recorded | 0.18 us | 0 |
| `TrackEvent` beyond the budget (counted) | 0.11 us | 0 |
| `TrackEvent` sent, no properties / one property | 0.17 / 0.20 us | 192 / 240 B |
| Log callback, a warning (time line + exception path) | 0.10 us | 0 |
| Log callback, a repeated error (APM-3 repeat path + time line) | 0.77 us | 0 |
| Plain `Log` line | 0.002 us | 0 |
| `CustomCollector.Update` / window close with nothing recorded | 0.02 / 0.07 us | 0 |
| Breadcrumb snapshot of a first occurrence (20 + 10 lines) | 2.5-4.3 us | 4.4 KB |
| Serializing a metric event (storage worker) | 1.6 us | 0 (integral values; a non-integral sum/min/max is written in round-trip form: one short string each) |

Idle (nothing recorded) allocates nothing more than before on the main thread.

### Wire format (SDK -> server)

```json
{"type":"custom_event", <common fields>, "name":"room_enter", "count":1,
 "props":{"seed":"991","mode":"hard"}, "ctx":{"levelId":"3","roomId":"r-12"}}
{"type":"custom_event", ..., "name":"enemy_killed", "count":5, "aggregated":true, "ctx_merged":true, "ctx":{...}}
{"type":"custom_metric", ..., "name":"room_switch_ms", "unit":"ms", "count":10,
 "sum":10160.875, "min":640.25, "max":2210.125, "window_ms":60000, "hist_v":1,
 "hist_keys":[1162,1164,1166], "hist_counts":[1,2,7], "ctx":{...}}
```

`processor/testdata/sdk_events_apm6.jsonl` are events serialized by the SDK on
Unity's Mono; the ingest tests store them.

Server validation: `custom_event`: the name rule (else rejected); props
validated like `ctx` (entries dropped or cut and counted, the event kept); a
missing or non-positive `count` reads as 1. `custom_metric` is decoded
strictly and **rejected unless consistent**: positive `count`, finite `sum`,
`min`, `max` with min <= max, `hist_v` 1, equal-length `hist_keys` /
`hist_counts` (1-2048) with valid keys and positive counts adding up to
`count`; an unprintable or over-long (> 16) unit is dropped.

### Ingest: unsupported types are rejected with a reason

Before: an unknown type was counted in `rejected` with the reason "unknown
type" (the response was already truthful; failed inserts were already never
accepted, APM-2/19). Now:

- `asset_load` and `http` (planned: APM-7 / APM-8) are rejected with
  `event <i>: type "http" is not supported yet`; any other unknown type with
  `event <i>: unknown type "<type>"` (quoted, cut to 64 characters).
- New response field `rejected_types`: rejected events per such type
  (`{"http":2}`); absent when none.
- `errors` holds at most 50 entries, then `... and <n> more` (a batch of 1000
  bad events produced 1000 strings).
- Unchanged: rejected events never make `stored` false (resending cannot help);
  a failed insert is retried server-side, then counted in `failed` (never in
  `accepted`) and answered 503 + `retry` (integration-tested for the new tables
  too).

### Schema

`Migrate` creates (also in `scripts/migrate.sql`; `CREATE TABLE IF NOT
EXISTS`, within the `apm` account's grant, idempotent, no change to existing
tables):

- `apm_custom_events`: common columns, `name`, `count UInt32`, `aggregated
  UInt8`, `ctx_merged UInt8`, `props Map(LowCardinality(String), String)`, the
  APM-2/5 columns; `ReplacingMergeTree`, `ORDER BY (app_version, name,
  timestamp, event_id)`, the event_id bloom filter index, TTL 30 days.
- `apm_custom_metrics`: common columns, `name`, `unit`, `count UInt64`,
  `value_sum`, `value_min`, `value_max Float64`, `window_ms UInt32`,
  `hist_version UInt8`, `hist_keys Array(Int16)`, `hist_counts Array(UInt64)`,
  `ctx_merged UInt8`, the APM-2/5 columns; same engine, key, index and TTL.
- Daily views (the one-year aggregated retention of CLAUDE.md; nothing in the
  server reads them yet): `apm_custom_event_daily` (SummingMergeTree,
  `total_count` per date, version, platform, build type, name) and
  `apm_custom_metric_daily` (AggregatingMergeTree: `sumState(count)`,
  `sumState(value_sum)`, `minState`, `maxState`, `sumMapState(hist_keys,
  hist_counts)`, so daily percentiles can be computed from it), both
  `TTL date + INTERVAL 365 DAY`. Like APM-17's views: created by `Migrate` when
  the account may (it may on 24.1 and 26.1 with CREATE TABLE; otherwise one
  warning per view at start, and an admin runs the two statements from
  `migrate.sql`). They only see rows inserted after they exist; `Migrate`
  creates them right after the tables, before the new binary serves.

Duplicates (APM-19): the ingest-time lookup covers the new tables (key column
`name`); the ReplacingMergeTree drops what slips through. Privacy deletion
(APM-14) finds the new tables by their `device_id` column (integration-tested:
a device's custom rows are deleted, the daily views are not touched).

### Admin API

All take the common filter (time range, `app_version`, `platform`,
`build_type` default release incl. `''`, repeatable `ctx=key:value`); default
range 7 days; numbers are `toInt64` counts and finite floats.

- `GET /api/custom/events` -> `{"events":[{"name","count","sessions","devices","last_seen"}],"truncated"}`:
  calls (`sum(count)`) per name, top 200.
- `GET /api/custom/metrics` -> `{"metrics":[{"name","unit","count","sessions","avg","min","max","p50","p90","p95","p99"}],"truncated"}`:
  samples and percentiles per name over the filter.
- `GET /api/custom/events/distribution?name=N&<group>` and
  `GET /api/custom/metrics/distribution?name=N&<group>` with exactly one of
  `key=<ctx key>`, `prop=<property key>` (events), `by=app_version|platform|device_model|os_version|build_type`
  (default `by=app_version`) -> `{"name","group_by","unit","total","merged","truncated","rows":[{"value","count","sessions",...percentiles for metrics}]}`,
  top 50 values. `merged`: calls/samples whose value is not known (counts the
  SDK merged across contexts and that lack the key; for `prop`, the aggregated
  events without properties); not in `rows`, included in `total`. A bad
  parameter is a 400.
- `/api/context/keys|values` accept `source=custom_events|custom_metrics`; `all`
  now includes the two tables.
- Percentiles are computed in Go from the merged histogram
  (`sumMapIf(hist_keys, hist_counts, hist_version = 1)`): exact rank, value
  within 2%, clamped to the exact min/max.

Admin UI: a **Custom** page (metrics: list with p50/p95/p99, then per context
key, version, device...; events: counts per context key, property or column;
a row adds a context filter).

### Rolling deploy and rollback

Old and new binaries can serve side by side: the old one does not know the
new tables (it rejects custom events with "unknown type" and stores the rest of
the batch), the new one stores everything. **Rollback:** redeploy the previous
binary; no schema action; the tables and views stay (unused; their rows expire
by TTL). Custom events sent while the old binary serves are rejected and lost
for good (the SDK drops rejected events): roll back the SDK builds first if
that matters. To remove the tables after a permanent rollback (admin):
`DROP VIEW apm_custom_event_daily; DROP VIEW apm_custom_metric_daily; DROP TABLE apm_custom_events; DROP TABLE apm_custom_metrics;`

### Verified

- Server: `go vet`, unit tests (383 passing test functions, among them the
  bucket math against a vector of keys and values computed by the SDK on
  Unity's Mono, p50/p95 of merged histograms within 2%, the SDK-serialized
  fixture stored as sent, validation and rejections, the 50-error cap, failed
  inserts of the new tables answered 503 and resent without duplicates, the
  admin parameter checks); admin UI: `tsc`, 96 vitest tests, `vite build`.
- Integration on ClickHouse 26.1.12, 24.1.8, 23.8.16 (58 / 58 / 54 pass, 0
  fail; 23.8 skips 2 that need access management), both insert paths: room
  switch times in 3 rooms x 6 sessions, two versions, a merged series and dev
  rows through ingest -> ClickHouse -> admin: per-room and per-version p50/p95
  within 2% of the exact nearest-rank percentiles, counts exact, `merged`,
  `build_type`, ctx filters, context keys; event counts by context and by
  property with `merged`; the daily views equal the tables (and give the same
  p95); asset_load/http/unknown rejected with reasons and stored nowhere; a
  re-sent batch dropped and `OPTIMIZE FINAL` keeping one row in the new tables;
  privacy deletion of a device's custom rows; fresh/legacy/service-account
  migration with 6 views.
- Migration rehearsal (a program outside the repo, `/tmp/claude-501/apm6/rehearsal`)
  on each version: production schema of 087940c (its `migrate.sql`), servers as a
  copy of the `apm` account (26.1, 24.1; 23.8: default), the 087940c binary
  ingesting 25 batches/s (APM-3 exceptions, an old-SDK event, perf samples and
  three APM-6 events) while the new binary starts and migrates (0.2-0.8 s), both
  serving: the old binary rejects the APM-6 events as unknown types and stores
  the rest; every acknowledged event stored exactly once (~1350 per run), exception
  `sum(count)`, custom event counts and metric samples exact, rejected events
  stored nowhere, daily views = tables; re-sends across binaries dropped as
  duplicates (and the custom events the old binary rejected stored by the new
  one); new admin API p50/p95 within 2%; old admin API on the migrated database;
  rollback to the old binary alone and redeploy of the new one; no warning or
  error in any server log. 37 checks per version, all passed (twice: before and
  after the review fixes).
- SDK: compiles at C# 8 with and without `UNITY_2021_2_OR_NEWER`; on Unity's
  Mono outside Unity 182 runtime tests pass (all logic tests of the new
  fixtures: time line, budget, aggregation, windows and sessions, histograms,
  timers, wire format, the warning-then-NRE breadcrumbs), 2 fail for the
  environment as before (JSONSerializeModule, gzip), 87 need the Unity runtime
  (among them the 5 new allocation tests and the `CustomApiTests` fixture).
  Unity EditMode could not run here (the sandbox blocks the licensing client).
- A code review (another agent) found two defects (a repeated long warning was
  copied on every call, ~8 KB; the per-minute budget was renewed by every
  `Flush`) and risks (errors evicted by warning spam, histogram collapse for
  wide metrics, window lost on a fatal exception, URL tokens in log lines, one
  name using up the budget, `apm17_fix_schema.sh` not knowing the new
  tables); all fixed with tests before the numbers above. Not changed: metric
  and aggregated rows carry the scene current when the window is sent, not
  where each sample was taken.
- Unity EditMode (run outside the sandbox): 285/285 pass, among them the
  allocation tests and `CustomApiTests`.
- On a device (Samsung A33, IL2CPP arm64, local server built from this branch):
  - Release build: a warning then an NRE 2 s later: the NRE's last breadcrumbs
    are `Event: room_enter roomId=r-12` and `Warning: Asset not found: ...`, in
    that order. Room switches (10 per room): admin p50 226.0 / 503.0 / 1075.7 ms
    against the exact 227.9 / 503.5 / 1070.1 (within 1%), p95 = the exact
    maximum, counts 10/10/10, merged 0. A named timer across Home/resume is not
    recorded (`timers_spanning_pause=1`; the one without a pause is: 1 row).
    300 frames of warning spam: 311 lines counted, the exception carries the
    custom event and 6 warning lines (`x300`, `x2` ...). 896 `TrackEvent` calls
    in 30 s: 10 sent one by one, 886 in one aggregated row, `sum(count)` = 896
    exactly; another name afterwards is still sent on its own with its
    property. Invalid property key and name refused with a warning (not an
    error); metric min/max exact (-15, 0.004). Consent denied: calls return
    false, nothing stored; granted again: stored. `http` and unknown types
    rejected with their reasons and `rejected_types`.
  - Jank `recent_events` (empty before APM-6): scene load, `Event: boss_spawn`,
    `Event: room_enter`, the warning and `Exception: NullReferenceException`, in
    order. It was checked on a jank produced by Home/resume, because a single
    long frame is never reported as a jank and a pause is reported as one: both
    are bugs of the jank detector from before APM-6 (APM-21).
  - Development build, two runs each: idle 13-14 frames with allocations, 87 KB
    a minute (as after APM-3); with `RecordMetric` and a timer every frame: 9
    frames, 34 KB (no upload in that minute), `update_ms` p50 0.043 ms
    unchanged; 1794 samples per name and minute in one row. Warning every
    frame: 373 B/frame with the SDK against 245.5 without; the difference is
    the SDK's idle allocations (48 B/frame) and Unity's conversion of the
    message for the log callback (~80 B): the SDK adds nothing per warning;
    `update_ms` p50 unchanged, frame time unchanged.
- Test environment (n192, server release 300, admin 301): the warning-then-NRE
  breadcrumbs, `enemy_killed` 304 = 10 + one aggregated row, admin room-switch
  percentiles within 2%; 14 kill/restart cycles: 1713 rows, no duplicate
  sequence number, event ids unique, gaps only at the 1000-number reservation
  boundaries.

## APM-11 and APM-15: exception detail page and session time line (additive index)

Deploy order: **server, then admin UI.** No SDK change: everything both pages
show is already sent by the APM-6 SDK (exceptions with context, error
sequence, breadcrumbs and recent log lines; custom events and metrics; every
event's context). The admin UI needs this server (it calls the new
endpoints); this server works with the previous admin UI.

### Admin API

All endpoints take the common filter (`start_time`, `end_time`,
`app_version`, `platform`, `build_type` default release incl. `''`,
repeatable `ctx=key:value`, `first_error`) unless said otherwise; counts are
`toInt64`, floats finite, every value a bound argument, results bounded.

Exceptions (APM-11):

- `GET /api/exceptions` adds `sort=count|sessions|first_seen|last_seen`
  (default `count`; the dates newest first; anything else is a 400) and, per
  group, `sessions_with_seq`: sessions whose rows carry an error sequence
  (SDK with APM-3). `first_error_sessions / sessions_with_seq` is the share of
  sessions in which the group was the session's first error (rows of older
  SDKs are in neither).
- `GET /api/exceptions/detail` (default 30 days) adds `sessions_with_seq`,
  `trend` (`interval_seconds`, `points[{timestamp, count, sessions}]`:
  occurrences per bucket; the interval is the smallest of 1 min, 5 min,
  15 min, 1 h, 3 h, 6 h, 1 day giving at most 200 buckets over the range) and
  `device_id` on each occurrence.
- `GET /api/exceptions/occurrences?fingerprint=F[&page=N][&page_size=M]`
  (default 30 days, page size 10, at most 50) -> `{fingerprint, occurrences,
  total_count, page, page_size}`: the group's rows, most recent first, each
  with time, count, version, build type, device, OS, scene, `session_id`,
  `device_id`, `context`, `breadcrumbs`, `error_seq`, `session_ms`,
  `unhandled`, `ctx_merged` (the fields of the detail's latest 20). The page
  is found by `(timestamp, event_id)` only, then the full rows (context,
  breadcrumbs) are read for its ids alone (one row per id). Only the 10000
  most recent rows can be paged through: a page beyond is a 400 saying so
  (narrow the time range for older ones).
- `GET /api/exceptions/distribution?fingerprint=F` and one of
  `by=app_version|build_type|device_model|platform|os_version` (default
  `app_version`) or `key=<context key>` (default 30 days) ->
  `{fingerprint, group_by, total, merged, truncated, rows[{value, count,
  sessions}]}`: occurrences (`sum(count)`) per value, top 50. For a context
  key, occurrences of counts the SDK merged across contexts that lack the key
  are `merged` (not under `""`, "key not set"), as in
  `/api/context/distribution` (APM-3).

Sessions (APM-15; these two take only a time range, not the common filter: a
session is shown whole):

- `GET /api/sessions/timeline?session_id=S[&at=T][&start_time&end_time][&perf=1]`
  -> `{session_id, start_time, end_time, found, session, items, counts,
  truncated, duplicate_breadcrumbs, perf, perf_interval_seconds}`.
  Range: as given; else `at` +- 24 h; else the last 7 days; at most 31 days
  (400). `session`: first and last row's time, `started_at` (from an
  exception's `session_ms`, when one carries it), device id, version, build
  type, platform, device, OS of the latest row, row count. `items`, sorted by
  time (then: context change, breadcrumb, rows by the SDK's `seq`), each with
  `time` and `type`:
  - `custom_event` (`name`, `props`, `count`; `aggregated`: a count of calls
    beyond the SDK's per-minute budget, from `time` to the end of that
    minute, without properties), `custom_metric` (one window aggregate:
    `count`, `sum`, `min`, `max`, `unit`, `window_ms`), `exception` (one row:
    first occurrence or repeat count, with `count`, `error_seq`,
    `session_ms`, `unhandled`, type, throw/call site, message cut to 2000
    characters, `fingerprint`), `crash` (type, fingerprint, the first 300
    characters of the stack), `jank` (with `recent_events`),
    `scene_load`, `startup`; each with its `context`.
  - `breadcrumb`: the lines exceptions (`breadcrumbs`), janks
    (`recent_events`) and crashes carry, with `kind` (`event`, `scene`,
    `warning`, `error`, `assert`, `exception`, `text`), `text`, `repeats` and
    `repeat_until` for a log line the SDK counted ("x37 until ..."), `source`.
    A line has only a time of day ("[HH:mm:ss.fff]", UTC): its date is that
    of the carrying row (the previous day if the time is later than the
    row's). A line carried by several rows is shown once (the highest count;
    compared on time, kind and the first 120 characters, since carriers cut
    lines at different lengths: a counted log line keeps ~140 characters of
    text in a jank, ~240 in an exception). A line that stands for a row on
    the time line is dropped: `Event: <name>` within 1 s of an individually
    sent custom event of that name, `Exception: <first line>` within 1 s of
    an exception row with that first line, `Error:`/`Assert: <text>` within
    1 s of an exception row of log type error/assert with that first line (a
    `Debug.LogError` is both), `Scene: <name>` within 5 s of a scene load of
    that scene. Both are counted in `duplicate_breadcrumbs`. Lines without a
    time (older SDKs) get the carrying row's time and `time_unknown`, and are
    shown once per source, kind and text (old SDKs repeat them on every
    jank).
  - `context`: a change of the game context, derived server-side (below):
    `changes[{key, from, to}]` (`""` = not set), `source`: the type of the
    row it was first seen on, `context`: the whole context after it.
  - Limits: 1000 rows per type (exceptions 500), 2000 breadcrumbs, 1000
    context changes; a type with more is listed in `truncated`, and the rows
    nearest to `at` are kept (the earliest without `at`; context changes:
    always the earliest). `counts` per type after the limits.
  - `perf=1`: `perf[{time, samples, fps_avg, fps_min, frame_ms_max,
    mem_mb_max}]`, the session's perf samples in at most 240 buckets of
    `perf_interval_seconds` (1 s to 1 day; `perf` in `truncated` if cut).
  - After the first query (the session's span) the per-type, context and
    perf queries run in parallel, cancelled with the request or after 25 s
    (below the admin server's 30 s write timeout). They run on a context
    without a deadline: clickhouse-go 2.17 writes `max_execution_time` into
    the settings map of a `clickhouse.Context` with a deadline on every query,
    which crashes concurrent queries (found by the perf probe below).
  - `found: false` (200, empty lists) when the session has no row in the range.
- `GET /api/sessions?device_id=D[&start_time&end_time]` (default 7 days, at
  most 31) -> `{device_id, start_time, end_time, sessions[{session_id,
  first_seen, last_seen, rows, exceptions, crashes, app_version, platform,
  device_model, build_type}], truncated}`: most recent first, at most 100.

### Context changes: derived, no SDK change

Every event carries the context current when it was recorded (APM-5). The
time line orders the session's rows of all eight tables by time and the
SDK's `seq` and reports a change where a row's context (its sorted entries,
hashed) differs from the previous row's (`lagInFrame` window in ClickHouse
over time, `seq`, source and hash only; the contexts of the changed rows are
then fetched by hash, and the diff is computed in Go). Counts
merged across contexts (`ctx_merged = 1`: their context is only what the
merged occurrences shared) are left out. Limits, honestly:

- A change is visible only at the next row that carries the context: with
  perf sampling on (one sample a second) within about a second; without it,
  at the next custom event, exception, jank or scene load, possibly much
  later. A context set and changed again between two rows is never seen.
- Metric and aggregated custom event rows carry the context of their first
  sample/call (`timestamp` = that moment), so they agree with the rest.
- Rows of the same millisecond are ordered by `seq` (the order the SDK
  stored them), not by when the context was read.

An SDK event "context changed" would remove the first two limits; it is not
needed for the tickets' scenarios and is left for when it is.

### Schema: `idx_session_id`

```sql
ALTER TABLE <t> ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
```

for the eight event tables; in `CREATE TABLE` for new installs and in
`scripts/migrate.sql`. `session_id` is in no sort key: without the index a
session's time line reads the `session_id` column of every row of every
table in the range. Metadata only; parts written afterwards carry the index;
existing parts get it when merged, or at once with (optional, a background
mutation per table that reads the column and writes the index):

```sql
ALTER TABLE <t> MATERIALIZE INDEX idx_session_id;
```

**Privileges.** `Migrate` adds the index where it may. The production `apm`
account (SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN) may not (ClickHouse
also checks the privilege when the index exists, so tables that have it are
skipped): the server starts, serves, and logs **one** warning per start,
`could not add the session_id index; ...`, naming the tables, until an admin
runs (on n192, as an admin):

```sh
clickhouse-client -d ozx_apm --multiquery <<'SQL'
ALTER TABLE apm_perf_samples ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_janks ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_startups ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_scene_loads ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_exceptions ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_crashes ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_custom_events ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE apm_custom_metrics ADD INDEX IF NOT EXISTS idx_session_id session_id TYPE bloom_filter(0.01) GRANULARITY 1;
SQL
# optional, existing parts at once:
# for t in apm_perf_samples apm_janks apm_startups apm_scene_loads apm_exceptions apm_crashes apm_custom_events apm_custom_metrics; do
#   clickhouse-client -d ozx_apm -q "ALTER TABLE $t MATERIALIZE INDEX idx_session_id"; done
clickhouse-client -d ozx_apm -q "SELECT name FROM system.tables WHERE database = 'ozx_apm' AND position(create_table_query, 'INDEX idx_session_id ') > 0"   # the 8 tables
```

(the same statements as in `scripts/migrate.sql`). The `apm` account's
grants are not changed. The order does not matter: the server works before
and after the admin step (only slower before), and the next start after it
logs no warning. Size, measured on the data below: 0.02% of
`apm_perf_samples`, 0.2% of `apm_exceptions`, under 1% of the smaller tables
(1.1 MB for 14 M rows).

### Cost

Measured on ClickHouse 26.1 locally, a scratch database with 7 days of
50,000 sessions of 30 minutes (about 150 at once), 20,000 devices, 3
versions: 10 M perf samples, 3 M exception rows (200 groups, one with a third
of the rows; a third of the rows with 10 breadcrumbs), 500 k custom events,
200 k metric rows, 200 k janks, 200 k scene loads, 50 k startups, 5 k
crashes; median of 3 runs; the query condition cache of 26.1 off, so every
run reads what a first request reads (`/tmp/claude-501/apm11/perf`):

| Request | Queries | Rows read, no index | With `idx_session_id` | Time no index / with |
|---|---|---|---|---|
| Time line from an occurrence (at, session's range) | 10 | 4.7 M (163 MB) | 0.61 M (59 MB) | 49 / 48 ms |
| Time line by id, 7 or 30 days | 10 | 16.2 M (453 MB) | 0.78 M (63 MB) | 58 / 46-60 ms |
| Unknown session, 30 days | 1 | 14.2 M (358 MB) | 0.14 M (3.5 MB) | 29 / 24 ms |
| + perf track | +1 | | +16 k | 47 ms |
| Sessions of a device, 7 days | 1 | 14.2 M (177 MB) | same (no device index) | 41 ms |
| Exception list, 24 h / 7 d | 2 | 1.5 M / 6.0 M | unchanged | 42 / 87-97 ms |
| Detail, largest group / small group, 30 d | 6 | 12.1 M / 5.7 M | unchanged | 132 / 80 ms |
| Occurrences, page 1 or 100 | 3 | 4.0 M (175-189 MB) | unchanged | 35-40 ms |
| Distribution by roomId / by version, 30 d | 3 / 2 | 6.0 M / 4.0 M | unchanged | 53 / 34 ms |

The time line's queries after the first run in parallel, so its time is about
that of its slowest query, not their sum (the "ch ms" sum is 70-86 ms).

- The time line's first query finds the session's first and last row; the
  others read only that span (one or two daily partitions). Per-type limits
  bound what is returned; the context query sorts the session's rows of all
  tables by time with only (time, seq, source, hash) (a few thousand rows per
  hour of play; a session kept open for days is bounded by the 31-day range).
- The detail's new trend query reads the group's `timestamp`, `count`,
  `session_id` (0.9 M rows, 31 MB, 9 ms for the small group over 30 days).
  The list's new `sessions_with_seq` is one more `uniqExactIf` over the
  same rows.
- The exception queries filter on `fingerprint` (second column of the sort
  key) and the time range, as the APM-4 detail did. An occurrence page sorts
  only `(timestamp, event_id)` of the group's rows (offset at most 10000),
  then reads context and breadcrumbs of its 10 ids.
- Sessions of a device read the `device_id` column of every table in the
  range (the search box offers up to 30 days: about 4 times the 7-day
  figure). A `device_id` bloom filter (tried: 0.5 M rows instead of 14 M,
  1.2 MB) is not part of this release; add it the same way if device lookups
  become slow.

### Admin UI

- Exceptions: sort (occurrences, affected sessions, first seen, last seen),
  "First error of the session only", a "First error in" column, rows open the
  detail page.
- Exception detail (`/exceptions/detail?fingerprint=F`, all filters of the
  filter bar apply): summary (occurrences, sessions, share of sessions where
  it was the first error, unhandled, first and last seen with their
  versions), trend, the stack as parsed by the server (inner exception
  chain, in-app frames, file:line where the build has them; for an exception
  logged inside an error, e.g. by the event bus, "logged from <LogError call
  site>" and the log call's stack; the raw text as sent), distribution by
  version, build type, device, platform, OS or any context key ("Filter"
  adds it to the filter bar), occurrences page by page (context, breadcrumbs
  and recent log lines by kind on a time line, session id, error number in
  the session, time into the session, device id, unhandled and merged
  flags) with links to the session time line at that moment and to the
  device's sessions.
- Sessions (`/sessions`): the time line (filter by type, optional FPS track,
  anchored and scrolled to the occurrence it was opened from, session start
  and "last data" markers), search by session id, a device's sessions.
- Game text (messages, breadcrumbs, context values, ids) is only rendered as
  text; links are built with `URLSearchParams`. Missing fields (old SDK rows,
  an older server) render as empty, never as an error or a blank page.

### Rolling deploy and rollback

Old and new binaries can serve side by side: the old binary does not read or
write the index (it is a skip index; inserts maintain it whoever inserts).
The admin UI of this release needs this server; the previous UI works with
it. **Rollback:** redeploy the previous server (and admin UI); no schema
action. The index stays and keeps being maintained on insert (harmless); to
remove it (admin): `ALTER TABLE <t> DROP INDEX IF EXISTS idx_session_id` per
table.

### Verified

- Server: `go vet`, unit tests (253 passing test functions, 407 with
  subtests): breadcrumb parsing (kinds, midnight, counted lines, malformed
  and hostile text), de-duplication and the rows that cover a line
  (custom event, exception, LogError/assert, scene), context diffs, the
  order of same-millisecond items, the nearest-to-`at` cut, the SQL builders
  (every value bound, every part limited to the session and range, merged
  counts left out of the context), intervals, sorts, the handlers' 400s and
  defaults, the parallel query context under `-race`.
- Integration on ClickHouse 26.1.12, 24.1.8, 23.8.16 (46 / 46 / 44 test
  functions pass, 0 fail; 23.8 skips 2 that need access management), both
  insert paths, ingest router -> ClickHouse -> admin router:
  - the ticket's "room only half built" session: the time line is, in
    order, context levelId, startup, scene load, roomId -> r-11, room_enter
    r-11, room_ready r-11, the metric aggregate, the counted warning, roomId
    -> r-12, room_enter r-12, the bus exception (first error), its repeat
    count, the aggregated custom event count, a LogError (shown once), an
    error line, the jank, and nothing after the last perf sample; session
    start from `session_ms`; 7 lines dropped as duplicates; perf track;
    the device's sessions; unknown session; 400s;
  - 1200 custom events in a session: 1000 returned, the ones nearest to
    `at`, truncated reported;
  - APM-11: the bus exception in three rooms (41/25/10 occurrences and a
    merged count of 7: by roomId 3 rooms, merged 7, total 83), by build type
    with a dev row, by version/device/platform/OS, trend adding up to the
    count, first error only (the Awake root cause), sorts, occurrences page
    by page without repeats, past the end, with a context filter;
  - rows written by the pre-APM-5 binary's INSERT: time line, occurrences,
    detail and distribution work and read the defaults;
  - migration: fresh install and the production fixture get the index; the
    service account gets one warning naming the 6 tables it could not
    index (the 2 it creates have it), the time line works without the
    index, and after the admin statements from `migrate.sql` a restart is
    quiet.
- Admin UI: `tsc`, 127 vitest tests (new: the detail page with stack,
  file:line, call site, first-error share, occurrence context/breadcrumbs by
  kind, encoded session and device links, roomId distribution with merged;
  an old SDK's group and an old server's response; 404; the time line of
  the half-built room in order, anchored on the exception, type filters,
  perf track, not found, a 400, an old session, search by session and
  device; `<script>`/`<img onerror>` in game text rendered as text), `vite
  build`.
- Migration rehearsal (a program outside the repo,
  `/tmp/claude-501/apm11/rehearsal`) on each version: production schema of
  a7cd2bc (its `migrate.sql`), servers as a copy of the `apm` account (26.1,
  24.1; 23.8: default, which adds the index itself); the a7cd2bc binary
  ingesting 25 batches/s (the half-built room: room_enter, the bus
  exception with breadcrumbs, a repeat count, an aggregated custom event,
  perf samples; an old-SDK event) while the new binary starts and migrates
  (0.2-0.9 s, one index warning naming the 8 tables), both serving; the
  admin step (ADD INDEX and MATERIALIZE INDEX on the 8 tables, 70 ms) under
  that load; every acknowledged event stored exactly once (~1500 per run),
  `sum(count)` exact, daily views equal the tables; re-sends across
  binaries dropped as duplicates; the new admin API's time line of a
  session the old binary stored and of one stored during the deploy (same
  order as above), occurrences with device and context, distribution by
  roomId, detail on both binaries, a device's sessions; rollback to the old
  binary alone on the indexed tables and its admin API; the new binary
  again with no warning. 37 checks per version, all passed (after the
  review fixes, with the final binary).
- Performance: above.
- A code review (another agent) found: every `Debug.LogError` shown twice
  on the time line (its row and its log line); deep occurrence pages
  sorting the full rows; the time line's queries run one after the other
  with no deadline; the context query sorting the maps; long lines of
  different carriers and untimed lines of old SDKs not de-duplicated; the
  device link without a range; the occurrence page not reset on a filter
  change; trend lines drawn across empty periods; the perf track cut
  silently; crash stacks sent in full; all fixed with tests. Running the
  perf probe on the parallel version then found the clickhouse-go settings
  map race (above), fixed with a test.
- Review before merge: the time line ran up to 9 queries at once on the
  connection pool that ingest shares (10 connections, one process for the
  SDK and admin ports): two time lines opened together could make ingest
  wait for a connection. At most 4 time line queries now run at once across
  requests (a test; time line still 37-46 ms on the 14 M row database).
- On a device (Samsung A33, IL2CPP arm64 release build, local server from
  this branch, then the test environment): the half-built room reads, from
  the occurrence's "session time line at this moment": scene load, levelId
  -> 3, roomId -> r-11, room_enter r-11, room_ready r-11, roomId r-11 ->
  r-12, room_enter r-12, the warning "Room layout missing for r-12", the
  exception, and no room_ready r-12. On n192 room_enter and the warning
  shared a millisecond and the warning came first: same-millisecond lines
  are now ordered after the row recorded before them in their carrier's
  list (fixed with a test; checked again on n192). Detail: throw site
  `RoomBuilder.LoadLayout` / `RoomLoader.LoadTemplate`, logged from
  `GameEventBus.Publish` (no file/line on a release build: OZX-1277), the
  occurrence's context `planId=p-7`, `masterSeed=184467440737`. Distribution
  by roomId: 1001/1002/1003 one each; the per-frame NRE in three rooms
  300/299/299 = the device's counts (898, merged 0); by version, build,
  model, platform, OS. "First error of the session" lists the Awake failure,
  not the NRE cascade after it. Sorts, `sort=bogus` 400, page 2, offset cap
  400, a `ctx` filter. 11 context changes for a room every 3 s; aggregated
  `enemy_killed` rows (1006, 1062) and metric windows on the time line; a
  device's sessions; a 40-day range 400; an unknown session `found: false`;
  a jank's recent events (scene, room_enter, the warning, the NRE).
- Test environment (server release 304, admin 305): one start-up warning
  naming the 8 tables (no ALTER ADD INDEX); time line of the session
  0.12 s over HTTPS without the index; exception list sorts ~0.05 s.

### Not verified

- File and line on a release build: they need OZX-1277.
- `idx_session_id` on n192: the `apm` account cannot add it; until an admin
  runs the statements above, time lines read the `session_id` column of
  every table in the range (0.12 s at the test environment's volume).
- Production volume: the numbers above are a local scratch database; the
  production row counts per day are not known here.
- Crash rows: the SDK sends none yet; the time line shows them from test
  rows only.
- The admin pages were tested with vitest (jsdom) and through their API on
  the test environment, not opened in a browser.

## APM-8: alerting (new issue, regression, crash rate spike, startup P95) and issue state (additive schema change)

Deploy order: **server (app 36 and app 38), then admin UI; then turn
alerting on for app 36 only.** No SDK change. Two new tables, created by
`Migrate` with the `CREATE TABLE` grant the `apm` account has; nothing
needs ALTER UPDATE, MODIFY or ADD INDEX. Alerting is **off** by default: the
server logs one info line, `alerting disabled (OZX_ALERT_ENABLED is not
true; ...)`, and does nothing else.

### Design

- The evaluator (`internal/alert`) reads **ClickHouse**, never ingest's
  memory, once per `interval` (60 s), starting 15 s after startup. A restart
  neither loses nor repeats alerts; the old in-memory `Aggregator` (never
  started, per-process counts) is removed. It runs its queries one at a
  time (at most one of the 10 pooled connections, which ingest shares),
  each bounded by `LIMIT`; each step's error is logged and the next step
  still runs.
- **Outbox** `apm_alerts` (MergeTree, append-only, no TTL): every alert has
  a deterministic key, e.g. `new_issue:exception:<fp>`,
  `regression:crash:<fp>:<state version>`,
  `crash_rate_spike:<version>:<5-min bucket>`,
  `startup_p95_regression:<new>:<previous>`, `test:<ms>:<random>`. Its rows:
  `pending` (detected, with the payload JSON), `claimed` / `sent` / `failed`
  per attempt (with `attempt`, `instance`, `error`), `abandoned`, `known`
  (a fingerprint recorded without an alert).
  - Detection skips keys that have any row, so an alert is detected once,
    whichever process or restart finds it.
  - Sending: an instance writes a `claimed` row for attempt N, waits 2 s,
    reads the claims of N back, and sends only if its claim is the first
    (earliest `inserted_at`, set by ClickHouse, so pod clocks do not
    matter; then instance name). Two instances (app 36 scaled to 2
    replicas, or alerting enabled on both apps by mistake) therefore send
    each alert once. A claim without an outcome counts as failed after
    6 min (longer than an evaluation can last), and an instance never sends
    its own claim once it is older than 6 min - 10 s, so a slow webhook does
    not let another instance overtake it. The remaining window: an instance
    stops after the webhook accepted the message but before writing `sent`;
    after 6 min another instance retries it (one duplicate message).
  - Retries: a failed attempt (non-2xx, timeout 10 s, connection error;
    Feishu's 200 with `code != 0`; a 3xx, since a redirected POST arrives
    as a GET without the message) is retried after 1, 2, 4, 8, 16 min;
    after 6 attempts the alert is `abandoned` (logged as an error). At most
    20 webhook requests per evaluation; alerts detected more than 24 h ago
    are no longer sent.
- **Issue state** `apm_issue_states` (ReplacingMergeTree(`version`),
  `ORDER BY (kind, fingerprint)`): each change is a new row with
  `version` = time in ms (above the previous row's); reads use `FINAL`.
  No row = unresolved. Merges keep only the latest row per issue (no
  history beyond `resolved_*` / `regressed_*` of the latest row).

Rules (release builds only: `build_type IN ('release', '')`; dev and editor
builds are ignored unless `include_dev_builds`):

- **New issue** (exception or crash fingerprint). "New" = not known to the
  alerting: no `apm_alerts` row of type `new_issue` for it. Known are:
  - every fingerprint in the retained rows when alerting first runs (the
    **baseline**, once per kind, marker key `baseline:<kind>`; one
    `INSERT ... SELECT` in ClickHouse, no limit): turning alerting on does
    not report the existing issues;
  - every fingerprint alerted (or suppressed) since. These rows never
    expire, so an issue that stops for more than the 30-day event retention
    and returns is **not** new (if it was resolved, it is a regression).
  Each evaluation looks at rows of the last `new_issue_window` (26 h) by
  event time; every `sweep_interval` (6 h, and at each start) at the whole
  retention, so events uploaded late from the offline queue (APM-2) are
  found within 6 h. A fingerprint first seen in a dev build is new when it
  first appears in a release build. **Storm limits**: at most
  `new_issue_max_per_cycle` (10) individual alerts per evaluation and
  `new_issue_max_per_hour` (30) per hour (counted in the outbox: across
  evaluations, restarts and replicas); the rest are recorded as known and
  summarised in one `new_issue_digest` alert per kind per 15 min (key by
  time bucket); beyond that they are only recorded (visible in the admin
  lists). A fingerprint algorithm change or a broken build therefore costs
  at most ~34 messages in its first hour per kind.
- **Regression**: an issue marked resolved that has rows after the
  resolution in its fix version or newer (`resolved_in_version`,
  inclusive), or, when the admin gave no fix version, in a version newer
  than `last_seen_version` (the newest version that had it when it was
  resolved). Old clients still running an affected version after the
  resolution are not a regression. One alert per resolution (key includes
  the state version); the issue is reopened (`unresolved`,
  `regressed_in_version`, `regressed_at`, `updated_by: alert`).
- **Version order**: a name is prefix (text before the first digit,
  lower-cased; `v` = none) + dot-separated numbers + optional pre-release
  (`-beta.1`, or any text after the numbers; `+build` ignored). Names with
  the same prefix compare like semantic versions (`1.10.0 > 1.9.9`,
  `1.4 = 1.4.0`, `2.0.0-beta.2 < 2.0.0-beta.10 < 2.0.0`,
  `apmtest-2.0.0 > apmtest-1.9.0`). Names that cannot be compared (no
  digits, different prefixes: `apmtest-2.0.0` vs `2.0.0`) fall back to
  time: the version whose first startup (in the retained `apm_startups`)
  is later is newer; a version without one counts as first seen when its
  rows were (the threshold version: at the resolution). Text after the
  numbers counts as a pre-release, so `1.4.2-hotfix < 1.4.2`.
- **Crash rate spike** (every `crash_rate_check_interval`, 5 min): per
  version, crashed sessions (`uniqExact(session_id)` of `apm_crashes`) /
  sessions (`uniq(session_id)` of perf samples, startups and crashes) over
  the last `crash_rate_window` (1 h). Fires when the version has at least
  `crash_rate_min_sessions` (50) sessions and `crash_rate_min_crashed_sessions`
  (5) crashed ones, and the rate is at least `crash_rate_min_rate` (0.5%)
  and `crash_rate_factor` (2) times the baseline: all versions over the
  `crash_rate_baseline_window` (24 h) before the window (refreshed hourly;
  ignored when it has fewer sessions than the minimum). Then quiet for that
  version for `crash_rate_cooldown` (6 h). Sessions are only counted from
  rows they sent: with perf sampling off a session is seen through its
  startup or crash only.
- **Startup P95 regression** (hourly): over `startup_window` (7 days),
  versions with at least `startup_min_samples` (100) startups; the newest
  (version order above) against the previous one: P95 of
  `phase1 + phase2 + tti` (the dashboard's startup) at least
  `startup_increase` (20%) and `startup_min_delta_ms` (100 ms) higher. Once
  per version pair.
- Jank and exception rate rules of the old evaluator are not carried over
  (not in the ticket; the new-issue and crash rules replace them).

### Webhook

`POST` to `webhook_url`, `Content-Type: application/json`, `User-Agent:
ozx-apm-alert/1`, 10 s timeout. `webhook_format`:

- `generic` (default):
  ```json
  {
    "schema": "ozx-apm-alert/1",
    "alert_key": "new_issue:exception:5b0c1d...",
    "type": "new_issue",        // new_issue | new_issue_digest | regression | crash_rate_spike | startup_p95_regression | test
    "severity": "warning",      // info | warning | critical (crashes, regressions, crash rate)
    "title": "New exception: System.NullReferenceException at Ozx.Enemies.EnemySpawner.Spawn ...",
    "kind": "exception",        // exception | crash (issue alerts only)
    "fingerprint": "5b0c1d...",
    "app_version": "2.0.0",
    "facts": [{"label": "First seen", "value": "2026-10-18 09:12 UTC in 2.0.0"}, ...],
    "metrics": {"count": 2, "sessions": 2},
    "admin_path": "/exceptions/detail?fingerprint=5b0c1d...",
    "detected_at": "2026-10-18T09:13:05Z",
    "url": "https://apm-admin.local.playquota.com/exceptions/detail?fingerprint=5b0c1d...",
    "text": "[OZX APM] New exception: ...\nFirst seen: ...\n...\nhttps://apm-admin.local.playquota.com/exceptions/detail?fingerprint=5b0c1d..."
  }
  ```
  `metrics` per type: new issue `count`, `sessions`; regression `count`,
  `sessions` (since the resolution, in the version); crash rate `rate`,
  `threshold`, `baseline`, `sessions`, `crashed_sessions`, `crashes`;
  startup `p95_ms`, `previous_p95_ms`, `increase`, `samples`,
  `previous_samples`. `url` only when `admin_base_url` is set.
- `slack`: `{"text": "<text>"}`; `feishu`: `{"msg_type": "text",
  "content": {"text": "<text>"}}` (Feishu/Lark custom bot; a reply with a
  non-zero `code` counts as a failure).
- Game text (types, messages, throw sites, stack lines, versions,
  fingerprints) in `title`, `facts` and `text` is made inert and bounded
  (`alert.CleanText`): one line, `@` followed by a zero-width space (no
  `@all` / `@here` / `@channel`), `<` `>` `&` replaced by look-alikes (no
  Slack `<!channel>` / `<url|text>`, no Feishu `<at>`), Markdown characters
  (`` ` `` `*` `~` `[` `]` `|`) replaced, `://` broken so URLs are not
  linked, at most 120-300 characters. Bodies are JSON-encoded, never
  templated.
- The URL is a secret: never logged, stored or returned; logs and stored
  errors name only its host (`webhook_host` in the startup line).

### Configuration

Every key is `alert.<key>` in `config.yaml` and the environment variable
`OZX_ALERT_<KEY>`:

| Key | Default | |
|---|---|---|
| `enabled` | false | `OZX_ALERT_ENABLED=true` starts the evaluator |
| `webhook_url` | "" | **secret**; required when enabled (else one error line, not started) |
| `webhook_format` | generic | generic, slack, feishu |
| `admin_base_url` | "" | e.g. `https://apm-admin.local.playquota.com`; links in alerts |
| `interval` | 60s | at least 10 s |
| `include_dev_builds` | false | |
| `new_issue_max_per_cycle` / `new_issue_max_per_hour` / `new_issue_window` / `sweep_interval` | 10 / 30 / 26h / 6h | |
| `crash_rate_window` / `_baseline_window` / `_check_interval` | 1h / 24h / 5m | |
| `crash_rate_min_sessions` / `_min_crashed_sessions` / `_min_rate` / `_factor` / `_cooldown` | 50 / 5 / 0.005 / 2 / 6h | |
| `startup_enabled` / `startup_window` / `_check_interval` / `_min_samples` / `_increase` / `_min_delta_ms` | true / 168h / 1h / 100 / 0.2 / 100 | |

Plus `OZX_ADMIN_ALERT_TOKEN` (`admin_server.alert_token`, **secret**):
enables `POST /api/alerts/test` (below); disabled (403) without it, one
info line `alert test API disabled: no token configured`.

**Found while doing this (not changed):** `config.Load` binds environment
variables only where `BindEnv` is called (privacy token, now the alert
keys); `viper.AutomaticEnv` without a key replacer never maps
`OZX_SERVER_ENABLED` to `server.enabled`. So `Dockerfile.admin`'s
`OZX_SERVER_ENABLED=false`, `OZX_ADMIN_SERVER_HOST`, `..._PORT` have no
effect (verified by a unit test run: `server.enabled` stays true): app 38
also runs the ingest listener on 8080 inside its container (not exposed),
and its admin server listens on 0.0.0.0:8081 (the default, which is why
nginx still reaches it). Harmless today; alerting does not depend on it
(it is enabled explicitly). Fixing it (a key replacer) would start honouring
every `OZX_*` variable set on both apps, so check their environments
first.

### Admin API

- `GET /api/issues/state?kind=exception|crash&fingerprint=F` -> the state:
  `{kind, fingerprint, status, resolved_in_version, last_seen_version,
  resolved_at, regressed_in_version, regressed_at, updated_at, updated_by,
  comment}` (`status: unresolved` and nothing else for an issue never
  changed).
- `POST /api/issues/state` with headers `Content-Type: application/json`
  and **`X-Apm-Admin: 1`**, body `{"kind","fingerprint","status":
  "resolved"|"unresolved","resolved_in_version" (optional, the version
  with the fix),"comment" (optional, 500 chars)}` -> 200 with the new
  state; 404 when the issue has no row in the retained data; 400 for a bad
  body or a missing header; 415 without JSON. Resolving records
  `last_seen_version` (newest version, release builds) and `resolved_at`;
  reopening keeps them. `X-Apm-Admin` is not in the CORS allow-list, so a
  page of another origin cannot send it; like the rest of the admin API
  this has no login (APM-14 "open risk").
- `GET /api/exceptions` adds `status=resolved|unresolved|all` (default all;
  a bound `fingerprint [NOT] IN (SELECT ... FINAL)` subquery); exception and
  crash lists and details carry `status`, `resolved_in_version`,
  `resolved_at`, `regressed_in_version` (`status` absent when the states
  cannot be read: the list is still served).
- `GET /api/alerts[?limit=N]` (default 50, at most 200) -> `{alerts:
  [{alert_key, type, kind, fingerprint, app_version, created_at, status,
  updated_at, attempts, error, title}]}`: the latest detected alerts and
  their delivery state.
- `POST /api/alerts/test` with `Authorization: Bearer <OZX_ADMIN_ALERT_TOKEN>`
  -> 202 `{alert_key, note}`: queues a `test` alert in the outbox; the
  evaluator (app 36) posts it at its next evaluation, so it works from app
  38 too and exercises the real path. 401 for a missing/wrong token (logged
  with the remote address, never the token), 403 without a configured
  token, 429 for a second request within 10 s. It never makes an outbound
  request itself.

### Admin UI

Exceptions: a Status column (Unresolved, Resolved in X, Regressed in X) and
a status filter. Exception detail and the crash panel: the state with "Mark
resolved" (optional "Fixed in version", suggested from the known versions;
empty = the next version) or "Reopen". The crash page opens the group of
`/crashes?fingerprint=F` (the link of a crash alert). All values render as
text; an older server (no `status`) shows no control.

### Schema

```sql
CREATE TABLE IF NOT EXISTS apm_issue_states (... ) ENGINE = ReplacingMergeTree(version) ORDER BY (kind, fingerprint);
CREATE TABLE IF NOT EXISTS apm_alerts (... ) ENGINE = MergeTree() ORDER BY (alert_key, at);
```

(full statements in `scripts/migrate.sql`, generated from `schema.go`).
`Migrate` creates them at startup with the `apm` account's `CREATE TABLE`
grant; if that fails it logs `could not create an alerting table; ...`
and the server still starts (ingest and pages unaffected; state endpoints
and the evaluator then fail). No TTL: a few rows per alert and one per
fingerprint (thousands), kept as the record of known fingerprints. No
change to the event tables.

### Cost (estimated, not measured)

Per evaluation (60 s), sequentially, one connection:

- new issues: one query per kind over the last 26 h of `apm_exceptions` /
  `apm_crashes` (`timestamp`, `fingerprint`, `build_type`, filtered by
  `fingerprint NOT IN (known)`, so the other columns are only read for new
  fingerprints' granules), `LIMIT 200`. At the APM-11 scratch volume
  (3 M exception rows a week) that is ~0.45 M rows a minute. The 6-hourly
  sweep reads 30 days (~13 M rows) once.
- regressions: only when resolved issues exist (at most 1000): one query
  per 200 of them, over the rows since the earliest resolution (at most 26 h; 30 days at the
  sweep) with `fingerprint IN (resolved)`.
- crash rate, every 5 min: `session_id` of the last hour of perf samples,
  startups and crashes; hourly the same over 24 h (at the scratch volume
  ~1.4 M perf rows).
- startup: hourly, 7 days of `apm_startups` (small).
- outbox: one query on `apm_alerts` (small); claims and outcomes are a few
  inserts per alert.

Measure on the test environment (`system.query_log`, user `apm`, queries
from the evaluator) before relying on these numbers.

### Deploy (CI/CD) and rollback

1. Deploy the server to **app 36 and app 38** (both run `Migrate`; the two
   new tables appear; no new warning). Then the admin UI (app 38 image
   holds both).
2. **App 38 (admin only): never set `OZX_ALERT_ENABLED`.** Set
   `OZX_ADMIN_ALERT_TOKEN` (secret, e.g. `openssl rand -hex 32`) to allow
   test alerts.
3. **App 36**: set as environment variables (secrets in the CI/CD secret
   store): `OZX_ALERT_ENABLED=true`, `OZX_ALERT_WEBHOOK_URL=<incoming
   webhook>` (secret), `OZX_ALERT_WEBHOOK_FORMAT=feishu|slack|generic`,
   `OZX_ALERT_ADMIN_BASE_URL=https://apm-admin.local.playquota.com`;
   redeploy (read at startup). Expect the log line `alerting enabled`
   with `webhook_host`, `webhook_format`, `admin_links: true`, and on the
   first evaluation `alert: baseline recorded; ...` once per kind.
   Scaling app 36 to 2 replicas is safe (each alert sent once, see above);
   so is enabling it on both apps by mistake, but do not.
4. Check: `POST /api/alerts/test` (below) arrives in the channel within
   about a minute; `GET /api/alerts` shows it `sent`.

**Rollback:** to stop alerts at once, set `OZX_ALERT_ENABLED=false` (or
remove it) on app 36 and redeploy. To roll back the release: redeploy the
previous server and admin UI; the old binary ignores the two tables
(keep them: dropping `apm_alerts` would make the next enable re-baseline,
which is harmless, but lose the record of what was sent; dropping
`apm_issue_states` loses the resolved marks). Pending alerts of the outbox
older than 24 h are not sent after a re-enable.

### Verification without a chat webhook

Locally:

```sh
cd server
go run ./cmd/alertsink -addr 127.0.0.1:9099          # prints every POST; -fail 2 answers 500 twice, -feishu answers {"code":0}
OZX_ALERT_ENABLED=true OZX_ALERT_WEBHOOK_URL=http://127.0.0.1:9099/hook \
OZX_ALERT_INTERVAL=10s OZX_ALERT_ADMIN_BASE_URL=http://localhost:3000 \
OZX_ADMIN_ALERT_TOKEN=dev-token go run ./cmd/server
curl -sS -X POST http://localhost:8081/api/alerts/test -H 'Authorization: Bearer dev-token'
curl -sS http://localhost:8081/api/alerts | jq
```

On a host the cluster can reach, without Go:

```sh
python3 -c 'import http.server as h
class H(h.BaseHTTPRequestHandler):
    def do_POST(s):
        print(s.rfile.read(int(s.headers.get("Content-Length", 0))).decode(), flush=True)
        s.send_response(200); s.end_headers()
h.HTTPServer(("0.0.0.0", 9099), H).serve_forever()'
```

Acceptance (APM-1 item 8) on the test environment: after step 3, build
the test game with a new `app_version` that throws an exception the old
one does not (e.g. a button raising `InvalidOperationException` in a new
method), release build; within `interval` (+ upload time) a `new_issue`
alert for that version arrives; `GET /api/alerts` shows it `sent`, and it
is not sent again. Then mark it resolved with a fix version from the
exception page, build that version still throwing it: a `regression`
alert arrives and the page shows "Regressed in <version>".

```sh
TOKEN=...   # OZX_ADMIN_ALERT_TOKEN, from the secret store
curl -sS -X POST https://apm-admin.local.playquota.com/api/alerts/test -H "Authorization: Bearer $TOKEN"
curl -sS https://apm-admin.local.playquota.com/api/alerts | jq '.alerts[:5]'
curl -sS -X POST https://apm-admin.local.playquota.com/api/issues/state -H 'Content-Type: application/json' -H 'X-Apm-Admin: 1' \
  -d '{"kind":"exception","fingerprint":"<fp>","status":"resolved","resolved_in_version":"<fix version>"}'
```

### Verified

- Unit tests (`go vet ./...`, `go test -race` on the changed packages):
  version order (semver, prefixes, pre-releases, fallback to first seen);
  inert text (mentions, Slack/Feishu markup, Markdown, URLs, bidi and
  control characters, invalid UTF-8, bounds); the three body formats, the
  admin link, Feishu's `code`, errors and timeouts never showing the URL;
  the evaluator on an in-memory store with the SQL's semantics: baseline,
  a new fingerprint sent once across more evaluations, a restart and a
  second instance, dev builds ignored until a release build has the
  fingerprint, the storm digest, the sweep finding a late upload, two
  instances claiming at once sending once, retry with
  backoff and abandon after 6 attempts, a stale claim retried, regressions
  (old version after the resolution not one, fix version and newer one,
  reopened, sent once, a second regression after resolving again, time
  fallback for `apmtest-` names, reopening retried when it failed), crash
  rate thresholds and cooldown, startup P95, a failing step not stopping
  the others, at most 20 sends per evaluation; the handlers (resolve,
  reopen, last seen version with incomparable names, 400/404/415, missing
  header, state annotation and the status filter, a failing state read
  still serving the list, the test endpoint's 403/401/202/429, the alert
  list); after a code review (another agent): the storm caps across
  evaluations (10, 30 per hour, one digest per 15 min), a slow instance not
  sending a claim that may count as stale, a redirect being a failure, the
  generic `alert_key` made inert; the configuration from `OZX_ALERT_*` and `OZX_ADMIN_ALERT_TOKEN`;
  the `status` subquery.
- Admin UI: `tsc`, vitest (141 tests, 14 new: badge and control, resolve
  with and without a version, reopen, errors as text, `<img onerror>` in
  versions rendered as text, the status filter, the crash page's
  `?fingerprint=`), `vite build`.

### Not verified

- **Nothing here ran against ClickHouse**: the author's sandbox could not
  reach a local ClickHouse. `tests/integration/alert_test.go` (APM-1
  acceptance 8 through the ingest router, resolve and regression with the
  status filter, crash rate spike and new crash, startup P95, two
  evaluators at once, the test endpoint and a failing webhook, the `apm`
  service account's grants, the schema from `Migrate` and `migrate.sql`)
  compiles and is vetted but has not been run on 26.1, 24.1 or 23.8; the
  SQL of `repository_alerts.go` is unverified on every version.
- A real Slack or Feishu webhook; the deploy on apps 36/38; n192 (the
  `apm` account creating the tables); query cost at any volume.
- Crash alerts from real crashes: the SDK sends none until APM-7.
- Two replicas of app 36 in a real deployment (only simulated).
- The admin pages in a browser (vitest/jsdom only).

## APM-7: native crashes, ANRs and kills (Android), IL2CPP symbolication (additive schema change)

Deploy order: **server (both apps: 36 and 38), then the symbol upload in
CI, then the admin UI, then SDK builds.** Each step works without the next:
an older server stores the new crash events without the new fields (the
SDK sends `fingerprint`, `stack`, `breadcrumbs`, `crash_type` as before);
crashes without uploaded symbols show addresses; the previous admin UI
reads the crash list and detail as before.

Before this the SDK never produced a crash event: a native crash, an ANR or
a kill ends the process before managed code can report anything, and an
IL2CPP release `libil2cpp.so` has no symbols, so a native stack is only
offsets.

### SDK: crash journal

`Core/CrashJournal.cs`. While the game runs (Android player, technical
consent, `ApmConfig.EnableCrashReporting`, on by default), the SDK keeps a
file per process in `<persistentDataPath>/ozx_apm_crash/`: pid, process
start, session id and start, device id, version, build type, device, ABI,
scene, foreground (not paused), the last perf sample's figures, the game
context, and the time line a crash carries (the 20 most recent breadcrumbs
and 10 most recent warning/error lines, cut to 200/300 characters, URL
query strings removed: what an exception's first occurrence carries).

- Written on its own thread (`OzxApm.Journal`), never on the main thread:
  recording code (custom events, breadcrumbs, scene loads, warning/error
  lines, `SetContext`, pause/resume) only calls `NotifyChanged`, a flag
  (an interlocked exchange; the writer is woken only when it sleeps). The
  writer writes at once, then at most every 100 ms while things change
  (bursts are coalesced), and every 5 s otherwise (a heartbeat: `written`
  says when the process was last alive).
- No allocation per write: the time line is formatted straight into
  reused buffers (Timeline.AppendSnapshot; `StripUrlQueries` now replaces
  in place, which also removes ~550 B per URL from exception snapshots),
  encoded into a reused byte array, written with an unbuffered FileStream.
- No fsync: what a process wrote is in the page cache, which survives the
  process (a native crash, a kill); only a power loss or kernel crash can
  lose the last write. Page-cache overwrites of the same 4 KB reach the
  flash at writeback, not at every write.
- Two files written in turn, each ending with an FNV-1a checksum: a process
  killed in the middle of a write leaves the other, previous write intact.
- A crash within ~0.1 s of the last change may miss that change (the
  writer had not written it yet); an unhandled managed exception that ends
  the process writes the journal synchronously first (with the exception
  on its time line).
- Clean shutdown (`OnApplicationQuit`/`OnDestroy`, `ApmClient.Shutdown`)
  marks the process's journal clean (`clean=1`) and keeps it until the
  next launch has read the exit record: not a crash, unless that record
  carries an ANR trace (below, "ANRs").

### SDK: report at the next launch

`Core/CrashReporter.cs`, on a background thread (`OzxApm.CrashReport`)
once technical consent is granted, for the journals of earlier processes
(the newest 5; a crash loop's older ones are deleted unreported):

- Android 11+ (API 30): `ActivityManager.getHistoricalProcessExitReasons`
  of the package (16 most recent) is matched by pid (and time). With the
  exit's trace (`getTraceInputStream`: the ANR trace on 11+, the native
  crash's tombstone on 12+, protobuf), copied to a file with
  `android.os.FileUtils.copy` and reduced on the device (`Utils/CrashTraceParser.cs`)
  to the signal, code, fault address, abort message, the "Cause:" line,
  and the stacks of the crashed thread (an ANR: main, which is blocked),
  UnityMain (where the C# code runs) and main, at most 64 frames each and
  16,000 characters. Registers, memory dumps, memory maps, open files,
  other threads and the log buffers (logcat may hold anything) never leave
  the device; the app's library paths (random install directories) are
  reduced to the file name (`libil2cpp.so`), system paths are kept.
- Classification (`crash_type`): `CRASH_NATIVE` -> `native`, `ANR` ->
  `anr`, `CRASH` -> `java`, always; `LOW_MEMORY` -> `oom` and the other
  kills (`SIGNALED`, `EXCESSIVE_RESOURCE_USAGE`, `USER_REQUESTED`, `FREEZER`,
  `OTHER`, ...) -> `killed` only in the foreground (the journal says not
  paused, or Android's importance is foreground or visible); background
  kills, `EXIT_SELF`, permission and package changes are not crashes.
  Without an exit record (Android 10 and older, or Android kept none) a
  journal that says foreground is reported as `unknown`.
- **A native crash recorded as a Java crash (race on Android 16).** On the
  A33 (Android 16, One UI), ForceCrash(AccessViolation) 5 times: 3 exits
  were `CRASH_NATIVE` (status 11, protobuf tombstone), 2 were `CRASH`
  ("APP CRASH(EXCEPTION)", status 0, **no trace**), with no Java exception
  at all. Logcat of such a run: Unity's handler logs its backtrace (tag
  `CRASH`) and writes `<persistentDataPath>/tombstone_01`, forwards the
  signal; debuggerd writes the system tombstone and tells ActivityManager
  (`ActivityManager: crash : com.ozx.apmtest`), which shows the crash
  dialog and kills the process (`Killing ... crash`, 546 ms after Unity's
  timestamp) before the signal ends it, so the exit is `CRASH`. Before
  this change the SDK sent those as `java` "Java crash: crash" without a
  stack: a second group. Now, when an exit has no trace (`CRASH`;
  `CRASH_NATIVE` without a tombstone, Android 11; `SIGNALED`, `UNKNOWN`,
  `OTHER`; no exit record at all), the report thread looks at Unity's own
  tombstone files (`tombstone_` + 1-4 digits in `persistentDataPath`;
  Unity rotates `tombstone_00`..`_02`, ~390 KB each, mode 600):
  - only files modified after the process started are opened, at most 8,
    each read up to 64 KB and parsed up to the end of the backtrace
    (header: pid, `Timestamp:`, signal; then the backtrace). Registers,
    the memory map and everything after the backtrace are never kept or
    sent; install paths are reduced like the system tombstone's;
  - it must be the journal's pid and its `Timestamp` must be after the
    process start (an older process with the same pid: rejected) and
    between 60 s before and 5 s after the exit (no exit record: up to 60 s
    after the journal's last write; the journal is rewritten every 5 s);
  - a match is reported as `native` (signal, code, the crashed thread's
    stack; `exit_reason` stays Android's `CRASH`), `details.detected_by`
    `exit_info+unity_tombstone` (or `journal+unity_tombstone`),
    `details.trace` `unity`. Its fingerprint equals the `CRASH_NATIVE` runs'
    (tested with the stacks of this run's logcat: Unity's 16 frames, the
    system tombstone's 31 + main), with and without symbols. For that the
    server no longer counts the app's ART-compiled Java code (`base.vdex`,
    `.odex`, `.oat`, ...) as in-app native frames (it made the longer
    system stack's third frame key differ); groups of native crashes whose
    first three distinct in-app keys included such a frame get a new
    fingerprint once (APM-7 is not released yet);
  - Unity's files are only read, never changed or deleted (they are
    Unity's; Unity overwrites them in rotation). Withdrawn or denied
    consent: nothing is read (the report does not run). A real Java crash
    (uncaught exception: no Unity tombstone of that pid) stays `java`
    without a stack: Android keeps no trace for `CRASH`, and catching it in
    the process would need a Java `UncaughtExceptionHandler` (a Java
    plugin or an `AndroidJavaProxy` running C# in the dying process): not
    done.
- The event keeps the crashed process's fields (session, version, scene,
  device, build type, context, breadcrumbs), the exit's time, Android's
  reason, status, description, importance and PSS/RSS, the ABI, the time
  into the session, and `details` (`detected_by` exit_info/journal,
  `journal_age_ms`, `uptime_ms`, `trace` proto/text/anr/none, `cause`, the
  last fps / frame_ms / frame_ms_max / mem_mb / gc_count). Its `event_id` is
  derived from the device id, pid and process start: a re-report (the app
  died again before the event was on disk) is a duplicate the server drops.
- The journals are deleted once the events are on disk (3 s wait on the
  storage worker); otherwise they are reported again at the next launch.
- JNI: at `Initialize`, on the main thread, the application context, the
  API level and the pid (four JNI calls); the exit records on the report
  thread (attached to the JVM while it runs). Every call is guarded: a
  failure means "no exit record" (the journal is then reported as
  `unknown` if it was in the foreground), never an error in the game.
- Consent (APM-14): no journal without technical consent; withdrawing it
  (Undecided) stops and deletes this process's journal and keeps those of
  earlier processes (collected with consent; reported once granted again);
  Denied deletes the directory, also at `Initialize`;
  `ResetDeviceId` deletes the journals of earlier processes (they carry the
  old id).
- iOS and the Editor: off (`GetStats().CrashReporting` false).
- Deletions of the directory (Denied, a reset before `Initialize`) and of
  earlier journals (`ResetDeviceId`) are synchronous (a few small files): a
  deferred delete raced the new journal and the report. The report also
  deletes, unreported, a journal whose device id is not the current one,
  and deletes every copied trace before it ends.

### SDK: ANRs (also those the process survives)

Device run (A33, Android 16, One UI): the UI thread blocked 25 s and 40 s
with taps. Android detected the ANR (`WindowManager: ANR in Window{...
UnityPlayerActivity}. Reason:Input dispatching timed out`, `ActivityManager:
anr : com.ozx.apmtest`, stacks dumped) but showed **no dialog and did not
kill the process**. The game later quit (`Application.Quit`: Unity's own
SIGKILL), and Android recorded that exit as `SIGNALED` (status 9,
importance 400) **with the ANR trace attached**
(`/data/system/procexitstore/anr_2026-10-19-21-44-39-650.gz`). The SDK
reported nothing: the journal was deleted at the clean quit, and a
background `SIGNALED` exit is not a crash. Play Console counts such ANRs.
Now:

- **The trace of any exit.** `getTraceInputStream` is tried for the exit
  record of every journal's pid, whatever the reason. Android's
  documentation: "typically it'll be available when the reason is
  REASON_ANR, though if the process gets an ANR but still continues running
  and it's killed for some other reasons later, the trace will be available
  too". Android 16's `SIGNALED` record above carries it; returning it for a
  non-ANR reason on the device is to be verified in the device run.
  An ANR trace (recognized by its header: `Subject:`, `----- pid N at ...`,
  thread dumps) is reported as `anr`: main and UnityMain threads as before,
  **time = the dump's time** from `----- pid 4796 at 2026-10-19
  21:44:39.650209283+0800 -----` (else the exit's), `cause` = the `Subject:`
  line (300 characters), `details.anr_trace_on` = the exit's reason
  (`SIGNALED`), `exit_reason` the exit's. Independently, the exit itself is
  classified without the trace: a clean or background end is not a crash
  (the device case: one event, the ANR); a foreground kill is a second event
  (`killed`, its own id).
- **Clean quit keeps the journal.** At `Shutdown`/`OnApplicationQuit` the
  journal is written once more with `clean=1` (one small file write on the
  main thread, at quit only) and kept instead of deleted. The next launch
  reads it like any other: an ANR trace on its exit is reported with that
  session's fields (session, version, scene, context, breadcrumbs); without
  one (or without an exit record) a clean journal is deleted unreported,
  whatever Android recorded (`quit=1` ends as `SIGNALED` 9 importance 400 on
  the A33: still not a crash). Consent as before: withdrawn/Denied deletes
  this process's journal, not marked clean.
- **Live: the ANR watchdog** (`Core/AnrWatchdog.cs`,
  `Utils/AndroidMainThreadProbe.cs`; `ApmConfig.EnableAnrWatchdog`, on by
  default with crash reporting; Android only). Most ANRs do not end the
  process, so waiting for the next launch reports them late or never. A
  thread (`OzxApm.AnrWatchdog`) checks the Java main thread once a second, in
  the foreground only, while the journal runs (technical consent): it
  queues an empty marker message (`Handler.sendEmptyMessage(What)` on a
  `Handler` of the main looper without a callback) and looks whether it is
  still queued (`hasMessages`); raw JNI with method ids and arguments made
  once, **no C# on the main thread, no managed allocation per check**
  (measured 0 B over 1000 checks), a pooled `Message` on the Java side. If the
  marker waits more than 5 s, and only then, it polls
  `ActivityManager.getProcessesInErrorState()` once a second (up to 30 s):
  when Android has this pid `NOT_RESPONDING`, the ANR is reported at once as
  `anr`, `details.detected_by=watchdog`, `trace=watchdog`, `cause` = Android's
  short message, the stack of the Java main thread
  (`Looper.getMainLooper().getThread().getStackTrace()`, 64 frames), the
  session, context and breadcrumbs of the moment, once per block, at most 3
  per process. A slow main thread that Android does not declare (no input
  waiting) is not reported. Its event id is the one the exit record's trace
  of the same ANR gets at the next launch (also written to the journal:
  `anr_id`, `anr_ms`; a trace within 2 minutes of it keeps that id **and
  the live report's timestamp**), so the server keeps one event. UnityMain's
  stack is not in the live report (native, not obtainable cheaply from
  Java); the exit trace has it. Verified on the A33 (round 2): One UI sets
  `NOT_RESPONDING` without a dialog, the watchdog reported 4 s after
  Android's `ANR in`.
- **One ANR, one row, one group** (device round 2 stored the same event id
  twice in two groups). Three causes, three fixes:
  - the duplicate lookup searched only the incoming events' timestamp
    range, and the trace's event had the dump time (4 s before the live
    one): crashes are now looked up 7 days around their timestamps (by
    event id, app version; the event_id bloom index; crashes are few), and
    the SDK gives the trace's report the live report's timestamp too
    (`apm_crashes` is a ReplacingMergeTree ordered by app_version,
    fingerprint, timestamp, event_id);
  - **the first report wins**: the live one (watchdog, main thread's Java
    stack) is stored, the exit trace's richer copy (UnityMain) is dropped as
    a duplicate. Replacing it would need a delete or a version column; not
    worth it for the UnityMain stack, which Android cannot connect to the
    block anyway (UnityMain was waiting for vsync on the device);
  - the fingerprint used UnityMain (only in the trace) and counted
    `$Proxy4.run` as in-app (live) but not `java.lang.Runnable.run`
    (trace). An ANR is now keyed by the **main (blocked) thread's Java
    frames only**: up to 3 in-app methods (file names and line numbers never
    count: "Unknown Source" / "unavailable" / "Proxy.java:1006"), skipping
    proxies (`$ProxyN`, `java.lang.reflect.Proxy`) and the framework and
    Unity player packages; native frames are not in the key (the live
    report has none). Without an in-app frame (the device case: the driver
    blocks the UI thread through a C# `AndroidJavaProxy`, all frames are
    Unity's reflection proxy), the key is the ANR's kind from its cause:
    "Input dispatching timed out" (Android's message up to its first
    `(`/`{`/`:`, without "ANR" / "user request after error:" / "bg anr:"),
    so all such ANRs of a version are one group. UnityMain stays in the
    stack shown.
  - Summary: "ANR: <kind> in <frame>", the frame being the first in-app
    method, else the first frame of the blocked thread that is not a wait
    (`Thread.sleep`, `Object.wait`, `Unsafe.park`, looper, reflection):
    "ANR: Input dispatching timed out in
    com.unity3d.player.ReflectionHelper.nativeProxyInvoke" (a C# call through
    an `AndroidJavaProxy` blocked the UI thread). The same for both sources.
  - Tests: the device's two stacks (fingerprint test), and an integration
    test that ingests both shapes with one event id (one row, one group,
    count 1; a second ANR of the same kind joins the group).
- **UnityMain frozen, UI thread fine** (`anr=20` with taps on the A33):
  Android raises no ANR, because input is dispatched to the Java UI thread,
  which stays responsive, so neither path reports it, and Play Console does
  not count it either. The player sees a frozen game. Recommendation, a
  follow-up ticket: a UnityMain watchdog ("hang" event, its own type, not
  `anr`): the journal thread already wakes every 5 s; a frame counter that
  `ApmClient.Update` increments (no allocation) and that has not moved for
  N seconds in the foreground (e.g. 5 s, configurable) would mark a hang,
  reported with the time line and, on Android, UnityMain's Java-visible state
  plus a native stack only if obtainable without signals (not cheaply today:
  so the stack would be missing). Not implemented in APM-7.
- Managed exceptions that do not end the process (an exception on a new
  thread, ForceCrash `MonoAbort`): IL2CPP keeps running, so they are not
  crashes; they are APM-3 exception events (device run: nothing in crashes,
  as expected).

### SDK: link.xml

`Runtime/link.xml` preserves the `OzxApm` assembly under managed code
stripping; because not every Unity version reads link.xml inside packages,
`Editor/LinkXmlProvider.cs` (`IUnityLinkerProcessor`, assembly
`OzxApm.Editor`) hands the same content to the linker from
`Temp/OzxApm/link.xml`. The package now depends on the built-in module
`com.unity.modules.androidjni`.

### SDK: cost

Unity's Mono outside Unity (M-series Mac), `/tmp/claude-501/apm7/bench`:

| Path | Thread | Time | Allocated |
|---|---|---|---|
| `NotifyChanged` (every recording call) | any | 8-13 ns | 0 |
| Warning repeated (time line + journal flag) | logging thread | 0.12-0.18 us | 0 |
| Journal write: 30 context keys, 20 breadcrumbs, 10 log lines with URLs, 3.7 KB | journal thread | 21-27 us | 0 |
| Report at launch (journal read, exit records, trace parse) | report thread | once per launch | a few KB to MB (the trace) |
| ANR watchdog check (marker posted / still queued?) | watchdog thread | 1-2 JNI calls per second, foreground only | 0 |
| ANR watchdog during a block of more than 5 s | watchdog thread | `getProcessesInErrorState` per second, 30 s at most | a few KB per poll |

At most 10 writes a second while something changes every frame (warning
spam), i.e. ~0.3 ms/s on the journal thread here; idle: one write per 5 s.
`ApmClient.Update` gains nothing. The main thread pays, once at
`Initialize`, four JNI calls; on pause/resume a flag.

### Wire format (SDK -> server)

```json
{"type":"crash", <common fields of the crashed session>, "crash_type":"native",
 "fingerprint":"896fbc8aa4063a37",
 "stack":"Thread \"UnityMain\" tid=12380 crashed\n#00 pc 00000000005417bc libil2cpp.so (BuildId: 0d65...)\n#01 pc 000000000004e0dc /apex/com.android.runtime/lib64/bionic/libc.so (abort+164) (BuildId: 2c4f)\n...",
 "breadcrumbs":["[10:00:01.000] Event: room_enter roomId=r-12", "[10:00:01.200] Warning: Asset not found: anim/x"],
 "exit_reason":"CRASH_NATIVE", "exit_status":11, "exit_description":"crash", "importance":100,
 "pss_kb":512000, "rss_kb":600000, "signal":"SIGSEGV", "signal_code":"SEGV_MAPERR",
 "fault_addr":"0x0000000000000008", "abi":"arm64-v8a", "session_ms":34000, "foreground":true,
 "details":{"detected_by":"exit_info","trace":"proto","fps":"58.9"}, "ctx":{"roomId":"r-12"}}
```

`server/internal/processor/testdata/sdk_events_apm7.jsonl` are four such
events (native, ANR, OOM, unknown) built by the SDK's CrashReporter and
serialized on Unity's Mono; the ingest test stores them.

### Server: ingest and the crash fingerprint

- `processor.NormalizeCrash` bounds every field (stack 32,000 characters,
  50 breadcrumbs of 512, description 1,000, abort message 2,000,
  identifiers 32 printable characters, numbers clamped, an unknown
  `crash_type` stored as `unknown`); `details` follow the game context's
  rules (counted in the response's errors when dropped).
- `fingerprint.ParseCrashStack` reads the SDK's form and Android's text
  forms (tombstone, ANR trace), bounded (8 threads x 256 frames of 64 KB).
- Crash fingerprint, version 1 (`internal/fingerprint/crash.go`): native:
  signal + the first 3 distinct in-app frames of the crashed thread; ANR:
  the main thread's in-app Java frames, else the ANR's kind (below); Java: the
  template of the description; kills: the exit reason. A frame is keyed by
  `module!function` when uploaded symbols (or the device) name it, else by
  `module!?` (its library, **never its address**): with symbols uploaded
  before the crash arrives (the CI step), the same bug is one group across
  builds; without, crashes nobody can name group per (type, signal, named
  frames such as libc's `abort`, libraries): coarser, but stable across
  builds. `crash_site` and the summary still show the address
  (`libil2cpp.so+0x5417bc`); the list and detail symbolicate it when read.
  A fault address of dashes (Unity's tombstone: `fault addr --------`) is
  left out of the summary.
  C# names are normalized like exception frames (`Type:Method` ->
  `Type.Method`, generic arguments dropped); a C++ name keeps its `::`
  (device FailFast: the summary showed `il2cpp..os..CrashHelpers..CrashImpl`,
  now `SIGTRAP in il2cpp::os::CrashHelpers::CrashImpl (libil2cpp.so)`). The
  summary is text in React (escaped), never HTML. The app's ART-compiled
  Java code (`base.vdex`, `.odex`, `.oat`) is not an in-app native frame.
  Crashes stored before their symbols were uploaded stay in that coarse
  group; later ones go to their method's group. The lookups at ingest have
  a 3 s budget per batch; after a timeout or error the batch's remaining
  crashes are keyed like unnamed frames and stored (one warning).
- **Interplay with APM-8 alerting.** The evaluator sends a "new crash"
  alert for every crash fingerprint it has not seen, and regressions of
  resolved ones. Keyed by address (the first design), every build without
  uploaded symbols would have turned every recurring native crash into a
  new fingerprint and a new alert, at each release. With unnamed frames
  keyed by library: a new build without symbols makes no new crash
  fingerprints (unit tests: three builds of one crash, ingest of two
  builds); what remains is, once, when symbols start being uploaded for a
  game that had none: crashes move from the coarse groups to one group per
  method, each a "new crash" alert, which is real news (the bugs become
  distinguishable). Avoid it by uploading symbols from the first release
  with this SDK. The cost of the coarse group: while symbols are missing,
  different native bugs with the same signal and libraries share a group,
  so resolving it and seeing it again (a regression) may be another bug;
  look at the detail's latest stack.
- **Crash rate (APM-8).** Crash events carry the crashed session's
  `session_id`, `app_version` and `build_type` (the journal's: `release`
  on a release build), and the time of the crash, so the evaluator's
  crashed sessions per version count them like any crash. A crash reaches
  the server only at the next launch: one reported more than the rate
  window (1 h by default) after it happened is not in the spike rule's
  window (it still counts in the 24 h baseline that follows). New-issue
  alerts are not affected (the evaluator's 6-hourly sweep covers late
  uploads).
- Client fingerprints: the server computes the fingerprint of every crash
  it can group, i.e. one with a parsable frame or any APM-7 field
  (`exit_reason`, `signal`, `abi`, `abort_message`, `exit_description`,
  `details`, `foreground`, `session_ms`): the client's is kept as
  `client_fingerprint`. A crash with neither (a producer other than the
  APM-7 SDK, a hand-written event, the API's older tests) keeps the
  fingerprint it was sent with, `fingerprint_version` 0: the server's would
  put every such crash of a type into one group. No SDK sent crash events
  before APM-7, so the only crash rows that exist (e.g. on n192) came from
  such producers; they keep their stored fingerprints either way (nothing
  is rewritten), and so do the APM-8 issues and the alert baseline built
  on them. A producer that wants the server's grouping sends a stack in
  the normalized form or the APM-7 fields.
- The ingest symbolizer remembers for a minute that a build has no
  symbols: crashes ingested within a minute after an upload may still be
  keyed as unnamed (they stay in that group; the list and detail name them).
- Duplicates (APM-19): a re-sent crash is looked up by `event_id` without
  the fingerprint filter (`EventIDLookup.AnyKey`), because its fingerprint
  can differ from the stored copy's when symbols were uploaded in between;
  crashes are few, the version and time filters and the event_id index
  bound the read. The ReplacingMergeTree safety net only collapses copies
  with the same fingerprint, so a duplicate that slips past the lookup
  (lookup failed) and has another fingerprint stays.
- `summary` (e.g. `SIGSEGV (SEGV_MAPERR) at 0x8 in Game.Rooms.RoomLoader.LoadTemplate (libil2cpp.so)`,
  `ANR: Input dispatching timed out ...`, `Killed for low memory in the foreground (PSS 812 MB)`)
  and `crash_site` are computed at ingest; the session time line shows the summary.

### Symbols: what OZX uploads at build time

Unity settings (2022.3, Android; in the build script or Player Settings):

| Setting | What it gives | Without it |
|---|---|---|
| `EditorUserBuildSettings.androidCreateSymbols = AndroidCreateSymbols.Debugging` ("Create symbols.zip": Debugging) | `<product>-<version>-v<code>-IL2CPP.symbols.zip` next to the APK/AAB, with `<abi>/libil2cpp.so` (symbol table and DWARF line tables; 92 MB on the test app), `libunity.so`, `libmain.so`: named `.so`, not `.sym.so`/`.dbg.so` | `Public`: symbol tables only (names, no lines). Copies also under `Library/Bee/artifacts/Android/il2cppOutput/build/<abi>/` (`.sym.so`, `.dbg.so`) |
| `PlayerSettings.SetAdditionalIl2CppArgs("--emit-source-mapping --emit-method-map")` | `LineNumberMappings.json` (C++ line -> C# file:line) and `MethodMap.tsv` (C function -> C# method). The place for CI: `<build>_BackUpThisFolder_ButDontShipItWithYourGame/il2cppOutput/Symbols/` next to the APK (copies also under `Library/Bee`) | no C# files/lines; C# names guessed from IL2CPP's function names (`Type_Method_m<hash>` -> `Type.Method`; wrong split for nested types and types with `_`) |

**Clean player build when the SDK version changes.** A release build of
the test app (IL2CPP, Medium stripping, Strip Engine Code) made
incrementally after changing the SDK logged `Could not produce class with
ID 115` (MonoScript) on every launch, 118 times, from
`AddComponent<ApmClient>()`: the stale build cache (`Library/Bee`) had
regenerated the engine class registration without `MonoScript`. The same
SDK built clean (`Library/Bee`, `Library/PlayerDataCache`,
`Library/Il2cppBuildCache` deleted) logged none. In CI, build the player
clean when the SDK version changes (delete those folders, or build in a
fresh checkout). To check a build:
`Library/Bee/artifacts/Android/il2cppOutput/UnityClassRegistration.cpp`
must contain `RegisterUnityClass<MonoScript>`, and logcat must show no
`class with ID 115`.

`--emit-source-mapping` is what Unity passes itself when Cloud
Diagnostics crash reporting is on; the source-mapping comments only change
the generated C++, not the binary. Both arguments are accepted by il2cpp in
Unity 2022.3.62 (verified on the test app's release build, below).

**Managed exceptions (APM-11 page; OZX-1277) on Unity 2022.3: method names
only, no file:line.** Unity's `Il2CppStacktraceInformation`
("IL2CPP Stacktrace Information: Method Name, File Name, and Line Number")
exists from Unity 2023.1; 2022.3 has no such setting (the build fails with
CS0117/CS0103). This release cannot add file:line to managed exception
stacks: such a stack is the text Unity formats (`Type:Method ()`), with no
native address to look up, so the uploaded symbols and line map only
serve native crash frames. Possible later, not in APM-7: the SDK capturing
the native return addresses of the throwing thread (IL2CPP's internal
stack walk is not a public API), or upgrading OZX to Unity 2023.1+ / Unity 6,
where the setting gives file:line on the device without the server.

The upload (CI, after the build; `server/cmd/apmsym`, plain Go, no CGO):

```bash
cd server && go build -o /usr/local/bin/apmsym ./cmd/apmsym
export OZX_SYMBOLS_UPLOAD_TOKEN=...      # the admin server's token (secret store)
apmsym upload -url https://<admin-host> -app-version "$VERSION" -app-build "$VERSION_CODE" \
  "$BUILD_DIR"/*.symbols.zip "$BUILD_DIR"/*_BackUpThisFolder_ButDontShipItWithYourGame/il2cppOutput
```

- It reads every ELF library under the paths (zips are extracted to a
  temporary directory; entry names reduced to their base name), keeps per
  ELF build id the richest file (line table, then DWARF, then most
  functions), reads `MethodMap.tsv` / `LineNumberMappings.json` when found,
  and builds per library: the functions (start, size, display name: the C#
  method from the method map, else the IL2CPP guess, else the demangled
  C++ name; and the raw symbol) and, from the DWARF line tables mapped
  through the line map, the addresses where the C# file:line changes
  (closed by an empty row; only within a function, never across). C# paths
  are cut to `Assets/...`, `Packages/...`, `Library/PackageCache/...` (the
  build machine's directories are not uploaded); a package referenced from
  outside the project (`"file:../sdk"` in `manifest.json`, as the test app
  references the OZX SDK) gets `Packages/<name from its package.json>/...`
  (the tool runs on the build machine and finds the package.json next to
  the sources), other paths keep their file name.
- **Inlined code** (fixed after device run S1): the DWARF line rows of code
  clang inlined into a function are the lines of the inlined function's
  body, which in IL2CPP's output is elsewhere in the same .cpp, under
  another C# method's `//<source_info>` comment. On S1 the frame
  `ApmTestDriver.UpdateApm7` (its `a?.Invoke()`: `Action_Invoke_..._inline`
  inlined) showed `SessionManager.cs:30`, a getter of the SDK. The tool
  now reads the outermost `DW_TAG_inlined_subroutine` ranges of every
  function and gives their addresses the call site in the function itself
  (`DW_AT_call_file`/`DW_AT_call_line`), splitting a row's range where an
  inlined call ends; a frame named after a function always gets a line of
  that function (no inline frames are emitted). DWARF line 0 (code the
  compiler attributes to no line) now shows no file:line instead of the
  previous statement's. Checked on the test app's release build against
  llvm-symbolizer (its outermost frame's line through the same line map),
  22,960 addresses in C# methods: equal on all of them (1,824 with a line,
  21,136 without on both sides); the extraction before this change
  differed on 1,127 (119 another line, 493 a line where there is none,
  515 none where there is one). Extraction of that build: 1.3 s.
  **Symbols uploaded before this change must be uploaded again** (the
  server only reads the stored rows; the newest upload of a build id wins,
  within a minute because of the upload cache); the server is unchanged.
- **Missing frames: tail calls.** A method whose last statement is a call
  (`Level1() { Level2(); }`, a lambda `() => Level1()`) is compiled to a
  jump, not a call, even with `[MethodImpl(NoInlining)]`: it leaves no
  return address, so the native stack (and the admin) skips it. On S1 the
  stack was `Level3 <- UpdateApm7 <- Update`: the lambda, `Level1` and
  `Level2` tail-called and are not in it. Nothing on the server can
  restore them; the line shown for the caller is the call it made
  (`ApmTestDriver.cs:1088`). Measured on the test app
  (89 MB `libil2cpp.dbg.so`, 29,611 functions, 843k DWARF rows, synthetic
  line map): 0.26 s.
- `libunity.so` / `libmain.so` in the zip are uploaded too (engine frames
  get C++ names). `-modules libil2cpp.so` restricts it.
- The test app's release build (Unity 2022.3.62f3, arm64, by the lead):
  `apmsym extract <zip> <BackUpThisFolder>/il2cppOutput` in 0.8 s:
  libil2cpp build id `96f86f45...`, 30,802 functions (11,614 C# methods,
  19,074 named from the method map), 22,203 line rows (28,935 of 870,397
  DWARF rows mapped to C#); libunity 27,460 functions; libmain 9. With the
  inlined-code attribution below: 14,304 line rows (37,170 rows mapped to
  C#; 392,593 rows in 150,092 inlined calls).
- Upload: parts of at most 900 KB gzip (below nginx's 1 MB default) with a
  random upload id, then a commit with the counts; retried 4 times. The
  server keeps one row per (upload, address) (a part sent twice counts
  once) and refuses a commit whose stored counts differ (409): only
  committed uploads are used. Stateless: works with several admin replicas
  and across both apps.
- `apmsym extract <paths>` shows what would be uploaded; `apmsym lookup
  <paths> <hex address>...` resolves addresses through the server's own
  resolver (the same file:line the admin will show; callers at pc-1,
  `-frame0` for a faulting first frame). The S1 frames on the test app's
  release build:

  ```
  0x5726b8  CrashChain.Level3(System.Int32)+0x90           Assets/Scripts/ApmTestDriver.cs:1559
  0x56a030  ApmTestDriver.UpdateApm7(System.Single)+0x38c  Assets/Scripts/ApmTestDriver.cs:1088  (was SessionManager.cs:30)
  0x56542c  ApmTestDriver.Update()+0x684                   Assets/Scripts/ApmTestDriver.cs:347
  ```

### Server: storage, symbolication, admin API

Schema (`schema.go`; `scripts/migrate.sql` regenerated):

```sql
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
CREATE TABLE IF NOT EXISTS apm_symbol_files (...)  -- one row per committed upload
CREATE TABLE IF NOT EXISTS apm_symbol_funcs (...)  -- build_id, upload_id, addr, size, name, raw_name
CREATE TABLE IF NOT EXISTS apm_symbol_lines (...)  -- build_id, upload_id, addr, file, line
```

- Columns: metadata only, idempotent, within the `apm` account's `ALTER
  ADD COLUMN`; fresh and migrated tables have the same columns in the same
  order. `fingerprint_version` 0 marks rows stored before (the SDK sent
  none; their fingerprint is whatever a client sent).
- Symbol tables: `CREATE TABLE IF NOT EXISTS` (the `apm` account may),
  ReplacingMergeTree `ORDER BY (build_id, upload_id, addr)`, partitioned by
  month of upload, **TTL 400 days after the upload** (a build crashes for as
  long as players run it, longer than crash rows live; uploading again
  starts a new period). No index, no view, no MODIFY: nothing for an
  admin to do. A refused CREATE is one warning per table at start
  (`could not create a symbol table`); only symbolication depends on them.
  No `device_id`: privacy deletion (APM-14) never touches them (tested).
- Symbolication when a crash is read (`internal/symbols.Symbolizer`, used
  by ingest for the fingerprint and by the admin API for display): per
  frame of the app's libraries (in-app native frames of the first 3
  threads, 64 each; the crash list only the top frame of each group), the
  newest committed upload of the frame's build id (or, for a frame without
  one, of the crash's version and ABI, marked "by version"; never without
  a known ABI), then the function at or before the address and the line row
  at or before it within that function. A caller frame's return address is
  looked up minus one (the call, not the next statement). One query per
  table per batch of 128 addresses: a UNION ALL of `ORDER BY addr DESC
  LIMIT 1` reads through the sort key (a granule or two each). Cached:
  resolved addresses per upload (LRU, 50,000 admin / 20,000 ingest) and the
  upload of a build id for 1 minute (a new upload is used within a minute;
  a build without symbols is not looked up again for a minute).
- Inlined code: a frame is named by the function the address is in; with
  IL2CPP's inlining its file:line can be inside a method that function
  inlined (the line table's location).

Admin API (both apps run it):

- `POST /api/symbols/part`, `POST /api/symbols/commit`: `Authorization:
  Bearer <token>`, compared in constant time with `admin_server.symbols_token`,
  environment **`OZX_SYMBOLS_UPLOAD_TOKEN`**; without it both answer 403
  (`symbol upload disabled: no token configured`, logged once at start); a
  missing or wrong token 401, logged with path and remote address, never
  the token. Body at most 1 MB compressed and 16 MB decompressed (413),
  100,000 rows per part, build ids 8-80 hex digits, upload ids 32, module a
  file name (no `/`), arch arm64/arm/x86/x86_64, names cut to 1024 bytes and
  files to 256, control characters and invalid UTF-8 replaced, addresses
  below 2^48; at most two parts inserted at once (ClickHouse connections
  are shared with ingest). Storage errors answer 503 (the tool retries).
  Set the token like the privacy token (APM-14): a secret environment
  variable on apps 36 and 38, `openssl rand -hex 32`, redeploy; the CI job
  gets the same value.
- `GET /api/symbols?build_id=&app_version=&module=`: committed uploads
  (no token, read-only like the rest of the admin API).
- `GET /api/crashes`: per group also `exit_reason`, `signal`, `crash_site`,
  `top_frame` (the latest crash's first in-app frame symbolicated now, e.g.
  `Game.Rooms.RoomLoader.LoadTemplate(System.Int32) (Assets/Scripts/Rooms/RoomLoader.cs:71)`),
  `symbolicated`, `foreground` (crashes in the foreground); `sample_message`
  is the latest summary; type and stack are the latest crash's (were `any`).
  The groups query takes the latest crash's `event_id`; the start of its
  stack (4,000 characters) is read for the page's groups only (`LIMIT 1 BY
  event_id`), not for every row.
- `GET /api/crashes/detail`: adds `sample` (the latest crash: every field
  above, `threads` with frames `{index, native, pc, module, build_id,
  function, offset, file, line, in_app, symbolicated, symbols_by, raw}`),
  `symbols` (per app library of the sample: build id, uploaded or not,
  with C# lines or not), `fingerprint_version`; occurrences (20) add
  session id, device id, summary and the exit fields.
- Session time line: a crash item's message is its summary.

Admin UI: Crashes list (summary, symbolicated top frame, foreground
count) opens a detail page (`/crashes/detail?fingerprint=F`): how the
process ended, the stack per thread (C# method, file:line; libraries
without symbols say so), the context, scene and breadcrumbs before the
crash, links to the session time line at the crash and to the device's
sessions, distributions, recent crashes. The exception detail page says
how to get file and line when an IL2CPP stack has none.

### Rolling deploy and rollback

Old and new binaries serve side by side: the old binary names its insert
columns (its rows read the defaults), ignores the symbol tables and has no
symbol endpoints (the tool gets 404/405: deploy the server first). A crash
stored by the old binary has the SDK's fingerprint (version 0), one stored
by the new one the server's: the same crash can be two groups during the
deploy (and, with APM-8 alerting on, two "new crash" alerts). No SDK sent
crash events before APM-7 and the server is deployed before the SDK, so in
practice no crash row comes from the old binary. **Rollback:** redeploy the previous binary; no schema action; the
columns and symbol tables stay (unused; symbols expire by TTL). The SDK
with APM-7 against an older server: crash events are stored with type,
fingerprint, stack and breadcrumbs; the rest is ignored. To roll back the
SDK, ship a build without it; journals left on devices are never read
(and stay in `ozx_apm_crash/`, a few KB) until a build with the SDK runs
again. **SDK rollback warning:** none beyond APM-14's.

To remove the tables after a permanent rollback (admin):
`DROP TABLE apm_symbol_files; DROP TABLE apm_symbol_funcs; DROP TABLE apm_symbol_lines;`

### Verified

- Server: `go vet`, `go test -race ./...` (unit): the crash stack parser (SDK,
  tombstone and ANR forms, hostile input), crash fingerprint (the same
  methods at other addresses in two builds are one group; without symbols
  too, never by address; partly named stacks; ANR by UnityMain; kills by
  reason), normalization,
  summaries, ingest of SDK-serialized crash events (fixture from the SDK on
  Unity's Mono) with and without symbols and with a failing symbol store,
  the symbols package (ELF symbol tables and build ids for arm64 and Thumb,
  DWARF line tables mapped through a LineNumberMappings.json in IL2CPP's
  format onto C# lines, function boundaries, method map names, IL2CPP name
  guess, demangling, upload validation), the symbolizer (return addresses,
  line ranges, caches, TTL, batching, fallback by version and ABI), the
  upload endpoints (auth 401/403, token never logged, gzip bomb and body
  limits 413, invalid rows 400, a part sent twice, early commit 409,
  storage 503), the admin symbolication of a stack (and `<script>` in frame
  text kept as text), config. `testdata/` has a library built like IL2CPP
  output with the NDK (`gen.sh`).
- `apmsym extract/lookup` on the test app's real `libil2cpp.sym.so` /
  `.dbg.so` (Unity 2022.3.62, arm64): build id `0d65a2d2...` equal in the
  shipped, `.sym` and `.dbg` files, 29,611 functions, 11,283 C# methods,
  `0x5417c0` -> `ApmTestDriver.Apply+0x4`.
- Admin UI: `tsc`, vitest (134 tests; new: the crash detail page with the
  symbolicated stack, file:line, exit info, symbol status, context and
  breadcrumbs, encoded links, `<script>`/`<img onerror>` in game text
  rendered as text, an older server's response, a kill without stack, 404;
  the list's top frame and row link; the exception page's file/line hint),
  `vite build`.
- A code review (another agent) found: deferred deletions racing the new
  journal and the report (a reset could still report old-id crashes; a
  Denied-then-Granted journal could be unlinked while written); re-sent
  crashes escaping the duplicate lookup when symbols changed their
  fingerprint; a symbol budget per crash instead of per batch; the crash
  list reading every row's stack and symbolicating whole stacks for one
  frame; the upload's concurrency slot taken after decoding 16 MB; copied
  traces left on disk when consent changed mid-report; the version
  fallback used without an ABI; the throttle not interruptible by Stop; a
  token length leak; the crash fingerprint sharing the exceptions'
  version constant. All fixed, with tests where they apply. Checked by it
  and found correct: tombstone.proto field numbers, LineNumberMappings
  parsing, ELF deduplication against the commit counts, zip extraction,
  upload auth and bounds, no HTML from game text, the JNI signatures, the
  journal's wake-up handshake and torn-write handling, the SQL on 23.8-26.1.
- Server unit tests: 453 passing test functions and subtests (`go test -short -v ./...`).
- SDK: compiles at C# 8 with and without `UNITY_2021_2_OR_NEWER`, for
  Android and iOS defines, and the editor assembly against Unity 2022.3's
  `UnityEditor.CoreModule`; on Unity's Mono outside Unity the new parser
  and journal/reporter tests pass (protobuf tombstone built field by field
  per tombstone.proto, text tombstone, ANR trace, hostile and truncated
  input, journal round trip with escaping, torn write and checksum, slot
  rotation, the writer thread's coalescing, the report's classification
  matrix, event fields, deterministic ids, journals kept until persisted,
  no report without consent, a journal of another device id, a crash loop;
  18 new tests), the previous fixtures still pass (198 pass in all; 2 fail
  for the environment as before, and `LinkXmlTests` cannot load the editor
  assembly outside Unity); the allocation numbers above. The test driver
  (`/tmp/claude-501/apm7/ApmTestDriver.cs`) compiles against this SDK.

### Not verified

- Anything on a device or in Unity: the EditMode tests that need the
  Unity runtime (`CrashReportingTests` through `ApmClient`, the allocation
  constraints, `LinkXmlTests`), JNI calls (`AndroidExitInfo`: exit records,
  `FileUtils.copy` of the trace, from the report thread), the tombstone of
  a real Android 12+ device (the parser follows tombstone.proto; the device
  plan checks it), Unity's own signal handler chaining to debuggerd (the
  tombstone and `CRASH_NATIVE` exist), whether blocking UnityMain produces
  an ANR on the test device, the low-memory kill's reason, the link.xml
  under High stripping, Unity 2020.3's `IUnityLinkerProcessor`.
- ClickHouse: the integration tests (`tests/integration/crash_symbols_test.go`:
  upload in parts, commit, 409, list; crashes before and after the upload,
  grouping, top frame, symbolicated detail with C# file:line, version
  fallback, time line summary, privacy tables; 300 addresses through the
  real queries) and the existing migration tests with the new columns and
  tables were compiled, **not run** (the sandbox cannot reach the local
  ClickHouse containers): run on 26.1, 24.1 and 23.8.
- The upload of a real build against the test environment (the build
  settings and `apmsym extract` were checked on a real build, above); cost
  of the symbol lookups at production volume.
- File and line of managed exception frames on Unity 2022.3: not
  available (above).
- iOS.
- After the device findings (frame lines, REASON_CRASH, ANRs): the Unity
  tombstone path on the device (the reader is tested with the content of
  its `CRASH` log lines; the file itself was not readable without root), the
  trace of a `SIGNALED` exit through `getTraceInputStream`, the watchdog's
  JNI calls and `NOT_RESPONDING` on One UI, the main thread's stack from
  `getStackTrace`, the journal kept at quit.

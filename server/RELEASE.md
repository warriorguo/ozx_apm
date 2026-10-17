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
- Not yet run on a device (A33 IL2CPP): see `/tmp/claude-501/apm6/device_test_plan.txt`
  (warning then NRE, room-switch p50/p95, timers across a pause, warning spam,
  budget, janks, consent, idle GC with metrics every frame, warning-spam cost).

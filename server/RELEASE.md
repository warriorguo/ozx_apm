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

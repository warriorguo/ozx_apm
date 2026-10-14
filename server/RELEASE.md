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

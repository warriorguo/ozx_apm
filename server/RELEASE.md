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

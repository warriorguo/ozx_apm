package handlers

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// Duplicate events (APM-19).
//
// Delivery is at least once: if the app is killed after the server stored a
// batch but before the SDK deleted it locally, the next launch sends the same
// events again. Before inserting, the handler drops events whose event_id
// already appeared earlier in the same batch or is already stored in the
// table. Dropped events count as accepted (the client must delete them), and
// are reported in IngestResponse.Duplicates.
//
// This is best effort: two requests carrying the same events at the same time
// both miss each other, and a failed or slow lookup inserts the events anyway
// (a missing event is worse than a duplicate). The ReplacingMergeTree tables
// (APM-19 phase B) remove what slips through on merge.
//
// Events without event_id (old SDKs) are never looked up or dropped.

var (
	// dedupLookupTimeout bounds one lookup query. On timeout the events are
	// inserted without de-duplication.
	dedupLookupTimeout = 5 * time.Second
	// dedupLookupChunk caps the event_ids per lookup query, which keeps the
	// query text well under ClickHouse's max_query_size (256 KiB by default).
	dedupLookupChunk = 500
)

// eventKey is what the lookup needs from an event: its id and the values of
// the table's sort-key prefix (app_version, key column, timestamp).
type eventKey struct {
	id         string
	appVersion string
	key        string
	ts         time.Time
}

type dedupResult struct {
	inBatch      int // repeated within this request
	stored       int // already in the table
	lookupFailed int // events inserted without a successful lookup
}

func (r dedupResult) total() int { return r.inBatch + r.stored }

// dedupe returns events without the duplicates. The input slice is not modified.
func dedupe[T any](ctx context.Context, store EventStore, logger *zap.Logger, table storage.EventTable,
	events []T, keyOf func(*T) eventKey) ([]T, dedupResult) {
	var res dedupResult
	if len(events) == 0 {
		return events, res
	}

	// 1. Within the batch: keep the first event of each id.
	kept := make([]T, 0, len(events))
	keys := make([]eventKey, 0, len(events))
	seen := make(map[string]struct{}, len(events))
	for i := range events {
		k := keyOf(&events[i])
		if k.id != "" {
			if _, dup := seen[k.id]; dup {
				res.inBatch++
				continue
			}
			seen[k.id] = struct{}{}
		}
		kept = append(kept, events[i])
		keys = append(keys, k)
	}

	// 2. Against the table, in chunks of ids.
	var withID []int // indexes into kept/keys of events that carry an id
	for i, k := range keys {
		if k.id != "" {
			withID = append(withID, i)
		}
	}
	if len(withID) == 0 {
		return kept, res
	}
	existing := make(map[string]struct{})
	for start := 0; start < len(withID); start += dedupLookupChunk {
		end := min(start+dedupLookupChunk, len(withID))
		q := lookupFor(table, keys, withID[start:end])
		lctx, cancel := context.WithTimeout(ctx, dedupLookupTimeout)
		found, err := store.ExistingEventIDs(lctx, q)
		cancel()
		if err != nil {
			res.lookupFailed += end - start
			logger.Warn("duplicate lookup failed; inserting these events without it",
				zap.String("table", table.Name), zap.Int("events", end-start), zap.Error(err))
			continue
		}
		for id := range found {
			existing[id] = struct{}{}
		}
	}
	if len(existing) == 0 {
		return kept, res
	}
	out := kept[:0]
	for i := range kept {
		if id := keys[i].id; id != "" {
			if _, dup := existing[id]; dup {
				res.stored++
				continue
			}
		}
		out = append(out, kept[i])
	}
	return out, res
}

// lookupFor builds the lookup for keys[idx...]: the distinct app versions and
// key-column values, and the timestamp range of exactly those events.
func lookupFor(table storage.EventTable, keys []eventKey, idx []int) storage.EventIDLookup {
	q := storage.EventIDLookup{Table: table, EventIDs: make([]string, 0, len(idx))}
	versions := map[string]struct{}{}
	values := map[string]struct{}{}
	for n, i := range idx {
		k := keys[i]
		q.EventIDs = append(q.EventIDs, k.id)
		if _, ok := versions[k.appVersion]; !ok {
			versions[k.appVersion] = struct{}{}
			q.AppVersions = append(q.AppVersions, k.appVersion)
		}
		if _, ok := values[k.key]; !ok {
			values[k.key] = struct{}{}
			q.Keys = append(q.Keys, k.key)
		}
		if n == 0 || k.ts.Before(q.MinTime) {
			q.MinTime = k.ts
		}
		if n == 0 || k.ts.After(q.MaxTime) {
			q.MaxTime = k.ts
		}
	}
	return q
}

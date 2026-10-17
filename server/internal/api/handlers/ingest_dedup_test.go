package handlers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// APM-19: duplicate events are dropped before insert and counted as accepted.

func TestDedup_ResendOfStoredBatchInsertsNothing(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	batch := batchOf(
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`),
		sdkEventJSON("perf_sample", "p2", 2, `,"fps":58.5`),
		sdkEventJSON("exception", "x1", 3, `,"fingerprint":"abc","count":1`),
	)
	_, first := postBatch(t, h, batch, nil)
	if first.Accepted != 3 || first.Duplicates != 0 {
		t.Fatalf("first upload: %+v", first)
	}

	w, resp := postBatch(t, h, batch, nil)
	if w.Code != http.StatusOK || !resp.Stored || resp.Retry || resp.Accepted != 3 || resp.Duplicates != 3 || resp.Failed != 0 {
		t.Fatalf("resend: code=%d resp=%+v", w.Code, resp)
	}
	if len(store.perfSamples) != 2 || len(store.exceptions) != 1 {
		t.Fatalf("stored %d perf samples, %d exceptions; want 2, 1", len(store.perfSamples), len(store.exceptions))
	}
}

func TestDedup_PartialOverlapInsertsOnlyNewEvents(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`),
		sdkEventJSON("perf_sample", "p2", 2, `,"fps":58.5`),
	), nil)

	_, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`),
		sdkEventJSON("perf_sample", "p2", 2, `,"fps":58.5`),
		sdkEventJSON("perf_sample", "p3", 3, `,"fps":57.5`),
		sdkEventJSON("exception", "x1", 4, `,"fingerprint":"abc","count":1`),
	), nil)
	if resp.Accepted != 4 || resp.Duplicates != 2 || !resp.Stored {
		t.Fatalf("unexpected response %+v", resp)
	}
	var ids []string
	for _, s := range store.perfSamples {
		ids = append(ids, s.EventID)
	}
	if len(ids) != 3 || ids[2] != "p3" || len(store.exceptions) != 1 {
		t.Fatalf("stored perf ids %v, %d exceptions", ids, len(store.exceptions))
	}
}

func TestDedup_RepeatWithinBatchKeepsFirst(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`),
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`),
		sdkEventJSON("perf_sample", "p2", 2, `,"fps":30`),
	), nil)
	if resp.Accepted != 3 || resp.Duplicates != 1 || len(store.perfSamples) != 2 {
		t.Fatalf("resp=%+v stored=%d", resp, len(store.perfSamples))
	}
	// Only the distinct ids are looked up.
	if len(store.lookups) != 1 || len(store.lookups[0].EventIDs) != 2 {
		t.Fatalf("lookups: %+v", store.lookups)
	}
}

func TestDedup_OldSDKEventsAreNeverLookedUpOrDropped(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	legacy := sdkEventJSON("perf_sample", "", 0, `,"fps":59.5`)
	for i := 0; i < 2; i++ {
		_, resp := postBatch(t, h, batchOf(legacy, legacy), nil)
		if resp.Accepted != 2 || resp.Duplicates != 0 {
			t.Fatalf("upload %d: %+v", i, resp)
		}
	}
	if len(store.perfSamples) != 4 {
		t.Fatalf("stored %d legacy events, want 4", len(store.perfSamples))
	}
	if len(store.lookups) != 0 {
		t.Fatalf("legacy events were looked up: %+v", store.lookups)
	}
	// Phase B: each gets its own server id, so identical legacy events never
	// collapse in the ReplacingMergeTree tables.
	ids := map[string]bool{}
	for _, s := range store.perfSamples {
		if !strings.HasPrefix(s.EventID, "srv-") || len(s.EventID) != len("srv-")+36 {
			t.Errorf("legacy event id %q", s.EventID)
		}
		ids[s.EventID] = true
	}
	if len(ids) != 4 {
		t.Errorf("legacy ids not unique: %v", ids)
	}
}

// A server-side insert retry must write the same server ids again, so the
// ReplacingMergeTree collapses a retry whose first attempt did reach the table.
func TestDedup_ServerIDsAreStableAcrossInsertRetries(t *testing.T) {
	store := &recordingStore{}
	h := newIngestHandler(store, zap.NewNop())
	postBatch(t, h, batchOf(sdkEventJSON("exception", "", 0, `,"fingerprint":"abc","count":1`)), nil)
	if len(store.attempts) != 2 || store.attempts[0] == "" || store.attempts[0] != store.attempts[1] {
		t.Fatalf("ids per attempt: %q", store.attempts)
	}
}

// recordingStore fails the first exception insert and records the event_id of
// every attempt.
type recordingStore struct {
	fakeEventStore
	attempts []string
}

func (r *recordingStore) InsertExceptions(_ context.Context, e []models.Exception) error {
	r.attempts = append(r.attempts, e[0].EventID)
	if len(r.attempts) == 1 {
		return errors.New("connection reset")
	}
	return nil
}

func TestNewServerEventID_IsAPrefixedV4UUID(t *testing.T) {
	re := regexp.MustCompile(`^srv-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newServerEventID()
		if !re.MatchString(id) || seen[id] {
			t.Fatalf("bad or repeated id %q", id)
		}
		seen[id] = true
	}
}

func TestDedup_MixedBatchLooksUpOnlyEventsWithID(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "", 0, `,"fps":59.5`),
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`),
	), nil)
	if resp.Accepted != 2 || len(store.lookups) != 1 || len(store.lookups[0].EventIDs) != 1 || store.lookups[0].EventIDs[0] != "p1" {
		t.Fatalf("resp=%+v lookups=%+v", resp, store.lookups)
	}
}

func TestDedup_LookupFailureInsertsAnyway(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	batch := batchOf(sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`))
	postBatch(t, h, batch, nil)

	store.lookupErr = errors.New("clickhouse: timeout")
	w, resp := postBatch(t, h, batch, nil)
	if w.Code != http.StatusOK || !resp.Stored || resp.Accepted != 1 || resp.Duplicates != 0 {
		t.Fatalf("code=%d resp=%+v", w.Code, resp)
	}
	if len(store.perfSamples) != 2 {
		t.Fatalf("stored %d, want 2 (the duplicate is inserted when the lookup fails)", len(store.perfSamples))
	}
}

func TestDedup_SlowLookupTimesOutAndInserts(t *testing.T) {
	old := dedupLookupTimeout
	dedupLookupTimeout = 20 * time.Millisecond
	defer func() { dedupLookupTimeout = old }()

	store := &fakeEventStore{lookupBlock: true}
	h := newIngestHandler(store, zap.NewNop())
	start := time.Now()
	_, resp := postBatch(t, h, batchOf(sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`)), nil)
	if resp.Accepted != 1 || !resp.Stored || len(store.perfSamples) != 1 {
		t.Fatalf("resp=%+v stored=%d", resp, len(store.perfSamples))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("request took %v", d)
	}
}

// A 503 after a partial failure makes the client resend everything; the
// tables that succeeded must not be written twice.
func TestDedup_RetryAfterPartialFailureDoesNotDuplicate(t *testing.T) {
	store := &fakeEventStore{failExceptionsTimes: 3} // first request: initial try + 2 retries fail
	h := newIngestHandler(store, zap.NewNop())
	batch := batchOf(
		sdkEventJSON("perf_sample", "p1", 1, `,"fps":59.5`),
		sdkEventJSON("exception", "x1", 2, `,"fingerprint":"abc","count":1`),
	)
	w, resp := postBatch(t, h, batch, nil)
	if w.Code != http.StatusServiceUnavailable || !resp.Retry {
		t.Fatalf("first: code=%d resp=%+v", w.Code, resp)
	}
	w, resp = postBatch(t, h, batch, nil)
	if w.Code != http.StatusOK || resp.Accepted != 2 || resp.Duplicates != 1 {
		t.Fatalf("resend: code=%d resp=%+v", w.Code, resp)
	}
	if len(store.perfSamples) != 1 || len(store.exceptions) != 1 {
		t.Fatalf("stored %d perf, %d exceptions; want 1, 1", len(store.perfSamples), len(store.exceptions))
	}
}

// The lookup filters on the sort-key prefix of each table and the exact
// timestamp range of the looked-up events.
func TestDedup_LookupUsesSortKeyPrefixAndTimeRange(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	t1 := time.UnixMilli(time.Now().Add(-time.Hour).UnixMilli()/1000*1000 + 123)
	t2 := t1.Add(90 * time.Second)
	postBatch(t, h, batchOf(
		eventAt("exception", "x2", 2, t2, `,"fingerprint":"fp-b","message":"error b","count":1`),
		eventAt("exception", "x1", 1, t1, `,"fingerprint":"fp-a","message":"error a","count":1`),
		eventAt("exception", "x3", 3, t1, `,"fingerprint":"fp-a","message":"error a","count":1`),
		eventAt("jank", "j1", 4, t1, `,"duration_ms":80,"max_frame_ms":80`),
	), nil)

	byTable := map[string]storage.EventIDLookup{}
	for _, q := range store.lookups {
		byTable[q.Table.Name] = q
	}
	// The key is the stored fingerprint, i.e. the server's (APM-4).
	fpA := fingerprint.Analyze(fingerprint.Input{Message: "error a"}).Fingerprint
	fpB := fingerprint.Analyze(fingerprint.Input{Message: "error b"}).Fingerprint
	exc := byTable["apm_exceptions"]
	if exc.Table.KeyColumn != "fingerprint" || len(exc.Keys) != 2 || exc.Keys[0] != fpB || exc.Keys[1] != fpA {
		t.Fatalf("exception lookup: %+v", exc)
	}
	if len(exc.AppVersions) != 1 || exc.AppVersions[0] != "1.0.0" {
		t.Fatalf("app versions: %+v", exc.AppVersions)
	}
	if !exc.MinTime.Equal(t1) || !exc.MaxTime.Equal(t2) {
		t.Fatalf("time range %v..%v, want %v..%v", exc.MinTime, exc.MaxTime, t1, t2)
	}
	if jank := byTable["apm_janks"]; jank.Table.KeyColumn != "scene" || len(jank.Keys) != 1 || jank.Keys[0] != "Main" {
		t.Fatalf("jank lookup: %+v", jank)
	}
}

func TestDedup_LargeBatchesAreLookedUpInChunks(t *testing.T) {
	old := dedupLookupChunk
	dedupLookupChunk = 2
	defer func() { dedupLookupChunk = old }()

	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	var events []string
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		events = append(events, sdkEventJSON("perf_sample", id, i, `,"fps":60`))
	}
	postBatch(t, h, batchOf(events...), nil)
	if len(store.lookups) != 3 {
		t.Fatalf("lookups=%d, want 3", len(store.lookups))
	}
	_, resp := postBatch(t, h, batchOf(events...), nil)
	if resp.Duplicates != 5 || len(store.perfSamples) != 5 {
		t.Fatalf("resp=%+v stored=%d", resp, len(store.perfSamples))
	}
	if len(store.lookups) != 6 {
		t.Fatalf("lookups=%d, want 3 per request", len(store.lookups))
	}
}

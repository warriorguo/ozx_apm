package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api/middleware"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// fakeEventStore records inserted events. failExceptions makes every
// exception insert fail; failExceptionsTimes makes only the first N fail.
// It remembers stored event_ids per table and answers ExistingEventIDs from
// them; lookupErr makes every lookup fail, lookupBlock makes it wait for the
// context to end.
type fakeEventStore struct {
	failExceptions      bool
	failExceptionsTimes int
	exceptionCalls      int
	perfSamples         []models.PerfSample
	janks               []models.Jank
	exceptions          []models.Exception
	crashes             []models.Crash
	ids                 map[string]map[string]bool
	lookups             []storage.EventIDLookup
	lookupErr           error
	lookupBlock         bool
}

func (f *fakeEventStore) remember(table, id string) {
	if id == "" {
		return
	}
	if f.ids == nil {
		f.ids = map[string]map[string]bool{}
	}
	if f.ids[table] == nil {
		f.ids[table] = map[string]bool{}
	}
	f.ids[table][id] = true
}

func (f *fakeEventStore) InsertPerfSamples(_ context.Context, s []models.PerfSample) error {
	f.perfSamples = append(f.perfSamples, s...)
	for _, e := range s {
		f.remember(storage.TablePerfSamples.Name, e.EventID)
	}
	return nil
}
func (f *fakeEventStore) InsertJanks(_ context.Context, j []models.Jank) error {
	f.janks = append(f.janks, j...)
	for _, e := range j {
		f.remember(storage.TableJanks.Name, e.EventID)
	}
	return nil
}
func (f *fakeEventStore) InsertStartups(context.Context, []models.Startup) error     { return nil }
func (f *fakeEventStore) InsertSceneLoads(context.Context, []models.SceneLoad) error { return nil }
func (f *fakeEventStore) InsertExceptions(_ context.Context, e []models.Exception) error {
	f.exceptionCalls++
	if f.failExceptions || f.exceptionCalls <= f.failExceptionsTimes {
		return errors.New("clickhouse unavailable")
	}
	f.exceptions = append(f.exceptions, e...)
	for _, x := range e {
		f.remember(storage.TableExceptions.Name, x.EventID)
	}
	return nil
}
func (f *fakeEventStore) InsertCrashes(_ context.Context, c []models.Crash) error {
	f.crashes = append(f.crashes, c...)
	for _, e := range c {
		f.remember(storage.TableCrashes.Name, e.EventID)
	}
	return nil
}
func (f *fakeEventStore) ExistingEventIDs(ctx context.Context, q storage.EventIDLookup) (map[string]struct{}, error) {
	f.lookups = append(f.lookups, q)
	if f.lookupBlock {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	found := map[string]struct{}{}
	for _, id := range q.EventIDs {
		if f.ids[q.Table.Name][id] {
			found[id] = struct{}{}
		}
	}
	return found, nil
}

func init() {
	// Keep the server-side insert retries fast in tests.
	insertRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
}

func postRaw(t *testing.T, h http.Handler, body []byte, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func postBatch(t *testing.T, h *IngestHandler, body string, header map[string]string) (*httptest.ResponseRecorder, IngestResponse) {
	t.Helper()
	w := postRaw(t, http.HandlerFunc(h.IngestEvents), []byte(body), header)
	var resp IngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return w, resp
}

// sdkEventJSON builds an event the way the Unity SDK serializes it.
// eventID "" produces an old-SDK event without event_id/seq.
func sdkEventJSON(eventType, eventID string, seq int, extra string) string {
	return eventAt(eventType, eventID, seq, time.Now(), extra)
}

func eventAt(eventType, eventID string, seq int, ts time.Time, extra string) string {
	ids := ""
	if eventID != "" {
		ids = `"event_id":"` + eventID + `","seq":` + strconv.Itoa(seq) + `,`
	}
	return `{"type":"` + eventType + `",` + ids + `"timestamp":` + strconv.FormatInt(ts.UnixMilli(), 10) +
		`,"app_version":"1.0.0","platform":"Android","device_model":"Pixel",` +
		`"os_version":"14","session_id":"s1","device_id":"d1","scene":"Main"` + extra + `}`
}

func batchOf(events ...string) string {
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

func TestIngest_AllStored(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())

	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "a", 1, `,"fps":59.5`),
		sdkEventJSON("exception", "b", 2, `,"fingerprint":"abc","count":1`),
	), nil)

	if w.Code != http.StatusOK || !resp.Stored || resp.Retry || resp.Accepted != 2 || resp.Failed != 0 {
		t.Fatalf("unexpected: code=%d resp=%+v", w.Code, resp)
	}
}

func TestIngest_StoresEventIDAndSeq(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())

	_, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "0f8fad5bd9cb469fa16570867728950e", 41, `,"fps":59.5`),
		sdkEventJSON("exception", "7c9e6679742540de944be07fc1f90ae7", 42, `,"fingerprint":"abc","count":1`),
		sdkEventJSON("perf_sample", "", 0, `,"fps":30`), // old SDK
	), nil)

	if resp.Accepted != 3 {
		t.Fatalf("unexpected response %+v", resp)
	}
	if store.perfSamples[0].EventID != "0f8fad5bd9cb469fa16570867728950e" || store.perfSamples[0].Seq != 41 {
		t.Errorf("perf sample event_id/seq not stored: %+v", store.perfSamples[0])
	}
	if store.exceptions[0].EventID != "7c9e6679742540de944be07fc1f90ae7" || store.exceptions[0].Seq != 42 {
		t.Errorf("exception event_id/seq not stored: %+v", store.exceptions[0])
	}
	// APM-19 phase B: an old-SDK event gets a server-generated id, seq stays 0.
	if !strings.HasPrefix(store.perfSamples[1].EventID, ServerEventIDPrefix) || store.perfSamples[1].Seq != 0 {
		t.Errorf("old-SDK event should have a srv- event_id and seq 0: %+v", store.perfSamples[1])
	}
}

func TestIngest_TransientInsertErrorIsRetriedServerSide(t *testing.T) {
	store := &fakeEventStore{failExceptionsTimes: 2}
	h := newIngestHandler(store, zap.NewNop())

	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "a", 1, ""),
		sdkEventJSON("exception", "b", 2, `,"fingerprint":"abc","count":1`),
	), nil)

	if w.Code != http.StatusOK || !resp.Stored || resp.Accepted != 2 {
		t.Fatalf("expected the retried insert to succeed: code=%d resp=%+v", w.Code, resp)
	}
	if store.exceptionCalls != 3 {
		t.Errorf("expected 3 insert attempts, got %d", store.exceptionCalls)
	}
	if len(store.perfSamples) != 1 {
		t.Errorf("perf samples written %d times, want once", len(store.perfSamples))
	}
}

func TestIngest_PersistentFailure_ClientWithEventIDsRetries(t *testing.T) {
	store := &fakeEventStore{failExceptions: true}
	h := newIngestHandler(store, zap.NewNop())

	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "a", 1, `,"fps":60`),
		sdkEventJSON("exception", "b", 2, `,"fingerprint":"abc","count":1`),
	), nil)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	if resp.Stored || !resp.Retry {
		t.Errorf("expected stored=false retry=true, got %+v", resp)
	}
	if resp.Accepted != 1 || resp.Failed != 1 {
		t.Errorf("expected accepted=1 failed=1 (truthful), got %+v", resp)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After on 503")
	}
}

func TestIngest_PersistentFailure_NewSDKHeaderRetries(t *testing.T) {
	store := &fakeEventStore{failExceptions: true}
	h := newIngestHandler(store, zap.NewNop())

	w, resp := postBatch(t, h, batchOf(sdkEventJSON("exception", "", 0, `,"fingerprint":"abc","count":1`)),
		map[string]string{SDKVersionHeader: "2.0.0"})

	if w.Code != http.StatusServiceUnavailable || !resp.Retry {
		t.Errorf("expected 503 retry for a new SDK, got code=%d resp=%+v", w.Code, resp)
	}
}

func TestIngest_PersistentFailure_LegacyClientKeepsOldBehaviour(t *testing.T) {
	store := &fakeEventStore{failExceptions: true}
	h := newIngestHandler(store, zap.NewNop())

	// Old SDK: no event_id, no version header. A resend could never be
	// de-duplicated, so answer 200 like before (but report the failure).
	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "", 0, `,"fps":60`),
		sdkEventJSON("exception", "", 0, `,"fingerprint":"abc","count":1`),
	), nil)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for a legacy client, got %d", w.Code)
	}
	if resp.Retry || resp.Stored || resp.Accepted != 1 || resp.Failed != 1 {
		t.Errorf("unexpected response %+v", resp)
	}
}

func TestIngest_RejectedEventsStillCountAsStored(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())

	// The second event fails validation (missing device_id). That is
	// permanent, so the batch is still "stored" and the client may delete it.
	invalid := `{"type":"perf_sample","event_id":"x","seq":3,"timestamp":` +
		strconv.FormatInt(time.Now().UnixMilli(), 10) + `,"app_version":"1.0.0","platform":"Android","session_id":"s1"}`
	w, resp := postBatch(t, h, batchOf(sdkEventJSON("perf_sample", "a", 1, ""), invalid), nil)

	if w.Code != http.StatusOK || !resp.Stored || resp.Retry || resp.Accepted != 1 || resp.Rejected != 1 {
		t.Errorf("unexpected result: code=%d resp=%+v", w.Code, resp)
	}
}

func TestIngest_AgeWindow(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())

	day := 24 * time.Hour
	_, resp := postBatch(t, h, batchOf(
		eventAt("perf_sample", "late", 1, time.Now().Add(-29*day), ""),
		eventAt("perf_sample", "tooold", 2, time.Now().Add(-31*day), ""),
	), nil)

	if resp.Accepted != 1 || resp.Rejected != 1 || len(store.perfSamples) != 1 {
		t.Fatalf("expected 29-day event accepted and 31-day event rejected, got %+v", resp)
	}
}

func TestIngest_ErrorIndexIsDecimal(t *testing.T) {
	h := newIngestHandler(&fakeEventStore{}, zap.NewNop())

	events := make([]string, 0, 13)
	for i := 0; i < 12; i++ {
		events = append(events, sdkEventJSON("perf_sample", "id"+strconv.Itoa(i), i+1, ""))
	}
	events = append(events, `"not an object"`)
	_, resp := postBatch(t, h, batchOf(events...), nil)

	want := "event 12: invalid format"
	for _, e := range resp.Errors {
		if e == want {
			return
		}
	}
	t.Errorf("expected error %q, got %v", want, resp.Errors)
}

func TestIngest_UnparseablePayloadHasRejectCode(t *testing.T) {
	h := newIngestHandler(&fakeEventStore{}, zap.NewNop())

	for _, body := range []string{"not valid json", `{"events":[{"type":"perf_sample"`, `{"events":"nope"}`} {
		w, resp := postBatch(t, h, body, nil)
		if w.Code != http.StatusBadRequest || resp.RejectCode != RejectInvalidJSON {
			t.Errorf("%q: expected 400 with reject_code, got code=%d resp=%+v", body, w.Code, resp)
		}
	}
}

func TestIngest_OneMalformedEventDoesNotSinkTheBatch(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())

	// What the pre-APM-2 SDK wrote under de-DE: "fps":59,50 is not JSON.
	broken := sdkEventJSON("perf_sample", "", 0, `,"fps":59,50`)
	body := batchOf(
		sdkEventJSON("perf_sample", "", 0, `,"fps":60.00`),
		broken,
		sdkEventJSON("exception", "", 0, `,"fingerprint":"f","message":"a {quoted} \"brace\"","count":1`),
	)
	w, resp := postBatch(t, h, body, nil)

	if w.Code != http.StatusOK || resp.Accepted != 2 || resp.Rejected != 1 {
		t.Errorf("expected 2 salvaged events and 1 rejected, got code=%d resp=%+v", w.Code, resp)
	}
}

func TestIngest_TooLarge(t *testing.T) {
	h := newIngestHandler(&fakeEventStore{}, zap.NewNop())
	w := postRaw(t, http.HandlerFunc(h.IngestEvents), bytes.Repeat([]byte(" "), maxIngestBodyBytes+10), nil)
	var resp IngestResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusRequestEntityTooLarge || resp.RejectCode != RejectTooLarge {
		t.Errorf("expected 413 with reject_code, got %d %q", w.Code, w.Body.String())
	}
}

// A 400 produced by middleware (or a proxy) carries no reject code, so the
// SDK must treat it as retryable rather than discard the batch.
func TestIngest_BrokenGzipIsABare400(t *testing.T) {
	h := newIngestHandler(&fakeEventStore{}, zap.NewNop())
	handler := middleware.Decompress(http.HandlerFunc(h.IngestEvents))

	w := postRaw(t, handler, []byte("definitely not gzip"), map[string]string{"Content-Encoding": "gzip"})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "reject_code") {
		t.Errorf("middleware 400 must not carry a reject code: %q", w.Body.String())
	}
}

func TestDecodeEventBatch_ScannerHandlesStringsAndNesting(t *testing.T) {
	body := []byte(`{"events":[{"a":"}{","b":[1,{"c":2}]},{"x":1,}, {"y":"\"}"}]}`)
	events, salvaged, err := decodeEventBatch(body)
	if err != nil || !salvaged {
		t.Fatalf("expected salvage, got err=%v salvaged=%v", err, salvaged)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(events))
	}
	if string(events[0]) != `{"a":"}{","b":[1,{"c":2}]}` || string(events[2]) != `{"y":"\"}"}` {
		t.Errorf("wrong split: %q", events)
	}
}

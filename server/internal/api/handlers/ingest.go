package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/processor"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

const (
	// SDKVersionHeader is sent by SDKs that understand IngestResponse.Retry
	// and keep batches until the server confirms them.
	SDKVersionHeader = "X-Apm-Sdk-Version"

	// RejectInvalidJSON: the body is not JSON at all; resending it can never succeed.
	RejectInvalidJSON = "invalid_json"
	// RejectTooLarge: the body exceeds maxIngestBodyBytes.
	RejectTooLarge = "payload_too_large"

	maxIngestBodyBytes = 8 << 20
	// Seconds a client should wait before retrying after a storage failure.
	storageRetryAfterSeconds = 30
	// maxResponseErrors bounds IngestResponse.Errors; the counts stay exact.
	maxResponseErrors = 50
	// maxShownTypeLength bounds an unknown type as quoted in an error.
	maxShownTypeLength = 64
)

// insertRetryDelays is the pause before each extra attempt of a failed table
// insert. A failed insert is retried here before answering, so a transient
// ClickHouse error does not make the client resend the whole batch (and
// duplicate the tables that did succeed).
var insertRetryDelays = []time.Duration{50 * time.Millisecond, 250 * time.Millisecond}

// EventStore is the subset of the repository the ingest handler writes to.
type EventStore interface {
	InsertPerfSamples(ctx context.Context, samples []models.PerfSample) error
	InsertJanks(ctx context.Context, janks []models.Jank) error
	InsertStartups(ctx context.Context, startups []models.Startup) error
	InsertSceneLoads(ctx context.Context, loads []models.SceneLoad) error
	InsertExceptions(ctx context.Context, exceptions []models.Exception) error
	InsertCrashes(ctx context.Context, crashes []models.Crash) error
	InsertCustomEvents(ctx context.Context, events []models.CustomEvent) error
	InsertCustomMetrics(ctx context.Context, rows []models.CustomMetric) error
	InsertRuns(ctx context.Context, runs []models.Run) error
	// ExistingEventIDs returns which of the looked-up event_ids are already stored.
	ExistingEventIDs(ctx context.Context, q storage.EventIDLookup) (map[string]struct{}, error)
}

type IngestHandler struct {
	repo      EventStore
	validator *processor.Validator
	enricher  *processor.Enricher
	logger    *zap.Logger
	// symbols names the native frames of crashes from uploaded symbols for
	// the fingerprint (APM-7); nil when the store has no symbol tables.
	symbols *symbols.Symbolizer
}

func NewIngestHandler(repo *storage.Repository, logger *zap.Logger) *IngestHandler {
	return newIngestHandler(repo, logger)
}

func newIngestHandler(repo EventStore, logger *zap.Logger) *IngestHandler {
	h := &IngestHandler{
		repo:      repo,
		validator: processor.NewValidator(),
		enricher:  processor.NewEnricher(),
		logger:    logger,
	}
	if st, ok := repo.(symbols.Store); ok {
		h.symbols = symbols.NewSymbolizer(st, crashSymbolCacheSize)
	}
	return h
}

// crashSymbolCacheSize: resolved addresses kept by the ingest symbolizer.
const crashSymbolCacheSize = 20000

// crashSymbolTimeout bounds the symbol lookups of one batch's crashes
// (together): on timeout or error the remaining crashes of the batch are
// fingerprinted by address (they group per build) and stored anyway.
const crashSymbolTimeout = 3 * time.Second

// crashSymbols is the symbol budget of one batch.
type crashSymbols struct {
	ctx    context.Context
	cancel context.CancelFunc
	failed bool
}

func (c *crashSymbols) close() {
	if c.cancel != nil {
		c.cancel()
	}
}

// maxResolvedFramesPerThread: native in-app frames per thread looked up at
// ingest (the fingerprint uses the first 3 distinct ones).
const maxResolvedFramesPerThread = 16

// crashResolver looks up the in-app native frames of a crash in the
// uploaded symbols and returns the resolver for its fingerprint.
func (h *IngestHandler) crashResolver(parent context.Context, budget *crashSymbols, c *models.Crash, threads []fingerprint.CrashThread) processor.CrashResolver {
	if h.symbols == nil || budget.failed {
		return nil
	}
	if budget.ctx == nil {
		budget.ctx, budget.cancel = context.WithTimeout(parent, crashSymbolTimeout)
	}
	type key struct {
		module, buildID string
		pc              uint64
		ret             bool
	}
	var frames []symbols.Frame
	var keys []key
	for _, t := range threads {
		n := 0
		for _, f := range t.Frames {
			if !f.Native || !f.InApp || n == maxResolvedFramesPerThread {
				continue
			}
			n++
			k := key{f.ModuleName(), f.BuildID, f.PC, f.Index > 0}
			keys = append(keys, k)
			frames = append(frames, symbols.Frame{Module: k.module, BuildID: k.buildID, PC: k.pc, ReturnAddress: k.ret})
		}
	}
	if len(frames) == 0 {
		return nil
	}
	got, err := h.symbols.Resolve(budget.ctx, frames, symbols.Fallback{AppVersion: c.AppVersion, ABI: c.ABI})
	if err != nil {
		budget.failed = true
		h.logger.Warn("crash symbol lookup failed; fingerprinting the batch's remaining crashes by address", zap.Error(err))
	}
	names := map[key]string{}
	for i, r := range got {
		names[keys[i]] = r.Name
	}
	if len(names) == 0 {
		return nil
	}
	return func(f fingerprint.CrashFrame) string {
		return names[key{f.ModuleName(), f.BuildID, f.PC, f.Index > 0}]
	}
}

// EventRequest represents the incoming event batch
type EventRequest struct {
	Events []json.RawMessage `json:"events"`
}

// EventWrapper is used to determine event type before full parsing
type EventWrapper struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"` // Unix milliseconds
	EventID   string `json:"event_id"`  // set by SDKs that can de-duplicate retries
}

// IngestResponse tells the client what happened to the batch.
//
//   - Stored: every event that passed validation was written.
//   - Retry: the client should keep the batch and send it again later
//     (only set for clients whose events carry event_id, see below).
//   - RejectCode: set, with HTTP 400, when the payload itself can never be
//     accepted. Clients may discard the batch only on an explicit RejectCode,
//     never on a bare 4xx from a proxy or middleware.
//
// Events rejected by validation are counted in Rejected and are not worth
// retrying, so they do not make Stored false. Every rejected event has its
// reason in Errors ("event <index>: <reason>"), among them event types this
// server does not store ("unknown type \"x\"", "type \"http\" is not
// supported yet"); Errors lists at most maxResponseErrors entries and then
// how many more there were. RejectedTypes counts the rejected events of each
// type the server does not know or store (APM-6).
//
// Duplicates counts events that were not inserted because their event_id was
// already stored or appeared earlier in the batch (APM-19). They are included
// in Accepted: the server has them, so the client must delete them.
type IngestResponse struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Rejected   int `json:"rejected"`
	Failed     int `json:"failed"`
	// RejectedTypes: rejected events per unsupported type (APM-6); omitted when none.
	RejectedTypes map[string]int `json:"rejected_types,omitempty"`
	Stored        bool           `json:"stored"`
	Retry         bool           `json:"retry"`
	RejectCode    string         `json:"reject_code,omitempty"`
	Errors        []string       `json:"errors,omitempty"`
}

func writeIngestResponse(w http.ResponseWriter, status int, resp IngestResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

func (h *IngestHandler) IngestEvents(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		http.Error(w, "missing body", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIngestBodyBytes+1))
	if err != nil {
		// Truncated upload, broken gzip stream, ...: not provably the client's
		// data, so no reject code and the client retries.
		http.Error(w, "failed to read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxIngestBodyBytes {
		writeIngestResponse(w, http.StatusRequestEntityTooLarge, IngestResponse{RejectCode: RejectTooLarge})
		return
	}

	rawEvents, salvaged, err := decodeEventBatch(body)
	if err != nil {
		writeIngestResponse(w, http.StatusBadRequest, IngestResponse{
			RejectCode: RejectInvalidJSON,
			Errors:     []string{"invalid JSON: " + err.Error()},
		})
		return
	}

	if len(rawEvents) == 0 {
		writeIngestResponse(w, http.StatusOK, IngestResponse{Accepted: 0, Stored: true})
		return
	}

	// Categorize events by type
	var (
		perfSamples []models.PerfSample
		janks       []models.Jank
		startups    []models.Startup
		sceneLoads  []models.SceneLoad
		exceptions  []models.Exception
		crashes     []models.Crash
		customs     []models.CustomEvent
		metricRows  []models.CustomMetric
		runs        []models.Run
		errors      []string
		rejected    int
		hasEventIDs bool
		ctxStats    processor.ContextStats
		propStats   processor.ContextStats
		detailStats processor.ContextStats
		symBudget   crashSymbols
		rejTypes    map[string]int
		moreErrors  int
		addError    = func(msg string) {
			if len(errors) < maxResponseErrors {
				errors = append(errors, msg)
			} else {
				moreErrors++
			}
		}
		reject     = func(i int, reason string) { rejected++; addError("event " + strconv.Itoa(i) + ": " + reason) }
		rejectType = func(i int, typ, reason string) {
			if rejTypes == nil {
				rejTypes = map[string]int{}
			}
			if len(rejTypes) < maxResponseErrors {
				rejTypes[typ]++
			}
			reject(i, reason)
		}
		sdkVersion = r.Header.Get(SDKVersionHeader)
		clientIP   = r.RemoteAddr // In production, extract from X-Forwarded-For
	)
	defer symBudget.close()
	if salvaged {
		addError("batch was not valid JSON; salvaged the well-formed events")
	}

	for i, rawEvent := range rawEvents {
		var wrapper EventWrapper
		if err := json.Unmarshal(rawEvent, &wrapper); err != nil {
			reject(i, "invalid format")
			continue
		}
		if wrapper.EventID != "" {
			hasEventIDs = true
		}

		timestamp := time.UnixMilli(wrapper.Timestamp)
		if timestamp.IsZero() || wrapper.Timestamp == 0 {
			timestamp = time.Now()
		}

		switch models.EventType(wrapper.Type) {
		case models.EventTypePerfSample:
			var event models.PerfSample
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			processor.NormalizePerfMemory(&event.PerfMemory) // APM-9
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidatePerfSample(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			perfSamples = append(perfSamples, event)

		case models.EventTypeJank:
			var event models.Jank
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateJank(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			janks = append(janks, event)

		case models.EventTypeStartup:
			var event models.Startup
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateStartup(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			startups = append(startups, event)

		case models.EventTypeSceneLoad:
			var event models.SceneLoad
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateSceneLoad(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			sceneLoads = append(sceneLoads, event)

		case models.EventTypeException:
			var event models.Exception
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateException(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			// Server-side fingerprint (APM-4), before the duplicate lookup
			// below, which matches on the stored fingerprint.
			processor.ApplyFingerprint(&event)
			processor.NormalizeOccurrenceInfo(&event)
			exceptions = append(exceptions, event)

		case models.EventTypeCrash:
			var event models.Crash
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateCrash(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			// APM-7: bounds, then the server's fingerprint (with the
			// uploaded symbols, when there are any), before the duplicate
			// lookup, which matches on the stored fingerprint.
			detailStats.Add(processor.NormalizeCrash(&event))
			threads := fingerprint.ParseCrashStack(event.Stack)
			processor.ApplyCrashFingerprint(&event, threads, h.crashResolver(r.Context(), &symBudget, &event, threads))
			crashes = append(crashes, event)

		case models.EventTypeCustomEvent:
			var event models.CustomEvent
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			propStats.Add(processor.NormalizeCustomEvent(&event))
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateCustomEvent(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			customs = append(customs, event)

		case models.EventTypeCustomMetric:
			var event models.CustomMetric
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateCustomMetric(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			metricRows = append(metricRows, event)

		case models.EventTypeRun:
			// APM-9: a game run that ended, with its peak memory.
			var event models.Run
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				reject(i, "invalid fields")
				continue
			}
			event.Timestamp = timestamp
			ctxStats.Add(processor.NormalizeEventContext(&event.EventContext, event.Platform))
			processor.NormalizeRun(&event)
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateRun(&event); err != nil {
				reject(i, err.Error())
				continue
			}
			runs = append(runs, event)

		case models.EventTypeAssetLoad, models.EventTypeHTTP:
			// Planned (asset loads, HTTP timings: APM-8), not stored yet:
			// rejected with a reason, never silently dropped.
			rejectType(i, wrapper.Type, "type "+strconv.Quote(wrapper.Type)+" is not supported yet")

		default:
			shown := wrapper.Type
			if len(shown) > maxShownTypeLength {
				shown = shown[:maxShownTypeLength] + "..."
			}
			shown = strings.ToValidUTF8(shown, "?")
			rejectType(i, shown, "unknown type "+strconv.Quote(shown))
		}
	}

	if ctxStats.Dropped > 0 || ctxStats.Truncated > 0 {
		// The events are kept; only the offending context entries are not.
		addError("context: " + strconv.Itoa(ctxStats.Dropped) + " entries dropped, " +
			strconv.Itoa(ctxStats.Truncated) + " values truncated (limits: 64 keys of [A-Za-z0-9_.-]{1,64}, values up to 256 characters)")
	}
	if detailStats.Dropped > 0 || detailStats.Truncated > 0 {
		addError("crash details: " + strconv.Itoa(detailStats.Dropped) + " entries dropped, " +
			strconv.Itoa(detailStats.Truncated) + " values truncated (the context's limits)")
	}
	if propStats.Dropped > 0 || propStats.Truncated > 0 {
		addError("props: " + strconv.Itoa(propStats.Dropped) + " entries dropped, " +
			strconv.Itoa(propStats.Truncated) + " values truncated (the context's limits)")
	}

	// Drop events that are already stored or repeated in this batch (APM-19).
	ctx := r.Context()
	var dups dedupResult
	add := func(d dedupResult) {
		dups.inBatch += d.inBatch
		dups.stored += d.stored
		dups.lookupFailed += d.lookupFailed
	}
	var d dedupResult
	perfSamples, d = dedupe(ctx, h.repo, h.logger, storage.TablePerfSamples, perfSamples,
		func(e *models.PerfSample) eventKey { return eventKey{e.EventID, e.AppVersion, e.Platform, e.Timestamp} })
	add(d)
	janks, d = dedupe(ctx, h.repo, h.logger, storage.TableJanks, janks,
		func(e *models.Jank) eventKey { return eventKey{e.EventID, e.AppVersion, e.Scene, e.Timestamp} })
	add(d)
	startups, d = dedupe(ctx, h.repo, h.logger, storage.TableStartups, startups,
		func(e *models.Startup) eventKey { return eventKey{e.EventID, e.AppVersion, e.Platform, e.Timestamp} })
	add(d)
	sceneLoads, d = dedupe(ctx, h.repo, h.logger, storage.TableSceneLoads, sceneLoads,
		func(e *models.SceneLoad) eventKey { return eventKey{e.EventID, e.AppVersion, e.SceneName, e.Timestamp} })
	add(d)
	exceptions, d = dedupe(ctx, h.repo, h.logger, storage.TableExceptions, exceptions,
		func(e *models.Exception) eventKey {
			return eventKey{e.EventID, e.AppVersion, e.Fingerprint, e.Timestamp}
		})
	add(d)
	crashes, d = dedupe(ctx, h.repo, h.logger, storage.TableCrashes, crashes,
		func(e *models.Crash) eventKey { return eventKey{e.EventID, e.AppVersion, e.Fingerprint, e.Timestamp} })
	add(d)
	customs, d = dedupe(ctx, h.repo, h.logger, storage.TableCustomEvents, customs,
		func(e *models.CustomEvent) eventKey { return eventKey{e.EventID, e.AppVersion, e.Name, e.Timestamp} })
	add(d)
	metricRows, d = dedupe(ctx, h.repo, h.logger, storage.TableCustomMetrics, metricRows,
		func(e *models.CustomMetric) eventKey { return eventKey{e.EventID, e.AppVersion, e.Name, e.Timestamp} })
	add(d)
	runs, d = dedupe(ctx, h.repo, h.logger, storage.TableRuns, runs,
		func(e *models.Run) eventKey { return eventKey{e.EventID, e.AppVersion, e.EndReason, e.Timestamp} })
	add(d)
	if dups.total() > 0 {
		h.logger.Info("dropped duplicate events",
			zap.Int("duplicates", dups.total()),
			zap.Int("in_batch", dups.inBatch),
			zap.Int("already_stored", dups.stored),
			zap.String("sdk_version", sdkVersion),
			zap.String("client_ip", clientIP),
		)
	}

	// Old-SDK events get a server-generated event_id (APM-19 phase B), after
	// the lookup so that they are never looked up. hasEventIDs above still
	// reflects what the client sent.
	assignServerEventIDs(perfSamples, func(e *models.PerfSample) *string { return &e.EventID })
	assignServerEventIDs(janks, func(e *models.Jank) *string { return &e.EventID })
	assignServerEventIDs(startups, func(e *models.Startup) *string { return &e.EventID })
	assignServerEventIDs(sceneLoads, func(e *models.SceneLoad) *string { return &e.EventID })
	assignServerEventIDs(exceptions, func(e *models.Exception) *string { return &e.EventID })
	assignServerEventIDs(crashes, func(e *models.Crash) *string { return &e.EventID })
	assignServerEventIDs(customs, func(e *models.CustomEvent) *string { return &e.EventID })
	assignServerEventIDs(metricRows, func(e *models.CustomMetric) *string { return &e.EventID })
	assignServerEventIDs(runs, func(e *models.Run) *string { return &e.EventID })

	// Insert events into ClickHouse. Each table insert is retried here
	// before answering. Only events whose insert succeeded count as accepted,
	// plus the duplicates, which are already stored.
	accepted, failed := dups.total(), 0

	insert := func(kind string, n int, fn func() error) {
		if n == 0 {
			return
		}
		if err := h.insertWithRetry(ctx, fn); err != nil {
			h.logger.Error("failed to insert "+kind, zap.Error(err), zap.Int("count", n), zap.String("sdk_version", sdkVersion))
			failed += n
			addError("storage error: " + kind)
			return
		}
		accepted += n
	}

	insert("perf samples", len(perfSamples), func() error { return h.repo.InsertPerfSamples(ctx, perfSamples) })
	insert("janks", len(janks), func() error { return h.repo.InsertJanks(ctx, janks) })
	insert("startups", len(startups), func() error { return h.repo.InsertStartups(ctx, startups) })
	insert("scene loads", len(sceneLoads), func() error { return h.repo.InsertSceneLoads(ctx, sceneLoads) })
	insert("exceptions", len(exceptions), func() error { return h.repo.InsertExceptions(ctx, exceptions) })
	insert("crashes", len(crashes), func() error { return h.repo.InsertCrashes(ctx, crashes) })
	insert("custom events", len(customs), func() error { return h.repo.InsertCustomEvents(ctx, customs) })
	insert("custom metrics", len(metricRows), func() error { return h.repo.InsertCustomMetrics(ctx, metricRows) })
	insert("runs", len(runs), func() error { return h.repo.InsertRuns(ctx, runs) })

	if moreErrors > 0 {
		errors = append(errors, "... and "+strconv.Itoa(moreErrors)+" more")
	}
	resp := IngestResponse{
		Accepted:      accepted,
		Duplicates:    dups.total(),
		Rejected:      rejected,
		Failed:        failed,
		Stored:        failed == 0,
		RejectedTypes: rejTypes,
		Errors:        errors,
	}
	status := http.StatusOK

	if failed > 0 {
		if hasEventIDs || sdkVersion != "" {
			// The client keeps the batch and resends it. Tables that did
			// succeed are not written again: their events carry event_id and
			// the resend drops them as duplicates (APM-19).
			resp.Retry = true
			status = http.StatusServiceUnavailable
			w.Header().Set("Retry-After", strconv.Itoa(storageRetryAfterSeconds))
		} else {
			// Old SDK: no event_id, so a resend could never be de-duplicated,
			// and old clients only look at the status code. Keep the previous
			// behaviour (200, client drops the batch) and make the loss visible.
			h.logger.Error("dropping events from a legacy client after storage failure",
				zap.Int("failed", failed), zap.Int("accepted", accepted), zap.String("client_ip", clientIP))
		}
	}

	h.logger.Info("ingested events",
		zap.Int("accepted", accepted),
		zap.Int("duplicates", dups.total()),
		zap.Int("dedup_lookup_failed", dups.lookupFailed),
		zap.Int("rejected", rejected),
		zap.Int("failed", failed),
		zap.Int("ctx_dropped", ctxStats.Dropped),
		zap.Int("ctx_truncated", ctxStats.Truncated),
		zap.Any("rejected_types", rejTypes),
		zap.String("sdk_version", sdkVersion),
		zap.String("client_ip", clientIP),
	)

	writeIngestResponse(w, status, resp)
}

func (h *IngestHandler) insertWithRetry(ctx context.Context, fn func() error) error {
	err := fn()
	for _, delay := range insertRetryDelays {
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		h.logger.Warn("retrying insert", zap.Error(err))
		err = fn()
	}
	return err
}

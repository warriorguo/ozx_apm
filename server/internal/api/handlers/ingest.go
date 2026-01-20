package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/processor"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type IngestHandler struct {
	repo      *storage.Repository
	validator *processor.Validator
	enricher  *processor.Enricher
	logger    *zap.Logger
}

func NewIngestHandler(repo *storage.Repository, logger *zap.Logger) *IngestHandler {
	return &IngestHandler{
		repo:      repo,
		validator: processor.NewValidator(),
		enricher:  processor.NewEnricher(),
		logger:    logger,
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
}

type IngestResponse struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors,omitempty"`
}

func (h *IngestHandler) IngestEvents(w http.ResponseWriter, r *http.Request) {
	var req EventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Events) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(IngestResponse{Accepted: 0})
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
		errors      []string
		rejected    int
	)

	clientIP := r.RemoteAddr // In production, extract from X-Forwarded-For

	for i, rawEvent := range req.Events {
		var wrapper EventWrapper
		if err := json.Unmarshal(rawEvent, &wrapper); err != nil {
			errors = append(errors, "event "+string(rune(i))+": invalid format")
			rejected++
			continue
		}

		timestamp := time.UnixMilli(wrapper.Timestamp)
		if timestamp.IsZero() || wrapper.Timestamp == 0 {
			timestamp = time.Now()
		}

		switch models.EventType(wrapper.Type) {
		case models.EventTypePerfSample:
			var event models.PerfSample
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				rejected++
				continue
			}
			event.Timestamp = timestamp
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidatePerfSample(&event); err != nil {
				rejected++
				continue
			}
			perfSamples = append(perfSamples, event)

		case models.EventTypeJank:
			var event models.Jank
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				rejected++
				continue
			}
			event.Timestamp = timestamp
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateJank(&event); err != nil {
				rejected++
				continue
			}
			janks = append(janks, event)

		case models.EventTypeStartup:
			var event models.Startup
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				rejected++
				continue
			}
			event.Timestamp = timestamp
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateStartup(&event); err != nil {
				rejected++
				continue
			}
			startups = append(startups, event)

		case models.EventTypeSceneLoad:
			var event models.SceneLoad
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				rejected++
				continue
			}
			event.Timestamp = timestamp
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateSceneLoad(&event); err != nil {
				rejected++
				continue
			}
			sceneLoads = append(sceneLoads, event)

		case models.EventTypeException:
			var event models.Exception
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				rejected++
				continue
			}
			event.Timestamp = timestamp
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateException(&event); err != nil {
				rejected++
				continue
			}
			exceptions = append(exceptions, event)

		case models.EventTypeCrash:
			var event models.Crash
			if err := json.Unmarshal(rawEvent, &event); err != nil {
				rejected++
				continue
			}
			event.Timestamp = timestamp
			event.Platform = h.enricher.NormalizePlatform(event.Platform)
			event.DeviceModel = h.enricher.NormalizeDeviceModel(event.DeviceModel)
			if err := h.validator.ValidateCrash(&event); err != nil {
				rejected++
				continue
			}
			crashes = append(crashes, event)

		default:
			rejected++
		}
	}

	// Insert events into ClickHouse
	ctx := r.Context()

	if len(perfSamples) > 0 {
		if err := h.repo.InsertPerfSamples(ctx, perfSamples); err != nil {
			h.logger.Error("failed to insert perf samples", zap.Error(err))
		}
	}
	if len(janks) > 0 {
		if err := h.repo.InsertJanks(ctx, janks); err != nil {
			h.logger.Error("failed to insert janks", zap.Error(err))
		}
	}
	if len(startups) > 0 {
		if err := h.repo.InsertStartups(ctx, startups); err != nil {
			h.logger.Error("failed to insert startups", zap.Error(err))
		}
	}
	if len(sceneLoads) > 0 {
		if err := h.repo.InsertSceneLoads(ctx, sceneLoads); err != nil {
			h.logger.Error("failed to insert scene loads", zap.Error(err))
		}
	}
	if len(exceptions) > 0 {
		if err := h.repo.InsertExceptions(ctx, exceptions); err != nil {
			h.logger.Error("failed to insert exceptions", zap.Error(err))
		}
	}
	if len(crashes) > 0 {
		if err := h.repo.InsertCrashes(ctx, crashes); err != nil {
			h.logger.Error("failed to insert crashes", zap.Error(err))
		}
	}

	accepted := len(perfSamples) + len(janks) + len(startups) + len(sceneLoads) + len(exceptions) + len(crashes)

	h.logger.Info("ingested events",
		zap.Int("accepted", accepted),
		zap.Int("rejected", rejected),
		zap.String("client_ip", clientIP),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(IngestResponse{
		Accepted: accepted,
		Rejected: rejected,
		Errors:   errors,
	})
}

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func TestIngestHandler_IngestEvents_EmptyBody(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewIngestHandler(nil, logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.IngestEvents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestIngestHandler_IngestEvents_InvalidJSON(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewIngestHandler(nil, logger)

	body := bytes.NewBufferString("not valid json")
	req := httptest.NewRequest(http.MethodPost, "/v1/events", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.IngestEvents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestIngestHandler_IngestEvents_EmptyBatch(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewIngestHandler(nil, logger)

	batch := models.EventBatch{Events: []models.RawEvent{}}
	body, _ := json.Marshal(batch)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.IngestEvents(w, req)

	// Empty batch should be accepted
	if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Errorf("expected status 200 or 202, got %d", w.Code)
	}
}

func TestIngestHandler_IngestEvents_ValidBatch(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewIngestHandler(nil, logger)

	batch := models.EventBatch{
		Events: []models.RawEvent{
			{
				Type:      models.EventType("perf_sample"),
				Timestamp: 1705315800000,
			},
		},
	}
	body, _ := json.Marshal(batch)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.IngestEvents(w, req)

	// Without a real repository, this will fail but should handle gracefully
	// The important thing is it doesn't panic
	if w.Code == http.StatusOK || w.Code == http.StatusAccepted || w.Code == http.StatusInternalServerError {
		t.Logf("status code: %d (expected given no repository)", w.Code)
	}
}

func TestIngestHandler_IngestEvents_MultipleEventTypes(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewIngestHandler(nil, logger)

	ts := int64(1705315800000)

	events := []models.RawEvent{
		{Type: models.EventType("perf_sample"), Timestamp: ts},
		{Type: models.EventType("jank"), Timestamp: ts},
		{Type: models.EventType("startup"), Timestamp: ts},
		{Type: models.EventType("exception"), Timestamp: ts},
		{Type: models.EventType("crash"), Timestamp: ts},
	}

	batch := models.EventBatch{Events: events}
	body, _ := json.Marshal(batch)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.IngestEvents(w, req)

	// Without repository, will fail but should not panic
	t.Logf("status code: %d", w.Code)
}

func TestIngestHandler_IngestEvents_UnknownEventType(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewIngestHandler(nil, logger)

	batch := models.EventBatch{
		Events: []models.RawEvent{
			{Type: models.EventType("unknown_type"), Timestamp: 1705315800000},
		},
	}
	body, _ := json.Marshal(batch)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.IngestEvents(w, req)

	// Unknown event types should be handled gracefully
	t.Logf("status code: %d", w.Code)
}

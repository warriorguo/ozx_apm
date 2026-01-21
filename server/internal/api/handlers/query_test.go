package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestQueryHandler_GetFPSMetrics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewQueryHandler(nil, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
	}{
		{
			name:           "default params - no repository",
			query:          "",
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "with time range",
			query:          "?start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z",
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "with filters",
			query:          "?app_version=1.0.0&platform=Android",
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/metrics/fps"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.GetFPSMetrics(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestQueryHandler_GetStartupMetrics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewQueryHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics/startup", nil)
	w := httptest.NewRecorder()

	handler.GetStartupMetrics(w, req)

	// Without repository, will return error
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestQueryHandler_GetJankMetrics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewQueryHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics/jank", nil)
	w := httptest.NewRecorder()

	handler.GetJankMetrics(w, req)

	// Without repository, will return error
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestQueryHandler_GetExceptions(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewQueryHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/exceptions", nil)
	w := httptest.NewRecorder()

	handler.GetExceptions(w, req)

	// Without repository, will return error
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestQueryHandler_GetCrashes(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewQueryHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/crashes", nil)
	w := httptest.NewRecorder()

	handler.GetCrashes(w, req)

	// Without repository, will return error
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestNewQueryHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewQueryHandler(nil, logger)

	if handler == nil {
		t.Error("expected non-nil handler")
	}
}

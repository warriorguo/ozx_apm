package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler_Health(t *testing.T) {
	tests := []struct {
		name           string
		expectedStatus int
		expectDegraded bool
	}{
		{
			name:           "no repository - degraded status",
			expectedStatus: http.StatusServiceUnavailable,
			expectDegraded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHealthHandler(nil)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()

			handler.Health(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var resp HealthResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Errorf("failed to unmarshal: %v", err)
			}

			if tt.expectDegraded {
				if resp.Status != "degraded" {
					t.Errorf("expected status=degraded, got %s", resp.Status)
				}
				if resp.Database != "not configured" {
					t.Errorf("expected database=not configured, got %s", resp.Database)
				}
			} else {
				if resp.Status != "ok" {
					t.Errorf("expected status=ok, got %s", resp.Status)
				}
			}
		})
	}
}

func TestHealthHandler_ResponseHeaders(t *testing.T) {
	handler := NewHealthHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.Health(w, req)

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %s", contentType)
	}
}

func TestHealthResponse_JSON(t *testing.T) {
	resp := HealthResponse{
		Status:   "ok",
		Database: "ok",
		Version:  "1.0.0",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded HealthResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Status != "ok" {
		t.Errorf("expected status=ok, got %s", decoded.Status)
	}
	if decoded.Version != "1.0.0" {
		t.Errorf("expected version=1.0.0, got %s", decoded.Version)
	}
}

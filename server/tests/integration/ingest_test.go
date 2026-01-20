package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// Note: These tests require a running ClickHouse instance
// Skip them if ClickHouse is not available

func TestIngestEvents_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup
	logger := zap.NewNop()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:         "localhost",
			Port:         8080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		ClickHouse: config.ClickHouseConfig{
			Host:     "localhost",
			Port:     9000,
			Database: "ozx_apm_test",
			Username: "default",
			Password: "",
		},
		Auth: config.AuthConfig{
			Enabled: false,
		},
		RateLimit: config.RateLimitConfig{
			Enabled: false,
		},
	}

	// Try to connect to ClickHouse
	client, err := storage.NewClickHouseClient(&cfg.ClickHouse, logger)
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()

	repo := storage.NewRepository(client, logger)
	router := api.NewRouter(cfg, repo, logger)

	// Create test request
	payload := map[string]interface{}{
		"events": []map[string]interface{}{
			{
				"type":         "perf_sample",
				"timestamp":    time.Now().UnixMilli(),
				"app_version":  "1.0.0",
				"platform":     "Android",
				"device_model": "Pixel 6",
				"os_version":   "Android 12",
				"session_id":   "test-session",
				"device_id":    "test-device",
				"scene":        "MainMenu",
				"fps":          60.0,
				"frame_time_ms": 16.67,
			},
		},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Execute
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp["accepted"].(float64) != 1 {
		t.Errorf("Expected 1 accepted event, got %v", resp["accepted"])
	}
}

func TestIngestEvents_InvalidJSON(t *testing.T) {
	logger := zap.NewNop()
	cfg := &config.Config{
		Auth: config.AuthConfig{Enabled: false},
		RateLimit: config.RateLimitConfig{Enabled: false},
	}

	// Create router without ClickHouse (for JSON parsing test)
	router := api.NewRouter(cfg, nil, logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestIngestEvents_EmptyBatch(t *testing.T) {
	logger := zap.NewNop()
	cfg := &config.Config{
		Auth: config.AuthConfig{Enabled: false},
		RateLimit: config.RateLimitConfig{Enabled: false},
	}

	router := api.NewRouter(cfg, nil, logger)

	payload := map[string]interface{}{
		"events": []map[string]interface{}{},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["accepted"].(float64) != 0 {
		t.Errorf("Expected 0 accepted events, got %v", resp["accepted"])
	}
}

func TestHealthEndpoint(t *testing.T) {
	logger := zap.NewNop()
	cfg := &config.Config{
		Auth: config.AuthConfig{Enabled: false},
		RateLimit: config.RateLimitConfig{Enabled: false},
	}

	router := api.NewRouter(cfg, nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Without ClickHouse, health check should return degraded
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["version"] != "1.0.0" {
		t.Errorf("Expected version 1.0.0, got %v", resp["version"])
	}
}

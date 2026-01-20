package integration

import (
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

func TestQueryFPSMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logger := zap.NewNop()
	cfg := &config.Config{
		ClickHouse: config.ClickHouseConfig{
			Host:     "localhost",
			Port:     9000,
			Database: "ozx_apm_test",
			Username: "default",
			Password: "",
		},
		Auth:      config.AuthConfig{Enabled: false},
		RateLimit: config.RateLimitConfig{Enabled: false},
	}

	client, err := storage.NewClickHouseClient(&cfg.ClickHouse, logger)
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()

	repo := storage.NewRepository(client, logger)
	router := api.NewRouter(cfg, repo, logger)

	// Query FPS metrics
	startTime := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	endTime := time.Now().Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics/fps?start_time="+startTime+"&end_time="+endTime, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Data field should exist (even if empty)
	if _, ok := resp["data"]; !ok {
		t.Error("Response missing 'data' field")
	}
}

func TestQueryStartupMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logger := zap.NewNop()
	cfg := &config.Config{
		ClickHouse: config.ClickHouseConfig{
			Host:     "localhost",
			Port:     9000,
			Database: "ozx_apm_test",
			Username: "default",
			Password: "",
		},
		Auth:      config.AuthConfig{Enabled: false},
		RateLimit: config.RateLimitConfig{Enabled: false},
	}

	client, err := storage.NewClickHouseClient(&cfg.ClickHouse, logger)
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()

	repo := storage.NewRepository(client, logger)
	router := api.NewRouter(cfg, repo, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics/startup", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestQueryExceptions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logger := zap.NewNop()
	cfg := &config.Config{
		ClickHouse: config.ClickHouseConfig{
			Host:     "localhost",
			Port:     9000,
			Database: "ozx_apm_test",
			Username: "default",
			Password: "",
		},
		Auth:      config.AuthConfig{Enabled: false},
		RateLimit: config.RateLimitConfig{Enabled: false},
	}

	client, err := storage.NewClickHouseClient(&cfg.ClickHouse, logger)
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()

	repo := storage.NewRepository(client, logger)
	router := api.NewRouter(cfg, repo, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/exceptions?app_version=1.0.0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestQueryCrashes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logger := zap.NewNop()
	cfg := &config.Config{
		ClickHouse: config.ClickHouseConfig{
			Host:     "localhost",
			Port:     9000,
			Database: "ozx_apm_test",
			Username: "default",
			Password: "",
		},
		Auth:      config.AuthConfig{Enabled: false},
		RateLimit: config.RateLimitConfig{Enabled: false},
	}

	client, err := storage.NewClickHouseClient(&cfg.ClickHouse, logger)
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()

	repo := storage.NewRepository(client, logger)
	router := api.NewRouter(cfg, repo, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/crashes?platform=Android&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Tests for actual DashboardHandler with nil repository

func TestNewDashboardHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewDashboardHandler(nil, logger)
	if handler == nil {
		t.Error("expected non-nil handler")
	}
}

func TestDashboardHandler_GetSummary_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewDashboardHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/summary", nil)
	w := httptest.NewRecorder()

	handler.GetSummary(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestDashboardHandler_GetTimeSeries_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewDashboardHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/timeseries?metric=fps", nil)
	w := httptest.NewRecorder()

	handler.GetTimeSeries(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestDashboardHandler_GetDistribution_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewDashboardHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/distribution?metric=fps", nil)
	w := httptest.NewRecorder()

	handler.GetDistribution(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestDashboardHandler_GetAppVersions_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewDashboardHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/versions", nil)
	w := httptest.NewRecorder()

	handler.GetAppVersions(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestDashboardHandler_GetScenes_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewDashboardHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/scenes", nil)
	w := httptest.NewRecorder()

	handler.GetScenes(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

// MockRepository implements a mock repository for testing
type MockRepository struct {
	SummaryFunc      func(ctx context.Context, startTime, endTime time.Time, appVersion, platform string) (*models.DashboardSummary, error)
	TimeSeriesFunc   func(ctx context.Context, metric string, startTime, endTime time.Time, interval, appVersion, platform string) ([]models.TimeSeriesPoint, error)
	DistributionFunc func(ctx context.Context, metric string, startTime, endTime time.Time, appVersion, platform, scene string) (*models.DistributionResponse, error)
	VersionsFunc     func(ctx context.Context) ([]string, error)
	ScenesFunc       func(ctx context.Context, appVersion string) ([]string, error)
}

func (m *MockRepository) GetDashboardSummary(ctx context.Context, startTime, endTime time.Time, appVersion, platform string) (*models.DashboardSummary, error) {
	if m.SummaryFunc != nil {
		return m.SummaryFunc(ctx, startTime, endTime, appVersion, platform)
	}
	return &models.DashboardSummary{
		TotalSessions:  1000,
		TotalEvents:    50000,
		CrashCount:     10,
		CrashRate:      0.01,
		ExceptionCount: 100,
		JankCount:      50,
		AvgFPS:         58.5,
		AvgStartupMs:   2500,
	}, nil
}

func (m *MockRepository) GetTimeSeries(ctx context.Context, metric string, startTime, endTime time.Time, interval, appVersion, platform string) ([]models.TimeSeriesPoint, error) {
	if m.TimeSeriesFunc != nil {
		return m.TimeSeriesFunc(ctx, metric, startTime, endTime, interval, appVersion, platform)
	}
	return []models.TimeSeriesPoint{
		{Timestamp: time.Now().Add(-1 * time.Hour), Value: 60.0},
		{Timestamp: time.Now(), Value: 59.5},
	}, nil
}

func (m *MockRepository) GetDistribution(ctx context.Context, metric string, startTime, endTime time.Time, appVersion, platform, scene string) (*models.DistributionResponse, error) {
	if m.DistributionFunc != nil {
		return m.DistributionFunc(ctx, metric, startTime, endTime, appVersion, platform, scene)
	}
	return &models.DistributionResponse{
		Metric: metric,
		Buckets: []models.DistributionBucket{
			{Bucket: "0-10", Count: 100, Pct: 10},
			{Bucket: "10-20", Count: 500, Pct: 50},
			{Bucket: "20-30", Count: 400, Pct: 40},
		},
		P50: 15.0,
		P90: 25.0,
		P95: 28.0,
		P99: 30.0,
	}, nil
}

func (m *MockRepository) GetAppVersions(ctx context.Context) ([]string, error) {
	if m.VersionsFunc != nil {
		return m.VersionsFunc(ctx)
	}
	return []string{"1.0.0", "1.1.0", "1.2.0"}, nil
}

func (m *MockRepository) GetScenes(ctx context.Context, appVersion string) ([]string, error) {
	if m.ScenesFunc != nil {
		return m.ScenesFunc(ctx, appVersion)
	}
	return []string{"MainMenu", "GamePlay", "Settings"}, nil
}

// DashboardRepository interface for dependency injection
type DashboardRepository interface {
	GetDashboardSummary(ctx context.Context, startTime, endTime time.Time, appVersion, platform string) (*models.DashboardSummary, error)
	GetTimeSeries(ctx context.Context, metric string, startTime, endTime time.Time, interval, appVersion, platform string) ([]models.TimeSeriesPoint, error)
	GetDistribution(ctx context.Context, metric string, startTime, endTime time.Time, appVersion, platform, scene string) (*models.DistributionResponse, error)
	GetAppVersions(ctx context.Context) ([]string, error)
	GetScenes(ctx context.Context, appVersion string) ([]string, error)
}

// TestDashboardHandler for testing with mock repository
type TestDashboardHandler struct {
	repo   DashboardRepository
	logger *zap.Logger
}

func NewTestDashboardHandler(repo DashboardRepository, logger *zap.Logger) *TestDashboardHandler {
	return &TestDashboardHandler{repo: repo, logger: logger}
}

func (h *TestDashboardHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	if start := q.Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			startTime = t
		}
	}
	if end := q.Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			endTime = t
		}
	}

	appVersion := q.Get("app_version")
	platform := q.Get("platform")

	summary, err := h.repo.GetDashboardSummary(ctx, startTime, endTime, appVersion, platform)
	if err != nil {
		h.logger.Error("failed to get dashboard summary", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	summary.TimeRange = models.TimeRange{Start: startTime, End: endTime}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

func (h *TestDashboardHandler) GetTimeSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	metric := q.Get("metric")
	if metric == "" {
		http.Error(w, "metric parameter required", http.StatusBadRequest)
		return
	}

	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	if start := q.Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			startTime = t
		}
	}
	if end := q.Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			endTime = t
		}
	}

	appVersion := q.Get("app_version")
	platform := q.Get("platform")

	duration := endTime.Sub(startTime)
	interval := "1 HOUR"
	if duration > 7*24*time.Hour {
		interval = "1 DAY"
	} else if duration < 6*time.Hour {
		interval = "5 MINUTE"
	}

	data, err := h.repo.GetTimeSeries(ctx, metric, startTime, endTime, interval, appVersion, platform)
	if err != nil {
		h.logger.Error("failed to get time series", zap.Error(err), zap.String("metric", metric))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := models.TimeSeriesResponse{
		Metric: metric,
		Data:   data,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *TestDashboardHandler) GetDistribution(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	metric := q.Get("metric")
	if metric == "" {
		http.Error(w, "metric parameter required", http.StatusBadRequest)
		return
	}

	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	if start := q.Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			startTime = t
		}
	}
	if end := q.Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			endTime = t
		}
	}

	appVersion := q.Get("app_version")
	platform := q.Get("platform")
	scene := q.Get("scene")

	dist, err := h.repo.GetDistribution(ctx, metric, startTime, endTime, appVersion, platform, scene)
	if err != nil {
		h.logger.Error("failed to get distribution", zap.Error(err), zap.String("metric", metric))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dist)
}

func (h *TestDashboardHandler) GetAppVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	versions, err := h.repo.GetAppVersions(ctx)
	if err != nil {
		h.logger.Error("failed to get app versions", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"versions": versions,
	})
}

func (h *TestDashboardHandler) GetScenes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	appVersion := r.URL.Query().Get("app_version")

	scenes, err := h.repo.GetScenes(ctx, appVersion)
	if err != nil {
		h.logger.Error("failed to get scenes", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"scenes": scenes,
	})
}

func TestGetSummary(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockRepository{}
	handler := NewTestDashboardHandler(mockRepo, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name:           "default time range",
			query:          "",
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var summary models.DashboardSummary
				if err := json.Unmarshal(body, &summary); err != nil {
					t.Errorf("failed to unmarshal response: %v", err)
				}
				if summary.TotalSessions != 1000 {
					t.Errorf("expected TotalSessions=1000, got %d", summary.TotalSessions)
				}
			},
		},
		{
			name:           "with time range",
			query:          "?start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z",
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var summary models.DashboardSummary
				if err := json.Unmarshal(body, &summary); err != nil {
					t.Errorf("failed to unmarshal response: %v", err)
				}
			},
		},
		{
			name:           "with filters",
			query:          "?app_version=1.0.0&platform=Android",
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var summary models.DashboardSummary
				if err := json.Unmarshal(body, &summary); err != nil {
					t.Errorf("failed to unmarshal response: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/summary"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.GetSummary(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestGetTimeSeries(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockRepository{}
	handler := NewTestDashboardHandler(mockRepo, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
	}{
		{
			name:           "valid metric",
			query:          "?metric=fps",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "missing metric",
			query:          "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "with time range",
			query:          "?metric=crash_count&start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "short time range",
			query:          "?metric=fps&start_time=2024-01-01T00:00:00Z&end_time=2024-01-01T05:00:00Z",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "long time range",
			query:          "?metric=fps&start_time=2024-01-01T00:00:00Z&end_time=2024-01-15T00:00:00Z",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/timeseries"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.GetTimeSeries(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestGetDistribution(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockRepository{}
	handler := NewTestDashboardHandler(mockRepo, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
	}{
		{
			name:           "valid metric",
			query:          "?metric=fps",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "missing metric",
			query:          "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "with scene filter",
			query:          "?metric=frame_time&scene=GamePlay",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/distribution"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.GetDistribution(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestGetAppVersions(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockRepository{}
	handler := NewTestDashboardHandler(mockRepo, logger)

	req := httptest.NewRequest(http.MethodGet, "/api/versions", nil)
	w := httptest.NewRecorder()

	handler.GetAppVersions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var result map[string][]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Errorf("failed to unmarshal response: %v", err)
	}

	versions := result["versions"]
	if len(versions) != 3 {
		t.Errorf("expected 3 versions, got %d", len(versions))
	}
}

func TestGetScenes(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockRepository{}
	handler := NewTestDashboardHandler(mockRepo, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
	}{
		{
			name:           "without filter",
			query:          "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with version filter",
			query:          "?app_version=1.0.0",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/scenes"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.GetScenes(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

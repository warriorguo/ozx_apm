package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Tests for actual CrashHandler and ExceptionHandler with nil repository

func TestNewCrashHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewCrashHandler(nil, logger)
	if handler == nil {
		t.Error("expected non-nil handler")
	}
}

func TestNewExceptionHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewExceptionHandler(nil, logger)
	if handler == nil {
		t.Error("expected non-nil handler")
	}
}

func TestCrashHandler_ListCrashes_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewCrashHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/crashes", nil)
	w := httptest.NewRecorder()

	handler.ListCrashes(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestCrashHandler_GetCrashDetail_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewCrashHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/crashes/detail?fingerprint=crash123", nil)
	w := httptest.NewRecorder()

	handler.GetCrashDetail(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestExceptionHandler_ListExceptions_NilRepo(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewExceptionHandler(nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/exceptions", nil)
	w := httptest.NewRecorder()

	handler.ListExceptions(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

// MockCrashRepository for crash handler tests
type MockCrashRepository struct {
	CrashGroupsFunc    func(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.CrashGroup, int64, error)
	CrashDetailFunc    func(ctx context.Context, fingerprint string, startTime, endTime time.Time) (*models.CrashDetail, error)
	ExceptionGroupFunc func(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.ExceptionGroup, int64, error)
}

func (m *MockCrashRepository) GetCrashGroups(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.CrashGroup, int64, error) {
	if m.CrashGroupsFunc != nil {
		return m.CrashGroupsFunc(ctx, startTime, endTime, appVersion, platform, page, pageSize)
	}
	return []models.CrashGroup{
		{
			Fingerprint:      "crash123",
			CrashType:        "SIGSEGV",
			SampleMessage:    "Segmentation fault",
			Count:            100,
			SessionCount:     50,
			FirstSeen:        time.Now().Add(-7 * 24 * time.Hour),
			LastSeen:         time.Now(),
			AffectedVersions: []string{"1.0.0", "1.1.0"},
			TopDevices:       []string{"Pixel 6", "Galaxy S21"},
		},
	}, 1, nil
}

func (m *MockCrashRepository) GetCrashDetail(ctx context.Context, fingerprint string, startTime, endTime time.Time) (*models.CrashDetail, error) {
	if m.CrashDetailFunc != nil {
		return m.CrashDetailFunc(ctx, fingerprint, startTime, endTime)
	}
	if fingerprint == "" {
		return nil, nil
	}
	return &models.CrashDetail{
		Fingerprint:  fingerprint,
		CrashType:    "SIGSEGV",
		Stack:        "at NativeMethod()\nat UnityEngine.Foo()",
		Count:        100,
		SessionCount: 50,
		FirstSeen:    time.Now().Add(-7 * 24 * time.Hour),
		LastSeen:     time.Now(),
		Occurrences: []models.CrashOccurrence{
			{
				Timestamp:   time.Now(),
				AppVersion:  "1.0.0",
				Platform:    "Android",
				DeviceModel: "Pixel 6",
				OSVersion:   "Android 13",
				Scene:       "GamePlay",
				Breadcrumbs: []string{"scene_load", "asset_load", "user_action"},
			},
		},
		VersionDist: []models.VersionDist{
			{Version: "1.0.0", Count: 80},
			{Version: "1.1.0", Count: 20},
		},
		DeviceDist: []models.DeviceDist{
			{Device: "Pixel 6", Count: 60},
			{Device: "Galaxy S21", Count: 40},
		},
		OSDist: []models.OSDist{
			{OS: "Android 13", Count: 70},
			{OS: "Android 12", Count: 30},
		},
	}, nil
}

func (m *MockCrashRepository) GetExceptionGroups(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.ExceptionGroup, int64, error) {
	if m.ExceptionGroupFunc != nil {
		return m.ExceptionGroupFunc(ctx, startTime, endTime, appVersion, platform, page, pageSize)
	}
	return []models.ExceptionGroup{
		{
			Fingerprint:  "exc123",
			Message:      "NullReferenceException: Object reference not set",
			Count:        500,
			SessionCount: 200,
			FirstSeen:    time.Now().Add(-7 * 24 * time.Hour),
			LastSeen:     time.Now(),
		},
	}, 1, nil
}

// CrashRepository interface
type CrashRepository interface {
	GetCrashGroups(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.CrashGroup, int64, error)
	GetCrashDetail(ctx context.Context, fingerprint string, startTime, endTime time.Time) (*models.CrashDetail, error)
	GetExceptionGroups(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.ExceptionGroup, int64, error)
}

// TestCrashHandler for testing
type TestCrashHandler struct {
	repo   CrashRepository
	logger *zap.Logger
}

func NewTestCrashHandler(repo CrashRepository, logger *zap.Logger) *TestCrashHandler {
	return &TestCrashHandler{repo: repo, logger: logger}
}

func (h *TestCrashHandler) ListCrashes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	endTime := time.Now()
	startTime := endTime.Add(-7 * 24 * time.Hour)

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

	page := 1
	if p := q.Get("page"); p != "" {
		if v, err := parseInt(p); err == nil && v > 0 {
			page = v
		}
	}

	pageSize := 20
	if ps := q.Get("page_size"); ps != "" {
		if v, err := parseInt(ps); err == nil && v > 0 && v <= 100 {
			pageSize = v
		}
	}

	crashes, totalCount, err := h.repo.GetCrashGroups(ctx, startTime, endTime, appVersion, platform, page, pageSize)
	if err != nil {
		h.logger.Error("failed to get crash groups", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := models.CrashListResponse{
		Crashes:    crashes,
		TotalCount: totalCount,
		Page:       page,
		PageSize:   pageSize,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *TestCrashHandler) GetCrashDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	fingerprint := q.Get("fingerprint")
	if fingerprint == "" {
		http.Error(w, "fingerprint parameter required", http.StatusBadRequest)
		return
	}

	endTime := time.Now()
	startTime := endTime.Add(-30 * 24 * time.Hour)

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

	detail, err := h.repo.GetCrashDetail(ctx, fingerprint, startTime, endTime)
	if err != nil {
		h.logger.Error("failed to get crash detail", zap.Error(err), zap.String("fingerprint", fingerprint))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if detail == nil {
		http.Error(w, "crash not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(detail)
}

// TestExceptionHandler for testing
type TestExceptionHandler struct {
	repo   CrashRepository
	logger *zap.Logger
}

func NewTestExceptionHandler(repo CrashRepository, logger *zap.Logger) *TestExceptionHandler {
	return &TestExceptionHandler{repo: repo, logger: logger}
}

func (h *TestExceptionHandler) ListExceptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	endTime := time.Now()
	startTime := endTime.Add(-7 * 24 * time.Hour)

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

	page := 1
	if p := q.Get("page"); p != "" {
		if v, err := parseInt(p); err == nil && v > 0 {
			page = v
		}
	}

	pageSize := 20
	if ps := q.Get("page_size"); ps != "" {
		if v, err := parseInt(ps); err == nil && v > 0 && v <= 100 {
			pageSize = v
		}
	}

	exceptions, totalCount, err := h.repo.GetExceptionGroups(ctx, startTime, endTime, appVersion, platform, page, pageSize)
	if err != nil {
		h.logger.Error("failed to get exception groups", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := models.ExceptionListResponse{
		Exceptions: exceptions,
		TotalCount: totalCount,
		Page:       page,
		PageSize:   pageSize,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func parseInt(s string) (int, error) {
	i := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid number")
		}
		i = i*10 + int(c-'0')
	}
	return i, nil
}

func TestListCrashes(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockCrashRepository{}
	handler := NewTestCrashHandler(mockRepo, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name:           "default params",
			query:          "",
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp models.CrashListResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Errorf("failed to unmarshal: %v", err)
				}
				if len(resp.Crashes) != 1 {
					t.Errorf("expected 1 crash, got %d", len(resp.Crashes))
				}
			},
		},
		{
			name:           "with pagination",
			query:          "?page=2&page_size=10",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with filters",
			query:          "?app_version=1.0.0&platform=Android",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with time range",
			query:          "?start_time=2024-01-01T00:00:00Z&end_time=2024-01-07T00:00:00Z",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid page",
			query:          "?page=-1",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "page_size too large",
			query:          "?page_size=200",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/crashes"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.ListCrashes(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestListCrashes_Error(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockCrashRepository{
		CrashGroupsFunc: func(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.CrashGroup, int64, error) {
			return nil, 0, errors.New("database error")
		},
	}
	handler := NewTestCrashHandler(mockRepo, logger)

	req := httptest.NewRequest(http.MethodGet, "/api/crashes", nil)
	w := httptest.NewRecorder()

	handler.ListCrashes(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestGetCrashDetail(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockCrashRepository{}
	handler := NewTestCrashHandler(mockRepo, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name:           "valid fingerprint",
			query:          "?fingerprint=crash123",
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var detail models.CrashDetail
				if err := json.Unmarshal(body, &detail); err != nil {
					t.Errorf("failed to unmarshal: %v", err)
				}
				if detail.Fingerprint != "crash123" {
					t.Errorf("expected fingerprint crash123, got %s", detail.Fingerprint)
				}
			},
		},
		{
			name:           "missing fingerprint",
			query:          "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "with time range",
			query:          "?fingerprint=crash123&start_time=2024-01-01T00:00:00Z&end_time=2024-01-31T00:00:00Z",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/crashes/detail"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.GetCrashDetail(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestGetCrashDetail_NotFound(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockCrashRepository{
		CrashDetailFunc: func(ctx context.Context, fingerprint string, startTime, endTime time.Time) (*models.CrashDetail, error) {
			return nil, nil
		},
	}
	handler := NewTestCrashHandler(mockRepo, logger)

	req := httptest.NewRequest(http.MethodGet, "/api/crashes/detail?fingerprint=notexist", nil)
	w := httptest.NewRecorder()

	handler.GetCrashDetail(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, w.Code)
	}
}

func TestGetCrashDetail_Error(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockCrashRepository{
		CrashDetailFunc: func(ctx context.Context, fingerprint string, startTime, endTime time.Time) (*models.CrashDetail, error) {
			return nil, errors.New("database error")
		},
	}
	handler := NewTestCrashHandler(mockRepo, logger)

	req := httptest.NewRequest(http.MethodGet, "/api/crashes/detail?fingerprint=crash123", nil)
	w := httptest.NewRecorder()

	handler.GetCrashDetail(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

func TestListExceptions(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockCrashRepository{}
	handler := NewTestExceptionHandler(mockRepo, logger)

	tests := []struct {
		name           string
		query          string
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name:           "default params",
			query:          "",
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp models.ExceptionListResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Errorf("failed to unmarshal: %v", err)
				}
				if len(resp.Exceptions) != 1 {
					t.Errorf("expected 1 exception, got %d", len(resp.Exceptions))
				}
			},
		},
		{
			name:           "with pagination",
			query:          "?page=1&page_size=50",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with filters",
			query:          "?app_version=1.0.0&platform=iOS",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/exceptions"+tt.query, nil)
			w := httptest.NewRecorder()

			handler.ListExceptions(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestListExceptions_Error(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockRepo := &MockCrashRepository{
		ExceptionGroupFunc: func(ctx context.Context, startTime, endTime time.Time, appVersion, platform string, page, pageSize int) ([]models.ExceptionGroup, int64, error) {
			return nil, 0, errors.New("database error")
		},
	}
	handler := NewTestExceptionHandler(mockRepo, logger)

	req := httptest.NewRequest(http.MethodGet, "/api/exceptions", nil)
	w := httptest.NewRecorder()

	handler.ListExceptions(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}
}

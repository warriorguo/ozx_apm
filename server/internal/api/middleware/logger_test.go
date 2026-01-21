package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestLogger(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		path         string
		status       int
		expectFields []string
	}{
		{
			name:         "GET request",
			method:       http.MethodGet,
			path:         "/api/test",
			status:       http.StatusOK,
			expectFields: []string{"method", "path", "status", "duration"},
		},
		{
			name:         "POST request",
			method:       http.MethodPost,
			path:         "/v1/events",
			status:       http.StatusCreated,
			expectFields: []string{"method", "path", "status"},
		},
		{
			name:         "error response",
			method:       http.MethodGet,
			path:         "/api/error",
			status:       http.StatusInternalServerError,
			expectFields: []string{"method", "path", "status"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			encoder := zapcore.NewJSONEncoder(zap.NewDevelopmentEncoderConfig())
			core := zapcore.NewCore(encoder, zapcore.AddSync(&logBuf), zapcore.DebugLevel)
			logger := zap.New(core)

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			})

			logHandler := Logger(logger)(handler)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()

			logHandler.ServeHTTP(w, req)

			if w.Code != tt.status {
				t.Errorf("expected status %d, got %d", tt.status, w.Code)
			}

			logOutput := logBuf.String()
			for _, field := range tt.expectFields {
				if !strings.Contains(logOutput, field) {
					t.Errorf("expected log to contain field %q, log: %s", field, logOutput)
				}
			}
		})
	}
}

func TestLogger_WithRequestID(t *testing.T) {
	var logBuf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewDevelopmentEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&logBuf), zapcore.DebugLevel)
	logger := zap.New(core)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	logHandler := Logger(logger)(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-Request-ID", "test-request-123")

	w := httptest.NewRecorder()
	logHandler.ServeHTTP(w, req)

	// Log should be recorded
	if logBuf.Len() == 0 {
		t.Error("expected log output")
	}
}

func TestLogger_Duration(t *testing.T) {
	var logBuf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewDevelopmentEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&logBuf), zapcore.DebugLevel)
	logger := zap.New(core)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	logHandler := Logger(logger)(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w := httptest.NewRecorder()

	logHandler.ServeHTTP(w, req)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "duration") {
		t.Errorf("expected log to contain duration field, log: %s", logOutput)
	}
}

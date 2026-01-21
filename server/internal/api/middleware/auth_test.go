package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuth(t *testing.T) {
	appKeys := map[string]string{
		"test-key-1": "TestApp1",
		"test-key-2": "TestApp2",
	}

	tests := []struct {
		name           string
		appKey         string
		expectedStatus int
	}{
		{
			name:           "valid app key",
			appKey:         "test-key-1",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "another valid app key",
			appKey:         "test-key-2",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid app key",
			appKey:         "invalid-key",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "missing app key",
			appKey:         "",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			authHandler := Auth(appKeys)(handler)

			req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
			if tt.appKey != "" {
				req.Header.Set("X-App-Key", tt.appKey)
			}

			w := httptest.NewRecorder()
			authHandler.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestAuth_EmptyAppKeys(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	authHandler := Auth(map[string]string{})(handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
	req.Header.Set("X-App-Key", "any-key")

	w := httptest.NewRecorder()
	authHandler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAuth_AppKeyHeader(t *testing.T) {
	if AppKeyHeader != "X-App-Key" {
		t.Errorf("expected AppKeyHeader=X-App-Key, got %s", AppKeyHeader)
	}
}

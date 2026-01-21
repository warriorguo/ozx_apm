package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	tests := []struct {
		name           string
		origins        []string
		requestOrigin  string
		method         string
		expectedOrigin string
		expectedStatus int
	}{
		{
			name:           "wildcard allows origin",
			origins:        []string{"*"},
			requestOrigin:  "http://example.com",
			method:         http.MethodGet,
			expectedOrigin: "http://example.com",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "specific origin allowed",
			origins:        []string{"http://localhost:3000", "http://admin.example.com"},
			requestOrigin:  "http://localhost:3000",
			method:         http.MethodGet,
			expectedOrigin: "http://localhost:3000",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "origin not in list",
			origins:        []string{"http://localhost:3000"},
			requestOrigin:  "http://attacker.com",
			method:         http.MethodGet,
			expectedOrigin: "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "preflight request",
			origins:        []string{"*"},
			requestOrigin:  "http://example.com",
			method:         http.MethodOptions,
			expectedOrigin: "http://example.com",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "no origin header",
			origins:        []string{"*"},
			requestOrigin:  "",
			method:         http.MethodGet,
			expectedOrigin: "",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			corsHandler := CORS(tt.origins)(handler)

			req := httptest.NewRequest(tt.method, "/api/test", nil)
			if tt.requestOrigin != "" {
				req.Header.Set("Origin", tt.requestOrigin)
			}
			if tt.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}

			w := httptest.NewRecorder()
			corsHandler.ServeHTTP(w, req)

			gotOrigin := w.Header().Get("Access-Control-Allow-Origin")
			if gotOrigin != tt.expectedOrigin {
				t.Errorf("expected Access-Control-Allow-Origin=%q, got %q", tt.expectedOrigin, gotOrigin)
			}

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Check other CORS headers when origin is allowed
			if tt.expectedOrigin != "" {
				if w.Header().Get("Access-Control-Allow-Methods") == "" {
					t.Error("expected Access-Control-Allow-Methods header")
				}
				if w.Header().Get("Access-Control-Allow-Headers") == "" {
					t.Error("expected Access-Control-Allow-Headers header")
				}
			}
		})
	}
}

func TestCORS_Headers(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	corsHandler := CORS([]string{"*"})(handler)

	req := httptest.NewRequest(http.MethodOptions, "/api/test", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, X-Custom-Header")

	w := httptest.NewRecorder()
	corsHandler.ServeHTTP(w, req)

	expectedHeaders := []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
		"Access-Control-Max-Age",
	}

	for _, h := range expectedHeaders {
		if w.Header().Get(h) == "" {
			t.Errorf("expected %s header to be set", h)
		}
	}
}

func TestCORS_Credentials(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	corsHandler := CORS([]string{"http://example.com"})(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Origin", "http://example.com")

	w := httptest.NewRecorder()
	corsHandler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("expected Access-Control-Allow-Credentials=true")
	}
}

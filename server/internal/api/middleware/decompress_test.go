package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecompress(t *testing.T) {
	tests := []struct {
		name            string
		contentEncoding string
		body            []byte
		compress        bool
		expectedBody    string
		expectedStatus  int
	}{
		{
			name:            "no compression",
			contentEncoding: "",
			body:            []byte("plain text body"),
			compress:        false,
			expectedBody:    "plain text body",
			expectedStatus:  http.StatusOK,
		},
		{
			name:            "gzip compressed",
			contentEncoding: "gzip",
			body:            []byte("compressed body"),
			compress:        true,
			expectedBody:    "compressed body",
			expectedStatus:  http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("failed to read body: %v", err)
				}
				gotBody = string(body)
				w.WriteHeader(http.StatusOK)
			})

			decompressHandler := Decompress(handler)

			var body io.Reader
			if tt.compress && len(tt.body) > 0 {
				var buf bytes.Buffer
				gw := gzip.NewWriter(&buf)
				gw.Write(tt.body)
				gw.Close()
				body = &buf
			} else {
				body = bytes.NewReader(tt.body)
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/events", body)
			if tt.contentEncoding != "" {
				req.Header.Set("Content-Encoding", tt.contentEncoding)
			}

			w := httptest.NewRecorder()
			decompressHandler.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if gotBody != tt.expectedBody {
				t.Errorf("expected body %q, got %q", tt.expectedBody, gotBody)
			}
		})
	}
}

func TestDecompress_InvalidGzip(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	decompressHandler := Decompress(handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader([]byte("not gzip data")))
	req.Header.Set("Content-Encoding", "gzip")

	w := httptest.NewRecorder()
	decompressHandler.ServeHTTP(w, req)

	// Invalid gzip should return 400
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestDecompress_EmptyGzip(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	decompressHandler := Decompress(handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Encoding", "gzip")

	w := httptest.NewRecorder()
	decompressHandler.ServeHTTP(w, req)

	// Empty body with gzip header is invalid
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestDecompress_LargePayload(t *testing.T) {
	largeData := make([]byte, 1024*1024) // 1MB
	for i := range largeData {
		largeData[i] = byte('a' + (i % 26))
	}

	var gotLen int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read body: %v", err)
		}
		gotLen = len(body)
		w.WriteHeader(http.StatusOK)
	})

	decompressHandler := Decompress(handler)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write(largeData)
	gw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/events", &buf)
	req.Header.Set("Content-Encoding", "gzip")

	w := httptest.NewRecorder()
	decompressHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	if gotLen != len(largeData) {
		t.Errorf("expected body length %d, got %d", len(largeData), gotLen)
	}
}

func TestDecompress_NoContentEncoding(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	})

	decompressHandler := Decompress(handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader([]byte("plain data")))
	// No Content-Encoding header

	w := httptest.NewRecorder()
	decompressHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	if w.Body.String() != "plain data" {
		t.Errorf("expected body 'plain data', got %s", w.Body.String())
	}
}

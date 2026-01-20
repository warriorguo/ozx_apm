package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

func Decompress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Encoding"), "gzip") {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "invalid gzip content", http.StatusBadRequest)
				return
			}
			defer gz.Close()
			r.Body = io.NopCloser(gz)
			r.Header.Del("Content-Encoding")
		}
		next.ServeHTTP(w, r)
	})
}

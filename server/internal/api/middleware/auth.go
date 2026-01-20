package middleware

import (
	"net/http"
)

const AppKeyHeader = "X-App-Key"

func Auth(validKeys map[string]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			appKey := r.Header.Get(AppKeyHeader)
			if appKey == "" {
				http.Error(w, "missing app key", http.StatusUnauthorized)
				return
			}

			if _, ok := validKeys[appKey]; !ok {
				http.Error(w, "invalid app key", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

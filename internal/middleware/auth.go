// Package middleware provides HTTP middleware for the carrier-gateway.
package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

type contextKey int

const clientIDKey contextKey = iota

// ClientIDFromContext returns the truncated API key identifier stored by
// RequireAPIKey, or empty string if the request was unauthenticated.
func ClientIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientIDKey).(string); ok {
		return v
	}
	return ""
}

// RequireAPIKey rejects requests that do not carry a valid Bearer token.
// Keys in skipPaths bypass authentication (e.g. /healthz, /metrics).
// Each key is identified by its first 8 characters + "..." for logging.
func RequireAPIKey(next http.Handler, keys []string, skipPaths []string, log *slog.Logger) http.Handler {
	// Pre-build lookup: full key → truncated ID.
	keyIDs := make(map[string]string, len(keys))
	keyBytes := make([][]byte, 0, len(keys))
	for _, k := range keys {
		id := k
		if len(id) > 8 {
			id = id[:8] + "..."
		}
		keyIDs[k] = id
		keyBytes = append(keyBytes, []byte(k))
	}

	skip := make(map[string]bool, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skip[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || token == r.Header.Get("Authorization") {
			writeUnauthorized(w)
			return
		}

		tokenB := []byte(token)
		for _, kb := range keyBytes {
			if subtle.ConstantTimeCompare(tokenB, kb) == 1 {
				ctx := context.WithValue(r.Context(), clientIDKey, keyIDs[string(kb)])
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		writeUnauthorized(w)
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{
		"error": "UNAUTHORIZED: missing or invalid API key",
	})
}

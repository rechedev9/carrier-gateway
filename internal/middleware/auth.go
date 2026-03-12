// Package middleware provides HTTP middleware for the carrier-gateway.
package middleware

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

type contextKey int

const clientIDKey contextKey = iota

// unauthorizedBody is pre-marshalled to avoid per-rejection allocations.
var unauthorizedBody = []byte(`{"error":"UNAUTHORIZED: missing or invalid API key"}` + "\n")

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
	// Pre-build parallel slices: keyBytes for comparison, keyIDs for context.
	keyBytes := make([][]byte, len(keys))
	keyIDs := make([]string, len(keys))
	for i, k := range keys {
		keyBytes[i] = []byte(k)
		id := k
		if len(id) > 8 {
			id = id[:8] + "..."
		}
		keyIDs[i] = id
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

		raw := r.Header.Get("Authorization")
		token := strings.TrimPrefix(raw, "Bearer ")
		if token == "" || token == raw {
			log.Warn("auth failed: missing bearer token",
				slog.String("path", r.URL.Path),
				slog.String("remote", r.RemoteAddr),
			)
			writeUnauthorized(w)
			return
		}

		// Check all keys to avoid leaking which index matched via timing.
		tokenB := []byte(token)
		matchIdx := -1
		for i, kb := range keyBytes {
			if subtle.ConstantTimeCompare(tokenB, kb) == 1 {
				matchIdx = i
			}
		}

		if matchIdx < 0 {
			log.Warn("auth failed: invalid API key",
				slog.String("path", r.URL.Path),
				slog.String("remote", r.RemoteAddr),
			)
			writeUnauthorized(w)
			return
		}

		ctx := context.WithValue(r.Context(), clientIDKey, keyIDs[matchIdx])
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write(unauthorizedBody)
}

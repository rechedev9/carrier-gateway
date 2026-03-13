// Package middleware provides HTTP middleware for the carrier-gateway.
package middleware

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type contextKey int

const clientIDKey contextKey = iota

// unauthorizedBody is pre-marshalled to avoid per-rejection allocations.
var unauthorizedBody = []byte(`{"error":"UNAUTHORIZED: missing or invalid API key"}` + "\n")

// tooManyRequestsBody is pre-marshalled for auth failure rate limiting.
var tooManyRequestsBody = []byte(`{"error":"TOO_MANY_REQUESTS: too many failed authentication attempts"}` + "\n")

// authFailureLimiter tracks per-IP rate limits on authentication failures.
// Burst of 10 failures allowed; tokens refill at ~1 per 6 seconds.
type authFailureLimiter struct {
	ips   sync.Map
	rate  rate.Limit
	burst int
}

func newAuthFailureLimiter() *authFailureLimiter {
	return &authFailureLimiter{
		rate:  rate.Every(6 * time.Second), // 1 token per 6 seconds
		burst: 10,
	}
}

// check returns false if the IP has exhausted its failure budget.
func (l *authFailureLimiter) check(ip string) bool {
	lim := l.getOrCreate(ip)
	return lim.Tokens() >= 1
}

// recordFailure consumes a token from the IP's bucket.
func (l *authFailureLimiter) recordFailure(ip string) {
	lim := l.getOrCreate(ip)
	lim.Allow()
}

func (l *authFailureLimiter) getOrCreate(ip string) *rate.Limiter {
	if v, ok := l.ips.Load(ip); ok {
		return v.(*rate.Limiter)
	}
	lim := rate.NewLimiter(l.rate, l.burst)
	actual, _ := l.ips.LoadOrStore(ip, lim)
	return actual.(*rate.Limiter)
}

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

	limiter := newAuthFailureLimiter()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skip[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip == "" {
			ip = r.RemoteAddr
		}

		if !limiter.check(ip) {
			log.Warn("auth rate limited",
				slog.String("path", r.URL.Path),
				slog.String("remote", r.RemoteAddr),
			)
			writeTooManyRequests(w)
			return
		}

		raw := r.Header.Get("Authorization")
		token := strings.TrimPrefix(raw, "Bearer ")
		if token == "" || token == raw {
			limiter.recordFailure(ip)
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
			limiter.recordFailure(ip)
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

func writeTooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusTooManyRequests)
	w.Write(tooManyRequestsBody)
}

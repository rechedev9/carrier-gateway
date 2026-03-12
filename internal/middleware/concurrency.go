package middleware

import (
	"log/slog"
	"net/http"
)

// concurrencyFullBody is pre-marshalled to avoid per-rejection allocations.
var concurrencyFullBody = []byte(`{"error":"SERVICE_UNAVAILABLE: too many concurrent requests"}` + "\n")

// LimitConcurrency caps in-flight requests to max. When the limit is reached,
// incoming requests receive 503 Service Unavailable with a Retry-After header.
func LimitConcurrency(next http.Handler, max int, log *slog.Logger) http.Handler {
	sem := make(chan struct{}, max)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			log.Warn("concurrency limit reached",
				slog.Int("max", max),
				slog.String("path", r.URL.Path),
				slog.String("remote", r.RemoteAddr),
			)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(concurrencyFullBody)
		}
	})
}

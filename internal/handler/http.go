// Package handler provides the HTTP layer for the carrier gateway.
// It registers POST /quotes and GET /metrics on a stdlib ServeMux,
// validates input, delegates to the orchestrator, and encodes JSON responses.
// Graceful shutdown is handled via the Shutdown method.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// Request size limits and defaults.
const (
	maxBodyBytes        = 1 << 20          // 1 MiB
	defaultTimeoutMs    = 5_000            // 5 seconds
	minTimeoutMs        = 100              // 100 ms
	maxTimeoutMs        = 30_000           // 30 seconds
	shutdownDrainWindow = 30 * time.Second // graceful drain timeout
)

// Handler holds all HTTP handler dependencies.
// The zero value is not valid — use New.
type Handler struct {
	orch     ports.OrchestratorPort
	metrics  ports.MetricsRecorder
	gatherer prometheus.Gatherer
	log      *slog.Logger
}

// New returns a Handler with all dependencies injected.
// gatherer must be the same registry where carrier metrics are registered.
func New(orch ports.OrchestratorPort, m ports.MetricsRecorder, gatherer prometheus.Gatherer, log *slog.Logger) *Handler {
	return &Handler{
		orch:     orch,
		metrics:  m,
		gatherer: gatherer,
		log:      log,
	}
}

// RegisterRoutes registers POST /quotes, GET /metrics, and GET /healthz on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /quotes", h.handlePostQuotes)
	mux.HandleFunc("GET /metrics", promhttp.HandlerFor(h.gatherer, promhttp.HandlerOpts{}).ServeHTTP)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// Shutdown drains in-flight requests using a 30-second drain context derived
// from the provided parent context. If the drain window expires before all
// connections close, it logs an error and returns an error — the caller should
// exit with code 1.
func (h *Handler) Shutdown(ctx context.Context, srv *http.Server) error {
	drainCtx, cancel := context.WithTimeout(ctx, shutdownDrainWindow)
	defer cancel()

	if err := srv.Shutdown(drainCtx); err != nil {
		h.log.Error("shutdown drain timeout exceeded",
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("http server shutdown: %w", err)
	}
	h.log.Info("http server shut down cleanly")
	return nil
}

// --- HTTP request/response types (unexported) ---

// quoteRequest is the inbound JSON schema for POST /quotes.
type quoteRequest struct {
	RequestID     string   `json:"request_id"`
	CoverageLines []string `json:"coverage_lines"`
	TimeoutMs     int      `json:"timeout_ms,omitempty"`
}

// quoteResponse is the outbound JSON schema for POST /quotes.
type quoteResponse struct {
	RequestID  string      `json:"request_id"`
	Quotes     []quoteItem `json:"quotes"`
	DurationMs int64       `json:"duration_ms"`
}

// quoteItem is a single carrier result inside quoteResponse.
type quoteItem struct {
	CarrierID    string `json:"carrier_id"`
	PremiumCents int64  `json:"premium_cents"`
	Currency     string `json:"currency"`
	IsHedged     bool   `json:"is_hedged"`
	LatencyMs    int64  `json:"latency_ms"`
}

// errorResponse is the outbound JSON schema for error responses.
type errorResponse struct {
	Error string `json:"error"`
}

// --- Handler methods ---

// handlePostQuotes handles POST /quotes.
func (h *Handler) handlePostQuotes(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var req quoteRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	// Before body is parsed, fall back to X-Request-ID header for log correlation.
	headerID := r.Header.Get("X-Request-ID")

	if err := dec.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			h.writeError(w, r, http.StatusBadRequest, headerID, "REQUEST_TOO_LARGE", "request body exceeds 1 MB limit")
			return
		}
		h.writeError(w, r, http.StatusBadRequest, headerID, "INVALID_JSON", "malformed JSON: "+err.Error())
		return
	}

	// After body is parsed, use the body's request_id for all log correlation.
	requestID := req.RequestID
	if requestID == "" {
		requestID = headerID
	}

	if err := validateQuoteRequest(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, requestID, "INVALID_REQUEST", err.Error())
		return
	}

	domainReq := buildDomainRequest(&req)

	h.log.Info("quote request received",
		slog.String("request_id", domainReq.RequestID),
		slog.Any("coverage_lines", domainReq.CoverageLines),
		slog.Duration("timeout", domainReq.Timeout),
	)

	results, err := h.orch.GetQuotes(r.Context(), domainReq)
	if err != nil {
		h.handleOrchError(w, r, requestID, err)
		return
	}

	if len(results) == 0 {
		h.writeError(w, r, http.StatusUnprocessableEntity, requestID, "NO_ELIGIBLE_CARRIERS", "no carriers available for the requested coverage lines")
		return
	}

	resp := quoteResponse{
		RequestID:  domainReq.RequestID,
		Quotes:     make([]quoteItem, 0, len(results)),
		DurationMs: time.Since(start).Milliseconds(),
	}
	for _, result := range results {
		resp.Quotes = append(resp.Quotes, quoteItem{
			CarrierID:    result.CarrierID,
			PremiumCents: result.Premium.Amount,
			Currency:     result.Premium.Currency,
			IsHedged:     result.IsHedged,
			LatencyMs:    result.Latency.Milliseconds(),
		})
	}

	h.log.Info("quote response sent",
		slog.String("request_id", domainReq.RequestID),
		slog.Int("quote_count", len(resp.Quotes)),
		slog.Int64("duration_ms", resp.DurationMs),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if encErr := json.NewEncoder(w).Encode(resp); encErr != nil {
		h.log.Error("failed to encode response",
			slog.String("request_id", domainReq.RequestID),
			slog.String("error", encErr.Error()),
		)
	}
}

// handleOrchError maps orchestrator errors to HTTP status codes.
func (h *Handler) handleOrchError(w http.ResponseWriter, r *http.Request, requestID string, err error) {
	switch {
	case errors.Is(err, domain.ErrCarrierTimeout):
		h.writeError(w, r, http.StatusGatewayTimeout, requestID, "TIMEOUT", "request timed out before carriers responded")
	default:
		h.log.Error("unexpected orchestrator error",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()),
		)
		// Do not expose internal error details to the caller.
		h.writeErrorBody(w, http.StatusInternalServerError, "internal error")
	}
}

// writeError logs and writes a structured JSON error response.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, status int, requestID, code, message string) {
	attrs := []slog.Attr{
		slog.String("request_id", requestID),
		slog.Int("status", status),
		slog.String("code", code),
		slog.String("message", message),
	}
	switch {
	case status >= 500:
		h.log.LogAttrs(r.Context(), slog.LevelError, "request error", attrs...)
	case status == http.StatusGatewayTimeout:
		h.log.LogAttrs(r.Context(), slog.LevelWarn, "request error", attrs...)
	default:
		h.log.LogAttrs(r.Context(), slog.LevelInfo, "request error", attrs...)
	}
	h.writeErrorBody(w, status, fmt.Sprintf("%s: %s", code, message))
}

// writeErrorBody writes a JSON error body with the given status and message.
func (h *Handler) writeErrorBody(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: message})
}

// --- Validation helpers ---

// validateQuoteRequest validates the inbound quote request, returning a
// descriptive error if any field is invalid.
func validateQuoteRequest(req *quoteRequest) error {
	if req.RequestID == "" {
		return fmt.Errorf("%w: request_id is required", domain.ErrInvalidRequest)
	}
	if len(req.CoverageLines) == 0 {
		return fmt.Errorf("%w: coverage_lines must contain at least one entry", domain.ErrInvalidRequest)
	}
	for _, line := range req.CoverageLines {
		if !isValidCoverageLine(line) {
			return fmt.Errorf("%w: unknown coverage_line %q: must be one of auto, homeowners, umbrella", domain.ErrInvalidRequest, line)
		}
	}
	if req.TimeoutMs < 0 {
		return fmt.Errorf("%w: timeout_ms must be non-negative, got %d", domain.ErrInvalidRequest, req.TimeoutMs)
	}
	if req.TimeoutMs > 0 && req.TimeoutMs < minTimeoutMs {
		return fmt.Errorf("%w: timeout_ms must be >= %d ms, got %d", domain.ErrInvalidRequest, minTimeoutMs, req.TimeoutMs)
	}
	if req.TimeoutMs > maxTimeoutMs {
		return fmt.Errorf("%w: timeout_ms must be <= %d ms, got %d", domain.ErrInvalidRequest, maxTimeoutMs, req.TimeoutMs)
	}
	return nil
}

// isValidCoverageLine returns true for the three supported coverage lines.
func isValidCoverageLine(line string) bool {
	switch domain.CoverageLine(line) {
	case domain.CoverageLineAuto, domain.CoverageLineHomeowners, domain.CoverageLineUmbrella:
		return true
	default:
		return false
	}
}

// buildDomainRequest converts a validated quoteRequest to a domain.QuoteRequest.
func buildDomainRequest(req *quoteRequest) domain.QuoteRequest {
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeoutMs
	}

	lines := make([]domain.CoverageLine, len(req.CoverageLines))
	for i, l := range req.CoverageLines {
		lines[i] = domain.CoverageLine(l)
	}

	return domain.QuoteRequest{
		RequestID:     req.RequestID,
		CoverageLines: lines,
		Timeout:       time.Duration(timeoutMs) * time.Millisecond,
	}
}

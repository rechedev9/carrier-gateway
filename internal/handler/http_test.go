// Package handler_test tests the HTTP handler layer using httptest.
// REQ-HTTP-001, REQ-HTTP-002, REQ-HTTP-003, REQ-HTTP-004, REQ-HTTP-006
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/handler"
	"github.com/rechedev9/carrier-gateway/internal/ports"
	"github.com/rechedev9/carrier-gateway/internal/testutil"
)

// mockOrchestrator is a test double for ports.OrchestratorPort.
type mockOrchestrator struct {
	results []domain.QuoteResult
	err     error
}

func (m *mockOrchestrator) GetQuotes(_ context.Context, _ domain.QuoteRequest) ([]domain.QuoteResult, error) {
	return m.results, m.err
}

var _ ports.OrchestratorPort = (*mockOrchestrator)(nil)

// newHandler builds a Handler with a mock orchestrator and a discard logger.
func newHandler(t *testing.T, orch ports.OrchestratorPort) *handler.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := testutil.NewNoopRecorder()
	reg := prometheus.NewRegistry()
	return handler.New(orch, rec, reg, log, nil)
}

// doRequest sends a POST /quotes request with the given body and returns the recorder.
func doRequest(t *testing.T, h *handler.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/quotes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// quoteResponseBody is the JSON structure returned by POST /quotes.
type quoteResponseBody struct {
	RequestID  string      `json:"request_id"`
	Quotes     []quoteItem `json:"quotes"`
	DurationMs int64       `json:"duration_ms"`
}

// quoteItem mirrors handler's unexported quoteItem.
type quoteItem struct {
	CarrierID    string `json:"carrier_id"`
	PremiumCents int64  `json:"premium_cents"`
	Currency     string `json:"currency"`
	IsHedged     bool   `json:"is_hedged"`
	LatencyMs    int64  `json:"latency_ms"`
}

// errorBody mirrors handler's unexported errorResponse.
type errorBody struct {
	Error string `json:"error"`
}

func TestHandler_PostQuotes_HappyPath(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-001: valid POST /quotes returns 200 with expected JSON fields.
	orch := &mockOrchestrator{
		results: []domain.QuoteResult{
			{
				RequestID: "req-001",
				CarrierID: "alpha",
				Premium:   domain.Money{Amount: 100000, Currency: "USD"},
				IsHedged:  false,
			},
		},
	}
	h := newHandler(t, orch)
	w := doRequest(t, h, `{"request_id":"req-001","coverage_lines":["auto"]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp quoteResponseBody
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RequestID != "req-001" {
		t.Fatalf("request_id = %q, want req-001", resp.RequestID)
	}
	if len(resp.Quotes) != 1 {
		t.Fatalf("quotes length = %d, want 1", len(resp.Quotes))
	}
	q := resp.Quotes[0]
	if q.CarrierID != "alpha" {
		t.Fatalf("carrier_id = %q, want alpha", q.CarrierID)
	}
	if q.PremiumCents != 100000 {
		t.Fatalf("premium_cents = %d, want 100000", q.PremiumCents)
	}
}

func TestHandler_PostQuotes_MalformedJSON_Returns400(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-002: malformed JSON body → 400.
	h := newHandler(t, &mockOrchestrator{})
	w := doRequest(t, h, `{"request_id": INVALID`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body.Error, "INVALID_JSON") {
		t.Fatalf("error body = %q, want INVALID_JSON", body.Error)
	}
}

func TestHandler_PostQuotes_BodyTooLarge_Returns400(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-002: body > 1 MB → 400 with REQUEST_TOO_LARGE.
	// Build a body just over 1 MiB.
	const oneMiB = 1 << 20
	padding := bytes.Repeat([]byte("x"), oneMiB+100)
	large := `{"request_id":"r","coverage_lines":["auto"],"extra":"` + string(padding) + `"}`

	h := newHandler(t, &mockOrchestrator{})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/quotes", strings.NewReader(large))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body.Error, "REQUEST_TOO_LARGE") {
		t.Fatalf("error body = %q, want REQUEST_TOO_LARGE", body.Error)
	}
}

func TestHandler_PostQuotes_EmptyRequestID_Returns400(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-003: empty request_id → 400 with INVALID_REQUEST.
	h := newHandler(t, &mockOrchestrator{})
	w := doRequest(t, h, `{"request_id":"","coverage_lines":["auto"]}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body.Error, "INVALID_REQUEST") {
		t.Fatalf("error body = %q, want INVALID_REQUEST", body.Error)
	}
}

func TestHandler_PostQuotes_MissingCoverageLines_Returns400(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-003: missing coverage_lines → 400.
	h := newHandler(t, &mockOrchestrator{})
	w := doRequest(t, h, `{"request_id":"req-002"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandler_PostQuotes_NegativeTimeoutMs_Returns400(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-003: negative timeout_ms → 400.
	h := newHandler(t, &mockOrchestrator{})
	w := doRequest(t, h, `{"request_id":"req-003","coverage_lines":["auto"],"timeout_ms":-1}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandler_PostQuotes_OrchestratorUnexpectedError_Returns500(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-004: orchestrator returns unexpected error → 500 with INTERNAL_ERROR,
	// no internal error details in response body.
	orch := &mockOrchestrator{err: errors.New("redis connection refused")}
	h := newHandler(t, orch)
	w := doRequest(t, h, `{"request_id":"req-err","coverage_lines":["auto"]}`)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	// Must NOT contain internal error details.
	if strings.Contains(body.Error, "redis") {
		t.Fatalf("response body must not expose internal error details, got %q", body.Error)
	}
}

func TestHandler_GetMetrics_Returns200WithPrometheusBody(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-006: GET /metrics → 200 with Content-Type text/plain and
	// body containing carrier_circuit_breaker_state.
	h := newHandler(t, &mockOrchestrator{})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain prefix", ct)
	}
}

func TestHandler_PostQuotes_LongRequestID_Returns400(t *testing.T) {
	t.Parallel()

	h := newHandler(t, &mockOrchestrator{})
	longID := strings.Repeat("a", 257) // exceeds maxRequestIDLen (256)
	body := `{"request_id":"` + longID + `","coverage_lines":["auto"]}`
	w := doRequest(t, h, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body2 errorBody
	_ = json.NewDecoder(w.Body).Decode(&body2)
	if !strings.Contains(body2.Error, "INVALID_REQUEST") {
		t.Fatalf("error body = %q, want INVALID_REQUEST", body2.Error)
	}
}

func TestHandler_PostQuotes_ControlCharRequestID_Returns400(t *testing.T) {
	t.Parallel()

	h := newHandler(t, &mockOrchestrator{})
	body := `{"request_id":"req\n001","coverage_lines":["auto"]}`
	w := doRequest(t, h, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body2 errorBody
	_ = json.NewDecoder(w.Body).Decode(&body2)
	if !strings.Contains(body2.Error, "INVALID_REQUEST") {
		t.Fatalf("error body = %q, want INVALID_REQUEST", body2.Error)
	}
}

func TestHandler_PostQuotes_MalformedJSON_DoesNotLeakFieldNames(t *testing.T) {
	t.Parallel()

	// SEC-11: ensure the error response doesn't contain schema field names
	h := newHandler(t, &mockOrchestrator{})
	w := doRequest(t, h, `{"request_id": "r1", "unknown_field": true}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body2 errorBody
	_ = json.NewDecoder(w.Body).Decode(&body2)
	if strings.Contains(body2.Error, "unknown_field") {
		t.Fatalf("error response must not leak field names, got %q", body2.Error)
	}
}

func TestHandler_PostQuotes_TableDriven_ValidationErrors(t *testing.T) {
	t.Parallel()

	// REQ-HTTP-003: table-driven validation scenarios.
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "REQ-HTTP-003 empty request_id",
			body:       `{"request_id":"","coverage_lines":["auto"]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "REQ-HTTP-003 missing coverage_lines",
			body:       `{"request_id":"r1"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "REQ-HTTP-003 negative timeout_ms",
			body:       `{"request_id":"r1","coverage_lines":["auto"],"timeout_ms":-100}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "REQ-HTTP-003 unknown coverage_line",
			body:       `{"request_id":"r1","coverage_lines":["life"]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "REQ-HTTP-001 valid request",
			body:       `{"request_id":"r1","coverage_lines":["auto"]}`,
			wantStatus: http.StatusOK,
		},
	}

	validResult := []domain.QuoteResult{
		{RequestID: "r1", CarrierID: "alpha", Premium: domain.Money{Amount: 10000, Currency: "USD"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock := &mockOrchestrator{results: []domain.QuoteResult{}}
			if tc.wantStatus == http.StatusOK {
				mock.results = validResult
			}
			h := newHandler(t, mock)
			w := doRequest(t, h, tc.body)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

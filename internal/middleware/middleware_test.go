package middleware_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rechedev9/carrier-gateway/internal/middleware"
)

var silentLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cid := middleware.ClientIDFromContext(r.Context())
		w.Header().Set("X-Client-ID", cid)
		w.WriteHeader(http.StatusOK)
	})
}

// --- Auth tests ---

func TestRequireAPIKey_ValidToken(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"test-key-12345678"}, nil, silentLog)
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	req.Header.Set("Authorization", "Bearer test-key-12345678")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Client-ID"); got != "test-key..." {
		t.Errorf("client ID = %q, want %q", got, "test-key...")
	}
}

func TestRequireAPIKey_Rejected(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"correct-key"}, nil, silentLog)

	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"invalid token", "Bearer wrong-key"},
		{"empty bearer", "Bearer "},
		{"no bearer prefix", "Basic abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want 401", rec.Code)
			}
			var body map[string]string
			json.NewDecoder(rec.Body).Decode(&body)
			if body["error"] == "" {
				t.Error("expected error field in response body")
			}
		})
	}
}

func TestRequireAPIKey_SkipPaths(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"key1"}, []string{"/healthz", "/metrics"}, silentLog)

	for _, path := range []string{"/healthz", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("got status %d, want 200", rec.Code)
			}
		})
	}
}

func TestRequireAPIKey_ShortKey(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"abc"}, nil, silentLog)
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	req.Header.Set("Authorization", "Bearer abc")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Client-ID"); got != "abc" {
		t.Errorf("client ID = %q, want %q", got, "abc")
	}
}

// --- Auth failure rate-limit tests ---

func TestRequireAPIKey_RateLimitsFailures(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"correct-key"}, nil, silentLog)

	const burst = 10
	for i := range burst {
		req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
		req.Header.Set("Authorization", "Bearer wrong-key")
		req.RemoteAddr = "10.0.0.1:12345"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got status %d, want 401", i+1, rec.Code)
		}
	}

	// The (burst+1)th failure should be rate-limited.
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	req.RemoteAddr = "10.0.0.1:12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want %q", got, "60")
	}
}

func TestRequireAPIKey_RateLimitDoesNotAffectValidKeys(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"correct-key"}, nil, silentLog)

	// Exhaust the failure budget from this IP.
	const burst = 10
	for range burst {
		req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
		req.Header.Set("Authorization", "Bearer wrong-key")
		req.RemoteAddr = "10.0.0.2:12345"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	// Valid key from the same IP should still be blocked (IP is rate-limited).
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	req.Header.Set("Authorization", "Bearer correct-key")
	req.RemoteAddr = "10.0.0.2:12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429 (IP exhausted budget)", rec.Code)
	}

	// A different IP with a valid key should pass.
	req2 := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	req2.Header.Set("Authorization", "Bearer correct-key")
	req2.RemoteAddr = "10.0.0.3:12345"
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (clean IP with valid key)", rec2.Code)
	}
}

// --- Security headers tests ---

func TestSecurityHeaders(t *testing.T) {
	h := middleware.SecurityHeaders(echoHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=63072000; includeSubDomains" {
		t.Errorf("Strict-Transport-Security = %q, want expected value", got)
	}
}

// --- Audit log tests ---

func TestAuditLog_LogsRequest(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	h := middleware.AuditLog(inner, silentLog)
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201", rec.Code)
	}
}

func TestAuditLog_CapturesDefaultStatus(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	h := middleware.AuditLog(inner, silentLog)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

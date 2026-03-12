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

func TestRequireAPIKey_MissingHeader(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"key1"}, nil, silentLog)
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
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
}

func TestRequireAPIKey_InvalidToken(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"correct-key"}, nil, silentLog)
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestRequireAPIKey_EmptyBearer(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"key1"}, nil, silentLog)
	req := httptest.NewRequest(http.MethodPost, "/quotes", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestRequireAPIKey_SkipPaths(t *testing.T) {
	h := middleware.RequireAPIKey(echoHandler(), []string{"key1"}, []string{"/healthz", "/metrics"}, silentLog)

	for _, path := range []string{"/healthz", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("path %s: got status %d, want 200", path, rec.Code)
		}
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
	// Use a handler that sets a known status code.
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
	// Handler that writes body without explicit WriteHeader → implicit 200.
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

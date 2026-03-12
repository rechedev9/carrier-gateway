package cleanup_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/cleanup"
	"github.com/rechedev9/carrier-gateway/internal/domain"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// stubRepo counts DeleteExpired calls for test assertions.
type stubRepo struct {
	calls atomic.Int64
}

func (s *stubRepo) Save(_ context.Context, _ string, _ []domain.QuoteResult) error {
	return nil
}

func (s *stubRepo) FindByRequestID(_ context.Context, _ string) ([]domain.QuoteResult, bool, error) {
	return nil, false, nil
}

func (s *stubRepo) DeleteExpired(_ context.Context) (int64, error) {
	s.calls.Add(1)
	return 0, nil
}

func TestTicker_CallsDeleteExpired(t *testing.T) {
	repo := &stubRepo{}
	ticker := cleanup.New(repo, 50*time.Millisecond, discardLog)
	go ticker.Start(t.Context())

	// Wait enough for at least 2 ticks.
	time.Sleep(160 * time.Millisecond)

	ticker.Stop()

	got := repo.calls.Load()
	if got < 2 {
		t.Errorf("expected at least 2 DeleteExpired calls, got %d", got)
	}
}

func TestTicker_StopIsClean(t *testing.T) {
	repo := &stubRepo{}
	ticker := cleanup.New(repo, 1*time.Hour, discardLog)
	go ticker.Start(t.Context())

	// Stop immediately — no ticks should have fired.
	time.Sleep(10 * time.Millisecond)
	ticker.Stop()

	before := repo.calls.Load()
	time.Sleep(100 * time.Millisecond)
	after := repo.calls.Load()

	if after != before {
		t.Errorf("calls increased after Stop: before=%d after=%d", before, after)
	}
}

func TestTicker_ContextCancellation(t *testing.T) {
	repo := &stubRepo{}

	ctx, cancel := context.WithCancel(t.Context())
	ticker := cleanup.New(repo, 1*time.Hour, discardLog)

	done := make(chan struct{})
	go func() {
		ticker.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Start returned after ctx cancellation — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

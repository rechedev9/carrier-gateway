// Package cleanup provides a background ticker that periodically purges
// expired quotes from the repository.
package cleanup

import (
	"context"
	"log/slog"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// Ticker periodically calls DeleteExpired on the repository to remove stale
// quote rows. It is safe for concurrent use — Start may only be called once.
type Ticker struct {
	repo     ports.QuoteRepository
	interval time.Duration
	log      *slog.Logger
	stop     chan struct{}
	done     chan struct{}
}

// New creates a Ticker that will call repo.DeleteExpired every interval.
func New(repo ports.QuoteRepository, interval time.Duration, log *slog.Logger) *Ticker {
	return &Ticker{
		repo:     repo,
		interval: interval,
		log:      log,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins the background cleanup loop. It blocks until Stop is called or
// ctx is cancelled. Callers should invoke Start in a separate goroutine.
func (t *Ticker) Start(ctx context.Context) {
	defer close(t.done)

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	t.log.Info("cleanup ticker started", slog.Duration("interval", t.interval))

	for {
		select {
		case <-ticker.C:
			t.sweep(ctx)
		case <-t.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop signals the ticker goroutine to exit and waits for it to finish.
func (t *Ticker) Stop() {
	close(t.stop)
	<-t.done
}

// sweep runs a single DeleteExpired pass and logs the result.
func (t *Ticker) sweep(ctx context.Context) {
	n, err := t.repo.DeleteExpired(ctx)
	if err != nil {
		t.log.Error("cleanup: delete expired quotes failed", slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		t.log.Info("cleanup: purged expired quotes", slog.Int64("count", n))
	}
}

// Package testutil provides shared test helpers that are importable across
// packages. Files in this package are NOT _test.go files so that other
// packages' test files can import them via a normal import path.
//
// Usage in a test file:
//
//	import "github.com/rechedev9/carrier-gateway/internal/testutil"
//
//	rec := testutil.NewNoopRecorder()
//	// pass rec wherever a ports.MetricsRecorder is required
package testutil

import (
	"sync/atomic"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// NoopRecorder is a thread-safe ports.MetricsRecorder stub that counts calls
// to each method. It discards all metric values — it is intended purely for
// verifying that the correct methods are called the correct number of times.
type NoopRecorder struct {
	RecordQuoteCount              atomic.Int64
	RecordHedgeCount              atomic.Int64
	RecordCBTransitionCount       atomic.Int64
	RecordRateLimitRejectionCount atomic.Int64
	RecordFanOutDurationCount     atomic.Int64
	SetCBStateCount               atomic.Int64
	SetP95LatencyCount            atomic.Int64
}

// NewNoopRecorder returns an initialised NoopRecorder.
func NewNoopRecorder() *NoopRecorder {
	return &NoopRecorder{}
}

// RecordQuote implements ports.MetricsRecorder.
func (n *NoopRecorder) RecordQuote(_ string, _ time.Duration, _ string) {
	n.RecordQuoteCount.Add(1)
}

// RecordHedge implements ports.MetricsRecorder.
func (n *NoopRecorder) RecordHedge(_, _ string) {
	n.RecordHedgeCount.Add(1)
}

// RecordCBTransition implements ports.MetricsRecorder.
func (n *NoopRecorder) RecordCBTransition(_ string, _, _ ports.CBState) {
	n.RecordCBTransitionCount.Add(1)
}

// RecordRateLimitRejection implements ports.MetricsRecorder.
func (n *NoopRecorder) RecordRateLimitRejection(_ string) {
	n.RecordRateLimitRejectionCount.Add(1)
}

// RecordFanOutDuration implements ports.MetricsRecorder.
func (n *NoopRecorder) RecordFanOutDuration(_ time.Duration) {
	n.RecordFanOutDurationCount.Add(1)
}

// SetCBState implements ports.MetricsRecorder.
func (n *NoopRecorder) SetCBState(_ string, _ ports.CBState) {
	n.SetCBStateCount.Add(1)
}

// SetP95Latency implements ports.MetricsRecorder.
func (n *NoopRecorder) SetP95Latency(_ string, _ float64) {
	n.SetP95LatencyCount.Add(1)
}

// Compile-time interface satisfaction check.
var _ ports.MetricsRecorder = (*NoopRecorder)(nil)

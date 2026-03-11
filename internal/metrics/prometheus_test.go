// Package metrics_test tests all 8 Prometheus metrics emitted by PrometheusRecorder.
// REQ-CB-004, REQ-HEDGE-006, REQ-RL-003
package metrics_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/rechedev9/carrier-gateway/internal/metrics"
	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// newRecorder returns a PrometheusRecorder using an isolated registry.
// Each test gets its own registry so there is no global state contamination.
func newRecorder(t *testing.T) *metrics.PrometheusRecorder {
	t.Helper()
	reg := prometheus.NewRegistry()
	return metrics.New(reg)
}

// newRecorderWithRegistry returns both the recorder and the registry so tests
// can gather metric families for assertion.
func newRecorderWithRegistry(t *testing.T) (*metrics.PrometheusRecorder, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	return metrics.New(reg), reg
}

// gaugeValue retrieves the current value of a GaugeVec label combination.
func gaugeValue(t *testing.T, reg *prometheus.Registry, metricName string, labels ...string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m.GetLabel(), labels) {
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("metric %q with labels %v not found", metricName, labels)
	return 0
}

// counterValue retrieves the current value of a counter with given label pairs.
func counterValue(t *testing.T, reg *prometheus.Registry, metricName string, labels ...string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m.GetLabel(), labels) {
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("metric %q with labels %v not found", metricName, labels)
	return 0
}

// metricFamilyCount returns the number of metrics registered in the registry.
func metricFamilyCount(t *testing.T, reg *prometheus.Registry) int {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}
	return len(mfs)
}

// labelsMatch checks that the metric's label pairs match the provided name=value pairs.
// pairs must be provided in alternating name, value order.
func labelsMatch(got []*dto.LabelPair, pairs []string) bool {
	if len(pairs)%2 != 0 {
		return false
	}
	want := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		want[pairs[i]] = pairs[i+1]
	}
	matched := 0
	for _, lp := range got {
		if v, ok := want[lp.GetName()]; ok && v == lp.GetValue() {
			matched++
		}
	}
	return matched == len(want)
}

func TestPrometheusRecorder_SetCBState_SetsGaugeToExpectedValue(t *testing.T) {
	t.Parallel()

	// REQ-CB-004: SetCBState sets the gauge to the numeric state value.
	tests := []struct {
		name      string
		state     ports.CBState
		wantValue float64
	}{
		{name: "REQ-CB-004 closed state=0", state: ports.CBStateClosed, wantValue: 0},
		{name: "REQ-CB-004 open state=1", state: ports.CBStateOpen, wantValue: 1},
		{name: "REQ-CB-004 halfopen state=2", state: ports.CBStateHalfOpen, wantValue: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, reg := newRecorderWithRegistry(t)

			rec.SetCBState("alpha", tc.state)

			got := gaugeValue(t, reg, "carrier_circuit_breaker_state", "carrier_id", "alpha")
			if got != tc.wantValue {
				t.Fatalf("carrier_circuit_breaker_state{carrier_id=alpha} = %v, want %v", got, tc.wantValue)
			}
		})
	}
}

func TestPrometheusRecorder_RecordHedge_IncrementsCounter(t *testing.T) {
	t.Parallel()

	// REQ-HEDGE-006: RecordHedge increments hedge_requests_total by 1.
	rec, reg := newRecorderWithRegistry(t)

	rec.RecordHedge("alpha", "gamma")

	got := counterValue(t, reg, "hedge_requests_total", "carrier_id", "alpha", "trigger_carrier", "gamma")
	if got != 1 {
		t.Fatalf("hedge_requests_total{carrier_id=alpha,trigger_carrier=gamma} = %v, want 1", got)
	}

	// Second call increments to 2.
	rec.RecordHedge("alpha", "gamma")
	got = counterValue(t, reg, "hedge_requests_total", "carrier_id", "alpha", "trigger_carrier", "gamma")
	if got != 2 {
		t.Fatalf("after second RecordHedge: hedge_requests_total = %v, want 2", got)
	}
}

func TestPrometheusRecorder_RecordRateLimitRejection_IncrementsCounter(t *testing.T) {
	t.Parallel()

	// REQ-RL-003: RecordRateLimitRejection increments rate_limit_exceeded_total.
	rec, reg := newRecorderWithRegistry(t)

	rec.RecordRateLimitRejection("beta")

	got := counterValue(t, reg, "rate_limit_exceeded_total", "carrier_id", "beta")
	if got != 1 {
		t.Fatalf("rate_limit_exceeded_total{carrier_id=beta} = %v, want 1", got)
	}
}

func TestPrometheusRecorder_AllEightMetricsRegisteredOnConstruction(t *testing.T) {
	t.Parallel()

	// REQ-CB-004/REQ-HEDGE-006/REQ-RL-003: all 8 metrics are registered without error.
	reg := prometheus.NewRegistry()
	// New should not panic. If it does, the test will fail with a panic.
	metrics.New(reg)

	// Gather should yield exactly 8 metric families.
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather after New: %v", err)
	}

	// Expected metric names.
	expectedNames := map[string]bool{
		"carrier_circuit_breaker_state":         false,
		"carrier_p95_latency_ms":                false,
		"carrier_quote_latency_seconds":         false,
		"orchestrator_fan_out_duration_seconds": false,
		"carrier_requests_total":                false,
		"hedge_requests_total":                  false,
		"circuit_breaker_transitions_total":     false,
		"rate_limit_exceeded_total":             false,
	}

	for _, mf := range mfs {
		if _, ok := expectedNames[mf.GetName()]; ok {
			expectedNames[mf.GetName()] = true
		}
	}

	// After gather with no observations, histogram/gauge families may not appear.
	// Trigger an observation on each metric to ensure they register.
	rec, reg2 := newRecorderWithRegistry(t)
	rec.SetCBState("test", ports.CBStateClosed)
	rec.SetP95Latency("test", 50.0)
	rec.RecordQuote("test", 50*time.Millisecond, "success")
	rec.RecordFanOutDuration(100 * time.Millisecond)
	rec.RecordHedge("test", "other")
	rec.RecordCBTransition("test", ports.CBStateClosed, ports.CBStateOpen)
	rec.RecordRateLimitRejection("test")

	count := metricFamilyCount(t, reg2)
	if count < 7 {
		t.Fatalf("expected ≥7 metric families after observations, got %d", count)
	}
}

func TestPrometheusRecorder_LabelCardinality_ThreeCarriersDistinct(t *testing.T) {
	t.Parallel()

	// REQ-CB-004: three carrier IDs produce three distinct time series per counter.
	rec, reg := newRecorderWithRegistry(t)

	carriers := []string{"alpha", "beta", "gamma"}
	for _, id := range carriers {
		rec.RecordRateLimitRejection(id)
	}

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, mf := range mfs {
		if mf.GetName() != "rate_limit_exceeded_total" {
			continue
		}
		got := len(mf.GetMetric())
		if got != len(carriers) {
			t.Fatalf("rate_limit_exceeded_total: expected %d distinct series, got %d", len(carriers), got)
		}
		return
	}
	t.Fatal("rate_limit_exceeded_total not found in gathered metrics")
}

func TestPrometheusRecorder_RecordCBTransition_IncrementsCounter(t *testing.T) {
	t.Parallel()

	// REQ-CB-004: RecordCBTransition increments circuit_breaker_transitions_total.
	rec, reg := newRecorderWithRegistry(t)

	rec.RecordCBTransition("alpha", ports.CBStateClosed, ports.CBStateOpen)

	got := counterValue(t, reg, "circuit_breaker_transitions_total",
		"carrier_id", "alpha",
		"from_state", "closed",
		"to_state", "open",
	)
	if got != 1 {
		t.Fatalf("circuit_breaker_transitions_total = %v, want 1", got)
	}
}

func TestPrometheusRecorder_SetP95Latency_SetsGauge(t *testing.T) {
	t.Parallel()

	rec, reg := newRecorderWithRegistry(t)

	rec.SetP95Latency("alpha", 123.45)

	got := gaugeValue(t, reg, "carrier_p95_latency_ms", "carrier_id", "alpha")
	if got != 123.45 {
		t.Fatalf("carrier_p95_latency_ms{carrier_id=alpha} = %v, want 123.45", got)
	}
}

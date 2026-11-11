package observability_test

import (
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/observability"
)

func TestTraceParentAndPercentiles(t *testing.T) {
	tp := observability.NewTraceParent()
	if observability.NormalizeTraceParent(tp) != tp {
		t.Fatalf("expected generated traceparent %s to be valid", tp)
	}
	if observability.NormalizeTraceParent("malformed") == "malformed" {
		t.Fatal("expected malformed traceparent to be regenerated")
	}

	h := observability.NewRingHistogram(128)
	for i := 1; i <= 100; i++ {
		h.Record(float64(i))
	}
	snap := h.Snapshot()
	if snap.Count != 100 || snap.P50 < 50 || snap.P95 < 95 || snap.P99 < 99 {
		t.Fatalf("unexpected percentile summary: %+v", snap)
	}
}

func TestLamportClockMonotonicity(t *testing.T) {
	var clock observability.LamportClock
	if v := clock.Tick(); v != 1 {
		t.Fatalf("expected 1, got %d", v)
	}
	if v := clock.Tick(); v != 2 {
		t.Fatalf("expected 2, got %d", v)
	}
	if cur := clock.Current(); cur != 2 {
		t.Fatalf("expected Current() == 2, got %d", cur)
	}
}

package trace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/trace"
)

func TestW3CTraceParentGenerationAndNormalization(t *testing.T) {
	tp := trace.NewTraceParent()
	parsed, err := trace.ParseTraceParent(strings.ToUpper(tp))
	if err != nil || parsed.String() != tp {
		t.Fatalf("expected case-insensitive ParseTraceParent normalization, got %+v err=%v", parsed, err)
	}
	normValid, ok := trace.NormalizeTraceParent("  " + strings.ToUpper(tp) + "  ")
	if !ok || normValid != tp {
		t.Fatalf("expected single-pass NormalizeTraceParent=(%q, true), got (%q, %v)", tp, normValid, ok)
	}

	invalid := []string{
		"",
		"bad-header",
		"00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736_00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7_01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-zzzz2f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	for _, raw := range invalid {
		if _, pErr := trace.ParseTraceParent(raw); pErr == nil {
			t.Fatalf("expected %q to be rejected by ParseTraceParent", raw)
		}
		if norm, valid := trace.NormalizeTraceParent(raw); valid || norm != "" {
			t.Fatalf("expected NormalizeTraceParent(%q) to return (\"\", false), got (%q, %v)", raw, norm, valid)
		}
	}

	allocs := testing.AllocsPerRun(100, func() {
		if _, pErr := trace.ParseTraceParent(tp); pErr != nil {
			t.Fatalf("ParseTraceParent(%q): %v", tp, pErr)
		}
	})
	if allocs != 0 {
		t.Fatalf("expected 0 heap allocations in ParseTraceParent on lowercase traceparent, got %v", allocs)
	}

	ctx := trace.WithTraceParent(context.Background(), tp)
	if got := trace.TraceParentFromContext(ctx); got != tp {
		t.Fatalf("expected TraceParentFromContext=%q, got %q", tp, got)
	}
}

func TestLookupAndWithTraceParentContextIdempotency(t *testing.T) {
	if got, ok := trace.LookupTraceParent(context.Background()); ok || got != "" {
		t.Fatalf("expected empty LookupTraceParent on background context, got (%q, %v)", got, ok)
	}

	generated := trace.TraceParentFromContext(context.Background())
	if _, err := trace.ParseTraceParent(generated); err != nil {
		t.Fatalf("expected TraceParentFromContext to synthesize valid traceparent, got %q err=%v", generated, err)
	}
	ensuredCtx := trace.WithTraceParent(context.Background(), generated)
	if got, ok := trace.LookupTraceParent(ensuredCtx); !ok || got != generated {
		t.Fatalf("expected trace.LookupTraceParent to read %q, got (%q, %v)", generated, got, ok)
	}
	if preserved := trace.TraceParentFromContext(ensuredCtx); preserved != generated {
		t.Fatalf("expected TraceParentFromContext to preserve existing traceparent %q, got %q", generated, preserved)
	}
}

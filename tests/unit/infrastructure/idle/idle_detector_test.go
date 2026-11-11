package idle_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/idle"
)

func TestUnixIdleProbeResolvesDesktopBackendsInOrder(t *testing.T) {
	ctx := context.Background()

	if got := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{}); got != -1 {
		t.Fatalf("expected -1 when RunCommand is nil, got %d", got)
	}

	secXss := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{
		RunCommand: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "xssstate" {
				return []byte("45200\n"), nil
			}
			return nil, errors.New("not installed")
		},
	})
	if secXss != 45 {
		t.Fatalf("expected 45s from xssstate, got %d", secXss)
	}

	secXprint := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{
		RunCommand: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "xprintidle" {
				return []byte("120999\n"), nil
			}
			return nil, errors.New("unavailable")
		},
	})
	if secXprint != 120 {
		t.Fatalf("expected 120s from xprintidle, got %d", secXprint)
	}

	secGdbus := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{
		RunCommand: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "gdbus" {
				return []byte("(uint64 185000,)\n"), nil
			}
			return nil, errors.New("unavailable")
		},
	})
	if secGdbus != 185 {
		t.Fatalf("expected 185s from gdbus Mutter IdleMonitor, got %d", secGdbus)
	}

	assertLoginctlSessionDiscoveryFallback(t, ctx)
}

func assertLoginctlSessionDiscoveryFallback(t *testing.T, ctx context.Context) {
	t.Helper()
	secLoginctlIdle := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "loginctl" {
				return nil, errors.New("unavailable")
			}
			if len(args) > 0 && args[0] == "list-sessions" {
				return []byte("  c2 1000 user0 seat0\n"), nil
			}
			if len(args) > 1 && args[0] == "show-session" && args[1] == "c2" {
				return []byte("IdleHint=yes\nIdleSinceHintMonotonic=100000000\n"), nil
			}
			return nil, errors.New("no ambient session")
		},
		MonotonicMicros: func() int64 { return 250_000_000 },
	})
	if secLoginctlIdle != 150 {
		t.Fatalf("expected 150s from discovered session c2 IdleHint=yes, got %d", secLoginctlIdle)
	}

	secLoginctlActive := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{
		RunCommand: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "loginctl" {
				return []byte("IdleHint=no\nIdleSinceHintMonotonic=100000000\n"), nil
			}
			return nil, errors.New("unavailable")
		},
		MonotonicMicros: func() int64 { return 250_000_000 },
	})
	if secLoginctlActive != 0 {
		t.Fatalf("expected 0s from active loginctl session, got %d", secLoginctlActive)
	}
}

func TestUnixIdleProbeReturnsNegativeOneWithoutFabricatedFallback(t *testing.T) {
	ctx := context.Background()

	headlessNoTools := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{
		RunCommand: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, errors.New("headless server without X11 or systemd session")
		},
	})
	if headlessNoTools != -1 {
		t.Fatalf("expected -1 when no idle backend exists, got %d", headlessNoTools)
	}

	waylandNoIdleDaemon := idle.ProbeUnixIdleWithDeps(ctx, idle.UnixIdleProbeDeps{
		RunCommand: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "loginctl" {
				return []byte("IdleHint=no\nIdleSinceHintMonotonic=0\n"), nil
			}
			return nil, errors.New("no X11")
		},
		MonotonicMicros: func() int64 { return 900_000_000 },
	})
	if waylandNoIdleDaemon != -1 {
		t.Fatalf("expected -1 when loginctl has zero IdleSinceHintMonotonic, got %d", waylandNoIdleDaemon)
	}
}

func TestParseLoginctlAndUnixIdleOutputs(t *testing.T) {
	if got := idle.ParseMillisIdleOutput(""); got != -1 {
		t.Fatalf("expected -1 on empty millis output, got %d", got)
	}
	if got := idle.ParseMillisIdleOutput("-500"); got != -1 {
		t.Fatalf("expected -1 on negative millis output, got %d", got)
	}
	if got := idle.ParseMillisIdleOutput("9999999999999999"); got != math.MaxInt32 {
		t.Fatalf("expected clamped MaxInt32 on huge millis output, got %d", got)
	}
	if got := idle.ParseGdbusIdleOutput("error: unknown service"); got != -1 {
		t.Fatalf("expected -1 on invalid gdbus output, got %d", got)
	}
	if got := idle.ParseLoginctlIdleOutput("IdleHint=yes\nIdleSinceHintMonotonic=500\n", 100); got != -1 {
		t.Fatalf("expected -1 when monotonic clock precedes hint timestamp, got %d", got)
	}
	if got := idle.ParseLoginctlIdleOutput("IdleHint=true\nIdleSinceHintMonotonic=10000000\nTimestampMonotonic=42000000\n", 0); got != 32 {
		t.Fatalf("expected TimestampMonotonic fallback 32s when nowMonotonicMicros=0, got %d", got)
	}
	if got := idle.ParseLoginctlIdleOutput("IdleHint=false\nIdleSinceHintMonotonic=10000000\n", 50_000_000); got != 0 {
		t.Fatalf("expected 0s when IdleHint=false, got %d", got)
	}
	if got := idle.ParseLoginctlIdleOutput("IdleHint=unknown\nIdleSinceHintMonotonic=10000000\n", 50_000_000); got != -1 {
		t.Fatalf("expected -1 when IdleHint is unknown, got %d", got)
	}
	if got := idle.ParseLoginctlIdleOutput("IdleHint=yes\n", 50_000_000); got != -1 {
		t.Fatalf("expected -1 when IdleSinceHintMonotonic is missing, got %d", got)
	}
	if got := idle.ParseProcUptimeMicros("not-a-number 0.00"); got != -1 {
		t.Fatalf("expected -1 on invalid /proc/uptime, got %d", got)
	}
	if got := idle.ParseProcUptimeMicros("123.45 456.78"); got != 123_450_000 {
		t.Fatalf("expected 123450000us from /proc/uptime, got %d", got)
	}
}

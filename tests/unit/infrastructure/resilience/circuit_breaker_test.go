package resilience_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/resilience"
)

func isCircuitOpenErr(err error) bool {
	appErr, ok := derr.AsAppError(err)
	return ok && appErr.Code == derr.CodeCircuitOpen
}

func TestCircuitBreakerPerCallOnceClosureAndHalfOpenRecovery(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	cb := resilience.NewCircuitBreakerWithClock(2, 10*time.Second, clock)
	if cb.State() != resilience.CircuitClosed {
		t.Fatalf("expected initial CLOSED state, got %s", cb.State())
	}

	done1, err := cb.Allow()
	if err != nil {
		t.Fatalf("first Allow failed: %v", err)
	}
	done2, err := cb.Allow()
	if err != nil {
		t.Fatalf("second Allow failed: %v", err)
	}

	done2(false)
	done2(false)
	done2(true)
	if cb.State() != resilience.CircuitClosed {
		t.Fatalf("expected single failure from idempotent closure to keep breaker CLOSED, got %s", cb.State())
	}

	done1(false)
	if cb.State() != resilience.CircuitOpen {
		t.Fatalf("expected second failure to trip breaker OPEN, got %s", cb.State())
	}

	if _, openErr := cb.Allow(); !isCircuitOpenErr(openErr) {
		t.Fatalf("expected CodeCircuitOpen while open, got %v", openErr)
	}

	advance(11 * time.Second)
	if cb.State() != resilience.CircuitHalfOpen {
		t.Fatalf("expected HALF_OPEN after cooldown, got %s", cb.State())
	}

	probeDone, err := cb.Allow()
	if err != nil {
		t.Fatalf("expected single HALF_OPEN probe admitted, got %v", err)
	}
	if _, secondErr := cb.Allow(); !isCircuitOpenErr(secondErr) {
		t.Fatalf("expected concurrent HALF_OPEN request rejected with CodeCircuitOpen, got %v", secondErr)
	}

	func() {
		defer probeDone(false)
	}()
	if cb.State() != resilience.CircuitOpen {
		t.Fatalf("expected early-return failure in HALF_OPEN to re-open breaker without deadlock, got %s", cb.State())
	}

	advance(11 * time.Second)
	recoveryDone, err := cb.Allow()
	if err != nil {
		t.Fatalf("expected second cooldown HALF_OPEN probe admitted, got %v", err)
	}
	recoveryDone(true)
	if cb.State() != resilience.CircuitClosed {
		t.Fatalf("expected successful HALF_OPEN probe to close breaker, got %s", cb.State())
	}
}

func TestCircuitBreakerExecuteHandlesErrorsAndPanicsWithoutSlotLeak(t *testing.T) {
	cb := resilience.NewCircuitBreaker(2, time.Second)

	boom := errors.New("upstream unavailable")
	if err := cb.Execute(func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("expected Execute to return callback error, got %v", err)
	}
	if cb.State() != resilience.CircuitClosed {
		t.Fatalf("expected CLOSED after 1 failure, got %s", cb.State())
	}

	didPanic := false
	func() {
		defer func() {
			if recover() != nil {
				didPanic = true
			}
		}()
		if err := cb.Execute(func() error {
			panic("unexpected crash")
		}); err != nil {
			return
		}
	}()
	if !didPanic {
		t.Fatal("expected Execute to re-panic after recording failure")
	}
	if cb.State() != resilience.CircuitOpen {
		t.Fatalf("expected panic to record failure and open circuit breaker, got %s", cb.State())
	}

	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	clockCB := resilience.NewCircuitBreakerWithClock(1, 10*time.Second, func() time.Time { return now })
	if err := clockCB.Execute(func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("expected clock breaker Execute failure, got %v", err)
	}
	if clockCB.State() != resilience.CircuitOpen {
		t.Fatalf("expected clock breaker OPEN, got %s", clockCB.State())
	}
	now = now.Add(11 * time.Second)
	if err := clockCB.Execute(func() error { return nil }); err != nil {
		t.Fatalf("expected HALF_OPEN recovery Execute to succeed, got %v", err)
	}
	if clockCB.State() != resilience.CircuitClosed {
		t.Fatalf("expected CLOSED after HALF_OPEN Execute success, got %s", clockCB.State())
	}
}

func TestCircuitBreakerConcurrentOutOfOrderCompletions(t *testing.T) {
	cb := resilience.NewCircuitBreaker(100, time.Second)
	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			done, err := cb.Allow()
			if err != nil {
				t.Errorf("Allow failed for worker %d: %v", idx, err)
				return
			}
			done(idx%3 != 0)
			done(false)
		}(i)
	}
	wg.Wait()
	if cb.State() != resilience.CircuitClosed {
		t.Fatalf("expected breaker to remain CLOSED below failure threshold, got %s", cb.State())
	}
}

func TestCircuitBreakerWithNameAndExecuteContextCancellation(t *testing.T) {
	cb := resilience.NewCircuitBreaker(1, time.Minute).WithName("SMTP")

	preCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	if err := cb.ExecuteContext(preCanceled, func(context.Context) error {
		ran = true
		return errors.New("should not run")
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected pre-canceled ExecuteContext to return context.Canceled, got %v", err)
	}
	if ran || cb.State() != resilience.CircuitClosed {
		t.Fatalf("expected pre-canceled call not to run or trip breaker, ran=%v state=%s", ran, cb.State())
	}

	inFlightCtx, cancelInFlight := context.WithCancel(context.Background())
	if err := cb.ExecuteContext(inFlightCtx, func(ctx context.Context) error {
		cancelInFlight()
		return ctx.Err()
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected caller-canceled in-flight ExecuteContext to return context.Canceled, got %v", err)
	}
	if cb.State() != resilience.CircuitClosed {
		t.Fatalf("expected caller context.Canceled not to trip upstream breaker, got %s", cb.State())
	}

	if err := cb.ExecuteContext(context.Background(), func(context.Context) error {
		return errors.New("smtp 451 failure")
	}); err == nil {
		t.Fatal("expected upstream failure")
	}
	openErr := cb.ExecuteContext(context.Background(), func(context.Context) error {
		return nil
	})
	if !isCircuitOpenErr(openErr) || !strings.Contains(openErr.Error(), "SMTP") {
		t.Fatalf("expected named SMTP circuit open error, got %v", openErr)
	}
}

package resilience

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

type CircuitState string

const (
	CircuitClosed   CircuitState = "CLOSED"
	CircuitOpen     CircuitState = "OPEN"
	CircuitHalfOpen CircuitState = "HALF_OPEN"
)

type CircuitBreaker struct {
	mu               sync.Mutex
	subsystem        string
	gen              uint64
	state            CircuitState
	failures         int
	failureThreshold int
	cooldown         time.Duration
	openedAt         time.Time
	halfOpenInFlight bool
	clock            func() time.Time
}

func NewCircuitBreaker(failureThreshold int, cooldown time.Duration) *CircuitBreaker {
	return NewCircuitBreakerWithClock(failureThreshold, cooldown, time.Now)
}

func NewCircuitBreakerWithClock(failureThreshold int, cooldown time.Duration, clock func() time.Time) *CircuitBreaker {
	if clock == nil {
		clock = time.Now
	}
	threshold, window := normalizeBreakerParams(failureThreshold, cooldown)
	return &CircuitBreaker{
		subsystem:        "External",
		state:            CircuitClosed,
		failureThreshold: threshold,
		cooldown:         window,
		clock:            clock,
	}
}

func (cb *CircuitBreaker) WithName(subsystem string) *CircuitBreaker {
	if cb == nil {
		return nil
	}
	clean := strings.TrimSpace(subsystem)
	if clean == "" {
		clean = "External"
	}
	cb.mu.Lock()
	cb.subsystem = clean
	cb.mu.Unlock()
	return cb
}

func normalizeBreakerParams(failureThreshold int, cooldown time.Duration) (int, time.Duration) {
	if failureThreshold < 1 {
		failureThreshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return failureThreshold, cooldown
}

func (cb *CircuitBreaker) subsystemLabelLocked() string {
	if cb.subsystem == "" {
		return "External"
	}
	return cb.subsystem
}

func (cb *CircuitBreaker) State() CircuitState {
	if cb == nil {
		return CircuitClosed
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == CircuitOpen && cb.clock().Sub(cb.openedAt) >= cb.cooldown {
		return CircuitHalfOpen
	}
	return cb.state
}

func (cb *CircuitBreaker) Allow() (func(success bool), error) {
	done, _, err := cb.AllowAbortable()
	return done, err
}

func (cb *CircuitBreaker) AllowAbortable() (func(success bool), func(), error) {
	if cb == nil {
		return func(bool) {}, func() {}, nil
	}
	cb.mu.Lock()
	if err := cb.acquireSlotLocked(); err != nil {
		cb.mu.Unlock()
		return nil, nil, err
	}
	callGen := cb.gen
	cb.mu.Unlock()

	var once sync.Once
	done := func(success bool) {
		once.Do(func() {
			cb.completeCall(callGen, success)
		})
	}
	abort := func() {
		once.Do(func() {
			cb.abortCall(callGen)
		})
	}
	return done, abort, nil
}

func (cb *CircuitBreaker) acquireSlotLocked() error {
	label := cb.subsystemLabelLocked()
	switch cb.state {
	case CircuitOpen:
		if cb.clock().Sub(cb.openedAt) < cb.cooldown {
			return derr.New(derr.CodeCircuitOpen, 503, "upstream "+label+" circuit breaker is open", nil)
		}
		cb.gen++
		cb.state = CircuitHalfOpen
		cb.halfOpenInFlight = true
		return nil
	case CircuitHalfOpen:
		if cb.halfOpenInFlight {
			return derr.New(derr.CodeCircuitOpen, 503, "upstream "+label+" probe in progress", nil)
		}
		cb.halfOpenInFlight = true
		return nil
	default:
		return nil
	}
}

func (cb *CircuitBreaker) completeCall(callGen uint64, success bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if callGen != cb.gen {
		return
	}
	if success {
		if cb.state == CircuitHalfOpen {
			cb.gen++
		}
		cb.failures = 0
		cb.state = CircuitClosed
		cb.halfOpenInFlight = false
		return
	}
	cb.halfOpenInFlight = false
	cb.failures++
	if cb.state == CircuitHalfOpen || cb.failures >= cb.failureThreshold {
		cb.gen++
		cb.failures = 0
		cb.state = CircuitOpen
		cb.openedAt = cb.clock()
	}
}

func (cb *CircuitBreaker) abortCall(callGen uint64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if callGen != cb.gen {
		return
	}
	cb.halfOpenInFlight = false
}

func (cb *CircuitBreaker) AllowContext(ctx context.Context) (func(success bool), func(), error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	return cb.AllowAbortable()
}

func (cb *CircuitBreaker) Execute(fn func() error) error {
	return cb.ExecuteContext(context.Background(), func(context.Context) error {
		return fn()
	})
}

func (cb *CircuitBreaker) ExecuteContext(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	done, abort, err := cb.AllowContext(ctx)
	if err != nil {
		return err
	}
	succeeded := false
	aborted := false
	defer func() {
		if aborted {
			abort()
			return
		}
		done(succeeded)
	}()
	runErr := fn(ctx)
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(runErr, context.Canceled) {
		aborted = true
		return runErr
	}
	succeeded = runErr == nil
	return runErr
}

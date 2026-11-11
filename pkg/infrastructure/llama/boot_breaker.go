package llama

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/resilience"
)

const MaxBootBreakerEntries = 256

type PoolBootCircuitBreaker struct {
	mu               sync.Mutex
	breakers         map[string]*resilience.CircuitBreaker
	order            []string
	maxEntries       int
	failureThreshold int
	cooldown         time.Duration
	clock            func() time.Time
}

func NewPoolBootCircuitBreaker(failureThreshold int, cooldown time.Duration) *PoolBootCircuitBreaker {
	return NewPoolBootCircuitBreakerWithClock(failureThreshold, cooldown, nil)
}

func NewPoolBootCircuitBreakerWithClock(failureThreshold int, cooldown time.Duration, clock func() time.Time) *PoolBootCircuitBreaker {
	if failureThreshold < 1 {
		failureThreshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &PoolBootCircuitBreaker{
		breakers:         make(map[string]*resilience.CircuitBreaker),
		maxEntries:       MaxBootBreakerEntries,
		failureThreshold: failureThreshold,
		cooldown:         cooldown,
		clock:            clock,
	}
}

func (p *PoolBootCircuitBreaker) WithMaxEntries(maxEntries int) *PoolBootCircuitBreaker {
	if p == nil || maxEntries < 1 {
		return p
	}
	p.mu.Lock()
	p.maxEntries = maxEntries
	p.evictOverflowLocked()
	p.mu.Unlock()
	return p
}

func BootKey(backend nodecore.EngineBackend, modelPath string) string {
	b := strings.ToLower(strings.TrimSpace(string(backend)))
	if b == "" {
		b = string(nodecore.BackendLlamaRPC)
	}
	p := filepath.Clean(strings.TrimSpace(modelPath))
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return b + "|" + p
}

func (p *PoolBootCircuitBreaker) Allow(key string) (func(success bool), error) {
	done, _, err := p.AllowAbortable(key)
	return done, err
}

func (p *PoolBootCircuitBreaker) AllowAbortable(key string) (func(success bool), func(), error) {
	if p == nil {
		return func(bool) {}, func() {}, nil
	}
	cb := p.breakerForKey(key)
	done, abort, err := cb.AllowAbortable()
	if err != nil {
		return nil, nil, derr.New(derr.CodeCircuitOpen, 503, fmt.Sprintf("pool boot circuit breaker is open for %s", key), nil)
	}
	return done, abort, nil
}

func (p *PoolBootCircuitBreaker) Execute(key string, fn func() error) error {
	done, err := p.Allow(key)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		done(succeeded)
	}()
	runErr := fn()
	succeeded = runErr == nil
	return runErr
}

func (p *PoolBootCircuitBreaker) State(key string) resilience.CircuitState {
	if p == nil {
		return resilience.CircuitClosed
	}
	norm := normalizeBootKey(key)
	p.mu.Lock()
	cb, exists := p.breakers[norm]
	p.mu.Unlock()
	if !exists {
		return resilience.CircuitClosed
	}
	return cb.State()
}

func (p *PoolBootCircuitBreaker) Len() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.breakers)
}

func (p *PoolBootCircuitBreaker) breakerForKey(key string) *resilience.CircuitBreaker {
	norm := normalizeBootKey(key)
	p.mu.Lock()
	defer p.mu.Unlock()
	if cb, exists := p.breakers[norm]; exists {
		return cb
	}
	p.evictBeforeInsertLocked()
	cb := resilience.NewCircuitBreakerWithClock(p.failureThreshold, p.cooldown, p.clock)
	p.breakers[norm] = cb
	p.order = append(p.order, norm)
	return cb
}

func (p *PoolBootCircuitBreaker) evictBeforeInsertLocked() {
	limit := p.maxEntries
	if limit < 1 {
		limit = MaxBootBreakerEntries
	}
	for len(p.breakers) >= limit && len(p.order) > 0 {
		oldest := p.order[0]
		p.order = p.order[1:]
		delete(p.breakers, oldest)
	}
}

func (p *PoolBootCircuitBreaker) evictOverflowLocked() {
	for len(p.breakers) > p.maxEntries && len(p.order) > 0 {
		oldest := p.order[0]
		p.order = p.order[1:]
		delete(p.breakers, oldest)
	}
}

func normalizeBootKey(key string) string {
	norm := strings.TrimSpace(key)
	if norm == "" {
		return "default"
	}
	return norm
}

package observability

import (
	"io"
	"log/slog"
	"math"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/trace"
)

func NewJSONLogger(w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func NewTraceParent() string {
	return trace.NewTraceParent()
}

func NormalizeTraceParent(raw string) string {
	if norm, ok := trace.NormalizeTraceParent(raw); ok {
		return norm
	}
	return trace.NewTraceParent()
}

type LamportClock struct {
	counter atomic.Uint64
}

func (c *LamportClock) Tick() uint64 {
	return c.counter.Add(1)
}

func (c *LamportClock) Current() uint64 {
	return c.counter.Load()
}

type PercentileSummary struct {
	Count int64   `json:"count"`
	Min   float64 `json:"min"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
}

type RingHistogram struct {
	mu      sync.Mutex
	samples []float64
	sorted  []float64
	cached  PercentileSummary
	dirty   bool
	head    int
	full    bool
	total   int64
}

func NewRingHistogram(capacity int) *RingHistogram {
	if capacity < 16 {
		capacity = 512
	}
	return &RingHistogram{samples: make([]float64, capacity)}
}

func (h *RingHistogram) Record(v float64) {
	if h == nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return
	}
	h.mu.Lock()
	h.samples[h.head] = v
	h.head++
	if h.head >= len(h.samples) {
		h.head = 0
		h.full = true
	}
	h.total++
	h.dirty = true
	h.mu.Unlock()
}

func (h *RingHistogram) Snapshot() PercentileSummary {
	if h == nil {
		return PercentileSummary{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.dirty {
		return h.cached
	}
	n := h.head
	if h.full {
		n = len(h.samples)
	}
	if n == 0 {
		h.dirty = false
		h.cached = PercentileSummary{}
		return h.cached
	}
	if cap(h.sorted) < n {
		h.sorted = make([]float64, n)
	} else {
		h.sorted = h.sorted[:n]
	}
	copy(h.sorted, h.samples[:n])
	slices.Sort(h.sorted)
	h.cached = PercentileSummary{
		Count: h.total,
		Min:   RoundMetric(h.sorted[0]),
		P50:   RoundMetric(percentileSorted(h.sorted, 0.50)),
		P95:   RoundMetric(percentileSorted(h.sorted, 0.95)),
		P99:   RoundMetric(percentileSorted(h.sorted, 0.99)),
		Max:   RoundMetric(h.sorted[len(h.sorted)-1]),
	}
	h.dirty = false
	return h.cached
}

func ComputePercentiles(values []float64) PercentileSummary {
	if len(values) == 0 {
		return PercentileSummary{}
	}
	cp := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 {
			cp = append(cp, v)
		}
	}
	if len(cp) == 0 {
		return PercentileSummary{}
	}
	slices.Sort(cp)
	return PercentileSummary{
		Count: int64(len(cp)),
		Min:   RoundMetric(cp[0]),
		P50:   RoundMetric(percentileSorted(cp, 0.50)),
		P95:   RoundMetric(percentileSorted(cp, 0.95)),
		P99:   RoundMetric(percentileSorted(cp, 0.99)),
		Max:   RoundMetric(cp[len(cp)-1]),
	}
}

func percentileSorted(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func RoundMetric(v float64) float64 {
	return math.Round(v*100) / 100
}

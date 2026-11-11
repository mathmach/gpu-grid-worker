package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

type contextKey struct{}

type TraceContext struct {
	Version string
	TraceID string
	SpanID  string
	Flags   string
}

func (tc TraceContext) String() string {
	return fmt.Sprintf("%s-%s-%s-%s", tc.Version, tc.TraceID, tc.SpanID, tc.Flags)
}

func NewTraceParent() string {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00-00000000000000000000000000000001-0000000000000001-01"
	}
	ensureNonZeroSlice(buf[:16])
	ensureNonZeroSlice(buf[16:24])
	return fmt.Sprintf("00-%s-%s-01", hex.EncodeToString(buf[:16]), hex.EncodeToString(buf[16:24]))
}

func ParseTraceParent(raw string) (TraceContext, error) {
	clean := strings.ToLower(strings.TrimSpace(raw))
	if len(clean) != 55 || clean[2] != '-' || clean[35] != '-' || clean[52] != '-' {
		return TraceContext{}, derr.New(derr.CodeValidation, 400, "invalid W3C traceparent format", nil)
	}
	version, traceID, spanID, flags := clean[0:2], clean[3:35], clean[36:52], clean[53:55]
	if version == "ff" || !isValidLowerHex(version) || !isValidLowerHex(flags) {
		return TraceContext{}, derr.New(derr.CodeValidation, 400, "invalid W3C traceparent version or flags", nil)
	}
	if !isValidLowerHex(traceID) || isAllZeroHex(traceID) || !isValidLowerHex(spanID) || isAllZeroHex(spanID) {
		return TraceContext{}, derr.New(derr.CodeValidation, 400, "traceparent trace-id and parent-id must be non-zero hex", nil)
	}
	return TraceContext{
		Version: version,
		TraceID: traceID,
		SpanID:  spanID,
		Flags:   flags,
	}, nil
}

func NormalizeTraceParent(raw string) (string, bool) {
	parsed, err := ParseTraceParent(raw)
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func WithTraceParent(ctx context.Context, tp string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	norm, ok := NormalizeTraceParent(tp)
	if !ok {
		norm = NewTraceParent()
	}
	return context.WithValue(ctx, contextKey{}, norm)
}

func LookupTraceParent(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(contextKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

func TraceParentFromContext(ctx context.Context) string {
	if tp, ok := LookupTraceParent(ctx); ok {
		return tp
	}
	return NewTraceParent()
}

func ensureNonZeroSlice(b []byte) {
	for _, v := range b {
		if v != 0 {
			return
		}
	}
	if len(b) > 0 {
		b[len(b)-1] = 1
	}
}

func isValidLowerHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func isAllZeroHex(s string) bool {
	for i := range len(s) {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

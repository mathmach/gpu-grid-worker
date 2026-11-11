package idle

import (
	"context"
	"math"
	"strconv"
	"strings"
)

type OSIdleDetector struct{}

func NewOSIdleDetector() *OSIdleDetector {
	return &OSIdleDetector{}
}

func (d *OSIdleDetector) UserIdleSeconds() int {
	return UserIdleSeconds()
}

type UnixCommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type UnixIdleProbeDeps struct {
	RunCommand      UnixCommandRunner
	MonotonicMicros func() int64
}

func ProbeUnixIdleWithDeps(ctx context.Context, deps UnixIdleProbeDeps) int {
	if deps.RunCommand == nil {
		return -1
	}
	if sec := probeX11IdleSeconds(ctx, deps.RunCommand); sec >= 0 {
		return sec
	}
	if sec := probeGdbusIdleSeconds(ctx, deps.RunCommand); sec >= 0 {
		return sec
	}
	return probeLoginctlIdleSeconds(ctx, deps)
}

func probeX11IdleSeconds(ctx context.Context, run UnixCommandRunner) int {
	if out, err := run(ctx, "xssstate", "-i"); err == nil {
		if sec := ParseMillisIdleOutput(string(out)); sec >= 0 {
			return sec
		}
	}
	if out, err := run(ctx, "xprintidle"); err == nil {
		if sec := ParseMillisIdleOutput(string(out)); sec >= 0 {
			return sec
		}
	}
	return -1
}

func probeGdbusIdleSeconds(ctx context.Context, run UnixCommandRunner) int {
	out, err := run(
		ctx,
		"gdbus",
		"call",
		"--session",
		"--dest", "org.gnome.Mutter.IdleMonitor",
		"--object-path", "/org/gnome/Mutter/IdleMonitor/Core",
		"--method", "org.gnome.Mutter.IdleMonitor.GetIdletime",
	)
	if err != nil {
		return -1
	}
	return ParseGdbusIdleOutput(string(out))
}

func probeLoginctlIdleSeconds(ctx context.Context, deps UnixIdleProbeDeps) int {
	var nowMicros int64
	if deps.MonotonicMicros != nil {
		nowMicros = deps.MonotonicMicros()
	}
	out, err := deps.RunCommand(ctx, "loginctl", "show-session", "-p", "IdleHint", "-p", "IdleSinceHintMonotonic", "-p", "TimestampMonotonic")
	if err == nil {
		return ParseLoginctlIdleOutput(string(out), nowMicros)
	}
	sessionID := discoverLoginctlSessionID(ctx, deps.RunCommand)
	if sessionID == "" {
		return -1
	}
	sessOut, sessErr := deps.RunCommand(ctx, "loginctl", "show-session", sessionID, "-p", "IdleHint", "-p", "IdleSinceHintMonotonic", "-p", "TimestampMonotonic")
	if sessErr != nil {
		return -1
	}
	return ParseLoginctlIdleOutput(string(sessOut), nowMicros)
}

func discoverLoginctlSessionID(ctx context.Context, run UnixCommandRunner) string {
	out, err := run(ctx, "loginctl", "list-sessions", "--no-legend")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] != "" {
			return fields[0]
		}
	}
	return ""
}

func ParseMillisIdleOutput(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return -1
	}
	ms, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || ms < 0 {
		return -1
	}
	return clampIdleSeconds(ms / 1000)
}

func ParseGdbusIdleOutput(raw string) int {
	if !strings.Contains(raw, "uint64") {
		return -1
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if len(fields) == 0 {
		return -1
	}
	ms, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	if err != nil || ms < 0 {
		return -1
	}
	return clampIdleSeconds(ms / 1000)
}

type loginctlIdleProps struct {
	hint          string
	sinceMicros   int64
	stampMicros   int64
	hasSinceField bool
}

func ParseLoginctlIdleOutput(raw string, nowMonotonicMicros int64) int {
	props := parseLoginctlProperties(raw)
	if !props.hasSinceField || props.sinceMicros <= 0 {
		return -1
	}
	effectiveNow := nowMonotonicMicros
	if effectiveNow <= 0 && props.stampMicros > 0 {
		effectiveNow = props.stampMicros
	}
	return computeLoginctlIdleSeconds(props.hint, props.sinceMicros, effectiveNow)
}

func parseLoginctlProperties(raw string) loginctlIdleProps {
	var props loginctlIdleProps
	for _, line := range strings.Split(raw, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		applyLoginctlProperty(&props, strings.TrimSpace(key), strings.TrimSpace(val))
	}
	return props
}

func applyLoginctlProperty(props *loginctlIdleProps, key, val string) {
	switch key {
	case "IdleHint":
		props.hint = strings.ToLower(val)
	case "IdleSinceHintMonotonic":
		if parsed, err := strconv.ParseUint(val, 10, 64); err == nil && parsed <= math.MaxInt64 {
			props.sinceMicros = int64(parsed)
			props.hasSinceField = true
		}
	case "TimestampMonotonic":
		if parsed, err := strconv.ParseUint(val, 10, 64); err == nil && parsed <= math.MaxInt64 {
			props.stampMicros = int64(parsed)
		}
	}
}

func computeLoginctlIdleSeconds(hint string, sinceMicros, nowMonotonicMicros int64) int {
	switch hint {
	case "yes", "true", "1":
		if nowMonotonicMicros < sinceMicros {
			return -1
		}
		return clampIdleSeconds((nowMonotonicMicros - sinceMicros) / 1_000_000)
	case "no", "false", "0":
		return 0
	default:
		return -1
	}
}

func clampIdleSeconds(sec int64) int {
	if sec < 0 {
		return -1
	}
	if sec > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(sec)
}

func ParseProcUptimeMicros(raw string) int64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return -1
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 {
		return -1
	}
	return int64(sec * 1_000_000)
}

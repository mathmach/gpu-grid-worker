//go:build !windows

package idle

import (
	"context"
	"os"
	"os/exec"
	"time"
)

func UserIdleSeconds() int {
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	return ProbeUnixIdleWithDeps(ctx, UnixIdleProbeDeps{
		RunCommand:      runLookPathCommand,
		MonotonicMicros: readSystemMonotonicMicros,
	})
}

func runLookPathCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	binPath, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, binPath, args...).Output()
}

func readProcUptimeMicros() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return -1
	}
	return ParseProcUptimeMicros(string(b))
}

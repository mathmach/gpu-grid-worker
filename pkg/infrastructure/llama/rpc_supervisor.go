package llama

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type RPCSupervisor struct {
	startupTimeout time.Duration
}

func NewRPCSupervisor() *RPCSupervisor {
	return &RPCSupervisor{startupTimeout: 12 * time.Second}
}

func (s *RPCSupervisor) SpawnRPCServer(ctx context.Context, runtimeDir, cacheDir string, gpus []protocol.GpuInfo, port int) (*SupervisedProcess, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	if _, err := newRPCServerBuilder().withPort(max(1, port)).withGPUs(gpus).buildArgs(); err != nil {
		return nil, err
	}
	binPath := FindExecutableRecursive(runtimeDir, rpcServerBinaryFileName())
	if binPath == "" {
		return nil, derr.New(derr.CodeUpstreamFailure, 502, "ggml-rpc-server binary not found in worker runtime", nil)
	}

	timeout := 12 * time.Second
	if s != nil && s.startupTimeout > 0 {
		timeout = s.startupTimeout
	}
	proc, _, err := LaunchLocalHTTPSubprocess(ctx, LocalHTTPLaunchSpec{
		BinaryPath:  binPath,
		WorkDir:     cacheDir,
		ProcessName: "ggml-rpc-server",
		BuildArgs: func(boundPort int) ([]string, error) {
			return newRPCServerBuilder().withPort(boundPort).withGPUs(gpus).buildArgs()
		},
		Port:           port,
		Readiness:      ReadinessTCP,
		StartupTimeout: timeout,
		MarkBootReady:  true,
		AllowPortRetry: true,
	})
	return proc, err
}

func rpcServerBinaryFileName() string {
	if runtime.GOOS == "windows" {
		return "ggml-rpc-server.exe"
	}
	return "ggml-rpc-server"
}

func WaitForLocalTCP(ctx context.Context, proc *SupervisedProcess, port int, timeout time.Duration, processName string) error {
	name := strings.TrimSpace(processName)
	if name == "" {
		name = "supervised process"
	}
	deadline := time.Now().Add(timeout)
	target := fmt.Sprintf("127.0.0.1:%d", port)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := net.DialTimeout("tcp", target, 250*time.Millisecond)
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				return closeErr
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-proc.Done():
			return fmt.Errorf("%s exited before TCP readiness: %s", name, proc.StderrTail())
		case <-ticker.C:
		}
	}
	return fmt.Errorf("timed out waiting for local %s startup", name)
}

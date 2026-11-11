package llama

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/tunnel"
)

type ReadinessMode uint8

const (
	ReadinessNone ReadinessMode = iota
	ReadinessTCP
	ReadinessHTTPHealth
)

func ReserveFreeLoopbackPort() (int, error) {
	return tunnel.ReserveLoopbackPort()
}

func IsAddressInUseOutput(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, token := range []string{
		"address already in use",
		"only one usage of each socket address",
		"eaddrinuse",
		"failed to bind",
		"cannot bind",
		"bind() failed",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

type LocalHTTPLaunchSpec struct {
	BinaryPath     string
	WorkDir        string
	ProcessName    string
	Backend        nodecore.EngineBackend
	BuildArgs      func(port int) ([]string, error)
	LibraryPath    string
	ExtraEnv       []string
	BaseEnv        []string
	BootBreaker    *PoolBootCircuitBreaker
	ModelKey       string
	Port           int
	Readiness      ReadinessMode
	HealthPath     string
	StartupTimeout time.Duration
	MarkBootReady  bool
	AllowPortRetry bool
}

func LaunchLocalHTTPSubprocess(ctx context.Context, spec LocalHTTPLaunchSpec) (*SupervisedProcess, int, error) {
	autoPort := spec.Port <= 0
	maxAttempts := 1
	if autoPort || spec.AllowPortRetry {
		maxAttempts = 2
	}

	var lastErr error
	for attempt := range maxAttempts {
		port, err := resolveAttemptPort(spec.Port, autoPort || attempt > 0, spec.AllowPortRetry)
		if err != nil {
			return nil, 0, err
		}
		proc, err := launchSingleHTTPAttempt(ctx, spec, port)
		if err == nil {
			return proc, port, nil
		}
		lastErr = err
		if ctx != nil && ctx.Err() != nil {
			return nil, 0, err
		}
		if !IsAddressInUseOutput(err.Error()) {
			return nil, 0, err
		}
	}
	return nil, 0, lastErr
}

func resolveAttemptPort(configuredPort int, allocateNew, allowRetry bool) (int, error) {
	if !allocateNew && configuredPort > 0 {
		if !allowRetry || isLoopbackPortBindable(configuredPort) {
			return configuredPort, nil
		}
	}
	return ReserveFreeLoopbackPort()
}

func isLoopbackPortBindable(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	if closeErr := ln.Close(); closeErr != nil {
		return false
	}
	return true
}

func launchSingleHTTPAttempt(ctx context.Context, spec LocalHTTPLaunchSpec, port int) (*SupervisedProcess, error) {
	if spec.BuildArgs == nil {
		return nil, errors.New("missing subprocess argument builder")
	}
	args, err := spec.BuildArgs(port)
	if err != nil {
		return nil, err
	}
	workDir := strings.TrimSpace(spec.WorkDir)
	if workDir == "" {
		workDir = filepath.Dir(spec.BinaryPath)
	}
	proc, err := StartSupervisedProcess(ctx, ProcessLaunchConfig{
		BinaryPath:     spec.BinaryPath,
		Args:           args,
		WorkDir:        workDir,
		BaseEnv:        spec.BaseEnv,
		ExtraEnv:       spec.ExtraEnv,
		LibraryPath:    spec.LibraryPath,
		BootBreaker:    spec.BootBreaker,
		BootKey:        BootKey(spec.Backend, spec.ModelKey),
		HealthCheckURL: resolveLaunchHealthURL(spec, port),
		StartupTimeout: spec.StartupTimeout,
		Readiness:      spec.Readiness,
		BoundPort:      port,
	})
	if err != nil {
		return nil, err
	}
	if err := awaitTCPReadinessIfConfigured(ctx, proc, spec, port); err != nil {
		return nil, err
	}
	return proc, nil
}

func resolveLaunchHealthURL(spec LocalHTTPLaunchSpec, port int) string {
	if spec.Readiness != ReadinessHTTPHealth {
		return ""
	}
	path := strings.TrimSpace(spec.HealthPath)
	if path == "" {
		path = "/health"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

func awaitTCPReadinessIfConfigured(ctx context.Context, proc *SupervisedProcess, spec LocalHTTPLaunchSpec, port int) error {
	if spec.Readiness != ReadinessTCP {
		return nil
	}
	if waitErr := WaitForLocalTCP(ctx, proc, port, spec.StartupTimeout, spec.ProcessName); waitErr != nil {
		if IsAddressInUseOutput(proc.StderrTail()) {
			proc.abortBoot()
		} else {
			proc.MarkBootOutcome(false)
		}
		proc.Stop()
		return waitErr
	}
	if spec.MarkBootReady {
		proc.MarkBootOutcome(true)
	}
	return nil
}

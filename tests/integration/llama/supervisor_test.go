package llama_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/resilience"
)

func TestLlamaSubprocessHelper(t *testing.T) {
	mode := extractTestHelperMode(os.Args)
	switch mode {
	case "hold":
		holdTimer := time.NewTimer(30 * time.Second)
		<-holdTimer.C
		os.Exit(0)
	case "exit0":
		os.Exit(0)
	case "crash":
		if _, err := os.Stderr.WriteString("fatal model load failure\n"); err != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func extractTestHelperMode(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--" {
			return args[i+1]
		}
	}
	return ""
}

func TestSupervisedProcessJobObjectAndConcurrentStopRaceFree(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}

	proc, err := llama.StartSupervisedProcess(context.Background(), llama.ProcessLaunchConfig{
		BinaryPath: exePath,
		Args:       []string{"-test.run=^TestLlamaSubprocessHelper$", "--", "hold"},
		BaseEnv:    []string{"PATH="},
	})
	if err != nil {
		t.Fatalf("StartSupervisedProcess failed: %v", err)
	}
	t.Cleanup(proc.Stop)
	if !proc.JobObjectAttached() {
		t.Fatalf("expected OS process guard / Job Object attached")
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proc.Stop()
		}()
	}
	wg.Wait()

	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("expected supervised child process terminated promptly")
	}
}

func TestCrashingSubprocessTripsPoolBootCircuitBreaker(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	breaker := llama.NewPoolBootCircuitBreaker(2, time.Minute)
	bootKey := llama.BootKey(nodecore.BackendLlamaLocal, "bad-weights.gguf")

	for range 2 {
		proc, startErr := llama.StartSupervisedProcess(context.Background(), llama.ProcessLaunchConfig{
			BinaryPath:  exePath,
			Args:        []string{"-test.run=^TestLlamaSubprocessHelper$", "--", "crash"},
			BootBreaker: breaker,
			BootKey:     bootKey,
		})
		if startErr != nil {
			t.Fatalf("unexpected start error before breaker opened: %v", startErr)
		}
		t.Cleanup(proc.Stop)
		<-proc.Done()
		if !strings.Contains(proc.StderrTail(), "fatal model load failure") {
			t.Fatalf("expected crash stderr captured, got %q", proc.StderrTail())
		}
	}

	thirdProc, thirdErr := llama.StartSupervisedProcess(context.Background(), llama.ProcessLaunchConfig{
		BinaryPath:  exePath,
		Args:        []string{"-test.run=^TestLlamaSubprocessHelper$", "--", "crash"},
		BootBreaker: breaker,
		BootKey:     bootKey,
	})
	if thirdProc != nil {
		thirdProc.Stop()
	}
	appErr, ok := derr.AsAppError(thirdErr)
	if !ok || appErr.Code != derr.CodeCircuitOpen {
		t.Fatalf("expected fast-fail CodeCircuitOpen on 3rd spawn attempt, got %v", thirdErr)
	}
}

func TestFastStopReleasesHalfOpenAndSupervisorHonorsContextCancel(t *testing.T) {
	now := time.Now()
	cb := llama.NewPoolBootCircuitBreakerWithClock(1, 5*time.Second, func() time.Time { return now })

	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	key := llama.BootKey(nodecore.BackendLlamaLocal, "fast-stop.gguf")
	done, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("Allow failed: %v", err)
	}
	done(false)
	now = now.Add(6 * time.Second)

	proc, err := llama.StartSupervisedProcess(context.Background(), llama.ProcessLaunchConfig{
		BinaryPath:  exePath,
		Args:        []string{"-test.run=^TestLlamaSubprocessHelper$", "--", "hold"},
		BootBreaker: cb,
		BootKey:     key,
	})
	if err != nil {
		t.Fatalf("expected HALF_OPEN probe process to launch: %v", err)
	}
	t.Cleanup(proc.Stop)
	proc.Stop()
	if cb.State(key) != resilience.CircuitHalfOpen {
		t.Fatalf("expected unverified Stop to abort HALF_OPEN probe without closing breaker, got %s", cb.State(key))
	}
	probe2Done, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("expected aborted HALF_OPEN slot to release for next probe, got %v", err)
	}
	probe2Done(true)
	if cb.State(key) != resilience.CircuitClosed {
		t.Fatalf("expected verified HALF_OPEN probe to close breaker, got %s", cb.State(key))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	t.Cleanup(cancel)
	started := time.Now()
	canceledProc, startErr := llama.StartSupervisedProcess(ctx, llama.ProcessLaunchConfig{
		BinaryPath:     exePath,
		Args:           []string{"-test.run=^TestLlamaSubprocessHelper$", "--", "hold"},
		HealthCheckURL: "http://127.0.0.1:9/health",
		StartupTimeout: 30 * time.Second,
	})
	if canceledProc != nil {
		canceledProc.Stop()
	}
	if startErr == nil {
		t.Fatal("expected StartSupervisedProcess to abort when context cancels during health polling")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("expected immediate cancellation on ctx.Done(), took %v", time.Since(started))
	}
}

func TestUnverifiedZeroExitSubprocessTripsPoolBootCircuitBreaker(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	breaker := llama.NewPoolBootCircuitBreaker(2, time.Minute)
	bootKey := llama.BootKey(nodecore.BackendLlamaLocal, "exit0-before-health.gguf")

	for range 2 {
		proc, startErr := llama.StartSupervisedProcess(context.Background(), llama.ProcessLaunchConfig{
			BinaryPath:  exePath,
			Args:        []string{"-test.run=^TestLlamaSubprocessHelper$", "--", "exit0"},
			BootBreaker: breaker,
			BootKey:     bootKey,
		})
		if startErr != nil {
			t.Fatalf("unexpected start error before breaker opened: %v", startErr)
		}
		t.Cleanup(proc.Stop)
		<-proc.Done()
	}

	thirdProc, thirdErr := llama.StartSupervisedProcess(context.Background(), llama.ProcessLaunchConfig{
		BinaryPath:  exePath,
		Args:        []string{"-test.run=^TestLlamaSubprocessHelper$", "--", "exit0"},
		BootBreaker: breaker,
		BootKey:     bootKey,
	})
	if thirdProc != nil {
		thirdProc.Stop()
	}
	appErr, ok := derr.AsAppError(thirdErr)
	if !ok || appErr.Code != derr.CodeCircuitOpen {
		t.Fatalf("expected unverified exit-0 before MarkReady() to trip CodeCircuitOpen on 3rd attempt, got %v", thirdErr)
	}
}

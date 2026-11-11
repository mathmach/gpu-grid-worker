package llama_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/resilience"
)

func TestBoundedTailBufferNeverExceedsCapacity(t *testing.T) {
	rb := llama.NewBoundedTailBuffer(64)
	chunk := strings.Repeat("ABCDEFGH", 50)
	for range 20 {
		if _, err := rb.Write([]byte(chunk)); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	if _, err := rb.Write([]byte("-FINAL_TAIL")); err != nil {
		t.Fatalf("write final failed: %v", err)
	}
	got := rb.String()
	if len(got) != 64 {
		t.Fatalf("expected bounded length 64, got %d", len(got))
	}
	if !strings.HasSuffix(got, "-FINAL_TAIL") {
		t.Fatalf("expected tail preserved, got %q", got)
	}
}

func TestBackendFactorySupportsOnlyGGUF(t *testing.T) {
	f := llama.NewBackendFactory("", "")
	if !f.SupportsFormat(nodecore.FormatGGUF) {
		t.Fatalf("expected GGUF format supported")
	}
	if f.SupportsFormat(nodecore.FormatSafetensors) {
		t.Fatalf("expected Safetensors format rejected")
	}
	if f.SupportsFormat(nodecore.FormatONNX) {
		t.Fatalf("expected ONNX format rejected")
	}
}

func TestLlamaServerBuilderSupportsMeshAndSoloModesAndContextScaling(t *testing.T) {
	meshArgs, err := llama.NewLlamaServerBuilder().
		WithModel("models/qwen.gguf").
		WithRPCEndpoints([]string{"127.0.0.1:50001", "127.0.0.1:50002"}).
		WithRPCDevices([]string{"RPC0", "RPC1"}).
		WithParallel(2).
		WithTensorSplit([]int{60, 40}).
		WithBind("127.0.0.1", 18080).
		BuildArgs()
	if err != nil {
		t.Fatalf("unexpected mesh BuildArgs error: %v", err)
	}
	assertArgPair(t, meshArgs, "--rpc", "127.0.0.1:50001,127.0.0.1:50002")
	assertArgPair(t, meshArgs, "--device", "RPC0,RPC1")
	assertArgPair(t, meshArgs, "-c", "4096")
	assertArgPair(t, meshArgs, "--tensor-split", "60,40")

	soloArgs, err := llama.NewLlamaServerBuilder().
		WithModel("models/qwen.gguf").
		WithMode(nodecore.PoolModeSolo).
		WithLocalDevices([]string{"Vulkan0", "Vulkan1"}).
		WithParallel(2).
		WithContextWindow(16384, 24576).
		WithBind("127.0.0.1", 18081).
		BuildArgs()
	if err != nil {
		t.Fatalf("unexpected solo BuildArgs error: %v", err)
	}
	if slices.Contains(soloArgs, "--rpc") {
		t.Fatalf("solo mode must not emit --rpc flag: %v", soloArgs)
	}
	assertArgPair(t, soloArgs, "--device", "Vulkan0,Vulkan1")
	assertArgPair(t, soloArgs, "-c", "32768")

	lowVRAMArgs, err := llama.NewLlamaServerBuilder().
		WithModel("models/qwen.gguf").
		WithLocalDevices([]string{"Vulkan0"}).
		WithParallel(1).
		WithContextWindow(131072, 3072).
		WithBind("127.0.0.1", 18082).
		BuildArgs()
	if err != nil {
		t.Fatalf("unexpected low-VRAM BuildArgs error: %v", err)
	}
	assertArgPair(t, lowVRAMArgs, "-c", "2048")
}

func TestPoolBootCircuitBreakerFailsFastBoundsMapAndRecoversAfterCooldown(t *testing.T) {
	now := time.Now()
	cb := llama.NewPoolBootCircuitBreakerWithClock(2, 10*time.Second, func() time.Time { return now })
	key := llama.BootKey(nodecore.BackendLlamaLocal, "corrupt.gguf")

	done1, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("expected closed breaker to allow: %v", err)
	}
	done1(false)
	if cb.State(key) != resilience.CircuitClosed {
		t.Fatalf("expected still closed after 1 failure, got %s", cb.State(key))
	}
	done2, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("expected closed breaker to allow 2nd call: %v", err)
	}
	done2(false)
	if cb.State(key) != resilience.CircuitOpen {
		t.Fatalf("expected open after 2 failures, got %s", cb.State(key))
	}
	_, err = cb.Allow(key)
	appErr, ok := derr.AsAppError(err)
	if !ok || appErr.Code != derr.CodeCircuitOpen {
		t.Fatalf("expected CodeCircuitOpen error, got %v", err)
	}

	otherKey := llama.BootKey(nodecore.BackendLlamaLocal, "healthy.gguf")
	otherDone, allowErr := cb.Allow(otherKey)
	if allowErr != nil {
		t.Fatalf("other model key must remain isolated and closed: %v", allowErr)
	}
	otherDone(true)

	now = now.Add(11 * time.Second)
	probeDone, allowErr := cb.Allow(key)
	if allowErr != nil {
		t.Fatalf("expected half-open probe allowed after cooldown: %v", allowErr)
	}
	probeDone(true)
	if cb.State(key) != resilience.CircuitClosed {
		t.Fatalf("expected closed after half-open success, got %s", cb.State(key))
	}

	bounded := llama.NewPoolBootCircuitBreakerWithClock(1, 5*time.Second, func() time.Time { return now }).WithMaxEntries(4)
	for i := range 10 {
		k := llama.BootKey(nodecore.BackendLlamaLocal, strings.Repeat("m", i+1)+".gguf")
		d, bErr := bounded.Allow(k)
		if bErr != nil {
			t.Fatalf("unexpected Allow error at %d: %v", i, bErr)
		}
		d(false)
	}
	if bounded.Len() != 4 {
		t.Fatalf("expected boot breaker map bounded to 4 entries, got %d", bounded.Len())
	}
}

func assertArgPair(t *testing.T, args []string, flagName, wantVal string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flagName {
			if args[i+1] != wantVal {
				t.Fatalf("flag %s expected %q, got %q in %v", flagName, wantVal, args[i+1], args)
			}
			return
		}
	}
	t.Fatalf("flag %s not found in %v", flagName, args)
}

package whisper_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/whisper"
)

func hasCode(err error, code derr.Code) bool {
	appErr, ok := derr.AsAppError(err)
	return ok && appErr.Code == code
}

func TestWhisperBinaryNameRunnerAndBackendFactoryValidationGuards(t *testing.T) {
	binName := whisper.BinaryFileName()
	if runtime.GOOS == "windows" && binName != "whisper-server.exe" {
		t.Fatalf("expected whisper-server.exe on windows, got %q", binName)
	}
	if runtime.GOOS != "windows" && binName != "whisper-server" {
		t.Fatalf("expected whisper-server on unix, got %q", binName)
	}

	cb := llama.NewPoolBootCircuitBreaker(2, 5*time.Second)
	runner := whisper.NewLocalRunner().
		WithBootBreaker(cb).
		WithBaseEnv([]string{"GGML_VK_DISABLE_ASYNC=1"}).
		WithStartupTimeout(15 * time.Second)
	runner.WithBaseEnv(nil)

	if backends := runner.SupportedBackends(""); len(backends) != 0 {
		t.Fatalf("expected empty SupportedBackends when runtimeDir is empty, got %v", backends)
	}

	_, soloErr := runner.SpawnSoloServer(context.Background(), llama.SoloLaunchSpec{
		RuntimeDir:     "",
		ModelPath:      "virtual/ggml-large-v3-turbo.bin",
		ListenPort:     18900,
		StartupTimeout: 2 * time.Second,
	})
	if !hasCode(soloErr, derr.CodeUpstreamFailure) || derr.StatusOf(soloErr) != 502 {
		t.Fatalf("expected 502 CodeUpstreamFailure when whisper-server binary is absent, got %v", soloErr)
	}

	var nilFactory *whisper.BackendFactory
	if nilFactory.IsReady() {
		t.Fatal("expected nil BackendFactory.IsReady() == false")
	}

	factory := whisper.NewBackendFactory("", "/opt/whisper/lib").
		WithBootBreaker(cb).
		WithBaseEnv([]string{"VK_ICD_FILENAMES=/etc/vulkan/icd.d/nvidia.json"})
	if factory.IsReady() {
		t.Fatal("expected unconfigured BackendFactory.IsReady() == false")
	}

	for _, supported := range []nodecore.ModelFormat{
		nodecore.FormatGGUF,
		nodecore.FormatWeightsBin,
		nodecore.FormatDirectory,
	} {
		if !factory.SupportsFormat(supported) {
			t.Fatalf("expected whisper BackendFactory to support format %q", supported)
		}
	}
	for _, unsupported := range []nodecore.ModelFormat{
		nodecore.FormatSafetensors,
		nodecore.FormatONNX,
		nodecore.FormatPyTorch,
	} {
		if factory.SupportsFormat(unsupported) {
			t.Fatalf("expected whisper BackendFactory to reject format %q", unsupported)
		}
	}

	_, poolErr := factory.SpawnPool(context.Background(), nodecore.PoolLaunchSpec{
		PoolID:     "pool-whisper-1",
		ModelPath:  "virtual/whisper-large-v3.gguf",
		ListenPort: 18901,
		Devices:    []string{"Vulkan0"},
	})
	if !hasCode(poolErr, derr.CodeValidation) || derr.StatusOf(poolErr) != 400 {
		t.Fatalf("expected 400 CodeValidation when spawning pool without whisper-server binary, got %v", poolErr)
	}
}

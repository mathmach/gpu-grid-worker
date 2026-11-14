package engines_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/engines"
)

func TestEngineSubprocessHelper(t *testing.T) {
	mode := extractHelperMode(os.Args)
	if mode == "sidecar-hold" {
		holdTimer := time.NewTimer(30 * time.Second)
		<-holdTimer.C
		os.Exit(0)
	}
}

func extractHelperMode(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--" {
			return args[i+1]
		}
	}
	return ""
}

func TestRegistrySupportsStandardAndCustomBackends(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	missingBin := filepath.Join(t.TempDir(), "missing-onnx-server")

	reg := engines.NewStandardRegistry(engines.EngineSuiteConfig{
		LlamaServerPath: exePath,
		SafetensorsConfig: engines.SidecarConfig{
			BinaryPath: exePath,
			Flavor:     engines.SidecarVLLM,
		},
		ONNXConfig: engines.SidecarConfig{
			BinaryPath: missingBin,
		},
	})

	for _, b := range []nodecore.EngineBackend{
		nodecore.BackendLlamaLocal,
		nodecore.BackendLlamaRPC,
		nodecore.BackendSafetensors,
		nodecore.BackendONNXRuntime,
	} {
		if !reg.SupportsBackend(b) {
			t.Fatalf("expected standard registry to support backend %s", b)
		}
	}

	for _, b := range []nodecore.EngineBackend{
		nodecore.BackendLlamaLocal,
		nodecore.BackendLlamaRPC,
		nodecore.BackendSafetensors,
	} {
		f, ok := reg.FactoryForBackend(b)
		if !ok || !f.IsReady() {
			t.Fatalf("expected backend %s ready, ok=%v", b, ok)
		}
	}
	if onnxFactory, ok := reg.FactoryForBackend(nodecore.BackendONNXRuntime); !ok || onnxFactory.IsReady() {
		t.Fatalf("expected missing onnx binary factory registered but IsReady()=false, ok=%v", ok)
	}

	reg.RegisterBackend(nodecore.BackendDiffusionSidecar, engines.NewSafetensorsSidecarFactory(engines.SidecarConfig{
		BinaryPath: exePath,
		Flavor:     engines.SidecarCustom,
	}))
	if diffFactory, ok := reg.FactoryForBackend(nodecore.BackendDiffusionSidecar); !reg.SupportsBackend(nodecore.BackendDiffusionSidecar) || !ok || !diffFactory.IsReady() {
		t.Fatalf("expected registry to accept canonical sidecar backend %s", nodecore.BackendDiffusionSidecar)
	}
}

func TestSafetensorsSidecarSupervisionStopsCleanly(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	sidecarExec := engines.NewSafetensorsSidecarFactory(engines.SidecarConfig{
		BinaryPath: exePath,
		Flavor:     engines.SidecarCustom,
		ExtraArgs:  []string{"-test.run=^TestEngineSubprocessHelper$", "--", "sidecar-hold"},
	})
	args, err := sidecarExec.BuildArgs(nodecore.PoolLaunchSpec{
		ModelPath:  "local-model-dir",
		ListenPort: 19003,
	})
	if err != nil || !slices.Contains(args, "--model") {
		t.Fatalf("unexpected custom sidecar args=%v err=%v", args, err)
	}
	proc, err := sidecarExec.SpawnPool(context.Background(), nodecore.PoolLaunchSpec{
		ModelPath:  "local-model-dir",
		ListenPort: 19003,
	})
	if err != nil {
		t.Fatalf("sidecar SpawnPool failed: %v", err)
	}
	t.Cleanup(proc.Stop)
	proc.Stop()
	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("expected sidecar process stopped cleanly")
	}
}

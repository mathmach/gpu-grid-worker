package engines_test

import (
	"slices"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/engines"
)

func TestSafetensorsAndONNXSidecarBuildersPureArgsAndFormats(t *testing.T) {
	vllmFactory := engines.NewSafetensorsSidecarFactory(engines.SidecarConfig{
		BinaryPath: "/opt/bin/vllm",
		Flavor:     engines.SidecarVLLM,
	})
	if !vllmFactory.SupportsFormat(nodecore.FormatSafetensors) || !vllmFactory.SupportsFormat(nodecore.FormatDirectory) {
		t.Fatal("expected SafetensorsSidecarFactory to support safetensors and directory formats")
	}
	if vllmFactory.SupportsFormat(nodecore.FormatGGUF) {
		t.Fatal("expected SafetensorsSidecarFactory to reject GGUF format")
	}
	vllmArgs, err := vllmFactory.BuildArgs(nodecore.PoolLaunchSpec{
		ModelPath:     "/models/Qwen2.5-7B",
		ListenPort:    19001,
		ContextLength: 8192,
		TotalGPUs:     2,
	})
	if err != nil {
		t.Fatalf("vllm BuildArgs failed: %v", err)
	}
	if !slices.Contains(vllmArgs, "serve") || !slices.Contains(vllmArgs, "--tensor-parallel-size") {
		t.Fatalf("unexpected vllm args: %v", vllmArgs)
	}

	onnxFactory := engines.NewONNXGenAIFactory(engines.SidecarConfig{
		BinaryPath: "/opt/bin/onnx-genai",
	})
	if !onnxFactory.SupportsFormat(nodecore.FormatONNX) {
		t.Fatal("expected ONNXGenAIFactory to support ONNX format")
	}
	onnxArgs, err := onnxFactory.BuildArgs(nodecore.PoolLaunchSpec{
		ModelPath:     "/models/phi3.onnx",
		ListenPort:    19002,
		ContextLength: 4096,
		Devices:       []string{"Vulkan0"},
	})
	if err != nil || !slices.Contains(onnxArgs, "--model-path") || !slices.Contains(onnxArgs, "Vulkan0") {
		t.Fatalf("unexpected ONNX args=%v err=%v", onnxArgs, err)
	}
}

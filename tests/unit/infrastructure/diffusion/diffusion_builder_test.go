package diffusion_test

import (
	"slices"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/diffusion"
)

func TestDiffusionServerBuilderWanMultiComponentLowVRAMAutoOffload(t *testing.T) {
	args, err := diffusion.NewDiffusionServerBuilder().
		WithBind("127.0.0.1", 18421).
		WithVRAMTotalMiB(12288).
		WithComponentPaths(map[string]string{
			string(nodecore.RoleDiffusionModel): "/cache/wan2.1-t2v-14b-q4_k_m.gguf",
			string(nodecore.RoleT5XXL):          "/cache/umt5-xxl-q5_k_m.gguf",
			string(nodecore.RoleVAE):            "/cache/wan_2.1_vae.safetensors",
			string(nodecore.RoleClipVision):     "/cache/clip_vision_h.safetensors",
		}).
		BuildArgs()
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}

	for _, expectedFlag := range []string{
		"--diffusion-model", "/cache/wan2.1-t2v-14b-q4_k_m.gguf",
		"--t5xxl", "/cache/umt5-xxl-q5_k_m.gguf",
		"--clip_vision", "/cache/clip_vision_h.safetensors",
		"--vae", "/cache/wan_2.1_vae.safetensors",
		"--listen-ip", "127.0.0.1",
		"--listen-port", "18421",
		"--vae-tiling",
		"--clip-on-cpu",
		"--diffusion-fa",
	} {
		if !slices.Contains(args, expectedFlag) {
			t.Fatalf("expected flag %q in low-VRAM Wan args, got %v", expectedFlag, args)
		}
	}
}

func TestDiffusionServerBuilderHighVRAMSingleFileOmitsCPUOffload(t *testing.T) {
	args, err := diffusion.NewDiffusionServerBuilder().
		WithModel("/cache/flux1-dev-q8_0.gguf").
		WithBind("127.0.0.1", 19001).
		WithVRAMTotalMiB(24576).
		BuildArgs()
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}
	if !slices.Contains(args, "-m") || !slices.Contains(args, "/cache/flux1-dev-q8_0.gguf") {
		t.Fatalf("expected single-file -m argument, got %v", args)
	}
	if slices.Contains(args, "--vae-tiling") || slices.Contains(args, "--clip-on-cpu") {
		t.Fatalf("24 GiB GPU must not force --vae-tiling or --clip-on-cpu by default, got %v", args)
	}
}

func TestDiffusionServerBuilderValidationGuards(t *testing.T) {
	if _, err := diffusion.NewDiffusionServerBuilder().WithBind("127.0.0.1", 18080).BuildArgs(); err == nil {
		t.Fatalf("expected error when model path is missing")
	}
	if _, err := diffusion.NewDiffusionServerBuilder().WithModel("/cache/sdxl.safetensors").WithBind("127.0.0.1", 0).BuildArgs(); err == nil {
		t.Fatalf("expected error when port is invalid")
	}
}

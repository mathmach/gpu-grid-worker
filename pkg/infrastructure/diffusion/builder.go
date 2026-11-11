package diffusion

import (
	"errors"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

const LowVRAMAutoOffloadThresholdMiB = 20480

type DiffusionServerBuilder struct {
	modelPath          string
	diffusionModelPath string
	t5xxlPath          string
	llmPath            string
	clipLPath          string
	clipGPath          string
	clipVisionPath     string
	vaePath            string
	host               string
	port               int
	vramTotalMiB       int
	flashAttn          bool
}

func NewDiffusionServerBuilder() *DiffusionServerBuilder {
	return &DiffusionServerBuilder{
		host:      "127.0.0.1",
		flashAttn: true,
	}
}

func (b *DiffusionServerBuilder) WithModel(path string) *DiffusionServerBuilder {
	b.modelPath = strings.TrimSpace(path)
	return b
}

func (b *DiffusionServerBuilder) WithVRAMTotalMiB(vramMiB int) *DiffusionServerBuilder {
	b.vramTotalMiB = vramMiB
	return b
}

func (b *DiffusionServerBuilder) WithBind(host string, port int) *DiffusionServerBuilder {
	if clean := strings.TrimSpace(host); clean != "" {
		b.host = clean
	}
	b.port = port
	return b
}

func (b *DiffusionServerBuilder) WithComponentPaths(paths map[string]string) *DiffusionServerBuilder {
	if len(paths) == 0 {
		return b
	}
	if v := strings.TrimSpace(paths[string(nodecore.RoleDiffusionModel)]); v != "" {
		b.diffusionModelPath = v
	}
	if v := strings.TrimSpace(paths[string(nodecore.RolePrimary)]); v != "" && b.modelPath == "" {
		b.modelPath = v
	}
	b.applyEncoderComponentPaths(paths)
	return b
}

func (b *DiffusionServerBuilder) applyEncoderComponentPaths(paths map[string]string) {
	if v := strings.TrimSpace(paths[string(nodecore.RoleT5XXL)]); v != "" {
		b.t5xxlPath = v
	}
	if v := strings.TrimSpace(paths[string(nodecore.RoleTextEncoder)]); v != "" {
		b.llmPath = v
	}
	if v := strings.TrimSpace(paths[string(nodecore.RoleClipL)]); v != "" {
		b.clipLPath = v
	}
	if v := strings.TrimSpace(paths[string(nodecore.RoleClipG)]); v != "" {
		b.clipGPath = v
	}
	if v := strings.TrimSpace(paths[string(nodecore.RoleClipVision)]); v != "" {
		b.clipVisionPath = v
	}
	if v := strings.TrimSpace(paths[string(nodecore.RoleVAE)]); v != "" {
		b.vaePath = v
	}
}

func (b *DiffusionServerBuilder) BuildArgs() ([]string, error) {
	if b.diffusionModelPath == "" && b.modelPath == "" {
		return nil, errors.New("empty diffusion model path")
	}
	if b.port <= 0 || b.port > 65535 {
		return nil, errors.New("invalid local port for sd-server")
	}

	args := b.buildWeightArgs()
	args = append(args, "--listen-ip", b.host, "--listen-port", strconv.Itoa(b.port))
	args = b.appendMemoryAndAccelerationArgs(args)
	return args, nil
}

func (b *DiffusionServerBuilder) buildWeightArgs() []string {
	hasAuxComponents := b.t5xxlPath != "" || b.llmPath != "" || b.clipLPath != "" || b.clipGPath != "" || b.clipVisionPath != "" || b.vaePath != ""
	primaryDiff := b.diffusionModelPath
	if primaryDiff == "" && hasAuxComponents {
		primaryDiff = b.modelPath
	}
	var args []string
	if primaryDiff != "" {
		args = append(args, "--diffusion-model", primaryDiff)
	} else {
		args = append(args, "-m", b.modelPath)
	}
	return b.appendAuxiliaryWeightArgs(args)
}

func (b *DiffusionServerBuilder) appendAuxiliaryWeightArgs(args []string) []string {
	pairs := [][2]string{
		{"--t5xxl", b.t5xxlPath},
		{"--llm", b.llmPath},
		{"--clip_l", b.clipLPath},
		{"--clip_g", b.clipGPath},
		{"--clip_vision", b.clipVisionPath},
		{"--vae", b.vaePath},
	}
	for _, pair := range pairs {
		if pair[1] != "" {
			args = append(args, pair[0], pair[1])
		}
	}
	return args
}

func (b *DiffusionServerBuilder) appendMemoryAndAccelerationArgs(args []string) []string {
	lowVRAM := b.vramTotalMiB > 0 && b.vramTotalMiB < LowVRAMAutoOffloadThresholdMiB
	if lowVRAM {
		args = append(args, "--vae-tiling", "--clip-on-cpu")
	}
	if b.flashAttn {
		args = append(args, "--diffusion-fa")
	}
	return args
}

package engines

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/diffusion"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/whisper"
)

type RuntimePackFinder interface {
	FindExistingRuntime(dataDir, binaryName string) string
}

type CompositeSoloConfig struct {
	DataDir           string
	RuntimeFS         RuntimePackFinder
	EnabledBackends   []string
	BaseEnv           []string
	BootTimeout       time.Duration
	BootBreaker       *llama.PoolBootCircuitBreaker
	SafetensorsConfig SidecarConfig
	ONNXConfig        SidecarConfig
	DiffusionSidecar  SidecarConfig
}

type CompositeSoloSupervisor struct {
	mu                 sync.RWMutex
	dataDir            string
	runtimeFS          RuntimePackFinder
	hasExplicitFilter  bool
	allowedBackends    map[nodecore.EngineBackend]bool
	bootTimeout        time.Duration
	llamaRunner        *llama.LocalSoloRunner
	diffusionRunner    *diffusion.LocalRunner
	whisperRunner      *whisper.LocalRunner
	safetensorsSidecar *SafetensorsSidecarFactory
	onnxSidecar        *ONNXGenAIFactory
	diffusionSidecar   *diffusionSidecarFactory
}

func NewCompositeSoloSupervisor(cfg CompositeSoloConfig) *CompositeSoloSupervisor {
	breaker := cfg.BootBreaker
	if breaker == nil {
		breaker = llama.NewPoolBootCircuitBreaker(3, 30*time.Second)
	}
	timeout := cfg.BootTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	stCfg := applySidecarDefaults(cfg.SafetensorsConfig, breaker, cfg.BaseEnv)
	onnxCfg := applySidecarDefaults(cfg.ONNXConfig, breaker, cfg.BaseEnv)
	diffSidecarCfg := applySidecarDefaults(cfg.DiffusionSidecar, breaker, cfg.BaseEnv)

	sup := &CompositeSoloSupervisor{
		dataDir:            strings.TrimSpace(cfg.DataDir),
		runtimeFS:          cfg.RuntimeFS,
		bootTimeout:        timeout,
		llamaRunner:        llama.NewLocalSoloRunner().WithBootBreaker(breaker).WithBaseEnv(cfg.BaseEnv).WithStartupTimeout(timeout),
		diffusionRunner:    diffusion.NewLocalRunner().WithBootBreaker(breaker).WithBaseEnv(cfg.BaseEnv).WithStartupTimeout(timeout),
		whisperRunner:      whisper.NewLocalRunner().WithBootBreaker(breaker).WithBaseEnv(cfg.BaseEnv).WithStartupTimeout(timeout),
		safetensorsSidecar: NewSafetensorsSidecarFactory(stCfg),
		onnxSidecar:        NewONNXGenAIFactory(onnxCfg),
		diffusionSidecar:   newDiffusionSidecarFactory(diffSidecarCfg),
	}
	sup.SetEnabledBackends(cfg.EnabledBackends)
	return sup
}

func (c *CompositeSoloSupervisor) SetEnabledBackends(enabled []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if enabled == nil {
		c.hasExplicitFilter = false
		c.allowedBackends = nil
	} else {
		c.hasExplicitFilter = true
		c.allowedBackends = buildAllowedBackendSet(enabled)
	}
	c.mu.Unlock()
}

func (c *CompositeSoloSupervisor) SupportedBackends(runtimeDir string) []string {
	if c == nil {
		return nil
	}
	var discovered []string
	discovered = appendUniqueBackends(discovered, c.llamaRunner.SupportedBackends(runtimeDir))
	diffDir := c.findPackDir(runtimeDir, "diffusion", diffusion.BinaryFileName())
	discovered = appendUniqueBackends(discovered, c.diffusionRunner.SupportedBackends(diffDir))
	whisperDir := c.findPackDir(runtimeDir, "audio", whisper.BinaryFileName())
	discovered = appendUniqueBackends(discovered, c.whisperRunner.SupportedBackends(whisperDir))
	discovered = c.appendReadySidecarBackends(discovered, runtimeDir)
	return c.filterEnabledBackends(discovered)
}

func (c *CompositeSoloSupervisor) appendReadySidecarBackends(out []string, runtimeDir string) []string {
	if c.resolveSafetensorsFactory(runtimeDir).IsReady() {
		out = appendUniqueBackends(out, []string{string(nodecore.BackendSafetensors)})
	}
	if c.resolveONNXFactory(runtimeDir).IsReady() {
		out = appendUniqueBackends(out, []string{string(nodecore.BackendONNXRuntime)})
	}
	if c.diffusionSidecar != nil && c.diffusionSidecar.IsReady() {
		out = appendUniqueBackends(out, []string{string(nodecore.BackendDiffusionSidecar)})
	}
	return out
}

func (c *CompositeSoloSupervisor) filterEnabledBackends(candidates []string) []string {
	c.mu.RLock()
	hasFilter := c.hasExplicitFilter
	allowed := c.allowedBackends
	c.mu.RUnlock()

	if !hasFilter {
		return candidates
	}
	out := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		if key := normalizeRegistryBackendKey(nodecore.EngineBackend(raw)); key != "" && allowed[key] {
			out = append(out, string(key))
		}
	}
	return out
}

func (c *CompositeSoloSupervisor) isBackendAllowed(parsed nodecore.EngineBackend) bool {
	c.mu.RLock()
	hasFilter := c.hasExplicitFilter
	allowed := c.allowedBackends
	c.mu.RUnlock()
	if !hasFilter {
		return true
	}
	return allowed[parsed]
}

func (c *CompositeSoloSupervisor) SpawnSoloServer(ctx context.Context, spec llama.SoloLaunchSpec) (*llama.SupervisedProcess, error) {
	if c == nil {
		return nil, derr.New(derr.CodeIllegalState, 503, "composite SOLO supervisor is not initialized", nil)
	}
	if spec.StartupTimeout <= 0 {
		spec.StartupTimeout = c.bootTimeout
	}
	target := strings.ToLower(strings.TrimSpace(spec.TargetBackend))
	if target == "" {
		return nil, derr.New(derr.CodeValidation, 400, "worker SOLO engine targetBackend is required", nil)
	}
	parsed, err := nodecore.ParseEngineBackend(target)
	if err != nil {
		return nil, derr.New(derr.CodeUnsupportedModelFormat, 400, "unsupported worker SOLO engine backend: "+strings.TrimSpace(spec.TargetBackend), nil)
	}
	if !c.isBackendAllowed(parsed) {
		return nil, derr.New(derr.CodeValidation, 403, "worker SOLO backend disabled by configuration: "+string(parsed), nil)
	}
	return c.dispatchSoloBackend(ctx, string(parsed), spec)
}

func (c *CompositeSoloSupervisor) dispatchSoloBackend(ctx context.Context, target string, spec llama.SoloLaunchSpec) (*llama.SupervisedProcess, error) {
	switch nodecore.EngineBackend(target) {
	case nodecore.BackendLlamaLocal, nodecore.BackendLlamaRPC, nodecore.BackendAudioLocal, nodecore.BackendOmniLocal:
		return c.llamaRunner.SpawnSoloServer(ctx, spec)
	case nodecore.BackendDiffusionLocal:
		if dir := c.findPackDir(spec.RuntimeDir, "diffusion", diffusion.BinaryFileName()); dir != "" {
			spec.RuntimeDir = dir
		}
		return c.diffusionRunner.SpawnSoloServer(ctx, spec)
	case nodecore.BackendWhisperLocal:
		if dir := c.findPackDir(spec.RuntimeDir, "audio", whisper.BinaryFileName()); dir != "" {
			spec.RuntimeDir = dir
		}
		return c.whisperRunner.SpawnSoloServer(ctx, spec)
	case nodecore.BackendSafetensors:
		return c.resolveSafetensorsFactory(spec.RuntimeDir).spawnWithReadiness(ctx, soloToPoolLaunchSpec(spec), llama.ReadinessTCP, spec.StartupTimeout, true)
	case nodecore.BackendONNXRuntime:
		return c.resolveONNXFactory(spec.RuntimeDir).spawnWithReadiness(ctx, soloToPoolLaunchSpec(spec), llama.ReadinessTCP, spec.StartupTimeout, true)
	case nodecore.BackendDiffusionSidecar:
		return c.diffusionSidecar.spawnWithReadiness(ctx, soloToPoolLaunchSpec(spec), llama.ReadinessTCP, spec.StartupTimeout, true)
	default:
		return nil, derr.New(derr.CodeUnsupportedModelFormat, 400, "unsupported worker SOLO engine backend: "+strings.TrimSpace(spec.TargetBackend), nil)
	}
}

func (c *CompositeSoloSupervisor) resolveSafetensorsFactory(runtimeDir string) *SafetensorsSidecarFactory {
	if c.safetensorsSidecar != nil && c.safetensorsSidecar.IsReady() {
		return c.safetensorsSidecar
	}
	bin := c.findSidecarBinary(runtimeDir, "safetensors", safetensorsDefaultBinaryName())
	if bin == "" {
		return c.safetensorsSidecar
	}
	cfg := c.safetensorsSidecar.cfg
	cfg.BinaryPath = bin
	return NewSafetensorsSidecarFactory(cfg)
}

func (c *CompositeSoloSupervisor) resolveONNXFactory(runtimeDir string) *ONNXGenAIFactory {
	if c.onnxSidecar != nil && c.onnxSidecar.IsReady() {
		return c.onnxSidecar
	}
	bin := c.findSidecarBinary(runtimeDir, "onnx", onnxDefaultBinaryName())
	if bin == "" {
		return c.onnxSidecar
	}
	cfg := c.onnxSidecar.cfg
	cfg.BinaryPath = bin
	return NewONNXGenAIFactory(cfg)
}

func (c *CompositeSoloSupervisor) findSidecarBinary(runtimeDir, packID, binaryName string) string {
	dir := c.findPackDir(runtimeDir, packID, binaryName)
	if dir == "" {
		return ""
	}
	return llama.FindExecutableRecursive(dir, binaryName)
}

func (c *CompositeSoloSupervisor) findPackDir(coreRuntimeDir, packID, binaryName string) string {
	if c.runtimeFS != nil && c.dataDir != "" {
		for _, sub := range []string{"runtimes", "packs"} {
			packRoot := filepath.Join(c.dataDir, sub, packID)
			if dir := strings.TrimSpace(c.runtimeFS.FindExistingRuntime(packRoot, binaryName)); dir != "" {
				return dir
			}
		}
		if dir := strings.TrimSpace(c.runtimeFS.FindExistingRuntime(c.dataDir, binaryName)); dir != "" {
			return dir
		}
	}
	if cleanCore := strings.TrimSpace(coreRuntimeDir); cleanCore != "" {
		if llama.FindExecutableRecursive(cleanCore, binaryName) != "" {
			return cleanCore
		}
	}
	return ""
}

func soloToPoolLaunchSpec(spec llama.SoloLaunchSpec) nodecore.PoolLaunchSpec {
	devices, weights, totalVRAM := llama.ExtractSoloGPUConfig(spec.GPUs)
	if spec.PoolVRAMMiB > totalVRAM {
		totalVRAM = spec.PoolVRAMMiB
	}
	return nodecore.PoolLaunchSpec{
		Mode:          nodecore.PoolModeSolo,
		ModelPath:     spec.ModelPath,
		MMProjPath:    spec.MMProjPath,
		VocoderPath:   spec.VocoderPath,
		Task:          nodecore.ModelTask(spec.Task),
		Devices:       devices,
		TotalGPUs:     max(len(devices), spec.TotalGPUs),
		TensorWeights: weights,
		ParallelSlots: max(1, spec.ParallelSlots),
		ContextLength: spec.ContextLength,
		TargetVRAMMiB: totalVRAM,
		ListenPort:    spec.ListenPort,
	}
}

func appendUniqueBackends(dst, additions []string) []string {
	for _, b := range additions {
		if !slices.Contains(dst, b) {
			dst = append(dst, b)
		}
	}
	return dst
}

func safetensorsDefaultBinaryName() string {
	if runtime.GOOS == "windows" {
		return "mistralrs-server.exe"
	}
	return "mistralrs-server"
}

func onnxDefaultBinaryName() string {
	if runtime.GOOS == "windows" {
		return "onnxruntime-genai-server.exe"
	}
	return "onnxruntime-genai-server"
}

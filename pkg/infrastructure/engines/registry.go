package engines

import (
	"context"
	"sync"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/diffusion"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/whisper"
)

type EngineSuiteConfig struct {
	LlamaServerPath     string
	DiffusionServerPath string
	WhisperServerPath   string
	LDLibraryPath       string
	BaseEnv             []string
	EnabledBackends     []string
	SafetensorsConfig   SidecarConfig
	ONNXConfig          SidecarConfig
	DiffusionSidecar    SidecarConfig
	BootBreaker         *llama.PoolBootCircuitBreaker
}

type Registry struct {
	mu        sync.RWMutex
	factories map[nodecore.EngineBackend]nodecore.InferenceBackendFactory
}

var _ nodecore.InferenceEngineRegistry = (*Registry)(nil)

func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[nodecore.EngineBackend]nodecore.InferenceBackendFactory),
	}
}

func NewStandardRegistry(cfg EngineSuiteConfig) *Registry {
	breaker := cfg.BootBreaker
	if breaker == nil {
		breaker = llama.NewPoolBootCircuitBreaker(3, 30*time.Second)
	}
	stCfg := applySidecarDefaults(cfg.SafetensorsConfig, breaker, cfg.BaseEnv)
	onnxCfg := applySidecarDefaults(cfg.ONNXConfig, breaker, cfg.BaseEnv)
	diffSidecarCfg := applySidecarDefaults(cfg.DiffusionSidecar, breaker, cfg.BaseEnv)

	reg := NewRegistry()
	allowed := buildAllowedBackendSet(cfg.EnabledBackends)
	registerIfEnabled(reg, allowed, nodecore.BackendLlamaLocal, newLlamaLocalFactory(cfg.LlamaServerPath, cfg.LDLibraryPath).withBootBreaker(breaker).withBaseEnv(cfg.BaseEnv))
	registerIfEnabled(reg, allowed, nodecore.BackendLlamaRPC, newLlamaRPCFactory(cfg.LlamaServerPath, cfg.LDLibraryPath).withBootBreaker(breaker).withBaseEnv(cfg.BaseEnv))
	registerIfEnabled(reg, allowed, nodecore.BackendSafetensors, NewSafetensorsSidecarFactory(stCfg))
	registerIfEnabled(reg, allowed, nodecore.BackendONNXRuntime, NewONNXGenAIFactory(onnxCfg))
	registerIfEnabled(reg, allowed, nodecore.BackendDiffusionLocal, diffusion.NewBackendFactory(cfg.DiffusionServerPath, cfg.LDLibraryPath).WithBootBreaker(breaker).WithBaseEnv(cfg.BaseEnv))
	registerIfEnabled(reg, allowed, nodecore.BackendDiffusionSidecar, newDiffusionSidecarFactory(diffSidecarCfg))
	registerIfEnabled(reg, allowed, nodecore.BackendWhisperLocal, whisper.NewBackendFactory(cfg.WhisperServerPath, cfg.LDLibraryPath).WithBootBreaker(breaker).WithBaseEnv(cfg.BaseEnv))
	registerIfEnabled(reg, allowed, nodecore.BackendAudioLocal, newLlamaSpecializedSoloFactory(cfg.LlamaServerPath, cfg.LDLibraryPath, nodecore.BackendAudioLocal).withBootBreaker(breaker).withBaseEnv(cfg.BaseEnv))
	registerIfEnabled(reg, allowed, nodecore.BackendOmniLocal, newLlamaSpecializedSoloFactory(cfg.LlamaServerPath, cfg.LDLibraryPath, nodecore.BackendOmniLocal).withBootBreaker(breaker).withBaseEnv(cfg.BaseEnv))
	return reg
}

func applySidecarDefaults(sc SidecarConfig, breaker *llama.PoolBootCircuitBreaker, baseEnv []string) SidecarConfig {
	if sc.BootBreaker == nil {
		sc.BootBreaker = breaker
	}
	if len(sc.Env) == 0 && len(baseEnv) > 0 {
		sc.Env = baseEnv
	}
	return sc
}

func buildAllowedBackendSet(enabled []string) map[nodecore.EngineBackend]bool {
	if len(enabled) == 0 {
		return nil
	}
	out := make(map[nodecore.EngineBackend]bool, len(enabled))
	for _, raw := range enabled {
		if key := normalizeRegistryBackendKey(nodecore.EngineBackend(raw)); key != "" {
			out[key] = true
		}
	}
	return out
}

func registerIfEnabled(reg *Registry, allowed map[nodecore.EngineBackend]bool, backend nodecore.EngineBackend, factory nodecore.InferenceBackendFactory) {
	if len(allowed) == 0 || allowed[backend] {
		reg.RegisterBackend(backend, factory)
	}
}

func (r *Registry) RegisterBackend(backend nodecore.EngineBackend, factory nodecore.InferenceBackendFactory) {
	key := normalizeRegistryBackendKey(backend)
	if key == "" || factory == nil {
		return
	}
	r.mu.Lock()
	r.factories[key] = factory
	r.mu.Unlock()
}

func (r *Registry) FactoryForBackend(backend nodecore.EngineBackend) (nodecore.InferenceBackendFactory, bool) {
	key := normalizeRegistryBackendKey(backend)
	if key == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[key]
	return f, ok && f != nil
}

func (r *Registry) SupportsBackend(backend nodecore.EngineBackend) bool {
	_, ok := r.FactoryForBackend(backend)
	return ok
}

func normalizeRegistryBackendKey(raw nodecore.EngineBackend) nodecore.EngineBackend {
	parsed, err := nodecore.ParseEngineBackend(string(raw))
	if err != nil {
		return ""
	}
	return parsed
}

type llamaLocalFactory struct {
	inner *llama.BackendFactory
}

var _ nodecore.InferenceBackendFactory = (*llamaLocalFactory)(nil)

func newLlamaLocalFactory(llamaServerPath, ldLibraryPath string) *llamaLocalFactory {
	return &llamaLocalFactory{
		inner: llama.NewBackendFactory(llamaServerPath, ldLibraryPath),
	}
}

func (f *llamaLocalFactory) withBootBreaker(cb *llama.PoolBootCircuitBreaker) *llamaLocalFactory {
	f.inner.WithBootBreaker(cb)
	return f
}

func (f *llamaLocalFactory) withBaseEnv(env []string) *llamaLocalFactory {
	f.inner.WithBaseEnv(env)
	return f
}

func (f *llamaLocalFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	return format == nodecore.FormatGGUF
}

func (f *llamaLocalFactory) IsReady() bool {
	return f != nil && f.inner != nil && f.inner.IsReady()
}

func (f *llamaLocalFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	if !f.IsReady() {
		return nil, derr.New(derr.CodeValidation, 400, "local llama-server binary is not configured or missing", nil)
	}
	spec.Mode = nodecore.PoolModeSolo
	spec.Backend = nodecore.BackendLlamaLocal
	spec.RPCEndpoints = nil
	return f.inner.SpawnPool(ctx, spec)
}

type llamaRPCFactory struct {
	inner *llama.BackendFactory
}

var _ nodecore.InferenceBackendFactory = (*llamaRPCFactory)(nil)

func newLlamaRPCFactory(llamaServerPath, ldLibraryPath string) *llamaRPCFactory {
	return &llamaRPCFactory{
		inner: llama.NewBackendFactory(llamaServerPath, ldLibraryPath),
	}
}

func (f *llamaRPCFactory) withBootBreaker(cb *llama.PoolBootCircuitBreaker) *llamaRPCFactory {
	f.inner.WithBootBreaker(cb)
	return f
}

func (f *llamaRPCFactory) withBaseEnv(env []string) *llamaRPCFactory {
	f.inner.WithBaseEnv(env)
	return f
}

func (f *llamaRPCFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	return format == nodecore.FormatGGUF
}

func (f *llamaRPCFactory) IsReady() bool {
	return f != nil && f.inner != nil && f.inner.IsReady()
}

func (f *llamaRPCFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	if !f.IsReady() {
		return nil, derr.New(derr.CodeValidation, 400, "llama-server binary is not configured or missing", nil)
	}
	if len(spec.RPCEndpoints) == 0 {
		return nil, derr.New(derr.CodeValidation, 400, "llama_rpc backend requires at least one RPC endpoint", nil)
	}
	spec.Mode = nodecore.PoolModeMesh
	spec.Backend = nodecore.BackendLlamaRPC
	return f.inner.SpawnPool(ctx, spec)
}

type llamaSpecializedSoloFactory struct {
	inner   *llama.BackendFactory
	backend nodecore.EngineBackend
}

var _ nodecore.InferenceBackendFactory = (*llamaSpecializedSoloFactory)(nil)

func newLlamaSpecializedSoloFactory(llamaServerPath, ldLibraryPath string, backend nodecore.EngineBackend) *llamaSpecializedSoloFactory {
	return &llamaSpecializedSoloFactory{
		inner:   llama.NewBackendFactory(llamaServerPath, ldLibraryPath),
		backend: backend,
	}
}

func (f *llamaSpecializedSoloFactory) withBootBreaker(cb *llama.PoolBootCircuitBreaker) *llamaSpecializedSoloFactory {
	f.inner.WithBootBreaker(cb)
	return f
}

func (f *llamaSpecializedSoloFactory) withBaseEnv(env []string) *llamaSpecializedSoloFactory {
	f.inner.WithBaseEnv(env)
	return f
}

func (f *llamaSpecializedSoloFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	return format == nodecore.FormatGGUF || format == nodecore.FormatDirectory
}

func (f *llamaSpecializedSoloFactory) IsReady() bool {
	return f != nil && f.inner != nil && f.inner.IsReady()
}

func (f *llamaSpecializedSoloFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	if !f.IsReady() {
		return nil, derr.New(derr.CodeValidation, 400, "llama-server binary is not configured or missing", nil)
	}
	spec.Mode = nodecore.PoolModeSolo
	spec.Backend = f.backend
	spec.RPCEndpoints = nil
	return f.inner.SpawnPool(ctx, spec)
}

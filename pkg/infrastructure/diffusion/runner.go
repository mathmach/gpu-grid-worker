package diffusion

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
)

const defaultDiffusionStartupTimeout = 120 * time.Second

type LocalRunner struct {
	bootBreaker    *llama.PoolBootCircuitBreaker
	baseEnv        []string
	startupTimeout time.Duration
}

func NewLocalRunner() *LocalRunner {
	return &LocalRunner{
		bootBreaker:    llama.NewPoolBootCircuitBreaker(3, 30*time.Second),
		startupTimeout: defaultDiffusionStartupTimeout,
	}
}

func (r *LocalRunner) WithBootBreaker(cb *llama.PoolBootCircuitBreaker) *LocalRunner {
	r.bootBreaker = cb
	return r
}

func (r *LocalRunner) WithBaseEnv(env []string) *LocalRunner {
	if len(env) == 0 {
		r.baseEnv = nil
		return r
	}
	r.baseEnv = append([]string(nil), env...)
	return r
}

func (r *LocalRunner) WithStartupTimeout(timeout time.Duration) *LocalRunner {
	if timeout > 0 {
		r.startupTimeout = timeout
	}
	return r
}

func BinaryFileName() string {
	if runtime.GOOS == "windows" {
		return "sd-server.exe"
	}
	return "sd-server"
}

func (r *LocalRunner) SupportedBackends(runtimeDir string) []string {
	if llama.FindExecutableRecursive(runtimeDir, BinaryFileName()) != "" {
		return []string{string(nodecore.BackendDiffusionLocal)}
	}
	return nil
}

func (r *LocalRunner) SpawnSoloServer(ctx context.Context, spec llama.SoloLaunchSpec) (*llama.SupervisedProcess, error) {
	binPath := llama.FindExecutableRecursive(spec.RuntimeDir, BinaryFileName())
	if binPath == "" {
		return nil, derr.New(derr.CodeUpstreamFailure, 502, "sd-server binary not found in worker runtime", nil)
	}
	devices, _, totalVRAM := llama.ExtractSoloGPUConfig(spec.GPUs)
	timeout := r.resolveStartupTimeout(spec.StartupTimeout)
	return r.launchWithBuilder(ctx, binPath, "", llama.VulkanVisibleDevicesEnv(devices), spec.ModelPath, spec.ListenPort, timeout, true, func(port int) ([]string, error) {
		return NewDiffusionServerBuilder().
			WithModel(spec.ModelPath).
			WithComponentPaths(spec.ComponentPaths).
			WithVRAMTotalMiB(totalVRAM).
			WithBind("127.0.0.1", port).
			BuildArgs()
	})
}

func (r *LocalRunner) resolveStartupTimeout(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if r != nil && r.startupTimeout > 0 {
		return r.startupTimeout
	}
	return defaultDiffusionStartupTimeout
}

func (r *LocalRunner) launchWithBuilder(
	ctx context.Context,
	binPath string,
	libPath string,
	extraEnv []string,
	modelKey string,
	port int,
	timeout time.Duration,
	markBootOnTCP bool,
	buildArgs func(port int) ([]string, error),
) (*llama.SupervisedProcess, error) {
	var breaker *llama.PoolBootCircuitBreaker
	var baseEnv []string
	if r != nil {
		breaker = r.bootBreaker
		baseEnv = r.baseEnv
	}
	proc, _, err := llama.LaunchLocalHTTPSubprocess(ctx, llama.LocalHTTPLaunchSpec{
		BinaryPath:     binPath,
		ProcessName:    "sd-server",
		Backend:        nodecore.BackendDiffusionLocal,
		BuildArgs:      buildArgs,
		LibraryPath:    libPath,
		ExtraEnv:       extraEnv,
		BaseEnv:        baseEnv,
		BootBreaker:    breaker,
		ModelKey:       modelKey,
		Port:           port,
		Readiness:      llama.ReadinessTCP,
		StartupTimeout: timeout,
		MarkBootReady:  markBootOnTCP,
	})
	return proc, err
}

type BackendFactory struct {
	binaryPath    string
	ldLibraryPath string
	runner        *LocalRunner
}

var _ nodecore.InferenceBackendFactory = (*BackendFactory)(nil)

func NewBackendFactory(binaryPath, ldLibraryPath string) *BackendFactory {
	return &BackendFactory{
		binaryPath:    strings.TrimSpace(binaryPath),
		ldLibraryPath: strings.TrimSpace(ldLibraryPath),
		runner:        NewLocalRunner(),
	}
}

func (f *BackendFactory) WithBootBreaker(cb *llama.PoolBootCircuitBreaker) *BackendFactory {
	f.runner.WithBootBreaker(cb)
	return f
}

func (f *BackendFactory) WithBaseEnv(env []string) *BackendFactory {
	f.runner.WithBaseEnv(env)
	return f
}

func (f *BackendFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	return format == nodecore.FormatGGUF || format == nodecore.FormatSafetensors || format == nodecore.FormatDirectory
}

func (f *BackendFactory) IsReady() bool {
	if f == nil {
		return false
	}
	_, ok := llama.ResolveExecutableBinary(f.binaryPath)
	return ok
}

func (f *BackendFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	binPath, ok := llama.ResolveExecutableBinary(f.binaryPath)
	if !ok {
		return nil, derr.New(derr.CodeValidation, 400, "sd-server binary is not configured or missing", nil)
	}
	compPaths := componentsToPathMap(spec.Components)
	ldExtra := strings.TrimSpace(spec.LDLibraryPath)
	if ldExtra == "" {
		ldExtra = f.ldLibraryPath
	}
	timeout := f.runner.resolveStartupTimeout(0)
	return f.runner.launchWithBuilder(ctx, binPath, ldExtra, llama.VulkanVisibleDevicesEnv(spec.Devices), spec.ModelPath, spec.ListenPort, timeout, false, func(port int) ([]string, error) {
		return NewDiffusionServerBuilder().
			WithModel(spec.ModelPath).
			WithComponentPaths(compPaths).
			WithVRAMTotalMiB(spec.TargetVRAMMiB).
			WithBind("127.0.0.1", port).
			BuildArgs()
	})
}

func componentsToPathMap(comps []nodecore.BundleComponent) map[string]string {
	if len(comps) == 0 {
		return nil
	}
	out := make(map[string]string, len(comps))
	for _, c := range comps {
		if p := strings.TrimSpace(c.FilePath); p != "" {
			out[string(c.Role)] = p
		}
	}
	return out
}

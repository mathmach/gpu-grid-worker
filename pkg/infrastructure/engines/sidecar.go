package engines

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
)

type SidecarFlavor string

const (
	SidecarMistralRS SidecarFlavor = "mistralrs"
	SidecarVLLM      SidecarFlavor = "vllm"
	SidecarCustom    SidecarFlavor = "custom"
)

type SidecarConfig struct {
	BinaryPath     string
	Flavor         SidecarFlavor
	Host           string
	ExtraArgs      []string
	Env            []string
	LibraryPath    string
	BootBreaker    *llama.PoolBootCircuitBreaker
	StartupTimeout time.Duration
}

type SafetensorsSidecarFactory struct {
	cfg SidecarConfig
}

var _ nodecore.InferenceBackendFactory = (*SafetensorsSidecarFactory)(nil)

func NewSafetensorsSidecarFactory(cfg SidecarConfig) *SafetensorsSidecarFactory {
	return &SafetensorsSidecarFactory{cfg: normalizeSidecarConfig(cfg)}
}

func (f *SafetensorsSidecarFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	return format == nodecore.FormatSafetensors || format == nodecore.FormatDirectory
}

func (f *SafetensorsSidecarFactory) IsReady() bool {
	return f != nil && isExecutableFileReady(f.cfg.BinaryPath)
}

func (f *SafetensorsSidecarFactory) BuildArgs(spec nodecore.PoolLaunchSpec) ([]string, error) {
	if err := validateSidecarLaunchSpec(spec); err != nil {
		return nil, err
	}
	host := strings.TrimSpace(f.cfg.Host)
	portStr := strconv.Itoa(spec.ListenPort)
	modelPath := strings.TrimSpace(spec.ModelPath)

	var args []string
	switch normalizeSafetensorsFlavor(f.cfg.Flavor) {
	case SidecarVLLM:
		args = buildVLLMArgs(modelPath, host, portStr, spec)
	case SidecarCustom:
		args = append(args, f.cfg.ExtraArgs...)
		args = append(args, "--model", modelPath, "--host", host, "--port", portStr)
		return args, nil
	default:
		args = buildMistralRSArgs(modelPath, host, portStr, spec)
	}
	args = append(args, f.cfg.ExtraArgs...)
	return args, nil
}

func (f *SafetensorsSidecarFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	return f.spawnWithReadiness(ctx, spec, llama.ReadinessNone, 0, false)
}

func (f *SafetensorsSidecarFactory) spawnWithReadiness(
	ctx context.Context,
	spec nodecore.PoolLaunchSpec,
	readiness llama.ReadinessMode,
	timeout time.Duration,
	markBoot bool,
) (*llama.SupervisedProcess, error) {
	if f == nil {
		return nil, derr.New(derr.CodeValidation, 400, "safetensors sidecar binary is not configured or missing", nil)
	}
	return launchSidecarSubprocess(ctx, f.cfg, nodecore.BackendSafetensors, "safetensors_sidecar", "safetensors sidecar binary is not configured or missing", spec, readiness, timeout, markBoot, f.BuildArgs)
}

type ONNXGenAIFactory struct {
	cfg SidecarConfig
}

var _ nodecore.InferenceBackendFactory = (*ONNXGenAIFactory)(nil)

func NewONNXGenAIFactory(cfg SidecarConfig) *ONNXGenAIFactory {
	return &ONNXGenAIFactory{cfg: normalizeSidecarConfig(cfg)}
}

func (f *ONNXGenAIFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	return format == nodecore.FormatONNX || format == nodecore.FormatDirectory
}

func (f *ONNXGenAIFactory) IsReady() bool {
	return f != nil && isExecutableFileReady(f.cfg.BinaryPath)
}

func (f *ONNXGenAIFactory) BuildArgs(spec nodecore.PoolLaunchSpec) ([]string, error) {
	if err := validateSidecarLaunchSpec(spec); err != nil {
		return nil, err
	}
	args := []string{
		"--model-path", strings.TrimSpace(spec.ModelPath),
		"--host", strings.TrimSpace(f.cfg.Host),
		"--port", strconv.Itoa(spec.ListenPort),
	}
	if spec.ContextLength > 0 {
		args = append(args, "--context-length", strconv.Itoa(spec.ContextLength))
	}
	if spec.ParallelSlots > 1 {
		args = append(args, "--max-batch-size", strconv.Itoa(spec.ParallelSlots))
	}
	if devs := joinCleanDevices(spec.Devices); devs != "" {
		args = append(args, "--device", devs)
	}
	args = append(args, f.cfg.ExtraArgs...)
	return args, nil
}

func (f *ONNXGenAIFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	return f.spawnWithReadiness(ctx, spec, llama.ReadinessNone, 0, false)
}

func (f *ONNXGenAIFactory) spawnWithReadiness(
	ctx context.Context,
	spec nodecore.PoolLaunchSpec,
	readiness llama.ReadinessMode,
	timeout time.Duration,
	markBoot bool,
) (*llama.SupervisedProcess, error) {
	if f == nil {
		return nil, derr.New(derr.CodeValidation, 400, "onnx_genai sidecar binary is not configured or missing", nil)
	}
	return launchSidecarSubprocess(ctx, f.cfg, nodecore.BackendONNXRuntime, "onnx_genai", "onnx_genai sidecar binary is not configured or missing", spec, readiness, timeout, markBoot, f.BuildArgs)
}

type diffusionSidecarFactory struct {
	cfg SidecarConfig
}

var _ nodecore.InferenceBackendFactory = (*diffusionSidecarFactory)(nil)

func newDiffusionSidecarFactory(cfg SidecarConfig) *diffusionSidecarFactory {
	return &diffusionSidecarFactory{cfg: normalizeSidecarConfig(cfg)}
}

func (f *diffusionSidecarFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	return format == nodecore.FormatSafetensors || format == nodecore.FormatPyTorch || format == nodecore.FormatDirectory
}

func (f *diffusionSidecarFactory) IsReady() bool {
	return f != nil && isExecutableFileReady(f.cfg.BinaryPath)
}

func (f *diffusionSidecarFactory) BuildArgs(spec nodecore.PoolLaunchSpec) ([]string, error) {
	if err := validateSidecarLaunchSpec(spec); err != nil {
		return nil, err
	}
	args := []string{
		"--model", strings.TrimSpace(spec.ModelPath),
		"--host", strings.TrimSpace(f.cfg.Host),
		"--port", strconv.Itoa(spec.ListenPort),
	}
	if devs := joinCleanDevices(spec.Devices); devs != "" {
		args = append(args, "--device", devs)
	}
	args = append(args, f.cfg.ExtraArgs...)
	return args, nil
}

func (f *diffusionSidecarFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	return f.spawnWithReadiness(ctx, spec, llama.ReadinessNone, 0, false)
}

func (f *diffusionSidecarFactory) spawnWithReadiness(
	ctx context.Context,
	spec nodecore.PoolLaunchSpec,
	readiness llama.ReadinessMode,
	timeout time.Duration,
	markBoot bool,
) (*llama.SupervisedProcess, error) {
	if f == nil {
		return nil, derr.New(derr.CodeValidation, 400, "diffusion_sidecar binary is not configured or missing", nil)
	}
	return launchSidecarSubprocess(ctx, f.cfg, nodecore.BackendDiffusionSidecar, "diffusion_sidecar", "diffusion_sidecar binary is not configured or missing", spec, readiness, timeout, markBoot, f.BuildArgs)
}

func normalizeSidecarConfig(cfg SidecarConfig) SidecarConfig {
	cfg.BinaryPath = strings.TrimSpace(cfg.BinaryPath)
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.BootBreaker == nil {
		cfg.BootBreaker = llama.NewPoolBootCircuitBreaker(3, 30*time.Second)
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 120 * time.Second
	}
	return cfg
}

func launchSidecarSubprocess(
	ctx context.Context,
	cfg SidecarConfig,
	backend nodecore.EngineBackend,
	processName string,
	missingMsg string,
	spec nodecore.PoolLaunchSpec,
	readiness llama.ReadinessMode,
	timeout time.Duration,
	markBoot bool,
	buildFn func(nodecore.PoolLaunchSpec) ([]string, error),
) (*llama.SupervisedProcess, error) {
	binPath, ok := llama.ResolveExecutableBinary(cfg.BinaryPath)
	if !ok {
		return nil, derr.New(derr.CodeValidation, 400, missingMsg, nil)
	}
	probeSpec := spec
	if readiness != llama.ReadinessNone && probeSpec.ListenPort <= 0 {
		probeSpec.ListenPort = 1
	}
	if _, err := buildFn(probeSpec); err != nil {
		return nil, err
	}
	effTimeout := timeout
	if effTimeout <= 0 {
		effTimeout = cfg.StartupTimeout
	}
	libPath := firstNonBlank(spec.LDLibraryPath, cfg.LibraryPath)
	proc, _, err := llama.LaunchLocalHTTPSubprocess(ctx, llama.LocalHTTPLaunchSpec{
		BinaryPath:  binPath,
		ProcessName: processName,
		Backend:     backend,
		BuildArgs: func(port int) ([]string, error) {
			attempt := spec
			attempt.ListenPort = port
			return buildFn(attempt)
		},
		LibraryPath:    libPath,
		BaseEnv:        cfg.Env,
		BootBreaker:    cfg.BootBreaker,
		ModelKey:       spec.ModelPath,
		Port:           spec.ListenPort,
		Readiness:      readiness,
		StartupTimeout: effTimeout,
		MarkBootReady:  markBoot,
		AllowPortRetry: true,
	})
	return proc, err
}

func validateSidecarLaunchSpec(spec nodecore.PoolLaunchSpec) error {
	if strings.TrimSpace(spec.ModelPath) == "" {
		return derr.New(derr.CodeValidation, 400, "empty model path for sidecar pool", nil)
	}
	if spec.ListenPort <= 0 || spec.ListenPort > 65535 {
		return derr.New(derr.CodeValidation, 400, "invalid listen port for sidecar pool", nil)
	}
	return nil
}

func normalizeSafetensorsFlavor(flavor SidecarFlavor) SidecarFlavor {
	switch strings.ToLower(strings.TrimSpace(string(flavor))) {
	case string(SidecarVLLM):
		return SidecarVLLM
	case string(SidecarCustom):
		return SidecarCustom
	default:
		return SidecarMistralRS
	}
}

func buildVLLMArgs(modelPath, host, portStr string, spec nodecore.PoolLaunchSpec) []string {
	args := []string{"serve", modelPath, "--host", host, "--port", portStr}
	if spec.ContextLength > 0 {
		args = append(args, "--max-model-len", strconv.Itoa(spec.ContextLength))
	}
	tp := max(len(spec.Devices), spec.TotalGPUs)
	if tp > 1 {
		args = append(args, "--tensor-parallel-size", strconv.Itoa(tp))
	}
	return args
}

func buildMistralRSArgs(modelPath, host, portStr string, spec nodecore.PoolLaunchSpec) []string {
	args := []string{"--port", portStr, "--serve-ip", host}
	if spec.ContextLength > 0 {
		args = append(args, "--max-seq-len", strconv.Itoa(spec.ContextLength))
	}
	args = append(args, "plain", "-m", modelPath)
	return args
}

func joinCleanDevices(devices []string) string {
	var clean []string
	for _, d := range devices {
		if trimmed := strings.TrimSpace(d); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	return strings.Join(clean, ",")
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isExecutableFileReady(path string) bool {
	_, ok := llama.ResolveExecutableBinary(path)
	return ok
}

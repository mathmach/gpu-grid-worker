package llama

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const defaultSoloStartupTimeout = 120 * time.Second

type SoloLaunchSpec struct {
	ModelPath      string
	MMProjPath     string
	VocoderPath    string
	Task           string
	TargetBackend  string
	RuntimeDir     string
	ListenPort     int
	ContextLength  int
	ParallelSlots  int
	PoolVRAMMiB    int
	TotalGPUs      int
	RPCEndpoints   []string
	TensorWeights  []int
	GPUs           []protocol.GpuInfo
	ComponentPaths map[string]string
	StartupTimeout time.Duration
}

type LocalSoloRunner struct {
	bootBreaker    *PoolBootCircuitBreaker
	baseEnv        []string
	startupTimeout time.Duration
}

func NewLocalSoloRunner() *LocalSoloRunner {
	return &LocalSoloRunner{
		bootBreaker:    NewPoolBootCircuitBreaker(3, 30*time.Second),
		startupTimeout: defaultSoloStartupTimeout,
	}
}

func (r *LocalSoloRunner) WithBootBreaker(cb *PoolBootCircuitBreaker) *LocalSoloRunner {
	r.bootBreaker = cb
	return r
}

func (r *LocalSoloRunner) WithBaseEnv(env []string) *LocalSoloRunner {
	if len(env) == 0 {
		r.baseEnv = nil
		return r
	}
	r.baseEnv = append([]string(nil), env...)
	return r
}

func (r *LocalSoloRunner) WithStartupTimeout(timeout time.Duration) *LocalSoloRunner {
	if timeout > 0 {
		r.startupTimeout = timeout
	}
	return r
}

func (r *LocalSoloRunner) SupportedBackends(runtimeDir string) []string {
	if FindExecutableRecursive(runtimeDir, llamaBinaryFileName()) != "" {
		return []string{
			string(nodecore.BackendLlamaLocal),
			string(nodecore.BackendAudioLocal),
			string(nodecore.BackendOmniLocal),
		}
	}
	return nil
}

func (r *LocalSoloRunner) SpawnSoloServer(ctx context.Context, spec SoloLaunchSpec) (*SupervisedProcess, error) {
	binPath := FindExecutableRecursive(spec.RuntimeDir, llamaBinaryFileName())
	if binPath == "" {
		return nil, derr.New(derr.CodeUpstreamFailure, 502, "llama-server binary not found in worker runtime", nil)
	}
	probeSpec := spec
	if probeSpec.ListenPort <= 0 {
		probeSpec.ListenPort = 1
	}
	_, bootBackend, err := buildSoloOrLeaderMeshArgs(probeSpec)
	if err != nil {
		return nil, err
	}

	var breaker *PoolBootCircuitBreaker
	var baseEnv []string
	timeout := defaultSoloStartupTimeout
	if r != nil {
		breaker = r.bootBreaker
		baseEnv = r.baseEnv
		if r.startupTimeout > 0 {
			timeout = r.startupTimeout
		}
	}
	if spec.StartupTimeout > 0 {
		timeout = spec.StartupTimeout
	}

	proc, _, err := LaunchLocalHTTPSubprocess(ctx, LocalHTTPLaunchSpec{
		BinaryPath: binPath,
		WorkDir:    filepath.Dir(binPath),
		Backend:    bootBackend,
		BuildArgs: func(port int) ([]string, error) {
			attempt := spec
			attempt.ListenPort = port
			args, _, bErr := buildSoloOrLeaderMeshArgs(attempt)
			return args, bErr
		},
		BaseEnv:        baseEnv,
		BootBreaker:    breaker,
		ModelKey:       spec.ModelPath,
		Port:           spec.ListenPort,
		Readiness:      ReadinessHTTPHealth,
		HealthPath:     "/health",
		StartupTimeout: timeout,
		AllowPortRetry: true,
	})
	return proc, err
}

func buildSoloOrLeaderMeshArgs(spec SoloLaunchSpec) ([]string, nodecore.EngineBackend, error) {
	devices, weights, totalVRAM := ExtractSoloGPUConfig(spec.GPUs)
	if spec.PoolVRAMMiB > totalVRAM {
		totalVRAM = spec.PoolVRAMMiB
	}
	normTask, err := nodecore.ParseModelTask(spec.Task)
	if err != nil {
		return nil, "", err
	}
	mmprojPath := resolveSoloComponentPath(spec.MMProjPath, spec.ComponentPaths, nodecore.RoleMMProj)
	vocoderPath := resolveSoloComponentPath(spec.VocoderPath, spec.ComponentPaths, nodecore.RoleVocoder)
	builder := NewLlamaServerBuilder().
		WithModel(spec.ModelPath).
		WithMMProj(mmprojPath).
		WithVocoder(vocoderPath).
		WithEmbeddings(normTask == nodecore.TaskEmbeddings).
		WithReranking(normTask == nodecore.TaskRerank).
		WithParallel(max(1, spec.ParallelSlots)).
		WithContextWindow(spec.ContextLength, totalVRAM).
		WithBind("127.0.0.1", spec.ListenPort)

	if len(spec.RPCEndpoints) > 0 {
		totalGPUs := max(len(spec.RPCEndpoints), spec.TotalGPUs)
		var meshWeights []int
		if len(spec.TensorWeights) == totalGPUs {
			meshWeights = spec.TensorWeights
		}
		args, err := builder.
			WithMode(nodecore.PoolModeMesh).
			WithRPCEndpoints(spec.RPCEndpoints).
			WithRPCDevices(resolveMeshRPCDevices(nil, totalGPUs)).
			WithTensorSplit(meshWeights).
			BuildArgs()
		return args, nodecore.BackendLlamaRPC, err
	}

	soloBackend := nodecore.BackendLlamaLocal
	if parsed, parseErr := nodecore.ParseEngineBackend(spec.TargetBackend); parseErr == nil {
		soloBackend = resolveSoloBackendLabel(parsed)
	}
	args, err := builder.
		WithMode(nodecore.PoolModeSolo).
		WithLocalDevices(devices).
		WithTensorSplit(weights).
		BuildArgs()
	return args, soloBackend, err
}

func resolveSoloComponentPath(explicit string, compMap map[string]string, role nodecore.ComponentRole) string {
	if clean := strings.TrimSpace(explicit); clean != "" {
		return clean
	}
	if len(compMap) == 0 {
		return ""
	}
	return strings.TrimSpace(compMap[string(role)])
}

func ExtractSoloGPUConfig(gpus []protocol.GpuInfo) ([]string, []int, int) {
	devices := make([]string, 0, len(gpus))
	weights := make([]int, 0, len(gpus))
	totalVRAM := 0
	for _, g := range gpus {
		if id := strings.TrimSpace(g.DeviceID); id != "" {
			devices = append(devices, id)
		}
		vram := g.VramFreeMiB
		if vram <= 0 {
			vram = max(1, g.VramTotalMiB)
		}
		weights = append(weights, vram)
		totalVRAM += vram
	}
	return devices, weights, totalVRAM
}

func VulkanVisibleDevicesEnv(deviceIDs []string) []string {
	if len(deviceIDs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(deviceIDs))
	indices := make([]string, 0, len(deviceIDs))
	for _, raw := range deviceIDs {
		idx, ok := parseVulkanDeviceIndex(raw)
		if ok && !seen[idx] {
			seen[idx] = true
			indices = append(indices, idx)
		}
	}
	if len(indices) == 0 {
		return nil
	}
	return []string{"GGML_VK_VISIBLE_DEVICES=" + strings.Join(indices, ",")}
}

func parseVulkanDeviceIndex(raw string) (string, bool) {
	clean := strings.TrimSpace(raw)
	if len(clean) >= 6 && strings.EqualFold(clean[:6], "vulkan") {
		clean = strings.TrimSpace(clean[6:])
	}
	if clean == "" {
		return "", false
	}
	for i := range len(clean) {
		if clean[i] < '0' || clean[i] > '9' {
			return "", false
		}
	}
	return clean, true
}

func llamaBinaryFileName() string {
	if runtime.GOOS == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}

func FindExecutableRecursive(rootDir, targetName string) string {
	cleanRoot := strings.TrimSpace(rootDir)
	if cleanRoot == "" {
		return ""
	}
	direct := filepath.Join(cleanRoot, targetName)
	if st, err := os.Stat(direct); err == nil && !st.IsDir() {
		return direct
	}
	var found string
	if walkErr := filepath.WalkDir(cleanRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), targetName) {
			found = path
			return fs.SkipAll
		}
		return nil
	}); walkErr != nil {
		return ""
	}
	return found
}

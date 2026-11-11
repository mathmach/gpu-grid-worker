package llama

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

type BackendFactory struct {
	llamaServerPath string
	ldLibraryPath   string
	baseEnv         []string
	bootBreaker     *PoolBootCircuitBreaker
}

var _ nodecore.InferenceBackendFactory = (*BackendFactory)(nil)

func NewBackendFactory(llamaServerPath, ldLibraryPath string) *BackendFactory {
	return &BackendFactory{
		llamaServerPath: strings.TrimSpace(llamaServerPath),
		ldLibraryPath:   strings.TrimSpace(ldLibraryPath),
		bootBreaker:     NewPoolBootCircuitBreaker(3, 30*time.Second),
	}
}

func (f *BackendFactory) WithBootBreaker(cb *PoolBootCircuitBreaker) *BackendFactory {
	f.bootBreaker = cb
	return f
}

func (f *BackendFactory) WithBaseEnv(env []string) *BackendFactory {
	if len(env) == 0 {
		f.baseEnv = nil
		return f
	}
	f.baseEnv = append([]string(nil), env...)
	return f
}

func (f *BackendFactory) SupportsFormat(format nodecore.ModelFormat) bool {
	if format == "" {
		return true
	}
	return format == nodecore.FormatGGUF
}

func (f *BackendFactory) IsReady() bool {
	if f == nil {
		return false
	}
	_, ok := ResolveExecutableBinary(f.llamaServerPath)
	return ok
}

func (f *BackendFactory) SpawnPool(ctx context.Context, spec nodecore.PoolLaunchSpec) (nodecore.RunningPoolProcess, error) {
	binPath, ok := ResolveExecutableBinary(f.llamaServerPath)
	if !ok {
		binPath = f.llamaServerPath
	}
	if _, _, err := buildLlamaLaunchArgs(spec); err != nil {
		return nil, err
	}
	ldExtra := strings.TrimSpace(spec.LDLibraryPath)
	if ldExtra == "" {
		ldExtra = f.ldLibraryPath
	}
	proc, _, err := LaunchLocalHTTPSubprocess(ctx, LocalHTTPLaunchSpec{
		BinaryPath: binPath,
		Backend:    effectiveBackendFromSpec(spec),
		BuildArgs: func(port int) ([]string, error) {
			attemptSpec := spec
			attemptSpec.ListenPort = port
			args, _, bErr := buildLlamaLaunchArgs(attemptSpec)
			return args, bErr
		},
		LibraryPath:    ldExtra,
		BaseEnv:        f.baseEnv,
		BootBreaker:    f.bootBreaker,
		ModelKey:       spec.ModelPath,
		Port:           spec.ListenPort,
		Readiness:      ReadinessNone,
		AllowPortRetry: true,
	})
	return proc, err
}

func effectiveBackendFromSpec(spec nodecore.PoolLaunchSpec) nodecore.EngineBackend {
	if isSoloPoolLaunch(spec) {
		return resolveSoloBackendLabel(spec.Backend)
	}
	return nodecore.BackendLlamaRPC
}

func ResolveExecutableBinary(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if filepath.IsAbs(trimmed) || strings.ContainsAny(trimmed, `/\`) {
		st, err := os.Stat(trimmed)
		return trimmed, err == nil && !st.IsDir()
	}
	st, err := os.Stat(trimmed)
	if err == nil && !st.IsDir() {
		return trimmed, true
	}
	resolved, lookErr := exec.LookPath(trimmed)
	if lookErr != nil || resolved == "" {
		return "", false
	}
	return resolved, true
}

func buildLlamaLaunchArgs(spec nodecore.PoolLaunchSpec) ([]string, nodecore.EngineBackend, error) {
	normTask, err := nodecore.ParseModelTask(string(spec.Task))
	if err != nil {
		return nil, "", err
	}
	mmprojPath := resolveSpecComponentPath(spec.MMProjPath, spec.Components, nodecore.RoleMMProj)
	vocoderPath := resolveSpecComponentPath(spec.VocoderPath, spec.Components, nodecore.RoleVocoder)
	builder := NewLlamaServerBuilder().
		WithModel(spec.ModelPath).
		WithMMProj(mmprojPath).
		WithVocoder(vocoderPath).
		WithEmbeddings(spec.Embeddings || normTask == nodecore.TaskEmbeddings).
		WithReranking(spec.Reranking || normTask == nodecore.TaskRerank).
		WithTensorSplit(spec.TensorWeights).
		WithParallel(max(1, spec.ParallelSlots)).
		WithContextWindow(spec.ContextLength, spec.TargetVRAMMiB).
		WithBind("127.0.0.1", spec.ListenPort)

	if isSoloPoolLaunch(spec) {
		builder.WithMode(nodecore.PoolModeSolo).WithLocalDevices(spec.Devices)
		args, err := builder.BuildArgs()
		return args, resolveSoloBackendLabel(spec.Backend), err
	}

	devices := resolveMeshRPCDevices(spec.Devices, spec.TotalGPUs)
	builder.WithMode(nodecore.PoolModeMesh).
		WithRPCEndpoints(spec.RPCEndpoints).
		WithRPCDevices(devices)
	args, err := builder.BuildArgs()
	return args, nodecore.BackendLlamaRPC, err
}

func resolveSpecComponentPath(explicit string, components []nodecore.BundleComponent, role nodecore.ComponentRole) string {
	if clean := strings.TrimSpace(explicit); clean != "" {
		return clean
	}
	for _, comp := range components {
		if comp.Role == role && strings.TrimSpace(comp.FilePath) != "" {
			return strings.TrimSpace(comp.FilePath)
		}
	}
	return ""
}

func isSoloPoolLaunch(spec nodecore.PoolLaunchSpec) bool {
	if spec.Mode == nodecore.PoolModeSolo {
		return true
	}
	switch spec.Backend {
	case nodecore.BackendLlamaLocal, nodecore.BackendAudioLocal, nodecore.BackendOmniLocal:
		return true
	}
	return len(spec.RPCEndpoints) == 0 && len(spec.Devices) > 0
}

func resolveSoloBackendLabel(backend nodecore.EngineBackend) nodecore.EngineBackend {
	switch backend {
	case nodecore.BackendAudioLocal, nodecore.BackendOmniLocal:
		return backend
	default:
		return nodecore.BackendLlamaLocal
	}
}

func resolveMeshRPCDevices(explicit []string, totalGPUs int) []string {
	devices := cleanNonEmptyList(explicit)
	if len(devices) > 0 {
		return devices
	}
	count := max(1, totalGPUs)
	out := make([]string, count)
	for i := range count {
		out[i] = fmt.Sprintf("RPC%d", i)
	}
	return out
}

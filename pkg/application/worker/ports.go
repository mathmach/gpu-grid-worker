package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type BootConfig struct {
	GatewayURL            string
	EmbeddedGatewayURL    string
	BootstrapURLs         []string
	BootstrapURLsExplicit bool
	BootstrapPublicKeys   []string
	Token                 string
	WorkerName            string
	Devices               []string
	ModelMode             string
	SelectedModels        []string
	EnabledBackends       []string
	AllowInsecureWS       bool
	CDNModelsBaseURL      string
	ScheduleMode          string
	IdleThresholdSec      int
	DataDir               string
	DefaultLanguage       string
	ControlPort           int
	SoloEngineBootTimeout time.Duration
	ReconnectDelay        time.Duration
	IdlePollInterval      time.Duration
	PauseDrainTimeout     time.Duration
}

type SoloEngineSpec struct {
	PoolID         string
	ExecutionMode  string
	ModelID        string
	ModelPath      string
	ModelSHA256    string
	ModelFormat    string
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
}

type HardwareInspector interface {
	MachineFingerprint() string
	ProbeGPUs(ctx context.Context, runtimeDir string) ([]protocol.GpuInfo, error)
	IsDedicatedGPU(name string) bool
}

type IdleDetector interface {
	UserIdleSeconds() int
}

type RunningRPCProcess interface {
	Stop()
	Done() <-chan struct{}
}

type RPCSupervisor interface {
	SpawnRPC(ctx context.Context, runtimeDir, cacheDir string, gpus []protocol.GpuInfo, port int) (RunningRPCProcess, error)
}

type SoloSupervisor interface {
	SupportedBackends(runtimeDir string) []string
	SpawnSolo(ctx context.Context, spec SoloEngineSpec) (RunningRPCProcess, error)
}

type DynamicBackendFilter interface {
	SetEnabledBackends(backends []string)
}

type TunnelSocket interface {
	ReadFrame() (bool, []byte, error)
	SendJSON(v any, timeout time.Duration) error
	SendStreamChunk(streamID uint32, payload []byte, timeout time.Duration) error
	Close() error
}

type TunnelDialer interface {
	Dial(ctx context.Context, wsURL string) (TunnelSocket, error)
}

type ArchiveVerifier interface {
	DownloadVerified(ctx context.Context, url, destPath, expectedSHA256 string) (string, error)
	ExtractArchive(archivePath, destDir string) error
}

type ModelCacheVerifier interface {
	EnsureModelCached(ctx context.Context, cacheRoot, modelID, fileName, downloadURL, expectedSHA256 string, onProgress func(downloaded, total int64)) (string, string, error)
}

type ModelCacheInventoryPort interface {
	ListCachedDigests(cacheRoot string) ([]string, error)
	AvailableDiskMiB(cacheRoot string) (int, error)
}

type LoopbackRelayPort interface {
	ListenLoopback(onAccept func(io.ReadWriteCloser)) (int, func(), error)
}

type ConfigStore interface {
	DataDir() string
	LoadConfig() (PersistedConfig, bool, error)
	SaveConfig(cfg PersistedConfig) error
}

type GatewayClient interface {
	NormalizeGatewayURL(raw string) (string, error)
	BuildWorkerWSURL(gwURL string, allowInsecureWS bool) (string, error)
	FetchRuntimeSHA256(ctx context.Context, gwURL, archiveName string) (string, error)
	FetchCatalogModels(ctx context.Context, gwURL string) ([]protocol.CatalogModelView, error)
}

type WorkerPortalPort interface {
	ExchangeWorkerAuthCode(ctx context.Context, gwURL, code, codeVerifier, lang string) (nodecore.WorkerAccountAuth, error)
	PollWorkerAuthState(ctx context.Context, gwURL, state, codeVerifier, lang string) (nodecore.WorkerAccountAuth, bool, error)
	ResolvePreDownloadArtifacts(ctx context.Context, gwURL, workerToken string, modelIDs []string) ([]protocol.WorkerModelArtifactView, error)
}

type NetworkBootstrapPort interface {
	FetchNetworkBootstrap(ctx context.Context, targetURL string) ([]byte, error)
}

type BrowserLauncher interface {
	OpenExternalURL(targetURL string) error
}

type RuntimeFS interface {
	FindExistingRuntime(dataDir, binaryName string) string
	PrepareStagingDir(dataDir string) (string, error)
	ActivateStagedRuntime(stagingDir, targetDir, binaryName string) error
	RemovePath(path string) error
	DefaultHostInfo() (hostname string, pid int)
}

type LoopbackTransport interface {
	AllocateFreePort() (int, error)
	DialStream(port int, timeout time.Duration) (io.ReadWriteCloser, error)
	ProbeHTTPHealth(ctx context.Context, port int) bool
}

type Dependencies struct {
	Boot           BootConfig
	Hardware       HardwareInspector
	Idle           IdleDetector
	RPC            RPCSupervisor
	Solo           SoloSupervisor
	Tunnel         TunnelDialer
	Archive        ArchiveVerifier
	ModelCache     ModelCacheVerifier
	CacheInventory ModelCacheInventoryPort
	ConfigStore    ConfigStore
	GatewayClient  GatewayClient
	Portal         WorkerPortalPort
	Bootstrap      NetworkBootstrapPort
	Browser        BrowserLauncher
	RuntimeFS      RuntimeFS
	Loopback       LoopbackTransport
	LoopbackRelay  LoopbackRelayPort
	Logger         *slog.Logger
}

type noopIdleDetector struct{}

func (noopIdleDetector) UserIdleSeconds() int { return -1 }

type noopRPCSupervisor struct{}

func (noopRPCSupervisor) SpawnRPC(_ context.Context, _, _ string, _ []protocol.GpuInfo, _ int) (RunningRPCProcess, error) {
	return nil, errors.New("rpc supervisor not configured")
}

type noopTunnelDialer struct{}

func (noopTunnelDialer) Dial(_ context.Context, _ string) (TunnelSocket, error) {
	return nil, errors.New("tunnel dialer not configured")
}

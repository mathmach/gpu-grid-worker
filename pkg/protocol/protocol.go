package protocol

const (
	ModelModeAuto  = "auto"
	ModelModeMulti = "multi"

	TunnelTargetRPC      = "rpc"
	TunnelTargetSoloHTTP = "solo_http"

	ExecutionModeSolo = "SOLO"
	ExecutionModeMesh = "MESH"

	PoolRoleMeshLeader = "LEADER"
	PoolRoleMeshPeer   = "PEER"

	TunnelTargetMeshRelay = "mesh_relay"

	ControlHello               = "hello"
	ControlRejected            = "rejected"
	ControlTunnelOpen          = "tunnel_open"
	ControlTunnelClose         = "tunnel_close"
	ControlState               = "state"
	ControlUpdateModels        = "update_models"
	ControlPauseState          = "pause_state"
	ControlJobCredit           = "job_credit"
	ControlConfigureSoloEngine = "configure_solo_engine"
	ControlSoloEngineReady     = "solo_engine_ready"
	ControlSoloEngineError     = "solo_engine_error"
	ControlStopSoloEngine      = "stop_solo_engine"
	ControlMeshRelayOpen       = "mesh_relay_open"
	ControlEnsureMeshModel     = "ensure_mesh_model"
	ControlMeshPeerReady       = "mesh_peer_ready"
	ControlMeshPeerError       = "mesh_peer_error"
	ControlStopMeshPeer        = "stop_mesh_peer"
	ControlCacheInventory      = "cache_inventory"

	WorkerInitiatedStreamMask uint32 = 0x80000000
)

type MeshPeerWire struct {
	WorkerID string `json:"workerId"`
	GPUCount int    `json:"gpuCount"`
}

type BundleComponentWire struct {
	Role        string `json:"role"`
	FileName    string `json:"fileName"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
}

type WorkerModelArtifactView struct {
	ModelID          string                `json:"modelId"`
	FileName         string                `json:"fileName,omitempty"`
	ModelSHA256      string                `json:"modelSha256,omitempty"`
	DownloadURL      string                `json:"downloadUrl,omitempty"`
	FileSizeMiB      int                   `json:"fileSizeMiB,omitempty"`
	BundleComponents []BundleComponentWire `json:"bundleComponents,omitempty"`
}

type GpuInfo struct {
	DeviceID     string `json:"deviceId"`
	Name         string `json:"name"`
	VramTotalMiB int    `json:"vramTotalMiB"`
	VramFreeMiB  int    `json:"vramFreeMiB"`
}

type CatalogModelView struct {
	ID                        string                `json:"id"`
	FileName                  string                `json:"fileName,omitempty"`
	ModelSHA256               string                `json:"modelSha256,omitempty"`
	Format                    string                `json:"format,omitempty"`
	Backend                   string                `json:"backend,omitempty"`
	Task                      string                `json:"task,omitempty"`
	StudioModalities          []string              `json:"studioModalities,omitempty"`
	InputModalities           []string              `json:"inputModalities,omitempty"`
	OutputModalities          []string              `json:"outputModalities,omitempty"`
	AllowsMesh                bool                  `json:"allowsMesh,omitempty"`
	BaseComputeUnits          int64                 `json:"baseComputeUnits,omitempty"`
	BundleComponents          []BundleComponentWire `json:"bundleComponents,omitempty"`
	BackendReady              bool                  `json:"backendReady"`
	Architecture              string                `json:"architecture"`
	Name                      string                `json:"name"`
	Quantization              string                `json:"quantization,omitempty"`
	SizeLabel                 string                `json:"sizeLabel,omitempty"`
	BlockCount                int                   `json:"blockCount"`
	ContextLength             int                   `json:"contextLength"`
	FileSizeMiB               int                   `json:"fileSizeMiB"`
	TargetVramMiB             int                   `json:"targetVramMiB"`
	TargetVramOverrideMiB     int                   `json:"targetVramOverrideMiB,omitempty"`
	IsDefault                 bool                  `json:"isDefault,omitempty"`
	Disabled                  bool                  `json:"disabled,omitempty"`
	RemoteOnly                bool                  `json:"remoteOnly,omitempty"`
	VerifiedOnly              bool                  `json:"verifiedOnly,omitempty"`
	Active                    bool                  `json:"active"`
	ReadyPools                int                   `json:"readyPools"`
	StandbyPools              int                   `json:"standbyPools"`
	WarmSlots                 int                   `json:"warmSlots"`
	ActiveInferences          int                   `json:"activeInferences"`
	WaitingWorkers            int                   `json:"waitingWorkers"`
	WaitingVramMiB            int                   `json:"waitingVramMiB"`
	MissingVramMiB            int                   `json:"missingVramMiB"`
	CanSwapOnDemand           bool                  `json:"canSwapOnDemand"`
	BestClearingPer1M         float64               `json:"bestClearingPer1M"`
	BestGrossOutputPer1M      float64               `json:"bestGrossOutputPer1M"`
	BestGrossInputPer1M       float64               `json:"bestGrossInputPer1M"`
	PriorityGrossOutputPer1M  float64               `json:"priorityGrossOutputPer1M,omitempty"`
	PriorityGrossInputPer1M   float64               `json:"priorityGrossInputPer1M,omitempty"`
	ReferenceUnitCost         float64               `json:"referenceUnitCost,omitempty"`
	PriorityReferenceUnitCost float64               `json:"priorityReferenceUnitCost,omitempty"`
	PriorityMultiplierBps     int                   `json:"priorityMultiplierBps,omitempty"`
	AvgTokensPerSec           float64               `json:"avgTokensPerSec"`
}

type ControlMsg struct {
	Type        string `json:"type"`
	TraceParent string `json:"traceparent,omitempty"`

	MachineID          string    `json:"machineId,omitempty"`
	WorkerToken        string    `json:"workerToken,omitempty"`
	OS                 string    `json:"os,omitempty"`
	Name               string    `json:"name,omitempty"`
	GPUs               []GpuInfo `json:"gpus,omitempty"`
	SupportedBackends  []string  `json:"supportedBackends,omitempty"`
	CachedModelSHA256s []string  `json:"cachedModelSha256s,omitempty"`
	DiskFreeMiB        int       `json:"diskFreeMiB,omitempty"`
	DiskFreeProbed     bool      `json:"diskFreeProbed,omitempty"`
	ModelMode          string    `json:"modelMode,omitempty"`
	SelectedModels     []string  `json:"selectedModels,omitempty"`
	ScheduleMode       string    `json:"scheduleMode,omitempty"`
	Paused             bool      `json:"paused,omitempty"`
	PauseReason        string    `json:"pauseReason,omitempty"`

	Reason string `json:"reason,omitempty"`

	StreamID     uint32 `json:"streamId,omitempty"`
	TunnelTarget string `json:"tunnelTarget,omitempty"`

	ExecutionMode    string                `json:"executionMode,omitempty"`
	ModelID          string                `json:"modelId,omitempty"`
	ModelSHA256      string                `json:"modelSha256,omitempty"`
	ModelDownloadURL string                `json:"modelDownloadUrl,omitempty"`
	ModelFileName    string                `json:"modelFileName,omitempty"`
	ModelFormat      string                `json:"modelFormat,omitempty"`
	Task             string                `json:"task,omitempty"`
	TargetBackend    string                `json:"targetBackend,omitempty"`
	ContextLength    int                   `json:"contextLength,omitempty"`
	ParallelSlots    int                   `json:"parallelSlots,omitempty"`
	VramTotalMiB     int                   `json:"vramTotalMiB,omitempty"`
	BundleComponents []BundleComponentWire `json:"bundleComponents,omitempty"`
	MeshPeers        []MeshPeerWire        `json:"meshPeers,omitempty"`
	TensorWeights    []int                 `json:"tensorWeights,omitempty"`
	PeerWorkerID     string                `json:"peerWorkerId,omitempty"`

	StateSeq     uint64      `json:"stateSeq,omitempty"`
	PoolID       string      `json:"poolId,omitempty"`
	AccountEmail string      `json:"accountEmail,omitempty"`
	Currency     string      `json:"currency,omitempty"`
	Worker       *WorkerView `json:"worker,omitempty"`

	Tokens        int     `json:"tokens,omitempty"`
	Blocks        int     `json:"blocks,omitempty"`
	ComputeUnits  int64   `json:"computeUnits,omitempty"`
	ClearingPer1M float64 `json:"clearingPer1M,omitempty"`
	EarnedNano    int64   `json:"earnedNano,omitempty"`
	Earned        float64 `json:"earned,omitempty"`
	Balance       float64 `json:"balance,omitempty"`
}

type WorkerView struct {
	ID                    string    `json:"id"`
	PoolID                string    `json:"poolId"`
	ModelID               string    `json:"modelId,omitempty"`
	ModelMode             string    `json:"modelMode,omitempty"`
	SelectedModels        []string  `json:"selectedModels,omitempty"`
	SupportedBackends     []string  `json:"supportedBackends,omitempty"`
	ExecutionMode         string    `json:"executionMode,omitempty"`
	TrustTier             string    `json:"trustTier,omitempty"`
	WaitingReason         string    `json:"waitingReason,omitempty"`
	MissingVramMiB        int       `json:"missingVramMiB,omitempty"`
	PoolRole              string    `json:"poolRole,omitempty"`
	MeshRole              string    `json:"meshRole,omitempty"`
	DiskFreeMiB           int       `json:"diskFreeMiB,omitempty"`
	OrderBookRank         int       `json:"orderBookRank"`
	OwnerID               string    `json:"ownerId,omitempty"`
	OwnerName             string    `json:"ownerName,omitempty"`
	OwnerEmail            string    `json:"ownerEmail,omitempty"`
	MachineID             string    `json:"machineId"`
	OS                    string    `json:"os"`
	Name                  string    `json:"name"`
	RemoteIP              string    `json:"remoteIp"`
	NetworkGroup          string    `json:"networkGroup,omitempty"`
	RttMs                 int64     `json:"rttMs"`
	P50RttMs              int64     `json:"p50RttMs,omitempty"`
	P95RttMs              int64     `json:"p95RttMs,omitempty"`
	P99RttMs              int64     `json:"p99RttMs,omitempty"`
	GPUs                  []GpuInfo `json:"gpus"`
	VramTotalMiB          int       `json:"vramTotalMiB"`
	VramFreeMiB           int       `json:"vramFreeMiB"`
	AssignedLayers        int       `json:"assignedLayers"`
	TotalLayers           int       `json:"totalLayers"`
	SharePct              float64   `json:"sharePct"`
	ClearingPer1M         float64   `json:"clearingPer1M"`
	GrossOutputPer1M      float64   `json:"grossOutputPer1M"`
	ComputeCUSec          float64   `json:"computeCuPerSec,omitempty"`
	ScheduleMode          string    `json:"scheduleMode,omitempty"`
	Paused                bool      `json:"paused"`
	PauseReason           string    `json:"pauseReason,omitempty"`
	ReliabilityPct        float64   `json:"reliabilityPct"`
	Status                string    `json:"status"`
	RPCPackets            int64     `json:"rpcPackets"`
	RPCBytes              int64     `json:"rpcBytes"`
	BlocksProcessed       int64     `json:"blocksProcessed"`
	TokensProcessed       int64     `json:"tokensProcessed"`
	ComputeUnitsProcessed int64     `json:"computeUnitsProcessed"`
	Balance               float64   `json:"balance"`
	ConnectedAt           string    `json:"connectedAt"`
}

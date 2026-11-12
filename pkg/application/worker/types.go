package worker

import (
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const (
	DefaultIdleThresholdSec  = 120
	MinIdleThresholdSec      = 15
	MaxIdleThresholdSec      = 3600
	DefaultReconnectDelay    = time.Second
	MaxReconnectDelay        = 30 * time.Second
	DefaultPauseDrainTimeout = 15 * time.Second

	SoloStatusIdle       = "IDLE"
	SoloStatusPulling    = "PULLING_MODEL"
	SoloStatusStarting   = "STARTING_SOLO"
	SoloStatusReady      = "SOLO_READY"
	SoloStatusError      = "ERROR"
	StatusProbeError     = "HARDWARE_PROBE_ERROR"
	StatusDownloading    = "DOWNLOADING"
	StatusPreDownloading = "PRE_DOWNLOADING"
	StatusPauseDraining  = "PAUSE_DRAINING"
)

type PersistedConfig = nodecore.WorkerPersistedConfig

type StartParams struct {
	GatewayURL         string   `json:"gatewayUrl"`
	WorkerToken        string   `json:"workerToken"`
	WorkerName         string   `json:"workerName"`
	SelectedIDs        []string `json:"selectedIds"`
	ScheduleMode       string   `json:"scheduleMode"`
	IdleThresholdSec   int      `json:"idleThresholdSec"`
	ModelMode          string   `json:"modelMode"`
	SelectedModels     []string `json:"selectedModels"`
	ModelSelectionSet  bool     `json:"-"`
	EnabledBackends    []string `json:"enabledBackends,omitempty"`
	EnabledBackendsSet bool     `json:"-"`
}

type UpdateSettings struct {
	GatewayURL        string
	GatewayURLSet     bool
	GatewayPinned     *bool
	WorkerToken       string
	WorkerTokenSet    bool
	WorkerName        string
	WorkerNameSet     bool
	SelectedIDs       []string
	SelectedIDsSet    bool
	ScheduleMode      string
	ScheduleModeSet   bool
	IdleThresholdSec  int
	IdleThresholdSet  bool
	ManualPaused      *bool
	ModelMode         string
	ModelModeSet      bool
	SelectedModels    []string
	SelectedModelsSet bool
	ModelSelectionSet bool
	EnabledBackends   []string
	BackendsSet       bool
	Language          string
	LanguageSet       bool
	UIMode            string
	UIModeSet         bool
}

type StatusSnapshot struct {
	Currency              string                      `json:"currency"`
	Language              string                      `json:"language"`
	UIMode                string                      `json:"uiMode"`
	PID                   int                         `json:"pid"`
	OS                    string                      `json:"os"`
	Arch                  string                      `json:"arch"`
	MachineID             string                      `json:"machineId"`
	GatewayURL            string                      `json:"gatewayUrl"`
	GatewayPinned         bool                        `json:"gatewayPinned,omitempty"`
	BootstrapVersion      uint64                      `json:"bootstrapVersion,omitempty"`
	WorkerToken           string                      `json:"-"`
	HasWorkerToken        bool                        `json:"hasWorkerToken"`
	BrowserAuthPending    bool                        `json:"browserAuthPending,omitempty"`
	WorkerName            string                      `json:"workerName"`
	AccountEmail          string                      `json:"accountEmail"`
	PreDownloadModelID    string                      `json:"preDownloadModelId,omitempty"`
	PreDownloadPct        int                         `json:"preDownloadPct,omitempty"`
	PreDownloadIndex      int                         `json:"preDownloadIndex,omitempty"`
	PreDownloadTotal      int                         `json:"preDownloadTotal,omitempty"`
	PoolID                string                      `json:"poolId"`
	PoolRole              string                      `json:"poolRole"`
	MeshRole              string                      `json:"meshRole,omitempty"`
	ExecutionMode         string                      `json:"executionMode,omitempty"`
	SoloEngineStatus      string                      `json:"soloEngineStatus"`
	SoloModelID           string                      `json:"soloModelId"`
	CachedModelSHA256s    []string                    `json:"cachedModelSha256s,omitempty"`
	DiskFreeMiB           int                         `json:"diskFreeMiB,omitempty"`
	DiskFreeProbed        bool                        `json:"diskFreeProbed,omitempty"`
	SupportedBackends     []string                    `json:"supportedBackends"`
	EnabledBackends       []string                    `json:"enabledBackends,omitempty"`
	OrderBookRank         int                         `json:"orderBookRank"`
	RttMs                 int64                       `json:"rttMs"`
	ClearingPer1M         float64                     `json:"clearingPer1M"`
	ModelMode             string                      `json:"modelMode"`
	SelectedModels        []string                    `json:"selectedModels"`
	ActiveModelID         string                      `json:"activeModelId"`
	WaitingReason         string                      `json:"waitingReason,omitempty"`
	MissingVramMiB        int                         `json:"missingVramMiB,omitempty"`
	CatalogModels         []protocol.CatalogModelView `json:"catalogModels"`
	ScheduleMode          string                      `json:"scheduleMode"`
	IdleThresholdSec      int                         `json:"idleThresholdSec"`
	IdleStatus            string                      `json:"idleStatus,omitempty"`
	ManualPaused          bool                        `json:"manualPaused"`
	AutoPaused            bool                        `json:"autoPaused"`
	PauseDraining         bool                        `json:"pauseDraining"`
	Paused                bool                        `json:"paused"`
	PauseReason           string                      `json:"pauseReason"`
	IdleSeconds           int                         `json:"idleSeconds"`
	ReliabilityPct        float64                     `json:"reliabilityPct"`
	DetectedGPUs          []protocol.GpuInfo          `json:"detectedGpus"`
	SelectedIDs           []string                    `json:"selectedIds"`
	SelectedVramMiB       int                         `json:"selectedVramMiB"`
	Running               bool                        `json:"running"`
	Status                string                      `json:"status"`
	SessionState          string                      `json:"sessionState"`
	AssignedLayers        int                         `json:"assignedLayers"`
	TotalLayers           int                         `json:"totalLayers"`
	SharePct              float64                     `json:"sharePct"`
	BlocksProcessed       int64                       `json:"blocksProcessed"`
	TokensProcessed       int64                       `json:"tokensProcessed"`
	ComputeUnitsProcessed int64                       `json:"computeUnitsProcessed"`
	Balance               float64                     `json:"balance"`
	LastError             string                      `json:"lastError"`
	Logs                  []string                    `json:"logs"`
}

type Snapshot = StatusSnapshot

func ClampIdleThreshold(sec int) int {
	if sec < MinIdleThresholdSec {
		return MinIdleThresholdSec
	}
	if sec > MaxIdleThresholdSec {
		return MaxIdleThresholdSec
	}
	return sec
}

func SelectedGPUsVRAM(detected []protocol.GpuInfo, selectedIDs []string) int {
	var total int
	for _, id := range selectedIDs {
		for _, g := range detected {
			if strings.EqualFold(g.DeviceID, id) {
				if g.VramFreeMiB > 0 {
					total += g.VramFreeMiB
				} else {
					total += g.VramTotalMiB
				}
				break
			}
		}
	}
	return total
}

func NormalizeBackendList(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		parsed, err := nodecore.ParseEngineBackend(item)
		if err != nil {
			continue
		}
		val := parsed.String()
		if !seen[val] {
			seen[val] = true
			out = append(out, val)
		}
	}
	return out
}

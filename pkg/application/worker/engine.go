package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type Engine struct {
	machineID         string
	hostPID           int
	controlPort       int
	dataDir           string
	allowInsecureWS   bool
	cdnModelsBaseURL  string
	hardware          HardwareInspector
	idle              IdleDetector
	rpc               RPCSupervisor
	solo              SoloSupervisor
	tunnel            TunnelDialer
	archive           ArchiveVerifier
	modelCache        ModelCacheVerifier
	cacheInventory    ModelCacheInventoryPort
	configStore       ConfigStore
	gatewayClient     GatewayClient
	portal            WorkerPortalPort
	browser           BrowserLauncher
	runtimeFS         RuntimeFS
	loopback          LoopbackTransport
	loopbackRelay     LoopbackRelayPort
	logger            *slog.Logger
	reconnectDelay    time.Duration
	idlePollInterval  time.Duration
	pauseDrainTimeout time.Duration
	soloBootTimeout   time.Duration

	lifecycleMu sync.Mutex
	settingsMu  sync.Mutex
	pauseMu     sync.Mutex
	runtimeMu   sync.Mutex
	meshPeerMu  sync.Mutex
	cfgWriteMu  sync.Mutex
	mu          sync.Mutex
	obsMu       sync.Mutex

	cachedIdleSec atomic.Int64

	nextSubID   uint64
	subscribers map[uint64]chan Snapshot

	language               string
	uiMode                 string
	currency               nodecore.DisplayCurrency
	gatewayURL             string
	workerToken            string
	workerName             string
	accountEmail           string
	browserAuth            *pendingBrowserAuth
	lastCompletedAuthState string
	preDownloadModelID     string
	preDownloadPct         int
	preDownloadIndex       int
	preDownloadTotal       int
	poolID                 string
	poolRole               string
	meshRole               string
	executionMode          string
	soloStatus             string
	soloModelID            string
	cachedModelSHA256s     []string
	diskFreeMiB            int
	diskFreeProbed         bool
	probedBackends         []string
	remotePackBackends     []string
	enabledBackends        []string
	enabledBackendsSet     bool
	orderBookRank          int
	rttMs                  int64
	clearingPer1M          float64
	modelMode              string
	selectedModels         []string
	activeModelID          string
	waitingReason          string
	missingVramMiB         int
	catalogModels          []protocol.CatalogModelView
	scheduleMode           string
	scheduleConfigured     bool
	idleThresholdSec       int
	idleStatus             string
	idleUnavailableLogged  bool
	manualPaused           bool
	autoPaused             bool
	pauseDraining          bool
	drainGen               uint64
	drainCancel            context.CancelFunc
	idleSeconds            int
	reliabilityPct         float64
	detectedGPUs           []protocol.GpuInfo
	selectedIDs            []string
	running                bool
	status                 string
	sessionState           nodecore.WorkerSessionState
	assignedLayers         int
	totalLayers            int
	sharePct               float64
	blocksProcessed        int64
	tokensProcessed        int64
	computeUnitsProcessed  int64
	priorBlocks            int64
	priorTokens            int64
	priorComputeUnits      int64
	sessStateBlocks        int64
	sessStateTokens        int64
	sessStateComputeUnits  int64
	sessCreditBlocks       int64
	sessCreditTokens       int64
	sessCreditComputeUnits int64
	lastWorkerStateSeq     uint64
	lastStateSeq           uint64
	minObservedBalance     float64
	balance                float64
	lastError              string
	logs                   []string

	runGen         uint64
	loopDone       chan struct{}
	activeSendJSON func(protocol.ControlMsg) error
	activeStreams  *tunnelStreams
	runCtx         context.Context
	cancelRun      context.CancelFunc
	runtimeDir     string
	localPort      int
	activeGPUs     []protocol.GpuInfo
	rpcGen         uint64
	rpcProc        RunningRPCProcess
	soloPort       int
	soloProc       RunningRPCProcess
	soloGen        uint64
	soloCancel     context.CancelFunc
	relayStops     []func()
	meshPeerGen    uint64
	meshPeerCancel context.CancelFunc
}

func NewEngine(deps Dependencies) *Engine {
	deps = normalizeDependencies(deps)
	boot := deps.Boot
	dataDir := resolveWorkerDataDir(boot.DataDir, deps.ConfigStore)
	defaultHost, hostPID := resolveHostDefaults(deps.RuntimeFS)
	var machineID string
	if deps.Hardware != nil {
		machineID = deps.Hardware.MachineFingerprint()
	}

	idleInitial := deps.Idle.UserIdleSeconds()
	ctrlPort := boot.ControlPort
	if ctrlPort <= 0 {
		ctrlPort = 17771
	}
	eng := &Engine{
		machineID:         machineID,
		hostPID:           hostPID,
		controlPort:       ctrlPort,
		dataDir:           dataDir,
		hardware:          deps.Hardware,
		idle:              deps.Idle,
		rpc:               deps.RPC,
		solo:              deps.Solo,
		tunnel:            deps.Tunnel,
		archive:           deps.Archive,
		modelCache:        deps.ModelCache,
		cacheInventory:    deps.CacheInventory,
		configStore:       deps.ConfigStore,
		gatewayClient:     deps.GatewayClient,
		portal:            deps.Portal,
		browser:           deps.Browser,
		runtimeFS:         deps.RuntimeFS,
		loopback:          deps.Loopback,
		loopbackRelay:     deps.LoopbackRelay,
		logger:            deps.Logger,
		reconnectDelay:    DefaultReconnectDelay,
		idlePollInterval:  2 * time.Second,
		pauseDrainTimeout: DefaultPauseDrainTimeout,
		soloBootTimeout:   boot.SoloEngineBootTimeout,
		language:          resolveInitialLanguage(boot.DefaultLanguage),
		uiMode:            string(nodecore.WorkerUISimple),
		gatewayURL:        "http://127.0.0.1:8000",
		workerName:        defaultHost,
		soloStatus:        SoloStatusIdle,
		modelMode:         string(nodecore.ModelModeAuto),
		scheduleMode:      string(nodecore.ScheduleAlways),
		idleThresholdSec:  DefaultIdleThresholdSec,
		idleSeconds:       idleInitial,
		reliabilityPct:    100.0,
		status:            "STOPPED",
		sessionState:      nodecore.WorkerClosed,
	}
	eng.cachedIdleSec.Store(int64(idleInitial))
	eng.cleanupLegacyStagingDirs()
	if strings.TrimSpace(boot.EmbeddedGatewayURL) != "" {
		if normEmb, err := eng.validateAndNormalizeGateway(boot.EmbeddedGatewayURL); err == nil {
			eng.gatewayURL = normEmb
		}
	}
	eng.loadPersistedConfig()
	eng.applyBootOverrides(boot)
	eng.syncSoloEnabledBackends()
	eng.idleStatus = resolveIdleStatus(eng.scheduleMode, eng.idleSeconds)
	eng.probedBackends = eng.probeBackendsForRuntime(eng.ResolveExistingRuntimeDir())
	eng.refreshDiskAndCacheInventory()
	return eng
}

func normalizeDependencies(deps Dependencies) Dependencies {
	if deps.Idle == nil {
		deps.Idle = noopIdleDetector{}
	}
	if deps.RPC == nil {
		deps.RPC = noopRPCSupervisor{}
	}
	if deps.Tunnel == nil {
		deps.Tunnel = noopTunnelDialer{}
	}
	populateOptionalRoleAdapters(&deps)
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return deps
}

func populateOptionalRoleAdapters(deps *Dependencies) {
	if deps.ModelCache == nil && deps.Archive != nil {
		if mc, ok := deps.Archive.(ModelCacheVerifier); ok {
			deps.ModelCache = mc
		}
	}
	if deps.CacheInventory == nil {
		deps.CacheInventory = resolveFallbackCacheInventory(deps.ModelCache, deps.Archive)
	}
	if deps.LoopbackRelay == nil && deps.Loopback != nil {
		if lr, ok := deps.Loopback.(LoopbackRelayPort); ok {
			deps.LoopbackRelay = lr
		}
	}
	if deps.Portal == nil && deps.GatewayClient != nil {
		if p, ok := deps.GatewayClient.(WorkerPortalPort); ok {
			deps.Portal = p
		}
	}
}

func resolveFallbackCacheInventory(mc ModelCacheVerifier, arc ArchiveVerifier) ModelCacheInventoryPort {
	if inv, ok := mc.(ModelCacheInventoryPort); ok {
		return inv
	}
	if inv, ok := arc.(ModelCacheInventoryPort); ok {
		return inv
	}
	return nil
}

func (e *Engine) syncSoloEnabledBackends() {
	filter, ok := e.solo.(DynamicBackendFilter)
	if !ok || filter == nil {
		return
	}
	e.mu.Lock()
	var configured []string
	if e.enabledBackendsSet {
		configured = make([]string, len(e.enabledBackends))
		copy(configured, e.enabledBackends)
	} else if len(e.enabledBackends) > 0 {
		configured = append([]string(nil), e.enabledBackends...)
	}
	e.mu.Unlock()
	filter.SetEnabledBackends(configured)
}

func resolveHostDefaults(fs RuntimeFS) (string, int) {
	if fs != nil {
		host, pid := fs.DefaultHostInfo()
		if strings.TrimSpace(host) != "" {
			return host, pid
		}
		return "GPU-NODE", pid
	}
	return "GPU-NODE", 0
}

func resolveInitialLanguage(defaultLang string) string {
	if defaultLang == "pt-BR" {
		return "pt-BR"
	}
	return "en"
}

func (e *Engine) sampleIdleSeconds() int {
	sec := e.idle.UserIdleSeconds()
	e.cachedIdleSec.Store(int64(sec))
	return sec
}

func (e *Engine) MachineID() string {
	return e.machineID
}

func (e *Engine) advanceSessionStateLocked(target nodecore.WorkerSessionState) {
	if e.sessionState == target {
		return
	}
	if next, err := e.sessionState.TransitionTo(target); err == nil {
		e.sessionState = next
		return
	}
	if target == nodecore.WorkerInPool {
		if next, ok := tryViaIntermediateSessionState(e.sessionState, nodecore.WorkerIdle, target); ok {
			e.sessionState = next
		}
		return
	}
	if target == nodecore.WorkerPauseDraining {
		if next, ok := tryViaIntermediateSessionState(e.sessionState, nodecore.WorkerInPool, target); ok {
			e.sessionState = next
		}
	}
}

func tryViaIntermediateSessionState(
	cur nodecore.WorkerSessionState,
	via nodecore.WorkerSessionState,
	target nodecore.WorkerSessionState,
) (nodecore.WorkerSessionState, bool) {
	mid, err := cur.TransitionTo(via)
	if err != nil {
		return cur, false
	}
	next, err := mid.TransitionTo(target)
	if err != nil {
		return cur, false
	}
	return next, true
}

const maxWorkerLogLines = 150

func (e *Engine) addLog(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
	e.logger.Info("worker_event", "message", msg)

	e.mu.Lock()
	e.logs = append(e.logs, line)
	if len(e.logs) > maxWorkerLogLines {
		copy(e.logs, e.logs[len(e.logs)-maxWorkerLogLines:])
		clear(e.logs[maxWorkerLogLines:])
		e.logs = e.logs[:maxWorkerLogLines]
	}
	e.mu.Unlock()
	e.notifyObservers()
}

func (e *Engine) setLastError(msg string) {
	e.mu.Lock()
	e.lastError = msg
	e.mu.Unlock()
	e.notifyObservers()
}

func (e *Engine) Snapshot() StatusSnapshot {
	idleCached := int(e.cachedIdleSec.Load())
	e.mu.Lock()
	e.idleSeconds = idleCached
	e.idleStatus = resolveIdleStatus(e.scheduleMode, idleCached)
	snap := e.buildSnapshotLocked()
	e.mu.Unlock()
	return snap
}

func (e *Engine) buildSnapshotLocked() StatusSnapshot {
	paused, pauseReason := e.effectivePauseLocked()
	soloStatus := e.soloStatus
	if soloStatus == "" {
		soloStatus = SoloStatusIdle
	}
	soloModel := e.soloModelID
	if soloModel == "" && soloStatus == SoloStatusReady {
		soloModel = e.activeModelID
	}
	return StatusSnapshot{
		Currency:              string(e.currency),
		Language:              e.language,
		UIMode:                e.uiMode,
		PID:                   e.hostPID,
		OS:                    runtime.GOOS,
		Arch:                  runtime.GOARCH,
		MachineID:             e.machineID,
		GatewayURL:            e.gatewayURL,
		WorkerToken:           e.workerToken,
		HasWorkerToken:        strings.TrimSpace(e.workerToken) != "",
		BrowserAuthPending:    e.browserAuth != nil && time.Now().UTC().Before(e.browserAuth.expiresAt),
		WorkerName:            e.workerName,
		AccountEmail:          e.accountEmail,
		PreDownloadModelID:    e.preDownloadModelID,
		PreDownloadPct:        e.preDownloadPct,
		PreDownloadIndex:      e.preDownloadIndex,
		PreDownloadTotal:      e.preDownloadTotal,
		PoolID:                e.poolID,
		PoolRole:              e.poolRole,
		MeshRole:              e.meshRole,
		ExecutionMode:         e.executionMode,
		SoloEngineStatus:      soloStatus,
		SoloModelID:           soloModel,
		CachedModelSHA256s:    append([]string(nil), e.cachedModelSHA256s...),
		DiskFreeMiB:           e.diskFreeMiB,
		DiskFreeProbed:        e.diskFreeProbed,
		SupportedBackends:     e.effectiveBackendsLocked(),
		EnabledBackends:       append([]string(nil), e.enabledBackends...),
		OrderBookRank:         e.orderBookRank,
		RttMs:                 e.rttMs,
		ClearingPer1M:         e.clearingPer1M,
		ModelMode:             e.modelMode,
		SelectedModels:        append([]string(nil), e.selectedModels...),
		ActiveModelID:         e.activeModelID,
		WaitingReason:         e.waitingReason,
		MissingVramMiB:        e.missingVramMiB,
		CatalogModels:         append([]protocol.CatalogModelView(nil), e.catalogModels...),
		ScheduleMode:          e.scheduleMode,
		IdleThresholdSec:      e.idleThresholdSec,
		IdleStatus:            e.idleStatus,
		ManualPaused:          e.manualPaused,
		AutoPaused:            e.autoPaused,
		PauseDraining:         e.pauseDraining,
		Paused:                paused,
		PauseReason:           pauseReason,
		IdleSeconds:           e.idleSeconds,
		ReliabilityPct:        e.reliabilityPct,
		DetectedGPUs:          append([]protocol.GpuInfo(nil), e.detectedGPUs...),
		SelectedIDs:           append([]string(nil), e.selectedIDs...),
		SelectedVramMiB:       SelectedGPUsVRAM(e.detectedGPUs, e.selectedIDs),
		Running:               e.running,
		Status:                e.status,
		SessionState:          string(e.sessionState),
		AssignedLayers:        e.assignedLayers,
		TotalLayers:           e.totalLayers,
		SharePct:              e.sharePct,
		BlocksProcessed:       e.blocksProcessed,
		TokensProcessed:       e.tokensProcessed,
		ComputeUnitsProcessed: e.computeUnitsProcessed,
		Balance:               e.balance,
		LastError:             e.lastError,
		Logs:                  append([]string(nil), e.logs...),
	}
}

func (e *Engine) SetLanguage(lang string) string {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	resolved, err := nodecore.ParseWorkerLanguage(lang)
	if err != nil {
		e.mu.Lock()
		e.lastError = err.Error()
		cur := e.language
		e.mu.Unlock()
		e.notifyObservers()
		return cur
	}
	e.mu.Lock()
	e.language = resolved
	e.mu.Unlock()
	e.persistConfig()
	e.notifyObservers()
	return resolved
}

func (e *Engine) UpdateSelectedGPUs(ids []string) {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	var clean []string
	for _, id := range ids {
		if t := strings.TrimSpace(id); t != "" {
			clean = append(clean, t)
		}
	}
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.selectedIDs = clean
	e.mu.Unlock()
	e.persistConfig()
	e.notifyObservers()
}

func (e *Engine) UpdateModelPreferences(mode string, selectedModels []string) error {
	return e.ApplySettings(UpdateSettings{
		ModelMode:         mode,
		SelectedModels:    selectedModels,
		ModelSelectionSet: true,
	})
}

func (e *Engine) Start(params StartParams) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	return e.startLocked(params)
}

func (e *Engine) startLocked(params StartParams) error {
	e.mu.Lock()
	alreadyRunning := e.running
	e.mu.Unlock()
	if alreadyRunning {
		return nil
	}
	if err := e.applyStartParams(params); err != nil {
		return err
	}

	gwURL, tok, hasDetected, err := e.validatePreStartTokenAndGateway()
	if err != nil {
		return err
	}
	if e.hardware == nil {
		msg := "HardwareProbeError: hardware inspector is not configured"
		e.mu.Lock()
		e.status = StatusProbeError
		e.lastError = msg
		e.mu.Unlock()
		e.notifyObservers()
		return derr.New(derr.CodeNoVulkanGPU, 422, msg, nil)
	}
	if !hasDetected {
		if err := e.ScanGPUs(context.Background(), gwURL); err != nil {
			return err
		}
	}

	chosen, wName, err := e.prepareLaunchState()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	e.mu.Lock()
	e.runGen++
	gen := e.runGen
	e.loopDone = loopDone
	e.runCtx = ctx
	e.cancelRun = cancel
	e.activeGPUs = chosen
	e.running = true
	e.soloStatus = SoloStatusIdle
	e.soloModelID = ""
	e.status = "STARTING"
	e.mu.Unlock()

	e.persistConfig()
	e.notifyObservers()
	go e.workerLoop(ctx, gen, loopDone, gwURL, tok, wName, chosen)
	return nil
}

func (e *Engine) validatePreStartTokenAndGateway() (string, string, bool, error) {
	e.mu.Lock()
	if e.workerToken == "" {
		msg := "enter your Marketplace Worker Token (wk_...) before starting"
		e.lastError = msg
		e.mu.Unlock()
		e.notifyObservers()
		return "", "", false, derr.New(derr.CodeValidation, 400, msg, nil)
	}
	gwURL := e.gatewayURL
	tok := e.workerToken
	hasDetected := len(e.detectedGPUs) > 0
	e.mu.Unlock()
	return gwURL, tok, hasDetected, nil
}

func (e *Engine) applyStartParams(p StartParams) error {
	parsedMode, parsedModels, hasModelSel, err := e.validateStartModelAndSchedule(p)
	if err != nil {
		e.setLastError(err.Error())
		return err
	}
	var normGW string
	if strings.TrimSpace(p.GatewayURL) != "" {
		normGW, err = e.validateAndNormalizeGateway(p.GatewayURL)
		if err != nil {
			e.setLastError(err.Error())
			return err
		}
	}

	e.mu.Lock()
	e.applyStartIdentityFieldsLocked(p, normGW)
	e.applyStartRoutingAndScheduleLocked(p, parsedMode, parsedModels, hasModelSel)
	syncBackends := p.EnabledBackendsSet || len(p.EnabledBackends) > 0
	e.mu.Unlock()
	if syncBackends {
		e.syncSoloEnabledBackends()
	}
	return nil
}

func (e *Engine) applyStartRoutingAndScheduleLocked(
	p StartParams,
	parsedMode nodecore.ModelSelectionMode,
	parsedModels []string,
	hasModelSel bool,
) {
	if sched, err := nodecore.ParseIdleMode(p.ScheduleMode); err == nil && strings.TrimSpace(p.ScheduleMode) != "" {
		e.scheduleMode = string(sched)
		e.scheduleConfigured = true
	}
	if p.IdleThresholdSec > 0 {
		e.idleThresholdSec = ClampIdleThreshold(p.IdleThresholdSec)
	}
	if hasModelSel {
		e.modelMode = string(parsedMode)
		if parsedMode == nodecore.ModelModeMulti || len(parsedModels) > 0 {
			e.selectedModels = parsedModels
		}
	}
	if p.EnabledBackendsSet || len(p.EnabledBackends) > 0 {
		e.enabledBackendsSet = true
		e.enabledBackends = NormalizeBackendList(p.EnabledBackends)
	}
}

func (e *Engine) validateStartModelAndSchedule(p StartParams) (nodecore.ModelSelectionMode, []string, bool, error) {
	if strings.TrimSpace(p.ScheduleMode) != "" {
		if _, err := nodecore.ParseIdleMode(p.ScheduleMode); err != nil {
			return "", nil, false, err
		}
	}
	if !p.ModelSelectionSet && strings.TrimSpace(p.ModelMode) == "" {
		return "", nil, false, nil
	}
	modeRaw := p.ModelMode
	if strings.TrimSpace(modeRaw) == "" {
		modeRaw = protocol.ModelModeAuto
	}
	normMode, normModels, err := nodecore.ParseModelSelection(modeRaw, p.SelectedModels)
	if err != nil {
		return "", nil, false, err
	}
	return normMode, normModels, true, nil
}

func (e *Engine) applyStartIdentityFieldsLocked(p StartParams, normGW string) {
	if normGW != "" {
		e.gatewayURL = normGW
	}
	if nextTok := strings.TrimSpace(p.WorkerToken); nextTok != "" {
		if nextTok != e.workerToken {
			e.accountEmail = ""
		}
		e.workerToken = nextTok
	}
	if strings.TrimSpace(p.WorkerName) != "" {
		e.workerName = strings.TrimSpace(p.WorkerName)
	}
	if len(p.SelectedIDs) > 0 {
		e.selectedIDs = append([]string(nil), p.SelectedIDs...)
	}
}

func (e *Engine) prepareLaunchState() ([]protocol.GpuInfo, string, error) {
	e.mu.Lock()
	if strings.TrimSpace(e.machineID) == "" {
		msg := "HardwareProbeError: unable to determine deterministic machine fingerprint"
		e.status = StatusProbeError
		e.lastError = msg
		e.mu.Unlock()
		e.notifyObservers()
		return nil, "", derr.New(derr.CodeNoVulkanGPU, 422, msg, nil)
	}
	if len(e.selectedIDs) == 0 {
		msg := "select at least one GPU before starting"
		e.lastError = msg
		e.mu.Unlock()
		e.notifyObservers()
		return nil, "", derr.New(derr.CodeValidation, 400, msg, nil)
	}
	chosen := selectMatchingGPUs(e.detectedGPUs, e.selectedIDs)
	if len(chosen) == 0 {
		msg := "none of the selected GPUs were detected on this machine"
		e.status = StatusProbeError
		e.lastError = msg
		e.mu.Unlock()
		e.notifyObservers()
		return nil, "", derr.New(derr.CodeNoVulkanGPU, 422, msg, nil)
	}

	e.manualPaused = false
	e.autoPaused = false
	e.pauseDraining = false
	e.poolID = ""
	e.poolRole = ""
	e.meshRole = ""
	e.executionMode = ""
	e.soloStatus = SoloStatusIdle
	e.soloModelID = ""
	e.preDownloadModelID = ""
	e.preDownloadPct = 0
	e.preDownloadIndex = 0
	e.preDownloadTotal = 0
	e.activeModelID = ""
	e.waitingReason = ""
	e.missingVramMiB = 0
	e.orderBookRank = 0
	e.lastError = ""
	authCancel := e.clearPendingBrowserAuthLocked()
	wName := e.workerName
	e.mu.Unlock()
	cancelIfNonNil(authCancel)
	return chosen, wName, nil
}

func selectMatchingGPUs(detected []protocol.GpuInfo, selectedIDs []string) []protocol.GpuInfo {
	var chosen []protocol.GpuInfo
	for _, id := range selectedIDs {
		for _, d := range detected {
			if strings.EqualFold(d.DeviceID, id) {
				chosen = append(chosen, d)
				break
			}
		}
	}
	return chosen
}

func (e *Engine) Stop(reasonLog string) {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()

	e.mu.Lock()
	e.runGen++
	loopDone := e.loopDone
	e.loopDone = nil
	cancel, drainCancel, soloCancel, meshPeerCancel, ts, wasRunning := e.detachActiveRunLocked()
	e.resetStoppedFieldsLocked()
	e.mu.Unlock()

	cancelIfNonNil(cancel)
	cancelIfNonNil(drainCancel)
	cancelIfNonNil(soloCancel)
	cancelIfNonNil(meshPeerCancel)
	if ts != nil {
		ts.closeAll()
	}
	e.stopMeshPeerModel("engine_stop")
	e.stopSoloEngine("engine_stop")
	e.stopLocalRPCServer()
	if loopDone != nil {
		<-loopDone
	}
	e.notifyObservers()
	if wasRunning && reasonLog != "" {
		e.addLog("%s", reasonLog)
	}
}

func (e *Engine) abortLoopFromWorker(gen uint64, lastErr, reasonLog string) {
	e.mu.Lock()
	if e.runGen != gen {
		e.mu.Unlock()
		return
	}
	e.runGen++
	e.loopDone = nil
	cancel, drainCancel, soloCancel, meshPeerCancel, ts, wasRunning := e.detachActiveRunLocked()
	if lastErr != "" {
		e.lastError = lastErr
	}
	e.resetStoppedFieldsLocked()
	e.mu.Unlock()

	cancelIfNonNil(cancel)
	cancelIfNonNil(drainCancel)
	cancelIfNonNil(soloCancel)
	cancelIfNonNil(meshPeerCancel)
	if ts != nil {
		ts.closeAll()
	}
	e.stopMeshPeerModel("loop_abort")
	e.stopSoloEngine("loop_abort")
	e.stopLocalRPCServer()
	e.notifyObservers()
	if wasRunning && reasonLog != "" {
		e.addLog("%s", reasonLog)
	}
}

func (e *Engine) detachActiveRunLocked() (context.CancelFunc, context.CancelFunc, context.CancelFunc, context.CancelFunc, *tunnelStreams, bool) {
	cancel := e.cancelRun
	drainCancel := e.drainCancel
	soloCancel := e.soloCancel
	meshPeerCancel := e.meshPeerCancel
	ts := e.activeStreams
	wasRunning := e.running
	e.runCtx = nil
	e.cancelRun = nil
	e.drainCancel = nil
	e.soloCancel = nil
	e.meshPeerCancel = nil
	e.rpcGen++
	e.soloGen++
	e.meshPeerGen++
	e.drainGen++
	e.activeSendJSON = nil
	e.activeStreams = nil
	e.runtimeDir = ""
	e.localPort = 0
	e.activeGPUs = nil
	return cancel, drainCancel, soloCancel, meshPeerCancel, ts, wasRunning
}

func cancelIfNonNil(cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) Shutdown() {
	e.CancelBrowserLogin()
	e.Stop("Worker daemon shutting down.")
}

func (e *Engine) resetStoppedFieldsLocked() {
	e.running = false
	e.manualPaused = false
	e.autoPaused = false
	e.pauseDraining = false
	e.status = "STOPPED"
	e.advanceSessionStateLocked(nodecore.WorkerClosed)
	e.poolID = ""
	e.poolRole = ""
	e.meshRole = ""
	e.executionMode = ""
	e.soloStatus = SoloStatusIdle
	e.soloModelID = ""
	e.preDownloadModelID = ""
	e.preDownloadPct = 0
	e.preDownloadIndex = 0
	e.preDownloadTotal = 0
	e.activeModelID = ""
	e.waitingReason = ""
	e.missingVramMiB = 0
	e.orderBookRank = 0
	e.assignedLayers = 0
	e.sharePct = 0
}

func (e *Engine) resetWorkerStatusAfterSoloLocked() {
	if !e.running {
		return
	}
	if e.pauseDraining {
		e.status = StatusPauseDraining
		return
	}
	if paused, _ := e.effectivePauseLocked(); paused {
		e.status = "PAUSED"
		return
	}
	if e.activeSendJSON != nil {
		e.status = "CONNECTED"
		return
	}
	e.status = "STARTING"
}

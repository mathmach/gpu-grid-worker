package worker

import (
	"cmp"
	"context"
	"encoding/json"
	"math/rand/v2"
	"runtime"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func ComputeReconnectBackoff(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = DefaultReconnectDelay
	}
	shift := min(max(0, attempt), 10)
	raw := base << shift
	if raw <= 0 || raw > MaxReconnectDelay {
		raw = MaxReconnectDelay
	}
	maxJitter := int64(raw) / 4
	if maxJitter <= 0 {
		return raw
	}
	return raw + time.Duration(rand.Int64N(maxJitter+1))
}

func (e *Engine) workerLoop(
	ctx context.Context,
	gen uint64,
	loopDone chan struct{},
	gwHTTP, workerToken, workerName string,
	gpus []protocol.GpuInfo,
) {
	defer close(loopDone)
	go e.runIdleMonitor(ctx)

	_, localPort, ok := e.bootstrapWorkerRuntimeAndPort(ctx, gen, gwHTTP)
	if !ok {
		return
	}
	if !e.ensureSelectedModelsPreDownloaded(ctx, gen, gwHTTP, workerToken) {
		return
	}

	wsURL, err := e.resolveWorkerWSURL(gwHTTP)
	if err != nil {
		e.abortLoopFromWorker(gen, err.Error(), "Invalid Gateway WebSocket URL: "+err.Error())
		return
	}

	attempt := 0
	for ctx.Err() == nil {
		rejected, receivedFrames, sessErr := e.serveGatewaySession(ctx, wsURL, workerToken, workerName, gpus, localPort)
		if rejected != "" {
			e.abortLoopFromWorker(gen, rejected, "Connection rejected by Gateway: "+rejected)
			return
		}
		if ctx.Err() != nil {
			return
		}
		if receivedFrames {
			attempt = 0
		}
		delay := ComputeReconnectBackoff(e.reconnectDelay, attempt)
		attempt++
		e.markReconnecting(sessErr, delay)
		if !sleepWithContext(ctx, delay) {
			return
		}
		localPort = e.currentLocalPort()
	}
}

func (e *Engine) resolveWorkerWSURL(gwHTTP string) (string, error) {
	if e.gatewayClient != nil {
		return e.gatewayClient.BuildWorkerWSURL(gwHTTP, e.allowInsecureWS)
	}
	norm, err := nodecore.NormalizeGatewayURL(gwHTTP)
	if err != nil {
		return "", err
	}
	ws := strings.Replace(norm, "https://", "wss://", 1)
	ws = strings.Replace(ws, "http://", "ws://", 1)
	return ws + "/ws/worker", nil
}

func (e *Engine) bootstrapWorkerRuntimeAndPort(
	ctx context.Context,
	gen uint64,
	gwHTTP string,
) (string, int, bool) {
	runtimeDir, err := e.EnsureRuntime(ctx, gwHTTP)
	if err != nil {
		if ctx.Err() != nil {
			e.abortLoopFromWorker(gen, "", "")
			return "", 0, false
		}
		e.abortLoopFromWorker(gen, err.Error(), "Failed to prepare Vulkan runtime.")
		return "", 0, false
	}
	localPort, err := e.allocateLoopbackPort()
	if err != nil {
		if ctx.Err() != nil {
			e.abortLoopFromWorker(gen, "", "")
			return "", 0, false
		}
		e.abortLoopFromWorker(gen, err.Error(), "Failed to allocate local loopback port.")
		return "", 0, false
	}

	idleNow := e.sampleIdleSeconds()
	e.mu.Lock()
	e.runtimeDir = runtimeDir
	e.localPort = localPort
	e.idleSeconds = idleNow
	logUnavail := e.updateIdleStatusLocked(idleNow)
	e.setInitialAutoPauseFromIdleLocked(idleNow)
	initiallyPaused, _ := e.effectivePauseLocked()
	e.mu.Unlock()

	if logUnavail {
		e.logIdleBackendUnavailable()
	}
	if initiallyPaused {
		e.addLog("Starting in PAUSED standby (0 MiB GPU VRAM reserved until idle threshold).")
	}
	return runtimeDir, localPort, true
}

func (e *Engine) markReconnecting(sessErr error, delay time.Duration) {
	e.mu.Lock()
	e.priorBlocks += max(e.sessStateBlocks, e.sessCreditBlocks)
	e.priorTokens += max(e.sessStateTokens, e.sessCreditTokens)
	e.priorComputeUnits += max(e.sessStateComputeUnits, e.sessCreditComputeUnits)
	e.sessStateBlocks = 0
	e.sessStateTokens = 0
	e.sessStateComputeUnits = 0
	e.sessCreditBlocks = 0
	e.sessCreditTokens = 0
	e.sessCreditComputeUnits = 0
	e.blocksProcessed = e.priorBlocks
	e.tokensProcessed = e.priorTokens
	e.computeUnitsProcessed = e.priorComputeUnits
	e.status = "RECONNECTING"
	e.advanceSessionStateLocked(nodecore.WorkerClosed)
	e.poolID = ""
	e.meshRole = ""
	e.activeModelID = ""
	e.executionMode = ""
	e.soloStatus = SoloStatusIdle
	e.soloModelID = ""
	e.waitingReason = ""
	e.missingVramMiB = 0
	e.orderBookRank = 0
	e.assignedLayers = 0
	e.mu.Unlock()

	e.addLog("Gateway connection lost (%v). Reconnecting in %s...", sessErr, delay.Round(time.Millisecond))
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (e *Engine) serveGatewaySession(ctx context.Context, wsURL, workerToken, workerName string, gpus []protocol.GpuInfo, localPort int) (string, bool, error) {
	sock, err := e.tunnel.Dial(ctx, wsURL)
	if err != nil {
		return "", false, err
	}
	sendJSON := func(msg protocol.ControlMsg) error {
		return sock.SendJSON(msg, 15*time.Second)
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)
	ts := newTunnelStreams(e, localPort, sock, sendJSON)
	e.refreshDiskAndCacheInventory()
	helloMsg := e.prepareSessionHello(workerToken, workerName, gpus, sendJSON, ts)

	cleanupDone := make(chan struct{})
	go closeSocketOnCancel(sessionCtx, cleanupDone, sock)
	defer e.teardownSession(cancelSession, cleanupDone, sock, ts)

	if !helloMsg.Paused {
		if err := e.ensureConnectedIdleRPC(sessionCtx); err != nil {
			return "", false, err
		}
	}

	e.logSessionConnected(wsURL, helloMsg)
	if err := sendJSON(helloMsg); err != nil {
		return "", false, err
	}
	return e.readGatewayFramesLoop(sessionCtx, sock, ts, sendJSON)
}

func closeSocketOnCancel(ctx context.Context, cleanupDone <-chan struct{}, sock TunnelSocket) {
	select {
	case <-ctx.Done():
		if err := sock.Close(); err != nil {
			return
		}
	case <-cleanupDone:
	}
}

func (e *Engine) teardownSession(cancelSession context.CancelFunc, cleanupDone chan struct{}, sock TunnelSocket, ts *tunnelStreams) {
	cancelSession()
	close(cleanupDone)
	e.mu.Lock()
	e.activeSendJSON = nil
	if e.activeStreams == ts {
		e.activeStreams = nil
	}
	e.advanceSessionStateLocked(nodecore.WorkerClosed)
	e.mu.Unlock()
	if err := sock.Close(); err != nil {
		e.logger.Debug("worker_ws_close_error", "error", err.Error())
	}
	ts.closeAll()
	e.stopMeshPeerModel("session_disconnect")
	e.stopSoloEngine("session_disconnect")
	e.stopLocalRPCServer()
}

func (e *Engine) prepareSessionHello(workerToken, workerName string, gpus []protocol.GpuInfo, sendJSON func(protocol.ControlMsg) error, ts *tunnelStreams) protocol.ControlMsg {
	idleNow := e.sampleIdleSeconds()
	e.mu.Lock()
	defer e.mu.Unlock()

	e.idleSeconds = idleNow
	e.updateIdleStatusLocked(idleNow)
	e.setInitialAutoPauseFromIdleLocked(idleNow)
	e.activeSendJSON = sendJSON
	e.activeStreams = ts
	e.sessStateBlocks = 0
	e.sessStateTokens = 0
	e.sessStateComputeUnits = 0
	e.sessCreditBlocks = 0
	e.sessCreditTokens = 0
	e.sessCreditComputeUnits = 0
	e.lastWorkerStateSeq = 0
	e.lastStateSeq = 0
	e.minObservedBalance = 0
	e.advanceSessionStateLocked(nodecore.WorkerConnectedUnauth)

	paused, pauseReason := e.effectivePauseLocked()
	if paused {
		e.status = "PAUSED"
		e.advanceSessionStateLocked(nodecore.WorkerPaused)
	} else {
		e.status = "CONNECTED"
		e.advanceSessionStateLocked(nodecore.WorkerIdle)
	}
	modelMode := e.modelMode
	selectedModels := append([]string(nil), e.selectedModels...)
	if normMode, normModels, err := nodecore.ParseModelSelection(e.modelMode, e.selectedModels); err == nil {
		modelMode = string(normMode)
		selectedModels = normModels
	}
	backends := e.effectiveBackendsLocked()

	return protocol.ControlMsg{
		Type:               protocol.ControlHello,
		MachineID:          e.machineID,
		WorkerToken:        workerToken,
		OS:                 runtime.GOOS,
		Name:               workerName,
		GPUs:               gpus,
		SupportedBackends:  backends,
		CachedModelSHA256s: append([]string(nil), e.cachedModelSHA256s...),
		DiskFreeMiB:        e.diskFreeMiB,
		DiskFreeProbed:     e.diskFreeProbed,
		ModelMode:          modelMode,
		SelectedModels:     selectedModels,
		ScheduleMode:       e.scheduleMode,
		Paused:             paused,
		PauseReason:        pauseReason,
	}
}

func (e *Engine) logSessionConnected(wsURL string, hello protocol.ControlMsg) {
	e.addLog("Connected to Gateway (%s) | Machine %s | Models: %s | Backends: %v",
		wsURL, e.machineID, strings.ToUpper(hello.ModelMode), hello.SupportedBackends)
}

func (e *Engine) readGatewayFramesLoop(sessionCtx context.Context, sock TunnelSocket, ts *tunnelStreams, sendJSON func(protocol.ControlMsg) error) (string, bool, error) {
	receivedFrames := false
	for {
		isBinary, data, err := sock.ReadFrame()
		if err != nil {
			return "", receivedFrames, err
		}
		receivedFrames = true
		if isBinary {
			ts.forwardInbound(data)
			continue
		}
		var ctrl protocol.ControlMsg
		if err := json.Unmarshal(data, &ctrl); err != nil {
			e.logger.Debug("worker_ws_frame_parse_failed", "error", err.Error())
			continue
		}
		if rejected := e.handleControlFrame(sessionCtx, ctrl, ts, sendJSON); rejected != "" {
			return rejected, receivedFrames, nil
		}
	}
}

func (e *Engine) handleControlFrame(sessionCtx context.Context, ctrl protocol.ControlMsg, ts *tunnelStreams, sendJSON func(protocol.ControlMsg) error) string {
	switch ctrl.Type {
	case protocol.ControlRejected:
		return ctrl.Reason
	case protocol.ControlTunnelOpen:
		ts.openStream(protocol.ExtractTraceParent(sessionCtx, ctrl), ctrl.StreamID, ctrl.TunnelTarget)
	case protocol.ControlTunnelClose:
		ts.closeStream(ctrl.StreamID, ctrl.Reason)
	case protocol.ControlConfigureSoloEngine:
		e.startConfigureSoloEngine(sessionCtx, ctrl, sendJSON)
	case protocol.ControlStopSoloEngine:
		e.stopSoloEngineAsync("gateway_stop_solo_engine")
	case protocol.ControlEnsureMeshModel:
		e.startEnsureMeshPeerModel(sessionCtx, ctrl, sendJSON)
	case protocol.ControlStopMeshPeer:
		e.stopMeshPeerModelAsync("gateway_stop_mesh_peer")
	case protocol.ControlState:
		e.applyGatewayState(ctrl)
	case protocol.ControlJobCredit:
		e.applyJobCredit(ctrl)
	}
	return ""
}

func (e *Engine) applyGatewayState(ctrl protocol.ControlMsg) {
	cfgChanged := false
	e.mu.Lock()
	staleView := ctrl.StateSeq > 0 && ctrl.StateSeq < e.lastWorkerStateSeq
	if !staleView {
		if ctrl.StateSeq > 0 {
			e.lastWorkerStateSeq = ctrl.StateSeq
		}
		e.poolID = ctrl.PoolID
		if ctrl.AccountEmail != "" && ctrl.AccountEmail != e.accountEmail {
			e.accountEmail = ctrl.AccountEmail
			cfgChanged = true
		}
		if parsed, err := nodecore.ParseDisplayCurrency(ctrl.Currency); err == nil && parsed != e.currency {
			e.currency = parsed
			cfgChanged = true
		}
		if ctrl.Worker != nil {
			e.applyWorkerStateViewLocked(ctrl.StateSeq, ctrl.ExecutionMode, ctrl.Worker)
		}
	} else if ctrl.Worker != nil {
		e.applyGatewayBalanceLocked(ctrl.StateSeq, ctrl.Worker.Balance)
	}
	e.mu.Unlock()
	if cfgChanged {
		e.persistConfig()
	}
	e.notifyObservers()
}

func (e *Engine) applyWorkerStateViewLocked(stateSeq uint64, msgExecMode string, w *protocol.WorkerView) {
	if !e.pauseDraining {
		e.status = w.Status
	}
	e.poolRole = w.PoolRole
	e.meshRole = w.MeshRole
	e.activeModelID = w.ModelID
	e.waitingReason = w.WaitingReason
	e.missingVramMiB = w.MissingVramMiB
	e.orderBookRank = w.OrderBookRank
	e.rttMs = w.RttMs
	e.clearingPer1M = w.ClearingPer1M
	e.assignedLayers = w.AssignedLayers
	e.totalLayers = w.TotalLayers
	e.sharePct = w.SharePct
	stateCU := w.ComputeUnitsProcessed
	if stateCU <= 0 && w.TokensProcessed > 0 {
		stateCU = w.TokensProcessed
	}
	e.sessStateBlocks = w.BlocksProcessed
	e.sessStateTokens = w.TokensProcessed
	e.sessStateComputeUnits = stateCU
	e.blocksProcessed = e.priorBlocks + max(e.sessStateBlocks, e.sessCreditBlocks)
	e.tokensProcessed = e.priorTokens + max(e.sessStateTokens, e.sessCreditTokens)
	e.computeUnitsProcessed = e.priorComputeUnits + max(e.sessStateComputeUnits, e.sessCreditComputeUnits)
	e.applyExecutionModeLocked(msgExecMode, w)
	e.applyGatewayBalanceLocked(stateSeq, w.Balance)
	e.syncSessionStateFromGatewayLocked(w)
	if w.ReliabilityPct > 0 {
		e.reliabilityPct = w.ReliabilityPct
	}
}

func (e *Engine) syncSessionStateFromGatewayLocked(w *protocol.WorkerView) {
	if e.pauseDraining {
		e.advanceSessionStateLocked(nodecore.WorkerPauseDraining)
		return
	}
	paused, _ := e.effectivePauseLocked()
	if paused {
		e.advanceSessionStateLocked(nodecore.WorkerPaused)
		return
	}
	if strings.EqualFold(w.Status, string(nodecore.WorkerQuarantined)) {
		e.advanceSessionStateLocked(nodecore.WorkerQuarantined)
		return
	}
	if w.PoolID != "" || e.poolID != "" {
		e.advanceSessionStateLocked(nodecore.WorkerInPool)
		return
	}
	e.advanceSessionStateLocked(nodecore.WorkerIdle)
}

func (e *Engine) applyExecutionModeLocked(msgExecMode string, w *protocol.WorkerView) {
	switch {
	case w.ExecutionMode != "":
		e.executionMode = w.ExecutionMode
	case msgExecMode != "":
		e.executionMode = msgExecMode
	case e.soloPort > 0:
		e.executionMode = protocol.ExecutionModeSolo
	case w.PoolID != "":
		e.executionMode = protocol.ExecutionModeMesh
	default:
		e.executionMode = ""
	}
}

func (e *Engine) applyGatewayBalanceLocked(stateSeq uint64, workerBalance float64) {
	if stateSeq > 0 {
		if stateSeq >= e.lastStateSeq {
			e.lastStateSeq = stateSeq
			e.balance = workerBalance
		}
		return
	}
	if e.lastStateSeq == 0 && workerBalance >= e.balance && workerBalance >= e.minObservedBalance {
		e.balance = workerBalance
	}
}

func (e *Engine) applyJobCredit(ctrl protocol.ControlMsg) {
	e.mu.Lock()
	if parsed, err := nodecore.ParseDisplayCurrency(ctrl.Currency); err == nil {
		e.currency = parsed
	}
	if ctrl.StateSeq > 0 {
		if ctrl.StateSeq >= e.lastStateSeq {
			e.lastStateSeq = ctrl.StateSeq
			e.balance = ctrl.Balance
		}
	} else if e.lastStateSeq == 0 {
		e.balance = ctrl.Balance
		if ctrl.Balance > e.minObservedBalance {
			e.minObservedBalance = ctrl.Balance
		}
	}
	creditCU := ctrl.ComputeUnits
	if creditCU <= 0 && ctrl.Tokens > 0 {
		creditCU = int64(ctrl.Tokens)
	}
	e.sessCreditBlocks += int64(ctrl.Blocks)
	e.sessCreditTokens += int64(ctrl.Tokens)
	e.sessCreditComputeUnits += creditCU
	e.blocksProcessed = e.priorBlocks + max(e.sessStateBlocks, e.sessCreditBlocks)
	e.tokensProcessed = e.priorTokens + max(e.sessStateTokens, e.sessCreditTokens)
	e.computeUnitsProcessed = e.priorComputeUnits + max(e.sessStateComputeUnits, e.sessCreditComputeUnits)
	if ctrl.ClearingPer1M > 0 {
		e.clearingPer1M = ctrl.ClearingPer1M
	}
	pLabel := cmp.Or(ctrl.PoolID, e.poolID)
	e.mu.Unlock()

	e.addLog("[%s] %d CU (%d blocks @ Clearing %.3f Cr/1M) -> +%.6f Cr (Balance: %.6f Cr)",
		pLabel, creditCU, ctrl.Blocks, ctrl.ClearingPer1M, ctrl.Earned, ctrl.Balance)
}

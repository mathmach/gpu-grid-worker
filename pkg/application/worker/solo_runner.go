package worker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const (
	soloHealthPollInterval = 50 * time.Millisecond
	soloHealthBootTimeout  = 90 * time.Second
)

func llamaServerBinaryName() string {
	if runtime.GOOS == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}

func (e *Engine) startConfigureSoloEngine(sessionCtx context.Context, ctrl protocol.ControlMsg, sendJSON func(protocol.ControlMsg) error) {
	soloCtx, cancel := context.WithCancel(sessionCtx)
	e.mu.Lock()
	prevCancel := e.soloCancel
	e.soloGen++
	gen := e.soloGen
	e.soloCancel = cancel
	e.mu.Unlock()

	cancelIfNonNil(prevCancel)
	go e.runConfigureSolo(soloCtx, gen, ctrl, sendJSON)
}

func (e *Engine) runConfigureSolo(ctx context.Context, gen uint64, ctrl protocol.ControlMsg, sendJSON func(protocol.ControlMsg) error) {
	e.stopDetachedSoloProcess()
	targetBackend := resolveTargetBackend(ctrl)
	if !e.setSoloStageIfCurrent(gen, ctrl.ModelID, SoloStatusPulling, StatusDownloading) {
		return
	}
	if err := e.validateSoloConfigureInputs(&ctrl); err != nil {
		e.reportSoloFailure(ctx, gen, ctrl, targetBackend, sendJSON, err)
		return
	}

	packRuntimeDir, err := e.ensureBackendRuntimePack(ctx, targetBackend)
	if err != nil {
		e.reportSoloFailure(ctx, gen, ctrl, targetBackend, sendJSON, err)
		return
	}

	modelPath, digest, compPaths, err := e.pullVerifiedSoloBundle(ctx, ctrl)
	digests, freeMiB, freeProbed := e.refreshDiskAndCacheInventory()
	if err != nil {
		e.reportSoloFailure(ctx, gen, ctrl, targetBackend, sendJSON, err)
		return
	}
	if !e.setSoloStageIfCurrent(gen, ctrl.ModelID, SoloStatusStarting, SoloStatusStarting) {
		return
	}

	soloPort, proc, relayStops, err := e.bootAndProbeSoloProcess(ctx, ctrl, modelPath, digest, targetBackend, packRuntimeDir, compPaths)
	if err != nil {
		e.reportSoloFailure(ctx, gen, ctrl, targetBackend, sendJSON, err)
		return
	}

	execMode := resolveSoloOrMeshExecMode(ctrl.ExecutionMode)
	if !e.activateSoloProcess(gen, ctrl.ModelID, soloPort, proc, relayStops, execMode) {
		invokeRelayStops(relayStops)
		proc.Stop()
		return
	}

	e.emitSoloReady(ctrl, digest, targetBackend, execMode, soloPort, digests, freeMiB, freeProbed, sendJSON)
	go e.watchSoloProcessExit(ctx, gen, ctrl, targetBackend, proc, sendJSON)
}

func (e *Engine) validateSoloConfigureInputs(ctrl *protocol.ControlMsg) error {
	if strings.TrimSpace(ctrl.Task) != "" {
		parsedTask, err := nodecore.ParseModelTask(ctrl.Task)
		if err != nil {
			return err
		}
		ctrl.Task = string(parsedTask)
	}
	return e.validateModelControlURLs(*ctrl)
}

func (e *Engine) setSoloStageIfCurrent(gen uint64, modelID, soloStage, workerStatus string) bool {
	e.mu.Lock()
	paused, _ := e.effectivePauseLocked()
	valid := e.running && !paused && !e.pauseDraining && e.soloGen == gen
	if valid {
		e.soloStatus = soloStage
		e.soloModelID = modelID
		if workerStatus != "" {
			e.status = workerStatus
		}
	}
	e.mu.Unlock()
	if valid {
		e.notifyObservers()
	}
	return valid
}

func resolveSoloOrMeshExecMode(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), protocol.ExecutionModeMesh) {
		return protocol.ExecutionModeMesh
	}
	return protocol.ExecutionModeSolo
}

func (e *Engine) watchSoloProcessExit(
	ctx context.Context,
	gen uint64,
	ctrl protocol.ControlMsg,
	targetBackend string,
	proc RunningRPCProcess,
	sendJSON func(protocol.ControlMsg) error,
) {
	if proc == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-proc.Done():
	}
	if ctx.Err() != nil {
		return
	}
	const crashReason = "solo engine process exited unexpectedly"
	ts, current := e.detachCrashedSoloProcess(gen, proc, crashReason)
	if !current {
		return
	}
	e.logger.Error("worker_solo_process_crashed", "pool_id", ctrl.PoolID, "model_id", ctrl.ModelID, "backend", targetBackend)
	if ts != nil {
		ts.closeTargetStreams(protocol.TunnelTargetSoloHTTP)
		ts.closeTargetStreams(protocol.TunnelTargetMeshRelay)
	}
	e.restoreIdleRPCAfterSolo()
	e.notifyObservers()
	if sendJSON != nil {
		if err := sendJSON(protocol.ControlMsg{
			Type:          protocol.ControlSoloEngineError,
			PoolID:        ctrl.PoolID,
			ModelID:       ctrl.ModelID,
			TargetBackend: targetBackend,
			Reason:        crashReason,
		}); err != nil {
			e.logger.Warn("solo_engine_crash_report_failed", "pool_id", ctrl.PoolID, "error", err.Error())
		}
	}
	e.addLog("SOLO engine exited unexpectedly for %s (%s)", ctrl.ModelID, targetBackend)
}

func (e *Engine) detachCrashedSoloProcess(gen uint64, proc RunningRPCProcess, reason string) (*tunnelStreams, bool) {
	e.mu.Lock()
	if !e.running || e.soloGen != gen || e.soloProc != proc {
		e.mu.Unlock()
		return nil, false
	}
	stops := e.relayStops
	e.relayStops = nil
	e.soloProc = nil
	e.soloPort = 0
	e.soloStatus = SoloStatusError
	e.lastError = reason
	if e.executionMode == protocol.ExecutionModeSolo || e.executionMode == protocol.ExecutionModeMesh {
		e.executionMode = ""
	}
	e.resetWorkerStatusAfterSoloLocked()
	ts := e.activeStreams
	e.mu.Unlock()

	invokeRelayStops(stops)
	return ts, true
}

func (e *Engine) emitSoloReady(
	ctrl protocol.ControlMsg,
	digest, targetBackend, execMode string,
	soloPort int,
	digests []string,
	freeMiB int,
	freeProbed bool,
	sendJSON func(protocol.ControlMsg) error,
) {
	if sendJSON != nil {
		if sErr := sendJSON(protocol.ControlMsg{
			Type:               protocol.ControlSoloEngineReady,
			PoolID:             ctrl.PoolID,
			ModelID:            ctrl.ModelID,
			ModelSHA256:        digest,
			TargetBackend:      targetBackend,
			ExecutionMode:      execMode,
			CachedModelSHA256s: digests,
			DiskFreeMiB:        freeMiB,
			DiskFreeProbed:     freeProbed,
		}); sErr != nil {
			e.logger.Warn("solo_engine_ready_send_failed", "pool_id", ctrl.PoolID, "error", sErr.Error())
		}
	}
	e.addLog("%s engine ready on 127.0.0.1:%d for model %s (%s)", execMode, soloPort, ctrl.ModelID, targetBackend)
}

func resolveTargetBackend(ctrl protocol.ControlMsg) string {
	if parsed, err := nodecore.ParseEngineBackend(ctrl.TargetBackend); err == nil {
		return parsed.String()
	}
	return string(nodecore.BackendLlamaLocal)
}

func (e *Engine) pullVerifiedSoloModel(ctx context.Context, ctrl protocol.ControlMsg) (string, string, error) {
	if e.modelCache == nil {
		return "", "", derr.New(derr.CodeUpstreamFailure, 503, "model cache verifier is not configured", nil)
	}
	_, _, baseURL, _, dataDir := e.snapshotDownloadPolicy()
	fileName := resolveModelCacheFileName(ctrl)
	rawURL := strings.TrimSpace(ctrl.ModelDownloadURL)
	if rawURL == "" {
		rawURL = protocol.BuildModelDownloadURL(baseURL, fileName)
	}
	downloadURL, err := e.validateDownloadURL(rawURL)
	if err != nil {
		return "", "", err
	}
	return e.modelCache.EnsureModelCached(ctx, dataDir, ctrl.ModelID, fileName, downloadURL, ctrl.ModelSHA256, nil)
}

func resolveModelCacheFileName(ctrl protocol.ControlMsg) string {
	if strings.TrimSpace(ctrl.ModelFileName) != "" {
		return ctrl.ModelFileName
	}
	if parsed, err := url.Parse(strings.TrimSpace(ctrl.ModelDownloadURL)); err == nil {
		base := path.Base(parsed.Path)
		if base != "" && base != "." && base != "/" {
			return base
		}
	}
	if strings.TrimSpace(ctrl.ModelID) != "" {
		ext := ".gguf"
		if strings.TrimSpace(ctrl.ModelFormat) != "" && !strings.EqualFold(ctrl.ModelFormat, string(nodecore.FormatGGUF)) {
			ext = "." + strings.ToLower(strings.TrimSpace(ctrl.ModelFormat))
		}
		return strings.TrimSpace(ctrl.ModelID) + ext
	}
	return ""
}

func (e *Engine) bootAndProbeSoloProcess(
	ctx context.Context,
	ctrl protocol.ControlMsg,
	modelPath, digest, targetBackend, packRuntimeDir string,
	compPaths map[string]string,
) (int, RunningRPCProcess, []func(), error) {
	if e.solo == nil {
		return 0, nil, nil, derr.New(derr.CodeUpstreamFailure, 503, "solo engine supervisor is not configured", nil)
	}
	soloPort, err := e.allocateLoopbackPort()
	if err != nil {
		return 0, nil, nil, err
	}

	e.mu.Lock()
	runtimeDir := packRuntimeDir
	if runtimeDir == "" {
		runtimeDir = e.runtimeDir
	}
	baseRuntimeDir := e.runtimeDir
	localRPCPort := e.localPort
	gpus := append([]protocol.GpuInfo(nil), e.activeGPUs...)
	e.mu.Unlock()

	execMode := resolveSoloOrMeshExecMode(ctrl.ExecutionMode)
	spec := SoloEngineSpec{
		PoolID:         ctrl.PoolID,
		ExecutionMode:  execMode,
		ModelID:        ctrl.ModelID,
		ModelPath:      modelPath,
		ModelSHA256:    digest,
		ModelFormat:    ctrl.ModelFormat,
		Task:           ctrl.Task,
		TargetBackend:  targetBackend,
		RuntimeDir:     runtimeDir,
		ListenPort:     soloPort,
		ContextLength:  ctrl.ContextLength,
		ParallelSlots:  max(1, ctrl.ParallelSlots),
		PoolVRAMMiB:    ctrl.VramTotalMiB,
		TensorWeights:  append([]int(nil), ctrl.TensorWeights...),
		GPUs:           gpus,
		ComponentPaths: compPaths,
	}

	relayStops, mErr := e.prepareSoloOrLeaderMeshRPC(ctx, ctrl, execMode, baseRuntimeDir, localRPCPort, gpus, &spec)
	if mErr != nil {
		return 0, nil, nil, mErr
	}

	proc, err := e.solo.SpawnSolo(ctx, spec)
	if err != nil {
		invokeRelayStops(relayStops)
		return 0, nil, nil, err
	}
	if bp, ok := proc.(nodecore.BoundPortProcess); ok && bp.BoundPort() > 0 {
		soloPort = bp.BoundPort()
	}
	if err := e.probeSoloHealthUntilReady(ctx, soloPort, proc); err != nil {
		invokeRelayStops(relayStops)
		proc.Stop()
		return 0, nil, nil, err
	}
	return soloPort, proc, relayStops, nil
}

func (e *Engine) prepareSoloOrLeaderMeshRPC(
	ctx context.Context,
	ctrl protocol.ControlMsg,
	execMode, baseRuntimeDir string,
	localRPCPort int,
	gpus []protocol.GpuInfo,
	spec *SoloEngineSpec,
) ([]func(), error) {
	if execMode == protocol.ExecutionModeMesh && len(ctrl.MeshPeers) > 0 {
		if baseRuntimeDir != "" && localRPCPort > 0 && len(gpus) > 0 {
			e.ensureLocalRPCAlive(ctx, baseRuntimeDir, gpus, localRPCPort)
			localRPCPort = e.currentLocalPort()
		}
		rpcEndpoints, stops, totalGPUs, mErr := e.prepareLeaderMeshSpec(ctrl.PoolID, localRPCPort, len(gpus), ctrl.MeshPeers)
		if mErr != nil {
			return nil, mErr
		}
		spec.RPCEndpoints = rpcEndpoints
		spec.TotalGPUs = totalGPUs
		return stops, nil
	}
	e.stopLocalRPCServer()
	return nil, nil
}

func (e *Engine) prepareLeaderMeshSpec(
	poolID string,
	localRPCPort int,
	localGPUCount int,
	peers []protocol.MeshPeerWire,
) ([]string, []func(), int, error) {
	if localRPCPort <= 0 {
		return nil, nil, 0, derr.New(derr.CodeUpstreamFailure, 503, "local ggml-rpc port is not active on MESH leader", nil)
	}
	peerEndpoints, stops, peerGPUs, err := e.openMeshPeerRelayListeners(poolID, peers)
	if err != nil {
		return nil, nil, 0, err
	}
	endpoints := make([]string, 0, 1+len(peerEndpoints))
	endpoints = append(endpoints, fmt.Sprintf("127.0.0.1:%d", localRPCPort))
	endpoints = append(endpoints, peerEndpoints...)
	return endpoints, stops, max(1, localGPUCount) + peerGPUs, nil
}

func (e *Engine) soloHealthTimeout() time.Duration {
	if e.soloBootTimeout > 0 {
		return e.soloBootTimeout
	}
	return soloHealthBootTimeout
}

func (e *Engine) probeSoloHealthUntilReady(ctx context.Context, port int, proc RunningRPCProcess) error {
	if e.loopback == nil {
		return derr.New(derr.CodeUpstreamFailure, 503, "loopback transport is not configured", nil)
	}
	probeCtx, cancel := context.WithTimeout(ctx, e.soloHealthTimeout())
	defer cancel()

	ticker := time.NewTicker(soloHealthPollInterval)
	defer ticker.Stop()

	for {
		if e.loopback.ProbeHTTPHealth(probeCtx, port) {
			return nil
		}
		select {
		case <-probeCtx.Done():
			return fmt.Errorf("solo health check cancelled or timed out: %w", probeCtx.Err())
		case <-proc.Done():
			return errors.New("solo engine exited before passing /health readiness check")
		case <-ticker.C:
		}
	}
}

func (e *Engine) activateSoloProcess(gen uint64, modelID string, soloPort int, proc RunningRPCProcess, relayStops []func(), execMode string) bool {
	e.mu.Lock()
	paused, _ := e.effectivePauseLocked()
	valid := e.running && !paused && !e.pauseDraining && e.soloGen == gen
	if valid {
		e.soloPort = soloPort
		e.soloProc = proc
		e.relayStops = relayStops
		e.executionMode = execMode
		e.soloStatus = SoloStatusReady
		e.soloModelID = modelID
		e.status = SoloStatusReady
	}
	e.mu.Unlock()
	if valid {
		e.notifyObservers()
	}
	return valid
}

func (e *Engine) reportSoloFailure(ctx context.Context, gen uint64, ctrl protocol.ControlMsg, targetBackend string, sendJSON func(protocol.ControlMsg) error, err error) {
	if ctx.Err() != nil {
		e.mu.Lock()
		if e.soloGen == gen {
			e.soloStatus = SoloStatusIdle
			e.soloModelID = ""
			e.resetWorkerStatusAfterSoloLocked()
		}
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	current := e.soloGen == gen
	if current {
		e.soloStatus = SoloStatusError
		e.soloModelID = ctrl.ModelID
		e.lastError = err.Error()
		e.resetWorkerStatusAfterSoloLocked()
	}
	e.mu.Unlock()
	if !current {
		return
	}
	e.restoreIdleRPCAfterSolo()
	e.notifyObservers()
	if sendJSON != nil {
		if sErr := sendJSON(protocol.ControlMsg{
			Type:          protocol.ControlSoloEngineError,
			PoolID:        ctrl.PoolID,
			ModelID:       ctrl.ModelID,
			TargetBackend: targetBackend,
			Reason:        err.Error(),
		}); sErr != nil {
			e.logger.Warn("solo_engine_error_send_failed", "pool_id", ctrl.PoolID, "error", sErr.Error())
		}
	}
	e.addLog("SOLO engine setup failed for %s: %v", ctrl.ModelID, err)
}

func (e *Engine) restoreIdleRPCAfterSolo() {
	e.mu.Lock()
	runCtx := e.runCtx
	e.mu.Unlock()
	if err := e.ensureConnectedIdleRPC(runCtx); err != nil && (runCtx == nil || runCtx.Err() == nil) {
		e.logger.Warn("worker_rpc_restore_after_solo_failed", "error", err.Error())
	}
}

func (e *Engine) stopDetachedSoloProcess() {
	e.mu.Lock()
	proc := e.soloProc
	e.soloProc = nil
	e.soloPort = 0
	stops := e.relayStops
	e.relayStops = nil
	ts := e.activeStreams
	if e.executionMode == protocol.ExecutionModeSolo || e.executionMode == protocol.ExecutionModeMesh {
		e.executionMode = ""
	}
	e.mu.Unlock()

	invokeRelayStops(stops)
	if ts != nil {
		ts.closeTargetStreams(protocol.TunnelTargetSoloHTTP)
		ts.closeTargetStreams(protocol.TunnelTargetMeshRelay)
	}
	if proc != nil {
		proc.Stop()
	}
}

func (e *Engine) detachSoloCancellation() (context.CancelFunc, RunningRPCProcess, []func(), *tunnelStreams, bool) {
	e.mu.Lock()
	cancel := e.soloCancel
	e.soloCancel = nil
	e.soloGen++
	proc := e.soloProc
	e.soloProc = nil
	hadSolo := proc != nil || e.soloPort > 0 || e.soloStatus != SoloStatusIdle
	e.soloPort = 0
	e.soloStatus = SoloStatusIdle
	e.soloModelID = ""
	stops := e.relayStops
	e.relayStops = nil
	ts := e.activeStreams
	if e.executionMode == protocol.ExecutionModeSolo || e.executionMode == protocol.ExecutionModeMesh {
		e.executionMode = ""
	}
	e.resetWorkerStatusAfterSoloLocked()
	e.mu.Unlock()
	return cancel, proc, stops, ts, hadSolo
}

func (e *Engine) finishStopSoloEngine(reason string, proc RunningRPCProcess, stops []func(), ts *tunnelStreams, hadSolo bool) {
	invokeRelayStops(stops)
	if ts != nil {
		ts.closeTargetStreams(protocol.TunnelTargetSoloHTTP)
		ts.closeTargetStreams(protocol.TunnelTargetMeshRelay)
	}
	if proc != nil {
		proc.Stop()
	}
	if reason == "gateway_stop_solo_engine" {
		e.restoreIdleRPCAfterSolo()
	}
	if hadSolo {
		e.logger.Info("worker_solo_engine_stopped", "reason", reason)
		e.notifyObservers()
	}
}

func (e *Engine) stopSoloEngine(reason string) {
	cancel, proc, stops, ts, hadSolo := e.detachSoloCancellation()
	cancelIfNonNil(cancel)
	e.finishStopSoloEngine(reason, proc, stops, ts, hadSolo)
}

func (e *Engine) stopSoloEngineAsync(reason string) {
	cancel, proc, stops, ts, hadSolo := e.detachSoloCancellation()
	cancelIfNonNil(cancel)
	go e.finishStopSoloEngine(reason, proc, stops, ts, hadSolo)
}

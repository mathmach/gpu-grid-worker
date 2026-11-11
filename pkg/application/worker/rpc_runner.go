package worker

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const rpcUnexpectedExitMsg = "local ggml-rpc-server exited unexpectedly"

func (e *Engine) allocateLoopbackPort() (int, error) {
	if e.loopback == nil {
		return 0, derr.New(derr.CodeUpstreamFailure, 503, "loopback transport is not configured", nil)
	}
	return e.loopback.AllocateFreePort()
}

func (e *Engine) currentLocalPort() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.localPort
}

func isProcessAlive(proc RunningRPCProcess) bool {
	if proc == nil {
		return false
	}
	select {
	case <-proc.Done():
		return false
	default:
		return true
	}
}

func (e *Engine) spawnLocalRPCServer(ctx context.Context, runtimeDir string, gpus []protocol.GpuInfo, port int) error {
	e.mu.Lock()
	e.rpcGen++
	myGen := e.rpcGen
	oldProc := e.rpcProc
	e.rpcProc = nil
	e.mu.Unlock()

	if oldProc != nil {
		oldProc.Stop()
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	spawnCtx := ctx
	if spawnCtx == nil {
		spawnCtx = context.Background()
	}
	cacheDir := filepath.Join(e.dataDir, "tensor-cache")
	proc, err := e.rpc.SpawnRPC(spawnCtx, runtimeDir, cacheDir, gpus, port)
	if err != nil {
		return err
	}
	if bp, ok := proc.(nodecore.BoundPortProcess); ok && bp.BoundPort() > 0 {
		port = bp.BoundPort()
	}

	ts, committed, cancelled := e.commitSpawnedRPCProcess(myGen, proc, port)
	if !committed {
		proc.Stop()
		if cancelled {
			return context.Canceled
		}
		return nil
	}
	if ts != nil {
		ts.setLocalPort(port)
	}
	go e.watchRPCProcessExit(spawnCtx, myGen, proc, runtimeDir, append([]protocol.GpuInfo(nil), gpus...), port)
	return nil
}

func (e *Engine) commitSpawnedRPCProcess(gen uint64, proc RunningRPCProcess, port int) (*tunnelStreams, bool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return nil, false, true
	}
	paused, _ := e.effectivePauseLocked()
	soloConflict := e.soloProc != nil && e.executionMode != protocol.ExecutionModeMesh
	if e.rpcGen != gen || paused || e.pauseDraining || soloConflict {
		return nil, false, false
	}
	e.rpcProc = proc
	e.localPort = port
	return e.activeStreams, true, false
}

func (e *Engine) watchRPCProcessExit(
	ctx context.Context,
	gen uint64,
	proc RunningRPCProcess,
	runtimeDir string,
	gpus []protocol.GpuInfo,
	port int,
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

	ts, unexpected := e.detachCrashedRPCProcess(gen, proc)
	if !unexpected {
		return
	}
	e.logger.Error("worker_rpc_process_crashed", "port", port)
	if ts != nil {
		ts.closeTargetStreams(protocol.TunnelTargetRPC)
	}
	e.notifyObservers()
	e.addLog("Local ggml-rpc-server on 127.0.0.1:%d exited unexpectedly; attempting respawn...", port)
	e.recoverCrashedRPCProcess(ctx, runtimeDir, gpus, port)
}

func (e *Engine) detachCrashedRPCProcess(gen uint64, proc RunningRPCProcess) (*tunnelStreams, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	paused, _ := e.effectivePauseLocked()
	if !e.running || paused || e.pauseDraining || e.rpcGen != gen || e.rpcProc != proc {
		return nil, false
	}
	e.rpcGen++
	e.rpcProc = nil
	e.lastError = rpcUnexpectedExitMsg
	return e.activeStreams, true
}

func (e *Engine) recoverCrashedRPCProcess(ctx context.Context, runtimeDir string, gpus []protocol.GpuInfo, port int) {
	attempt := 0
	for ctx.Err() == nil {
		if !e.shouldRespawnLocalRPC() {
			return
		}
		err := e.spawnLocalRPCServer(ctx, runtimeDir, gpus, port)
		if err == nil {
			actualPort := e.currentLocalPort()
			e.clearRecoveredRPCError()
			e.addLog("Recovered local ggml-rpc-server on 127.0.0.1:%d.", actualPort)
			return
		}
		if ctx.Err() != nil {
			return
		}
		e.logger.Error("worker_rpc_respawn_failed", "port", port, "attempt", attempt+1, "error", err.Error())
		delay := ComputeReconnectBackoff(e.reconnectDelay, attempt)
		attempt++
		if !sleepWithContext(ctx, delay) {
			return
		}
	}
}

func (e *Engine) shouldRespawnLocalRPC() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	paused, _ := e.effectivePauseLocked()
	if !e.running || paused || e.pauseDraining || e.activeSendJSON == nil {
		return false
	}
	if e.soloProc != nil && e.executionMode != protocol.ExecutionModeMesh {
		return false
	}
	if !slices.Contains(e.effectiveBackendsLocked(), string(nodecore.BackendLlamaRPC)) {
		return false
	}
	return !isProcessAlive(e.rpcProc)
}

func (e *Engine) clearRecoveredRPCError() {
	e.mu.Lock()
	if e.lastError == rpcUnexpectedExitMsg {
		e.lastError = ""
	}
	e.mu.Unlock()
	e.notifyObservers()
}

func (e *Engine) stopLocalRPCServer() {
	e.mu.Lock()
	e.rpcGen++
	proc := e.rpcProc
	e.rpcProc = nil
	e.mu.Unlock()

	if proc != nil {
		proc.Stop()
	}
}

func (e *Engine) releaseLocalGPUForPause() {
	e.mu.Lock()
	ts := e.activeStreams
	e.mu.Unlock()

	if ts != nil {
		ts.closeAll()
	}
	e.stopMeshPeerModel("pause")
	e.stopSoloEngine("pause")
	e.stopLocalRPCServer()
}

func (e *Engine) resumeLocalGPUFromPause(ctx context.Context) error {
	return e.ensureConnectedIdleRPC(ctx)
}

func (e *Engine) ensureConnectedIdleRPC(ctx context.Context) error {
	e.mu.Lock()
	paused, _ := e.effectivePauseLocked()
	eligible := e.running &&
		!paused &&
		!e.pauseDraining &&
		e.activeSendJSON != nil &&
		(e.soloProc == nil || e.executionMode == protocol.ExecutionModeMesh) &&
		slices.Contains(e.effectiveBackendsLocked(), string(nodecore.BackendLlamaRPC))
	runtimeDir := e.runtimeDir
	port := e.localPort
	gpus := append([]protocol.GpuInfo(nil), e.activeGPUs...)
	proc := e.rpcProc
	runCtx := e.runCtx
	e.mu.Unlock()

	if !eligible || runtimeDir == "" || port <= 0 || len(gpus) == 0 || isProcessAlive(proc) {
		return nil
	}
	if ctx == nil {
		ctx = runCtx
	}
	return e.spawnLocalRPCServer(ctx, runtimeDir, gpus, port)
}

func (e *Engine) reconcileIdleRPCForBackends(effective []string) {
	if !slices.Contains(effective, string(nodecore.BackendLlamaRPC)) {
		e.mu.Lock()
		isMesh := e.executionMode == protocol.ExecutionModeMesh && e.soloProc != nil
		e.mu.Unlock()
		if !isMesh {
			e.stopLocalRPCServer()
		}
		return
	}
	e.mu.Lock()
	runCtx := e.runCtx
	e.mu.Unlock()
	if err := e.ensureConnectedIdleRPC(runCtx); err != nil && (runCtx == nil || runCtx.Err() == nil) {
		e.logger.Warn("worker_rpc_backend_enable_failed", "error", err.Error())
	}
}

func (e *Engine) ensureLocalRPCAlive(ctx context.Context, runtimeDir string, gpus []protocol.GpuInfo, localPort int) {
	e.mu.Lock()
	paused, _ := e.effectivePauseLocked()
	proc := e.rpcProc
	e.mu.Unlock()

	if paused {
		e.releaseLocalGPUForPause()
		return
	}
	if isProcessAlive(proc) {
		return
	}
	if spawnErr := e.spawnLocalRPCServer(ctx, runtimeDir, gpus, localPort); spawnErr != nil && (ctx == nil || ctx.Err() == nil) {
		e.logger.Error("worker_rpc_ensure_alive_failed", "port", localPort, "error", spawnErr.Error())
		e.addLog("Warning restarting local ggml-rpc-server: %v", spawnErr)
	}
}

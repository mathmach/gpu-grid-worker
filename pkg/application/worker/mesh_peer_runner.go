package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func (e *Engine) refreshDiskAndCacheInventory() ([]string, int, bool) {
	inv := e.cacheInventory
	if inv == nil {
		return nil, 0, false
	}
	e.mu.Lock()
	dataDir := e.dataDir
	e.mu.Unlock()

	digests, dErr := inv.ListCachedDigests(dataDir)
	if dErr != nil {
		digests = nil
	}
	freeMiB, fErr := inv.AvailableDiskMiB(dataDir)
	probed := fErr == nil
	if !probed {
		freeMiB = 0
	}

	e.mu.Lock()
	e.cachedModelSHA256s = append([]string(nil), digests...)
	e.diskFreeMiB = freeMiB
	e.diskFreeProbed = probed
	e.mu.Unlock()
	return digests, freeMiB, probed
}

func (e *Engine) startEnsureMeshPeerModel(
	sessionCtx context.Context,
	ctrl protocol.ControlMsg,
	sendJSON func(protocol.ControlMsg) error,
) {
	peerCtx, cancel := context.WithCancel(sessionCtx)
	e.mu.Lock()
	prevCancel := e.meshPeerCancel
	e.meshPeerGen++
	gen := e.meshPeerGen
	e.meshPeerCancel = cancel
	e.mu.Unlock()

	cancelIfNonNil(prevCancel)
	go e.runEnsureMeshPeerModel(peerCtx, gen, ctrl, sendJSON)
}

func (e *Engine) detachMeshPeerCancellation() context.CancelFunc {
	e.mu.Lock()
	cancel := e.meshPeerCancel
	e.meshPeerCancel = nil
	e.meshPeerGen++
	e.mu.Unlock()
	return cancel
}

func (e *Engine) finishStopMeshPeerModel(reason string) {
	e.meshPeerMu.Lock()
	e.meshPeerMu.Unlock()
	if reason != "" {
		e.logger.Debug("worker_mesh_peer_model_cancelled", "reason", reason)
	}
}

func (e *Engine) stopMeshPeerModel(reason string) {
	cancel := e.detachMeshPeerCancellation()
	cancelIfNonNil(cancel)
	e.finishStopMeshPeerModel(reason)
}

func (e *Engine) stopMeshPeerModelAsync(reason string) {
	cancel := e.detachMeshPeerCancellation()
	cancelIfNonNil(cancel)
	go e.finishStopMeshPeerModel(reason)
}

func (e *Engine) runEnsureMeshPeerModel(
	ctx context.Context,
	gen uint64,
	ctrl protocol.ControlMsg,
	sendJSON func(protocol.ControlMsg) error,
) {
	e.meshPeerMu.Lock()
	defer e.meshPeerMu.Unlock()

	if !e.isMeshPeerGenCurrent(gen) {
		return
	}
	digests, freeMiB, probed := e.refreshDiskAndCacheInventory()
	if err := e.validateModelControlURLs(ctrl); err != nil {
		e.reportMeshPeerError(ctx, gen, ctrl, digests, freeMiB, probed, sendJSON, err)
		return
	}
	if err := e.checkMandatoryModelStorage(ctrl, digests, freeMiB, probed); err != nil {
		e.reportMeshPeerError(ctx, gen, ctrl, digests, freeMiB, probed, sendJSON, err)
		return
	}

	_, digest, _, err := e.pullVerifiedSoloBundle(ctx, ctrl)
	digests, freeMiB, probed = e.refreshDiskAndCacheInventory()
	if err != nil {
		e.reportMeshPeerError(ctx, gen, ctrl, digests, freeMiB, probed, sendJSON, err)
		return
	}
	if ctx.Err() != nil || !e.isMeshPeerGenCurrent(gen) {
		return
	}
	if err := e.ensureConnectedIdleRPC(ctx); err != nil {
		e.reportMeshPeerError(ctx, gen, ctrl, digests, freeMiB, probed, sendJSON, err)
		return
	}
	e.notifyObservers()
	if sendJSON != nil {
		readyMsg := protocol.ControlMsg{
			Type:               protocol.ControlMeshPeerReady,
			PoolID:             ctrl.PoolID,
			ModelID:            ctrl.ModelID,
			ModelSHA256:        digest,
			CachedModelSHA256s: digests,
			DiskFreeMiB:        freeMiB,
			DiskFreeProbed:     probed,
		}
		if sErr := sendJSON(readyMsg); sErr != nil {
			e.logger.Warn("mesh_peer_ready_send_failed", "pool_id", ctrl.PoolID, "error", sErr.Error())
		}
	}
	e.addLog("MESH peer verified mandatory model %s in local CAS", ctrl.ModelID)
}

func (e *Engine) checkMandatoryModelStorage(
	ctrl protocol.ControlMsg,
	cached []string,
	freeMiB int,
	probed bool,
) error {
	if !probed {
		return nil
	}
	missingMiB := e.estimateMissingModelMiB(ctrl, cached)
	if missingMiB > 0 && freeMiB < missingMiB {
		return derr.New(
			derr.CodeValidation,
			507,
			fmt.Sprintf("insufficient free disk space for mandatory model %s: have %d MiB, need %d MiB", ctrl.ModelID, freeMiB, missingMiB),
			nil,
		)
	}
	return nil
}

func (e *Engine) estimateMissingModelMiB(ctrl protocol.ControlMsg, cached []string) int {
	if len(ctrl.BundleComponents) > 0 {
		var missingBytes int64
		for _, comp := range ctrl.BundleComponents {
			if comp.SHA256 != "" && nodecore.WorkerHasCachedSHA256(cached, comp.SHA256) {
				continue
			}
			if comp.SizeBytes > 0 {
				missingBytes += comp.SizeBytes
			}
		}
		if missingBytes > 0 {
			return int((missingBytes + (1<<20 - 1)) >> 20)
		}
	}
	if ctrl.ModelSHA256 != "" && nodecore.WorkerHasCachedSHA256(cached, ctrl.ModelSHA256) {
		return 0
	}
	return e.lookupCatalogModelSizeMiB(ctrl.ModelID)
}

func (e *Engine) lookupCatalogModelSizeMiB(modelID string) int {
	norm := nodecore.NormalizeModelID(modelID)
	if norm == "" {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range e.catalogModels {
		if nodecore.NormalizeModelID(m.ID) == norm {
			return m.FileSizeMiB
		}
	}
	return 0
}

func (e *Engine) isMeshPeerGenCurrent(gen uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	paused, _ := e.effectivePauseLocked()
	return e.running && !paused && !e.pauseDraining && e.meshPeerGen == gen
}

func (e *Engine) reportMeshPeerError(
	ctx context.Context,
	gen uint64,
	ctrl protocol.ControlMsg,
	digests []string,
	freeMiB int,
	probed bool,
	sendJSON func(protocol.ControlMsg) error,
	err error,
) {
	if ctx.Err() != nil {
		return
	}
	e.mu.Lock()
	current := e.meshPeerGen == gen
	if current && err != nil {
		e.lastError = err.Error()
	}
	e.mu.Unlock()
	if !current {
		return
	}
	e.notifyObservers()
	reason := "mandatory MESH model verification failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		reason = err.Error()
	}
	if sendJSON != nil {
		errMsg := protocol.ControlMsg{
			Type:               protocol.ControlMeshPeerError,
			PoolID:             ctrl.PoolID,
			ModelID:            ctrl.ModelID,
			Reason:             reason,
			CachedModelSHA256s: digests,
			DiskFreeMiB:        freeMiB,
			DiskFreeProbed:     probed,
		}
		if sErr := sendJSON(errMsg); sErr != nil {
			e.logger.Warn("mesh_peer_error_send_failed", "pool_id", ctrl.PoolID, "error", sErr.Error())
		}
	}
	e.addLog("MESH peer model verification failed for %s: %s", ctrl.ModelID, reason)
}

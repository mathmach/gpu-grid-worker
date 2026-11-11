package worker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func (e *Engine) ensureSelectedModelsPreDownloaded(ctx context.Context, gen uint64, gwHTTP, workerToken string) bool {
	e.mu.Lock()
	mode := e.modelMode
	selected := append([]string(nil), e.selectedModels...)
	e.mu.Unlock()

	if mode != protocol.ModelModeMulti || len(selected) == 0 || e.portal == nil || e.modelCache == nil {
		return true
	}
	if !e.waitWhilePausedPreDownload(ctx, gen) {
		return false
	}

	artifacts, err := e.portal.ResolvePreDownloadArtifacts(ctx, gwHTTP, workerToken, selected)
	if err != nil {
		return e.handlePreDownloadFailure(ctx, gen, err)
	}

	cached, freeMiB, probed := e.refreshDiskAndCacheInventory()
	pending, missingMiB := filterUncachedArtifacts(artifacts, cached)
	if len(pending) == 0 {
		e.addLog("All %d selected model(s) already verified in local CAS.", len(artifacts))
		return true
	}
	if probed && missingMiB > 0 && freeMiB < missingMiB {
		diskErr := derr.New(
			derr.CodeValidation,
			507,
			fmt.Sprintf("insufficient free disk space for selected models: have %d MiB, need %d MiB", freeMiB, missingMiB),
			nil,
		)
		return e.handlePreDownloadFailure(ctx, gen, diskErr)
	}

	for idx, art := range pending {
		if !e.waitWhilePausedPreDownload(ctx, gen) {
			return false
		}
		if !e.pullSinglePreDownloadArtifact(ctx, gen, art, idx+1, len(pending)) {
			return false
		}
	}
	e.clearPreDownloadProgress(gen)
	e.addLog("Pre-downloaded and verified %d selected model(s) in local CAS.", len(pending))
	return true
}

func (e *Engine) waitWhilePausedPreDownload(ctx context.Context, gen uint64) bool {
	for {
		if ctx.Err() != nil {
			e.abortLoopFromWorker(gen, "", "")
			return false
		}
		e.mu.Lock()
		current := e.running && e.runGen == gen
		paused, _ := e.effectivePauseLocked()
		statusChanged := false
		if current && paused && e.status != "PAUSED" {
			e.status = "PAUSED"
			statusChanged = true
		}
		e.mu.Unlock()
		if statusChanged {
			e.notifyObservers()
		}
		if !current {
			return false
		}
		if !paused {
			return true
		}
		if !sleepWithContext(ctx, 50*time.Millisecond) {
			e.abortLoopFromWorker(gen, "", "")
			return false
		}
	}
}

func (e *Engine) pullSinglePreDownloadArtifact(
	ctx context.Context,
	gen uint64,
	art protocol.WorkerModelArtifactView,
	index, total int,
) bool {
	if !e.setPreDownloadProgress(gen, art.ModelID, index, total, 0) {
		return false
	}
	onProgress := func(downloaded, size int64) {
		if size <= 0 {
			return
		}
		pct := int(min(100, max(0, (downloaded*100)/size)))
		e.updatePreDownloadPercent(gen, pct)
	}

	err := e.downloadVerifiedArtifactSpec(ctx, gen, art, onProgress)
	e.refreshDiskAndCacheInventory()
	if err != nil {
		return e.handlePreDownloadFailure(ctx, gen, err)
	}
	e.updatePreDownloadPercent(gen, 100)
	return ctx.Err() == nil
}

func (e *Engine) downloadVerifiedArtifactSpec(
	ctx context.Context,
	gen uint64,
	art protocol.WorkerModelArtifactView,
	onProgress func(downloaded, total int64),
) error {
	_, _, baseURL, _, dataDir := e.snapshotDownloadPolicy()
	if len(art.BundleComponents) > 0 {
		return e.downloadPreDownloadBundle(ctx, gen, dataDir, baseURL, art, onProgress)
	}
	fileName := strings.TrimSpace(art.FileName)
	if fileName == "" {
		fileName = strings.TrimSpace(art.ModelID) + ".gguf"
	}
	rawURL := strings.TrimSpace(art.DownloadURL)
	if rawURL == "" {
		rawURL = protocol.BuildModelDownloadURL(baseURL, fileName)
	}
	validURL, err := e.validateDownloadURL(rawURL)
	if err != nil {
		return err
	}
	_, _, err = e.modelCache.EnsureModelCached(ctx, dataDir, art.ModelID, fileName, validURL, art.ModelSHA256, onProgress)
	return err
}

func (e *Engine) downloadPreDownloadBundle(
	ctx context.Context,
	gen uint64,
	dataDir, baseURL string,
	art protocol.WorkerModelArtifactView,
	onProgress func(downloaded, total int64),
) error {
	for _, comp := range art.BundleComponents {
		if !e.waitWhilePausedPreDownload(ctx, gen) {
			return context.Canceled
		}
		cacheKey := strings.TrimSpace(art.ModelID)
		cleanSHA := strings.ToLower(strings.TrimSpace(comp.SHA256))
		if len(cleanSHA) >= 16 {
			cacheKey = "cas-" + cleanSHA[:16]
		}
		rawURL := strings.TrimSpace(comp.DownloadURL)
		if rawURL == "" {
			rawURL = protocol.BuildComponentDownloadURL(baseURL, art.ModelID, comp.FileName)
		}
		validURL, err := e.validateDownloadURL(rawURL)
		if err != nil {
			return err
		}
		if _, _, err := e.modelCache.EnsureModelCached(ctx, dataDir, cacheKey, comp.FileName, validURL, cleanSHA, onProgress); err != nil {
			return err
		}
	}
	return nil
}

func filterUncachedArtifacts(
	artifacts []protocol.WorkerModelArtifactView,
	cached []string,
) ([]protocol.WorkerModelArtifactView, int) {
	var pending []protocol.WorkerModelArtifactView
	totalMissingMiB := 0
	for _, art := range artifacts {
		miss := artifactMissingMiB(art, cached)
		if miss < 0 {
			continue
		}
		pending = append(pending, art)
		totalMissingMiB += miss
	}
	return pending, totalMissingMiB
}

func artifactMissingMiB(art protocol.WorkerModelArtifactView, cached []string) int {
	if len(art.BundleComponents) > 0 {
		return bundleMissingMiB(art, cached)
	}
	if art.ModelSHA256 != "" && nodecore.WorkerHasCachedSHA256(cached, art.ModelSHA256) {
		return -1
	}
	return max(0, art.FileSizeMiB)
}

func bundleMissingMiB(art protocol.WorkerModelArtifactView, cached []string) int {
	var missingBytes int64
	allCached := true
	for _, comp := range art.BundleComponents {
		if comp.SHA256 != "" && nodecore.WorkerHasCachedSHA256(cached, comp.SHA256) {
			continue
		}
		allCached = false
		missingBytes += max(int64(0), comp.SizeBytes)
	}
	if allCached {
		return -1
	}
	if missingBytes > 0 {
		return int((missingBytes + (1<<20 - 1)) >> 20)
	}
	return max(0, art.FileSizeMiB)
}

func (e *Engine) setPreDownloadProgress(gen uint64, modelID string, index, total, pct int) bool {
	e.mu.Lock()
	current := e.running && e.runGen == gen
	if current {
		if !e.manualPaused {
			e.status = StatusPreDownloading
		}
		e.preDownloadModelID = modelID
		e.preDownloadIndex = index
		e.preDownloadTotal = total
		e.preDownloadPct = pct
	}
	e.mu.Unlock()
	if current {
		e.notifyObservers()
	}
	return current
}

func (e *Engine) updatePreDownloadPercent(gen uint64, pct int) {
	e.mu.Lock()
	changed := e.running && e.runGen == gen && e.preDownloadPct != pct
	if changed {
		e.preDownloadPct = pct
	}
	e.mu.Unlock()
	if changed {
		e.notifyObservers()
	}
}

func (e *Engine) clearPreDownloadProgress(gen uint64) {
	e.mu.Lock()
	current := e.running && e.runGen == gen
	if current {
		e.preDownloadModelID = ""
		e.preDownloadPct = 0
		e.preDownloadIndex = 0
		e.preDownloadTotal = 0
		if paused, _ := e.effectivePauseLocked(); paused {
			e.status = "PAUSED"
		} else {
			e.status = "STARTING"
		}
	}
	e.mu.Unlock()
	if current {
		e.notifyObservers()
	}
}

func (e *Engine) handlePreDownloadFailure(ctx context.Context, gen uint64, err error) bool {
	if ctx.Err() != nil {
		e.abortLoopFromWorker(gen, "", "")
		return false
	}
	msg := err.Error()
	e.abortLoopFromWorker(gen, msg, "Model pre-download failed: "+msg)
	return false
}

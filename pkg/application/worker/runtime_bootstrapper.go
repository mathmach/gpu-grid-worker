package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func rpcBinaryName() string {
	if runtime.GOOS == "windows" {
		return "ggml-rpc-server.exe"
	}
	return "ggml-rpc-server"
}

func (e *Engine) cleanupLegacyStagingDirs() {
	if e.runtimeFS == nil || strings.TrimSpace(e.dataDir) == "" {
		return
	}
	for _, rel := range []string{
		"runtime-staging",
		"runtime-" + runtime.GOOS + ".staging",
		"runtime.zip",
		"runtime.tar.gz",
	} {
		e.cleanupRuntimePath(filepath.Join(e.dataDir, rel))
	}
}

func (e *Engine) ResolveExistingRuntimeDir() string {
	if e.runtimeFS == nil {
		return ""
	}
	return e.runtimeFS.FindExistingRuntime(e.dataDir, rpcBinaryName())
}

func (e *Engine) probeBackendsForRuntime(runtimeDir string) []string {
	raw := []string{string(nodecore.BackendLlamaRPC)}
	if strings.TrimSpace(runtimeDir) != "" && e.runtimeFS != nil && e.runtimeFS.FindExistingRuntime(runtimeDir, rpcBinaryName()) == "" {
		raw = nil
	}
	if e.solo != nil {
		raw = append(raw, e.solo.SupportedBackends(runtimeDir)...)
	} else if strings.TrimSpace(runtimeDir) != "" && e.runtimeFS != nil {
		if e.runtimeFS.FindExistingRuntime(runtimeDir, llamaServerBinaryName()) != "" {
			raw = append(raw, string(nodecore.BackendLlamaLocal))
		}
		if e.findExistingPackDir("diffusion", diffusionServerBinaryName()) != "" {
			raw = append(raw, string(nodecore.BackendDiffusionLocal))
		}
		if e.findExistingPackDir("audio", whisperServerBinaryName()) != "" {
			raw = append(raw, string(nodecore.BackendWhisperLocal))
		}
	}
	e.mu.Lock()
	raw = append(raw, e.remotePackBackends...)
	e.mu.Unlock()
	normalized := NormalizeBackendList(raw)
	if len(normalized) == 0 {
		return []string{string(nodecore.BackendLlamaRPC)}
	}
	return normalized
}

func (e *Engine) probeRemoteModularPacks(ctx context.Context, baseURL string) {
	if e.gatewayClient == nil || strings.TrimSpace(baseURL) == "" {
		return
	}
	var discovered []string
	for _, candidate := range []nodecore.EngineBackend{nodecore.BackendDiffusionLocal, nodecore.BackendWhisperLocal} {
		_, _, archiveName, ok := runtimePackSpecForBackend(string(candidate))
		if !ok {
			continue
		}
		if sha, err := e.gatewayClient.FetchRuntimeSHA256(ctx, baseURL, archiveName); err == nil && sha != "" {
			discovered = append(discovered, string(candidate))
		}
	}
	e.mu.Lock()
	e.remotePackBackends = NormalizeBackendList(discovered)
	curDir := e.runtimeDir
	e.mu.Unlock()
	if curDir == "" {
		curDir = e.ResolveExistingRuntimeDir()
	}
	e.refreshProbedBackends(curDir)
}

func (e *Engine) effectiveBackendsLocked() []string {
	probed := e.probedBackends
	if len(probed) == 0 {
		probed = []string{string(nodecore.BackendLlamaRPC)}
	}
	if !e.enabledBackendsSet && len(e.enabledBackends) == 0 {
		return append([]string(nil), probed...)
	}
	allowed := make(map[string]bool, len(e.enabledBackends))
	for _, b := range e.enabledBackends {
		allowed[b] = true
	}
	filtered := make([]string, 0, len(probed))
	for _, b := range probed {
		if allowed[b] {
			filtered = append(filtered, b)
		}
	}
	return filtered
}

func (e *Engine) refreshProbedBackends(runtimeDir string) {
	probed := e.probeBackendsForRuntime(runtimeDir)
	e.mu.Lock()
	prev := e.effectiveBackendsLocked()
	e.probedBackends = probed
	next := e.effectiveBackendsLocked()
	var sendFn func(protocol.ControlMsg) error
	var modelMode string
	var selectedModels []string
	if !slices.Equal(prev, next) && e.activeSendJSON != nil {
		sendFn = e.activeSendJSON
		modelMode = e.modelMode
		selectedModels = append([]string(nil), e.selectedModels...)
	}
	e.mu.Unlock()

	if sendFn != nil {
		wireMode, wireModels := canonicalWireModelSelection(modelMode, selectedModels)
		if err := sendFn(protocol.ControlMsg{
			Type:              protocol.ControlUpdateModels,
			ModelMode:         wireMode,
			SelectedModels:    wireModels,
			SupportedBackends: next,
		}); err != nil {
			e.logger.Warn("worker_probed_backends_sync_failed", "error", err.Error())
		}
	}
}

func (e *Engine) EnsureRuntime(ctx context.Context, gatewayURL string) (string, error) {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()

	if existing := e.ResolveExistingRuntimeDir(); existing != "" {
		if baseURL, err := e.validateAndNormalizeGateway(gatewayURL); err == nil && strings.TrimSpace(baseURL) != "" {
			e.probeRemoteModularPacks(ctx, baseURL)
		}
		e.refreshProbedBackends(existing)
		return existing, nil
	}
	if e.gatewayClient == nil || e.archive == nil || e.runtimeFS == nil {
		return "", derr.New(derr.CodeUpstreamFailure, 503, "worker runtime bootstrapper ports are not configured", nil)
	}

	baseURL, err := e.validateAndNormalizeGateway(gatewayURL)
	if err != nil {
		return "", err
	}
	archiveName := runtimeArchiveName()
	expectedSHA, err := e.gatewayClient.FetchRuntimeSHA256(ctx, baseURL, archiveName)
	if err != nil {
		return "", err
	}

	rawDownloadURL := baseURL + "/assets/" + archiveName
	downloadURL, err := e.validateGatewayAssetDownloadURL(rawDownloadURL, baseURL)
	if err != nil {
		return "", err
	}

	stageParent := filepath.Join(e.dataDir, e.newStagingDirName())
	defer e.cleanupRuntimePath(stageParent)
	stagingRoot, err := e.runtimeFS.PrepareStagingDir(stageParent)
	if err != nil {
		return "", err
	}
	archivePath := filepath.Join(stageParent, archiveName)

	e.addLog("Downloading Vulkan runtime (%s) from %s...", runtime.GOOS, downloadURL)
	if _, err := e.archive.DownloadVerified(ctx, downloadURL, archivePath, expectedSHA); err != nil {
		return "", fmt.Errorf("failed to download Vulkan runtime from %s: %w", baseURL, err)
	}

	dir, err := e.extractAndPromoteRuntime(archivePath, stagingRoot)
	if err != nil {
		return "", err
	}
	e.probeRemoteModularPacks(ctx, baseURL)
	e.refreshProbedBackends(dir)
	e.addLog("Vulkan runtime ready at %s", dir)
	return dir, nil
}

func (e *Engine) cleanupRuntimePath(path string) {
	if e.runtimeFS != nil {
		if err := e.runtimeFS.RemovePath(path); err != nil {
			e.logger.Debug("runtime_cleanup_failed", "path", path, "error", err.Error())
		}
	}
}

func (e *Engine) extractAndPromoteRuntime(archivePath, stagingRoot string) (string, error) {
	e.addLog("Extracting verified Vulkan runtime into local cache...")
	if err := e.archive.ExtractArchive(archivePath, stagingRoot); err != nil {
		e.cleanupRuntimePath(stagingRoot)
		return "", err
	}
	runtimeRoot := filepath.Join(e.dataDir, "runtime-"+runtime.GOOS)
	if err := e.runtimeFS.ActivateStagedRuntime(stagingRoot, runtimeRoot, rpcBinaryName()); err != nil {
		return "", err
	}
	resolved := e.ResolveExistingRuntimeDir()
	if resolved == "" {
		return "", derr.New(derr.CodeUpstreamFailure, 502, "ggml-rpc-server binary missing after runtime extraction", nil)
	}
	return resolved, nil
}

func runtimeArchiveName() string {
	if runtime.GOOS == "windows" {
		return "worker-runtime-windows-amd64.zip"
	}
	return "worker-runtime-linux-amd64.tar.gz"
}

func (e *Engine) ScanGPUs(ctx context.Context, gatewayURL string) error {
	if e.hardware == nil {
		msg := "HardwareProbeError: hardware inspector is not configured"
		e.mu.Lock()
		if !e.running {
			e.status = StatusProbeError
		}
		e.lastError = msg
		e.mu.Unlock()
		e.notifyObservers()
		return derr.New(derr.CodeNoVulkanGPU, 422, msg, nil)
	}
	gw, err := e.updateGatewayForScan(gatewayURL)
	if err != nil {
		e.setLastError(err.Error())
		return err
	}

	runtimeDir, err := e.EnsureRuntime(ctx, gw)
	if err != nil {
		e.setLastError(err.Error())
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	devices, err := e.hardware.ProbeGPUs(probeCtx, runtimeDir)
	if err != nil {
		return e.recordHardwareProbeFailure(err)
	}
	if len(devices) == 0 {
		return e.recordHardwareProbeFailure(derr.New(derr.CodeNoVulkanGPU, 422, "no Vulkan GPUs detected on this machine", nil))
	}

	e.mu.Lock()
	e.detectedGPUs = devices
	e.lastError = ""
	if !e.running && e.status == StatusProbeError {
		e.status = "STOPPED"
	}
	e.selectedIDs = e.resolveSelectedDevices(devices, e.selectedIDs)
	e.mu.Unlock()

	e.persistConfig()
	e.addLog("Detected %d Vulkan GPU(s) ready for compute.", len(devices))
	go e.prefetchCatalogAsync(gw)
	return nil
}

func (e *Engine) recordHardwareProbeFailure(err error) error {
	e.mu.Lock()
	e.detectedGPUs = nil
	if !e.running {
		e.status = StatusProbeError
	}
	e.lastError = err.Error()
	e.mu.Unlock()
	e.addLog("HardwareProbeError: %v", err)
	if _, ok := derr.AsAppError(err); ok {
		return err
	}
	return derr.Wrap(derr.CodeNoVulkanGPU, 422, "HardwareProbeError: "+err.Error(), err)
}

func (e *Engine) updateGatewayForScan(gatewayURL string) (string, error) {
	gw := strings.TrimSpace(gatewayURL)
	if gw == "" {
		e.mu.Lock()
		resolved := e.gatewayURL
		e.mu.Unlock()
		return resolved, nil
	}
	norm, err := e.validateAndNormalizeGateway(gw)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.gatewayURL = norm
	e.mu.Unlock()
	e.persistConfig()
	return norm, nil
}

func (e *Engine) prefetchCatalogAsync(targetGW string) {
	catCtx, catCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer catCancel()
	if _, err := e.RefreshCatalog(catCtx, targetGW); err != nil {
		e.logger.Debug("catalog_prefetch_skipped", "error", err.Error())
	}
}

func (e *Engine) resolveSelectedDevices(devices []protocol.GpuInfo, current []string) []string {
	if len(current) == 1 && strings.EqualFold(current[0], "all") {
		all := make([]string, len(devices))
		for i, d := range devices {
			all[i] = d.DeviceID
		}
		return all
	}
	if matched := matchExistingDeviceIDs(devices, current); len(matched) > 0 {
		return matched
	}
	if dedicated := e.filterDedicatedDevices(devices); len(dedicated) > 0 {
		return dedicated
	}
	if len(devices) > 0 {
		return []string{devices[0].DeviceID}
	}
	return nil
}

func (e *Engine) filterDedicatedDevices(devices []protocol.GpuInfo) []string {
	if e.hardware == nil {
		return nil
	}
	var dedicated []string
	for _, d := range devices {
		if e.hardware.IsDedicatedGPU(d.Name) {
			dedicated = append(dedicated, d.DeviceID)
		}
	}
	return dedicated
}

func matchExistingDeviceIDs(devices []protocol.GpuInfo, current []string) []string {
	var valid []string
	for _, id := range current {
		for _, d := range devices {
			if strings.EqualFold(d.DeviceID, id) {
				valid = append(valid, d.DeviceID)
				break
			}
		}
	}
	return valid
}

func (e *Engine) RefreshCatalog(ctx context.Context, gatewayURL string) ([]protocol.CatalogModelView, error) {
	if e.gatewayClient == nil {
		return nil, derr.New(derr.CodeUpstreamFailure, 503, "gateway client port is not configured", nil)
	}
	gw := strings.TrimSpace(gatewayURL)
	if gw == "" {
		e.mu.Lock()
		gw = e.gatewayURL
		e.mu.Unlock()
	} else {
		norm, err := e.validateAndNormalizeGateway(gw)
		if err != nil {
			return nil, err
		}
		gw = norm
	}

	models, err := e.gatewayClient.FetchCatalogModels(ctx, gw)
	if err != nil {
		return nil, err
	}
	if err := e.validateCatalogModelsDownloadURLs(models, gw); err != nil {
		return nil, err
	}
	e.probeRemoteModularPacks(ctx, gw)
	e.mu.Lock()
	e.gatewayURL = gw
	e.catalogModels = models
	e.mu.Unlock()
	e.notifyObservers()
	return models, nil
}

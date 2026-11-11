package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

func diffusionServerBinaryName() string {
	if runtime.GOOS == "windows" {
		return "sd-server.exe"
	}
	return "sd-server"
}

func whisperServerBinaryName() string {
	if runtime.GOOS == "windows" {
		return "whisper-server.exe"
	}
	return "whisper-server"
}

func runtimePackSpecForBackend(targetBackend string) (string, string, string, bool) {
	ext := ".tar.gz"
	osArch := "linux-amd64"
	if runtime.GOOS == "windows" {
		ext = ".zip"
		osArch = "windows-amd64"
	}
	switch nodecore.EngineBackend(strings.ToLower(strings.TrimSpace(targetBackend))) {
	case nodecore.BackendDiffusionLocal:
		return "diffusion", diffusionServerBinaryName(), "worker-runtime-diffusion-" + osArch + ext, true
	case nodecore.BackendWhisperLocal:
		return "audio", whisperServerBinaryName(), "worker-runtime-audio-" + osArch + ext, true
	default:
		return "", "", "", false
	}
}

func (e *Engine) currentRuntimeDir() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runtimeDir
}

func (e *Engine) currentGatewayURL() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.gatewayURL
}

func findExistingPackDir(runtimeFS RuntimeFS, dataDir, runtimeDir, packName, binaryName string) string {
	if runtimeFS == nil || binaryName == "" {
		return ""
	}
	if strings.TrimSpace(dataDir) != "" && strings.TrimSpace(packName) != "" {
		packRoot := filepath.Join(dataDir, "runtimes", packName)
		if dir := runtimeFS.FindExistingRuntime(packRoot, binaryName); dir != "" {
			return dir
		}
	}
	if strings.TrimSpace(runtimeDir) != "" {
		if dir := runtimeFS.FindExistingRuntime(runtimeDir, binaryName); dir != "" {
			return dir
		}
	}
	if strings.TrimSpace(dataDir) != "" {
		return runtimeFS.FindExistingRuntime(dataDir, binaryName)
	}
	return ""
}

func (e *Engine) findExistingPackDir(packName, binaryName string) string {
	return findExistingPackDir(e.runtimeFS, e.dataDir, e.currentRuntimeDir(), packName, binaryName)
}

func (e *Engine) ensureBackendRuntimePack(ctx context.Context, targetBackend string) (string, error) {
	packName, binaryName, archiveName, modular := runtimePackSpecForBackend(targetBackend)
	if !modular {
		return e.currentRuntimeDir(), nil
	}

	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()

	if existing := e.findExistingPackDir(packName, binaryName); existing != "" {
		return existing, nil
	}
	if e.gatewayClient == nil || e.archive == nil || e.runtimeFS == nil {
		if cur := e.currentRuntimeDir(); cur != "" {
			return cur, nil
		}
		return "", derr.New(derr.CodeUpstreamFailure, 503, "modular runtime bootstrapper ports are not configured", nil)
	}

	baseURL, err := e.validateAndNormalizeGateway(e.currentGatewayURL())
	if err != nil {
		return "", err
	}
	return e.downloadAndExtractPackLocked(ctx, baseURL, packName, binaryName, archiveName)
}

func (e *Engine) downloadAndExtractPackLocked(ctx context.Context, baseURL, packName, binaryName, archiveName string) (string, error) {
	expectedSHA, err := e.gatewayClient.FetchRuntimeSHA256(ctx, baseURL, archiveName)
	if err != nil {
		return "", err
	}
	rawDownloadURL := baseURL + "/assets/" + archiveName
	downloadURL, err := e.validateGatewayAssetDownloadURL(rawDownloadURL, baseURL)
	if err != nil {
		return "", err
	}

	packParent := filepath.Join(e.dataDir, "runtimes", packName)
	stageParent := filepath.Join(packParent, e.newStagingDirName())
	defer e.cleanupRuntimePath(stageParent)
	stagingRoot, err := e.runtimeFS.PrepareStagingDir(stageParent)
	if err != nil {
		return "", err
	}
	archivePath := filepath.Join(stageParent, archiveName)

	e.addLog("Downloading %s Vulkan runtime pack from %s...", packName, downloadURL)
	if _, err := e.archive.DownloadVerified(ctx, downloadURL, archivePath, expectedSHA); err != nil {
		return "", fmt.Errorf("failed to download %s runtime pack: %w", packName, err)
	}

	if err := e.archive.ExtractArchive(archivePath, stagingRoot); err != nil {
		e.cleanupRuntimePath(stagingRoot)
		return "", err
	}
	targetDir := filepath.Join(packParent, "runtime-"+runtime.GOOS)
	if err := e.runtimeFS.ActivateStagedRuntime(stagingRoot, targetDir, binaryName); err != nil {
		return "", err
	}
	resolved := e.findExistingPackDir(packName, binaryName)
	if resolved == "" {
		return "", derr.New(derr.CodeUpstreamFailure, 502, binaryName+" missing after modular pack extraction", nil)
	}
	e.refreshProbedBackends(e.currentRuntimeDir())
	e.addLog("Modular %s runtime ready at %s", packName, resolved)
	return resolved, nil
}

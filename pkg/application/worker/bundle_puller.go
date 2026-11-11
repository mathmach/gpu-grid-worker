package worker

import (
	"context"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func (e *Engine) pullVerifiedSoloBundle(ctx context.Context, ctrl protocol.ControlMsg) (string, string, map[string]string, error) {
	if len(ctrl.BundleComponents) == 0 {
		path, digest, err := e.pullVerifiedSoloModel(ctx, ctrl)
		return path, digest, nil, err
	}
	if e.modelCache == nil {
		return "", "", nil, derr.New(derr.CodeUpstreamFailure, 503, "model cache verifier is not configured", nil)
	}

	_, _, baseURL, _, dataDir := e.snapshotDownloadPolicy()
	compPaths := make(map[string]string, len(ctrl.BundleComponents))
	manifestComps := make([]nodecore.BundleComponent, 0, len(ctrl.BundleComponents))
	var primaryPath string

	for _, comp := range ctrl.BundleComponents {
		localPath, verifiedSHA, err := e.pullSingleBundleComponent(ctx, dataDir, baseURL, ctrl.ModelID, comp)
		if err != nil {
			return "", "", nil, err
		}
		role := strings.TrimSpace(comp.Role)
		if role == "" {
			role = string(nodecore.InferComponentRoleFromName(comp.FileName))
		}
		compPaths[role] = localPath
		primaryPath = selectPrimaryComponentPath(primaryPath, role, localPath)
		manifestComps = append(manifestComps, nodecore.BundleComponent{
			Role:      nodecore.ComponentRole(role),
			FileName:  comp.FileName,
			FilePath:  localPath,
			SHA256:    verifiedSHA,
			SizeBytes: comp.SizeBytes,
		})
	}

	var parsedTask nodecore.ModelTask
	if strings.TrimSpace(ctrl.Task) != "" {
		var err error
		parsedTask, err = nodecore.ParseModelTask(ctrl.Task)
		if err != nil {
			return "", "", nil, err
		}
	}

	digest := strings.TrimSpace(ctrl.ModelSHA256)
	if digest == "" {
		digest = nodecore.ModelBundleManifest{
			BundleID:   ctrl.ModelID,
			Task:       parsedTask,
			Components: manifestComps,
		}.ComputeManifestDigest()
	}
	return primaryPath, digest, compPaths, nil
}

func (e *Engine) pullSingleBundleComponent(
	ctx context.Context,
	dataDir, baseURL, modelID string,
	comp protocol.BundleComponentWire,
) (string, string, error) {
	cacheKey := strings.TrimSpace(modelID)
	cleanSHA := strings.ToLower(strings.TrimSpace(comp.SHA256))
	if len(cleanSHA) >= 16 {
		cacheKey = "cas-" + cleanSHA[:16]
	}
	rawURL := strings.TrimSpace(comp.DownloadURL)
	if rawURL == "" {
		rawURL = protocol.BuildComponentDownloadURL(baseURL, modelID, comp.FileName)
	}
	downloadURL, err := e.validateDownloadURL(rawURL)
	if err != nil {
		return "", "", err
	}
	return e.modelCache.EnsureModelCached(ctx, dataDir, cacheKey, comp.FileName, downloadURL, cleanSHA, nil)
}

func selectPrimaryComponentPath(current, role, candidate string) string {
	if current == "" {
		return candidate
	}
	r := nodecore.ComponentRole(role)
	if r == nodecore.RoleDiffusionModel || r == nodecore.RolePrimary {
		return candidate
	}
	return current
}

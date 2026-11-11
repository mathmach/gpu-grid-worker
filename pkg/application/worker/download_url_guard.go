package worker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func isLocalOrPrivateGatewayURL(rawGW string) bool {
	norm, err := nodecore.NormalizeGatewayURL(rawGW)
	if err != nil {
		return false
	}
	parsed, err := url.Parse(norm)
	if err != nil {
		return false
	}
	return nodecore.IsLocalOrPrivateGatewayHost(parsed.Hostname())
}

func resolveDownloadCandidate(rawURL, cdnBase string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || cdnBase == "" {
		return trimmed
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.Contains(trimmed, "://") || strings.HasPrefix(trimmed, "//") {
		return trimmed
	}
	return strings.TrimRight(cdnBase, "/") + "/" + strings.TrimLeft(trimmed, "/")
}

func (e *Engine) snapshotDownloadPolicy() (gwURL, cdnBase, baseURL string, allowPlainHTTP bool, dataDir string) {
	e.mu.Lock()
	gwURL = e.gatewayURL
	cdnBase = e.cdnModelsBaseURL
	allowInsecure := e.allowInsecureWS
	dataDir = e.dataDir
	e.mu.Unlock()

	baseURL = gwURL
	if cdnBase != "" {
		baseURL = cdnBase
	}
	allowPlainHTTP = allowInsecure || isLocalOrPrivateGatewayURL(gwURL)
	return gwURL, cdnBase, baseURL, allowPlainHTTP, dataDir
}

func (e *Engine) validateDownloadURL(rawURL string) (string, error) {
	gwURL, cdnBase, _, allowPlainHTTP, _ := e.snapshotDownloadPolicy()
	candidate := resolveDownloadCandidate(rawURL, cdnBase)
	return nodecore.ValidateWorkerDownloadURL(candidate, gwURL, allowPlainHTTP)
}

func (e *Engine) validateGatewayAssetDownloadURL(rawURL, gatewayBaseURL string) (string, error) {
	e.mu.Lock()
	allowInsecure := e.allowInsecureWS
	e.mu.Unlock()
	allowPlainHTTP := allowInsecure || isLocalOrPrivateGatewayURL(gatewayBaseURL)
	return nodecore.ValidateWorkerDownloadURL(rawURL, gatewayBaseURL, allowPlainHTTP)
}

func (e *Engine) validateCatalogModelsDownloadURLs(models []protocol.CatalogModelView, gatewayBaseURL string) error {
	e.mu.Lock()
	cdnBase := e.cdnModelsBaseURL
	allowInsecure := e.allowInsecureWS
	e.mu.Unlock()
	allowPlainHTTP := allowInsecure || isLocalOrPrivateGatewayURL(gatewayBaseURL)
	for _, m := range models {
		for _, comp := range m.BundleComponents {
			if strings.TrimSpace(comp.DownloadURL) == "" {
				continue
			}
			candidate := resolveDownloadCandidate(comp.DownloadURL, cdnBase)
			if _, err := nodecore.ValidateWorkerDownloadURL(candidate, gatewayBaseURL, allowPlainHTTP); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) validateModelControlURLs(ctrl protocol.ControlMsg) error {
	gwURL, cdnBase, baseURL, allowPlainHTTP, _ := e.snapshotDownloadPolicy()
	if len(ctrl.BundleComponents) == 0 {
		rawURL := strings.TrimSpace(ctrl.ModelDownloadURL)
		if rawURL == "" {
			rawURL = protocol.BuildModelDownloadURL(baseURL, resolveModelCacheFileName(ctrl))
		}
		candidate := resolveDownloadCandidate(rawURL, cdnBase)
		_, err := nodecore.ValidateWorkerDownloadURL(candidate, gwURL, allowPlainHTTP)
		return err
	}
	for _, comp := range ctrl.BundleComponents {
		rawURL := strings.TrimSpace(comp.DownloadURL)
		if rawURL == "" {
			rawURL = protocol.BuildComponentDownloadURL(baseURL, ctrl.ModelID, comp.FileName)
		}
		candidate := resolveDownloadCandidate(rawURL, cdnBase)
		if _, err := nodecore.ValidateWorkerDownloadURL(candidate, gwURL, allowPlainHTTP); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) newStagingDirName() string {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("runtime-staging-%d", e.hostPID)
	}
	return fmt.Sprintf("runtime-staging-%d-%s", e.hostPID, hex.EncodeToString(rnd[:]))
}

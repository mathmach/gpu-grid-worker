package worker

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

const (
	networkBootstrapTimeout      = 2500 * time.Millisecond
	unconfiguredLocalGatewayStub = "http://127.0.0.1:8000"
)

type bootstrapFetchResult struct {
	manifest nodecore.NetworkBootstrapManifest
	ok       bool
}

func (e *Engine) initBootstrapConfig(boot BootConfig) {
	e.bootstrapPubKeys = resolveBootTrustedKeys(boot.BootstrapPublicKeys)
	e.bootstrapURLs = resolveBootDiscoveryURLs(boot.BootstrapURLs, e.allowInsecureWS, e.bootstrap != nil)
}

func resolveBootTrustedKeys(configured []string) []string {
	norm, err := nodecore.NormalizeBootstrapPublicKeyList(configured)
	if err == nil && len(norm) > 0 {
		return norm
	}
	return []string{nodecore.DefaultNetworkBootstrapPublicKeyHex}
}

func resolveBootDiscoveryURLs(configured []string, allowLocal, hasPort bool) []string {
	if len(configured) > 0 {
		if norm, err := nodecore.NormalizeBootstrapDiscoveryURLList(configured, allowLocal); err == nil && len(norm) > 0 {
			return norm
		}
	}
	if hasPort {
		return nodecore.DefaultNetworkBootstrapURLs()
	}
	return nil
}

func (e *Engine) RefreshNetworkBootstrap(ctx context.Context) (string, bool) {
	if e.bootstrap == nil {
		return e.currentGatewayURL(), false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.bootstrapMu.Lock()
	defer e.bootstrapMu.Unlock()

	curGW, candidates, opts, skip := e.prepareBootstrapRefresh()
	if skip || len(candidates) == 0 {
		return curGW, false
	}
	best, found := e.fetchHighestValidBootstrapManifest(ctx, candidates, opts)
	if !found {
		return curGW, false
	}
	return e.commitBootstrapManifest(best)
}

func (e *Engine) prepareBootstrapRefresh() (string, []string, nodecore.VerifyBootstrapOptions, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.bootGatewayPinned || e.gatewayPinned {
		return e.gatewayURL, nil, nodecore.VerifyBootstrapOptions{}, true
	}
	opts := nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: append([]string(nil), e.bootstrapPubKeys...),
		MinVersion:        e.bootstrapVersion,
		Now:               time.Now().UTC(),
		AllowLocalTargets: e.allowInsecureWS,
	}
	return e.gatewayURL, e.buildBootstrapCandidateURLsLocked(), opts, false
}

func (e *Engine) buildBootstrapCandidateURLsLocked() []string {
	seen := make(map[string]bool, 1+len(e.discoveredBootstrapURLs)+len(e.bootstrapURLs))
	var out []string
	appendUnique := func(u string) {
		if u != "" && !seen[u] && len(out) < nodecore.MaxNetworkBootstrapMirrors+1 {
			seen[u] = true
			out = append(out, u)
		}
	}
	if canProbeGatewayWellKnown(e.gatewayURL, e.allowInsecureWS) {
		appendUnique(strings.TrimRight(e.gatewayURL, "/") + nodecore.WellKnownNetworkBootstrapPath)
	}
	for _, u := range e.discoveredBootstrapURLs {
		appendUnique(u)
	}
	for _, u := range e.bootstrapURLs {
		appendUnique(u)
	}
	return out
}

func canProbeGatewayWellKnown(gwURL string, allowLocal bool) bool {
	clean := strings.TrimSpace(gwURL)
	if clean == "" || strings.EqualFold(clean, unconfiguredLocalGatewayStub) {
		return false
	}
	u, err := url.Parse(clean)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") && !nodecore.IsLocalOrPrivateGatewayHost(u.Hostname()) {
		return true
	}
	return allowLocal && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https"))
}

func (e *Engine) fetchHighestValidBootstrapManifest(
	ctx context.Context,
	candidates []string,
	opts nodecore.VerifyBootstrapOptions,
) (nodecore.NetworkBootstrapManifest, bool) {
	fetchCtx, cancel := context.WithTimeout(ctx, networkBootstrapTimeout)
	defer cancel()

	results := make(chan bootstrapFetchResult, len(candidates))
	var wg sync.WaitGroup
	for _, targetURL := range candidates {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			results <- e.fetchSingleBootstrapCandidate(fetchCtx, u, opts)
		}(targetURL)
	}
	wg.Wait()
	close(results)

	var best nodecore.NetworkBootstrapManifest
	found := false
	for res := range results {
		if res.ok && (!found || res.manifest.Version > best.Version) {
			best = res.manifest
			found = true
		}
	}
	return best, found
}

func (e *Engine) fetchSingleBootstrapCandidate(
	ctx context.Context,
	targetURL string,
	opts nodecore.VerifyBootstrapOptions,
) bootstrapFetchResult {
	raw, err := e.bootstrap.FetchNetworkBootstrap(ctx, targetURL)
	if err != nil {
		e.logger.Debug("network_bootstrap_fetch_failed", "url", targetURL, "error", err.Error())
		return bootstrapFetchResult{}
	}
	doc, err := nodecore.VerifyNetworkBootstrapManifest(raw, opts)
	if err != nil {
		e.logger.Warn("network_bootstrap_verify_rejected", "url", targetURL, "error", err.Error())
		return bootstrapFetchResult{}
	}
	return bootstrapFetchResult{manifest: doc, ok: true}
}

func (e *Engine) commitBootstrapManifest(best nodecore.NetworkBootstrapManifest) (string, bool) {
	e.mu.Lock()
	if e.bootGatewayPinned || e.gatewayPinned || best.Version < e.bootstrapVersion {
		cur := e.gatewayURL
		e.mu.Unlock()
		return cur, false
	}
	urlChanged := best.GatewayURL != e.gatewayURL
	verAdvanced := best.Version > e.bootstrapVersion
	mirrorsChanged := len(best.BootstrapURLs) > 0 && !slices.Equal(best.BootstrapURLs, e.discoveredBootstrapURLs)
	changed := urlChanged || verAdvanced || mirrorsChanged

	e.gatewayURL = best.GatewayURL
	e.bootstrapVersion = best.Version
	if len(best.BootstrapURLs) > 0 {
		e.discoveredBootstrapURLs = append([]string(nil), best.BootstrapURLs...)
	}
	resolvedGW := e.gatewayURL
	e.mu.Unlock()

	if !changed {
		return resolvedGW, false
	}
	e.persistConfig()
	if urlChanged {
		e.addLog("Network bootstrap updated Gateway endpoint -> %s (version %d)", resolvedGW, best.Version)
	} else {
		e.notifyObservers()
	}
	return resolvedGW, true
}

func (e *Engine) resolveMigratedReconnectEndpoint(ctx context.Context, prevGW string) (string, string, bool) {
	nextGW, _ := e.RefreshNetworkBootstrap(ctx)
	if nextGW == "" || nextGW == prevGW {
		return prevGW, "", false
	}
	nextWS, err := e.resolveWorkerWSURL(nextGW)
	if err != nil {
		e.logger.Warn("network_bootstrap_ws_url_invalid", "gatewayUrl", nextGW, "error", err.Error())
		return prevGW, "", false
	}
	return nextGW, nextWS, true
}

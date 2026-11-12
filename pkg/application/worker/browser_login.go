package worker

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

const (
	browserAuthChallengeTTL = 5 * time.Minute
	browserAuthPollRetry    = 350 * time.Millisecond
)

type pendingBrowserAuth struct {
	state        string
	codeVerifier string
	gatewayURL   string
	authURL      string
	expiresAt    time.Time
	cancel       context.CancelFunc
}

func (e *Engine) StartBrowserLogin(gatewayURLOverride string) (string, error) {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	normGW, pinGateway, machineID, workerName, port, err := e.prepareBrowserLoginContext(gatewayURLOverride)
	if err != nil {
		e.setLastError(err.Error())
		return "", err
	}
	stateHex, err := nodecore.RandomHex(24)
	if err != nil {
		return "", derr.Wrap(derr.CodeInternal, 500, "failed to generate OAuth state nonce", err)
	}
	verifier, challenge, err := nodecore.GeneratePKCEPair()
	if err != nil {
		return "", derr.Wrap(derr.CodeInternal, 500, "failed to generate PKCE challenge", err)
	}

	authURL := buildGatewayWorkerAuthorizeURL(normGW, stateHex, challenge, port, machineID, workerName)
	expiresAt := time.Now().UTC().Add(browserAuthChallengeTTL)
	pollCtx, cancelPoll := context.WithDeadline(context.Background(), expiresAt)

	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		cancelPoll()
		return "", derr.New(derr.CodeIllegalState, 409, "stop the worker before changing the linked account", nil)
	}
	prevCancel := e.clearPendingBrowserAuthLocked()
	lang := e.language
	if pinGateway {
		e.gatewayPinned = true
	}
	e.gatewayURL = normGW
	e.lastCompletedAuthState = ""
	e.browserAuth = &pendingBrowserAuth{
		state:        stateHex,
		codeVerifier: verifier,
		gatewayURL:   normGW,
		authURL:      authURL,
		expiresAt:    expiresAt,
		cancel:       cancelPoll,
	}
	e.lastError = ""
	e.mu.Unlock()

	cancelIfNonNil(prevCancel)
	if e.portal != nil {
		go e.runBrowserAuthPollLoop(pollCtx, normGW, stateHex, verifier, lang)
	}
	e.notifyObservers()

	if e.browser != nil {
		if launchErr := e.browser.OpenExternalURL(authURL); launchErr != nil {
			e.mu.Lock()
			abortCancel := e.clearPendingBrowserAuthLocked()
			e.lastError = launchErr.Error()
			e.mu.Unlock()
			cancelIfNonNil(abortCancel)
			e.notifyObservers()
			return authURL, launchErr
		}
	}
	return authURL, nil
}

func (e *Engine) ReopenBrowserLogin() error {
	e.mu.Lock()
	var authURL string
	if e.browserAuth != nil && time.Now().UTC().Before(e.browserAuth.expiresAt) {
		authURL = e.browserAuth.authURL
	}
	e.mu.Unlock()

	if authURL == "" {
		_, err := e.StartBrowserLogin("")
		return err
	}
	if e.browser == nil {
		return derr.New(derr.CodeInternal, 500, "browser launcher is not configured", nil)
	}
	if err := e.browser.OpenExternalURL(authURL); err != nil {
		e.setLastError(err.Error())
		return err
	}
	return nil
}

func (e *Engine) CancelBrowserLogin() {
	e.mu.Lock()
	hadPending := e.browserAuth != nil
	cancel := e.clearPendingBrowserAuthLocked()
	e.mu.Unlock()

	cancelIfNonNil(cancel)
	if hadPending {
		e.notifyObservers()
	}
}

func (e *Engine) clearPendingBrowserAuthLocked() context.CancelFunc {
	if e.browserAuth == nil {
		return nil
	}
	cancel := e.browserAuth.cancel
	e.browserAuth = nil
	return cancel
}

func (e *Engine) CompleteBrowserLogin(ctx context.Context, rawCode, rawState string) (StatusSnapshot, error) {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	if e.portal == nil {
		return StatusSnapshot{}, derr.New(derr.CodeUpstreamFailure, 503, "worker portal port is not configured", nil)
	}
	if snap, ok := e.consumeCompletedBrowserAuthSnapshot(rawCode, rawState); ok {
		return snap, nil
	}
	pending, lang, err := e.consumeMatchingBrowserAuth(rawCode, rawState)
	if err != nil {
		return StatusSnapshot{}, err
	}
	cancelIfNonNil(pending.cancel)

	authRes, err := e.portal.ExchangeWorkerAuthCode(ctx, pending.gatewayURL, strings.TrimSpace(rawCode), pending.codeVerifier, lang)
	if err != nil {
		if snap, ok := e.consumeCompletedBrowserAuthSnapshot(rawCode, rawState); ok {
			return snap, nil
		}
		e.setLastError(err.Error())
		return StatusSnapshot{}, err
	}
	return e.finalizeBrowserLinkedAccount(pending.gatewayURL, authRes)
}

func (e *Engine) consumeCompletedBrowserAuthSnapshot(rawCode, rawState string) (StatusSnapshot, bool) {
	cleanCode := strings.TrimSpace(rawCode)
	cleanState := strings.TrimSpace(rawState)
	if cleanCode == "" || cleanState == "" {
		return StatusSnapshot{}, false
	}
	e.mu.Lock()
	matched := e.browserAuth == nil &&
		e.lastCompletedAuthState != "" &&
		nodecore.ConstantTimeTokenEqual(e.lastCompletedAuthState, cleanState) &&
		strings.TrimSpace(e.workerToken) != ""
	if matched {
		e.lastCompletedAuthState = ""
	}
	e.mu.Unlock()
	if !matched {
		return StatusSnapshot{}, false
	}
	return e.Snapshot(), true
}

func (e *Engine) OpenWithdrawPage() error {
	e.RefreshNetworkBootstrap(context.Background())
	e.mu.Lock()
	gw := e.gatewayURL
	e.mu.Unlock()

	normGW, err := e.validateAndNormalizeGateway(gw)
	if err != nil {
		e.setLastError(err.Error())
		return err
	}
	if e.browser == nil {
		return derr.New(derr.CodeInternal, 500, "browser launcher is not configured", nil)
	}
	target := normGW + "/?tab=wallet#withdraw"
	if err := e.browser.OpenExternalURL(target); err != nil {
		e.setLastError(err.Error())
		return err
	}
	return nil
}

func (e *Engine) runBrowserAuthPollLoop(ctx context.Context, gwURL, state, verifier, lang string) {
	for ctx.Err() == nil {
		if !e.pollSingleBrowserAuthAttempt(ctx, gwURL, state, verifier, lang) {
			break
		}
	}
	if ctx.Err() != nil {
		e.clearMatchingBrowserAuthOnError(state, "")
	}
}

func (e *Engine) pollSingleBrowserAuthAttempt(ctx context.Context, gwURL, state, verifier, lang string) bool {
	t0 := time.Now()
	authRes, ready, err := e.portal.PollWorkerAuthState(ctx, gwURL, state, verifier, lang)
	if err == nil && ready {
		e.completePolledBrowserLogin(state, gwURL, authRes)
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		if e.shouldAbortBrowserPollOnError(state, err) {
			return false
		}
		return sleepWithContext(ctx, browserAuthPollRetry)
	}
	if time.Since(t0) < browserAuthPollRetry {
		return sleepWithContext(ctx, browserAuthPollRetry)
	}
	return true
}

func (e *Engine) shouldAbortBrowserPollOnError(state string, err error) bool {
	appErr, ok := derr.AsAppError(err)
	if !ok {
		return false
	}
	if appErr.HTTPStatus == 404 {
		return true
	}
	if appErr.HTTPStatus == 400 || appErr.HTTPStatus == 401 || appErr.HTTPStatus == 403 {
		e.clearMatchingBrowserAuthOnError(state, err.Error())
		return true
	}
	return false
}

func (e *Engine) clearMatchingBrowserAuthOnError(state, errMsg string) {
	e.mu.Lock()
	var cancel context.CancelFunc
	matched := e.browserAuth != nil && nodecore.ConstantTimeTokenEqual(e.browserAuth.state, state)
	if matched {
		cancel = e.clearPendingBrowserAuthLocked()
		if errMsg != "" {
			e.lastError = errMsg
		}
	}
	e.mu.Unlock()

	cancelIfNonNil(cancel)
	if matched {
		e.notifyObservers()
	}
}

func (e *Engine) completePolledBrowserLogin(state, gwURL string, authRes nodecore.WorkerAccountAuth) {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	e.mu.Lock()
	if e.running || e.browserAuth == nil || !nodecore.ConstantTimeTokenEqual(e.browserAuth.state, state) {
		e.mu.Unlock()
		return
	}
	e.lastCompletedAuthState = state
	e.mu.Unlock()

	if _, err := e.finalizeBrowserLinkedAccount(gwURL, authRes); err != nil {
		e.setLastError(err.Error())
	}
}

func (e *Engine) finalizeBrowserLinkedAccount(gwURL string, authRes nodecore.WorkerAccountAuth) (StatusSnapshot, error) {
	tok := strings.TrimSpace(authRes.WorkerToken)
	if tok == "" {
		return StatusSnapshot{}, derr.New(derr.CodeUpstreamFailure, 502, "gateway login did not return a worker token", nil)
	}
	resolvedEmail := strings.TrimSpace(authRes.AccountEmail)

	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return StatusSnapshot{}, derr.New(derr.CodeIllegalState, 409, "stop the worker before changing the linked account", nil)
	}
	if e.workerToken != tok {
		e.balance = 0
		e.minObservedBalance = 0
		e.lastWorkerStateSeq = 0
		e.lastStateSeq = 0
	}
	e.gatewayURL = gwURL
	e.workerToken = tok
	e.accountEmail = resolvedEmail
	authCancel := e.clearPendingBrowserAuthLocked()
	e.lastError = ""
	e.mu.Unlock()

	cancelIfNonNil(authCancel)
	e.persistConfig()
	e.addLog("Account %s linked via browser login.", resolvedEmail)
	go e.warmHardwareAndCatalogAfterLogin(gwURL)
	return e.Snapshot(), nil
}

func (e *Engine) prepareBrowserLoginContext(gatewayURLOverride string) (string, bool, string, string, int, error) {
	e.mu.Lock()
	running := e.running
	e.mu.Unlock()
	if running {
		return "", false, "", "", 0, derr.New(derr.CodeIllegalState, 409, "stop the worker before changing the linked account", nil)
	}
	explicitOverride := strings.TrimSpace(gatewayURLOverride) != ""
	if !explicitOverride {
		e.RefreshNetworkBootstrap(context.Background())
	}
	e.mu.Lock()
	fallbackGW := e.gatewayURL
	machineID := e.machineID
	workerName := e.workerName
	port := e.controlPort
	e.mu.Unlock()

	targetGW := cmp.Or(strings.TrimSpace(gatewayURLOverride), fallbackGW)
	normGW, err := e.validateAndNormalizeGateway(targetGW)
	if err != nil {
		return "", false, "", "", 0, err
	}
	if _, wsErr := e.resolveWorkerWSURL(normGW); wsErr != nil {
		return "", false, "", "", 0, wsErr
	}
	if port <= 0 {
		port = nodecore.DefaultWorkerControlPort
	}
	return normGW, explicitOverride && normGW != fallbackGW, machineID, workerName, port, nil
}

func (e *Engine) consumeMatchingBrowserAuth(rawCode, rawState string) (pendingBrowserAuth, string, error) {
	cleanCode := strings.TrimSpace(rawCode)
	cleanState := strings.TrimSpace(rawState)
	if cleanCode == "" || cleanState == "" {
		return pendingBrowserAuth{}, "", derr.New(derr.CodeValidation, 400, "missing authorization code or state", nil)
	}

	now := time.Now().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.running {
		return pendingBrowserAuth{}, "", derr.New(derr.CodeIllegalState, 409, "stop the worker before changing the linked account", nil)
	}
	if e.browserAuth == nil || !now.Before(e.browserAuth.expiresAt) {
		if e.browserAuth != nil {
			cancelIfNonNil(e.browserAuth.cancel)
		}
		e.browserAuth = nil
		return pendingBrowserAuth{}, "", derr.New(derr.CodeUnauthorized, 401, "browser login session expired or not started", nil)
	}
	if !nodecore.ConstantTimeTokenEqual(cleanState, e.browserAuth.state) {
		return pendingBrowserAuth{}, "", derr.New(derr.CodeForbidden, 403, "invalid OAuth state parameter", nil)
	}
	matched := *e.browserAuth
	e.browserAuth = nil
	return matched, e.language, nil
}

func (e *Engine) warmHardwareAndCatalogAfterLogin(gwURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := e.RefreshCatalog(ctx, gwURL); err != nil {
		e.logger.Debug("post_login_catalog_prefetch_failed", "error", err.Error())
	}
	e.mu.Lock()
	hasGPUs := len(e.detectedGPUs) > 0
	e.mu.Unlock()
	if !hasGPUs && e.hardware != nil && e.ResolveExistingRuntimeDir() != "" {
		if err := e.ScanGPUs(ctx, gwURL); err != nil {
			e.logger.Debug("post_login_gpu_scan_failed", "error", err.Error())
		}
	}
}

func buildGatewayWorkerAuthorizeURL(normGW, state, challenge string, port int, machineID, workerName string) string {
	q := url.Values{}
	q.Set("worker_auth", "1")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("port", fmt.Sprintf("%d", port))
	if strings.TrimSpace(machineID) != "" {
		q.Set("machine_id", strings.TrimSpace(machineID))
	}
	if strings.TrimSpace(workerName) != "" {
		q.Set("machine_name", strings.TrimSpace(workerName))
	}
	return normGW + "/?" + q.Encode()
}

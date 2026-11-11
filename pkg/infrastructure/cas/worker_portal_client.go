package cas

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func (c *HTTPGatewayClient) ExchangeWorkerAuthCode(
	ctx context.Context,
	gwURL, code, codeVerifier, lang string,
) (nodecore.WorkerAccountAuth, error) {
	norm, err := workerconfig.NormalizeGatewayURL(gwURL)
	if err != nil {
		return nodecore.WorkerAccountAuth{}, err
	}
	reqBody, err := json.Marshal(struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"codeVerifier"`
	}{
		Code:         strings.TrimSpace(code),
		CodeVerifier: strings.TrimSpace(codeVerifier),
	})
	if err != nil {
		return nodecore.WorkerAccountAuth{}, derr.Wrap(derr.CodeValidation, 400, "invalid auth exchange payload", err)
	}
	exCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(exCtx, http.MethodPost, norm+"/api/auth/worker-cli/exchange", bytes.NewReader(reqBody))
	if err != nil {
		return nodecore.WorkerAccountAuth{}, derr.Wrap(derr.CodeValidation, 400, "invalid gateway exchange URL", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cleanLang := strings.TrimSpace(lang); cleanLang != "" {
		req.Header.Set("X-Language", cleanLang)
	}
	resp, err := c.authHTTPClient().Do(req)
	if err != nil {
		return nodecore.WorkerAccountAuth{}, derr.Wrap(derr.CodeUpstreamFailure, 502, "failed to reach Gateway auth exchange endpoint", err)
	}
	defer closeBodyQuietly(resp.Body)
	bounded := io.LimitReader(resp.Body, 1<<20)
	if resp.StatusCode != http.StatusOK {
		return nodecore.WorkerAccountAuth{}, decodeGatewayAuthError(resp.StatusCode, bounded)
	}
	return decodeGatewayAuthSuccess(bounded)
}

func (c *HTTPGatewayClient) PollWorkerAuthState(
	ctx context.Context,
	gwURL, state, codeVerifier, lang string,
) (nodecore.WorkerAccountAuth, bool, error) {
	norm, err := workerconfig.NormalizeGatewayURL(gwURL)
	if err != nil {
		return nodecore.WorkerAccountAuth{}, false, err
	}
	reqBody, err := json.Marshal(struct {
		State        string `json:"state"`
		CodeVerifier string `json:"codeVerifier"`
	}{
		State:        strings.TrimSpace(state),
		CodeVerifier: strings.TrimSpace(codeVerifier),
	})
	if err != nil {
		return nodecore.WorkerAccountAuth{}, false, derr.Wrap(derr.CodeValidation, 400, "invalid auth poll payload", err)
	}
	pCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(pCtx, http.MethodPost, norm+"/api/auth/worker-cli/poll", bytes.NewReader(reqBody))
	if err != nil {
		return nodecore.WorkerAccountAuth{}, false, derr.Wrap(derr.CodeValidation, 400, "invalid gateway poll URL", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cleanLang := strings.TrimSpace(lang); cleanLang != "" {
		req.Header.Set("X-Language", cleanLang)
	}
	resp, err := c.pollHTTPClient().Do(req)
	if err != nil {
		return nodecore.WorkerAccountAuth{}, false, derr.Wrap(derr.CodeUpstreamFailure, 502, "failed to reach Gateway auth poll endpoint", err)
	}
	defer closeBodyQuietly(resp.Body)
	bounded := io.LimitReader(resp.Body, 1<<20)
	if resp.StatusCode == http.StatusAccepted {
		return nodecore.WorkerAccountAuth{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nodecore.WorkerAccountAuth{}, false, decodeGatewayAuthError(resp.StatusCode, bounded)
	}
	acc, err := decodeGatewayAuthSuccess(bounded)
	if err != nil {
		return nodecore.WorkerAccountAuth{}, false, err
	}
	return acc, true, nil
}

func (c *HTTPGatewayClient) ResolvePreDownloadArtifacts(
	ctx context.Context,
	gwURL, workerToken string,
	modelIDs []string,
) ([]protocol.WorkerModelArtifactView, error) {
	norm, err := workerconfig.NormalizeGatewayURL(gwURL)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(struct {
		ModelIDs []string `json:"modelIds"`
	}{ModelIDs: modelIDs})
	if err != nil {
		return nil, derr.Wrap(derr.CodeValidation, 400, "invalid model resolve request payload", err)
	}
	resCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(resCtx, http.MethodPost, norm+"/api/worker/models/resolve", bytes.NewReader(reqBody))
	if err != nil {
		return nil, derr.Wrap(derr.CodeValidation, 400, "invalid gateway model resolve URL", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(workerToken))

	resp, err := c.catalogHTTPClient().Do(req)
	if err != nil {
		return nil, derr.Wrap(derr.CodeUpstreamFailure, 502, "failed to reach Gateway model resolve endpoint", err)
	}
	defer closeBodyQuietly(resp.Body)
	bounded := io.LimitReader(resp.Body, 4<<20)
	if resp.StatusCode != http.StatusOK {
		return nil, decodeGatewayAuthError(resp.StatusCode, bounded)
	}
	var payload struct {
		Artifacts []protocol.WorkerModelArtifactView `json:"artifacts"`
	}
	if err := json.NewDecoder(bounded).Decode(&payload); err != nil {
		return nil, derr.Wrap(derr.CodeUpstreamFailure, 502, "invalid gateway model resolve JSON response", err)
	}
	return payload.Artifacts, nil
}

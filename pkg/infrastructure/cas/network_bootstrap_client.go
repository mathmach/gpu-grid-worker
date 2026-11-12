package cas

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

const bootstrapRequestTimeout = 2500 * time.Millisecond

func (c *HTTPGatewayClient) bootstrapHTTPClient() *http.Client {
	if c != nil && c.bootstrapClient != nil {
		return c.bootstrapClient
	}
	return newSafeHTTPClient(bootstrapRequestTimeout)
}

func (c *HTTPGatewayClient) FetchNetworkBootstrap(ctx context.Context, targetURL string) ([]byte, error) {
	safeCtx, parsedURL, err := ValidateFetchURL(ctx, targetURL)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(safeCtx, bootstrapRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, derr.Wrap(derr.CodeValidation, 400, "invalid network bootstrap request URL", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.bootstrapHTTPClient().Do(req)
	if err != nil {
		return nil, derr.Wrap(derr.CodeUpstreamFailure, 502, "failed to fetch network bootstrap manifest", err)
	}
	defer closeBodyQuietly(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, derr.New(derr.CodeUpstreamFailure, resp.StatusCode, fmt.Sprintf("network bootstrap mirror returned HTTP %d", resp.StatusCode), nil)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, nodecore.MaxNetworkBootstrapManifestBytes+1))
	if err != nil {
		return nil, derr.Wrap(derr.CodeUpstreamFailure, 502, "failed to read network bootstrap body", err)
	}
	if len(raw) == 0 || len(raw) > nodecore.MaxNetworkBootstrapManifestBytes {
		return nil, derr.New(derr.CodeValidation, 400, "network bootstrap manifest exceeds 64 KiB limit", nil)
	}
	return raw, nil
}

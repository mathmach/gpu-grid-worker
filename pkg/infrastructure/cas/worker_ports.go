package cas

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type WorkerCASAdapter struct {
	verifier *RuntimeArchiveVerifier
	client   *http.Client
}

func NewWorkerCASAdapter(verifier *RuntimeArchiveVerifier, client *http.Client) *WorkerCASAdapter {
	if verifier == nil {
		verifier = NewRuntimeArchiveVerifier()
	}
	return &WorkerCASAdapter{verifier: verifier, client: client}
}

func (a *WorkerCASAdapter) maxDownloadBytes() int64 {
	if a != nil && a.verifier != nil && a.verifier.maxDownloadBytes > 0 {
		return a.verifier.maxDownloadBytes
	}
	return DefaultMaxDownloadBytes
}

func (a *WorkerCASAdapter) DownloadVerified(ctx context.Context, downloadURL, destPath, expectedSHA256 string) (string, error) {
	var cli *http.Client
	if a != nil {
		cli = a.client
	}
	return downloadVerifiedBounded(ctx, cli, downloadURL, destPath, expectedSHA256, a.maxDownloadBytes(), nil)
}

func (a *WorkerCASAdapter) ExtractArchive(archivePath, destDir string) error {
	if a != nil && a.verifier != nil {
		return a.verifier.ExtractArchive(archivePath, destDir)
	}
	return NewRuntimeArchiveVerifier().ExtractArchive(archivePath, destDir)
}

func (a *WorkerCASAdapter) EnsureModelCached(ctx context.Context, cacheRoot, modelID, fileName, downloadURL, expectedSHA256 string, onProgress func(downloaded, total int64)) (string, string, error) {
	resolvedName := strings.TrimSpace(fileName)
	if resolvedName == "" && strings.TrimSpace(modelID) != "" {
		resolvedName = strings.TrimSpace(modelID) + ".gguf"
	}
	var cli *http.Client
	if a != nil {
		cli = a.client
	}
	return ensureModelCachedBoundedWithProgress(ctx, cli, downloadURL, cacheRoot, expectedSHA256, resolvedName, a.maxDownloadBytes(), onProgress)
}

func (a *WorkerCASAdapter) ListCachedDigests(cacheRoot string) ([]string, error) {
	return ListCachedDigests(cacheRoot)
}

func (a *WorkerCASAdapter) AvailableDiskMiB(cacheRoot string) (int, error) {
	return AvailableDiskMiB(cacheRoot)
}

type HTTPGatewayClient struct {
	manifestClient *http.Client
	catalogClient  *http.Client
	authClient     *http.Client
	pollClient     *http.Client
}

func NewGatewayClient() *HTTPGatewayClient {
	return &HTTPGatewayClient{
		manifestClient: newSafeHTTPClient(4 * time.Second),
		catalogClient:  newSafeHTTPClient(6 * time.Second),
		authClient:     newSafeHTTPClient(8 * time.Second),
		pollClient:     newSafeHTTPClient(18 * time.Second),
	}
}

func (c *HTTPGatewayClient) NormalizeGatewayURL(raw string) (string, error) {
	return workerconfig.NormalizeGatewayURL(raw)
}

func (c *HTTPGatewayClient) BuildWorkerWSURL(gwURL string, allowInsecureWS bool) (string, error) {
	norm, err := workerconfig.NormalizeGatewayURL(gwURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(norm)
	if err != nil {
		return "", derr.Validationf(derr.CodeValidation, "invalid gateway URL %q", gwURL)
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return "wss://" + strings.TrimPrefix(norm, "https://") + "/ws/worker", nil
	}
	if !allowInsecureWS && !workerconfig.IsLocalOrPrivateLANHost(parsed.Hostname()) {
		return "", derr.New(derr.CodeValidation, 400, "public WAN gateway requires wss:// transport unless insecure WS is explicitly enabled", nil)
	}
	return "ws://" + strings.TrimPrefix(norm, "http://") + "/ws/worker", nil
}

func (c *HTTPGatewayClient) FetchRuntimeSHA256(ctx context.Context, gwURL, archiveName string) (string, error) {
	norm, err := workerconfig.NormalizeGatewayURL(gwURL)
	if err != nil {
		return "", err
	}
	mCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(mCtx, http.MethodGet, norm+"/api/runtime/manifest", nil)
	if err != nil {
		return "", derr.Wrap(derr.CodeDigestMismatch, 502, "gateway runtime manifest did not provide a valid SHA-256 digest", err)
	}
	resp, err := c.manifestHTTPClient().Do(req)
	if err != nil {
		return "", derr.Wrap(derr.CodeDigestMismatch, 502, "gateway runtime manifest did not provide a valid SHA-256 digest", err)
	}
	defer closeBodyQuietly(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", derr.New(derr.CodeDigestMismatch, 502, "gateway runtime manifest did not provide a valid SHA-256 digest", nil)
	}
	return decodeManifestArchiveDigest(io.LimitReader(resp.Body, 4<<20), archiveName)
}

func decodeManifestArchiveDigest(body io.Reader, archiveName string) (string, error) {
	var payload struct {
		Artifacts map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return "", derr.Wrap(derr.CodeDigestMismatch, 502, "gateway runtime manifest did not provide a valid SHA-256 digest", err)
	}
	if art, ok := payload.Artifacts[archiveName]; ok {
		return normalizeDigestHex(art.SHA256)
	}
	return "", derr.New(derr.CodeDigestMismatch, 502, "gateway runtime manifest did not provide a valid SHA-256 digest", nil)
}

func (c *HTTPGatewayClient) FetchCatalogModels(ctx context.Context, gwURL string) ([]protocol.CatalogModelView, error) {
	norm, err := workerconfig.NormalizeGatewayURL(gwURL)
	if err != nil {
		return nil, err
	}
	cli := c.catalogHTTPClient()
	models, statusCode, apiErr := fetchAPIModelsEndpoint(ctx, cli, norm+"/api/models")
	if apiErr == nil {
		return models, nil
	}
	if statusCode != http.StatusNotFound {
		return nil, apiErr
	}
	return fetchOpenAIModelsEndpoint(ctx, cli, norm+"/v1/models")
}

func (c *HTTPGatewayClient) manifestHTTPClient() *http.Client {
	if c != nil && c.manifestClient != nil {
		return c.manifestClient
	}
	return newSafeHTTPClient(4 * time.Second)
}

func (c *HTTPGatewayClient) catalogHTTPClient() *http.Client {
	if c != nil && c.catalogClient != nil {
		return c.catalogClient
	}
	return newSafeHTTPClient(6 * time.Second)
}

func (c *HTTPGatewayClient) authHTTPClient() *http.Client {
	if c != nil && c.authClient != nil {
		return c.authClient
	}
	return newSafeHTTPClient(8 * time.Second)
}

func (c *HTTPGatewayClient) pollHTTPClient() *http.Client {
	if c != nil && c.pollClient != nil {
		return c.pollClient
	}
	return newSafeHTTPClient(18 * time.Second)
}

func decodeGatewayAuthError(statusCode int, body io.Reader) error {
	var errPayload struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(body).Decode(&errPayload); err != nil || strings.TrimSpace(errPayload.Error) == "" {
		return derr.New(defaultAuthErrorCode(statusCode), statusCode, fmt.Sprintf("gateway login failed (HTTP %d)", statusCode), nil)
	}
	code := derr.Code(strings.TrimSpace(errPayload.Code))
	if code == "" {
		code = defaultAuthErrorCode(statusCode)
	}
	return derr.New(code, statusCode, strings.TrimSpace(errPayload.Error), nil)
}

func defaultAuthErrorCode(statusCode int) derr.Code {
	switch statusCode {
	case http.StatusUnauthorized:
		return derr.CodeInvalidCredentials
	case http.StatusTooManyRequests:
		return derr.CodeRateLimited
	case http.StatusForbidden:
		return derr.CodeForbidden
	default:
		if statusCode >= 400 && statusCode < 500 {
			return derr.CodeValidation
		}
		return derr.CodeUpstreamFailure
	}
}

func decodeGatewayAuthSuccess(body io.Reader) (nodecore.WorkerAccountAuth, error) {
	var payload struct {
		Account struct {
			Email       string `json:"email"`
			WorkerToken string `json:"workerToken"`
		} `json:"account"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nodecore.WorkerAccountAuth{}, derr.Wrap(derr.CodeUpstreamFailure, 502, "invalid gateway login JSON response", err)
	}
	tok := strings.TrimSpace(payload.Account.WorkerToken)
	if tok == "" {
		return nodecore.WorkerAccountAuth{}, derr.New(derr.CodeUpstreamFailure, 502, "gateway login response did not include a worker token", nil)
	}
	return nodecore.WorkerAccountAuth{
		AccountEmail: cmp.Or(strings.TrimSpace(payload.Account.Email), ""),
		WorkerToken:  tok,
	}, nil
}

func fetchAPIModelsEndpoint(ctx context.Context, client *http.Client, endpoint string) ([]protocol.CatalogModelView, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer closeBodyQuietly(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Models []protocol.CatalogModelView `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, resp.StatusCode, err
	}
	return payload.Models, resp.StatusCode, nil
}

func fetchOpenAIModelsEndpoint(ctx context.Context, client *http.Client, endpoint string) ([]protocol.CatalogModelView, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer closeBodyQuietly(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway model catalog returned HTTP %d", resp.StatusCode)
	}
	return decodeOpenAICatalogPayload(io.LimitReader(resp.Body, 4<<20))
}

func decodeOpenAICatalogPayload(body io.Reader) ([]protocol.CatalogModelView, error) {
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			TargetVramMiB int    `json:"target_vram_mib"`
			Active        bool   `json:"active"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]protocol.CatalogModelView, 0, len(payload.Data))
	for _, item := range payload.Data {
		norm := nodecore.NormalizeModelID(item.ID)
		if norm == "" {
			continue
		}
		out = append(out, protocol.CatalogModelView{
			ID:            norm,
			Name:          item.ID,
			TargetVramMiB: item.TargetVramMiB,
			Active:        item.Active,
		})
	}
	return out, nil
}

type LocalRuntimeFS struct{}

func NewRuntimeFS() *LocalRuntimeFS {
	return &LocalRuntimeFS{}
}

func (LocalRuntimeFS) FindExistingRuntime(dataDir, binaryName string) string {
	cleanBin := strings.TrimSpace(binaryName)
	if cleanBin == "" {
		return ""
	}
	if found := findRuntimeInDataDir(dataDir, cleanBin); found != "" {
		return found
	}
	return findBundledAssetsRuntime(cleanBin)
}

func findRuntimeInDataDir(dataDir, binaryName string) string {
	trimmed := strings.TrimSpace(dataDir)
	if trimmed == "" {
		return ""
	}
	direct := filepath.Join(trimmed, binaryName)
	if isRegularFile(direct) {
		return filepath.Dir(direct)
	}
	for _, sub := range []string{
		filepath.Join(trimmed, "runtime-"+runtime.GOOS),
		filepath.Join(trimmed, "runtimes"),
	} {
		if found := searchBinaryRecursive(sub, binaryName); found != "" {
			return filepath.Dir(found)
		}
	}
	return ""
}

var bundledAssetBinDirs = []string{"llama-bin", "diffusion-bin", "audio-bin"}

func findBundledAssetsRuntime(binaryName string) string {
	var roots []string
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		roots = append(roots, exeDir, filepath.Dir(exeDir))
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd)
	}
	for _, root := range roots {
		for _, sub := range bundledAssetBinDirs {
			cand := filepath.Join(root, "assets", sub, binaryName)
			if isRegularFile(cand) {
				return filepath.Dir(cand)
			}
		}
	}
	return ""
}

func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func searchBinaryRecursive(root, targetName string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	var found string
	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == targetName {
			found = p
			return io.EOF
		}
		return nil
	})
	if walkErr != nil && walkErr != io.EOF {
		return ""
	}
	return found
}

func (LocalRuntimeFS) PrepareStagingDir(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(dataDir, "runtime-"+runtime.GOOS+".*.staging")
}

func (LocalRuntimeFS) ActivateStagedRuntime(stagingDir, targetDir, binaryName string) error {
	if searchBinaryRecursive(stagingDir, binaryName) == "" {
		if rmErr := os.RemoveAll(stagingDir); rmErr != nil {
			return rmErr
		}
		return derr.New(derr.CodeUpstreamFailure, 502, binaryName+" binary missing after runtime extraction", nil)
	}
	backupDir := fmt.Sprintf("%s.old.%d", targetDir, time.Now().UnixNano())
	hadExisting, err := displaceExistingRuntimeDir(targetDir, backupDir)
	if err != nil {
		return err
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		return rollbackRuntimeActivation(err, backupDir, targetDir, hadExisting)
	}
	cleanupBackupRuntimeDir(backupDir, hadExisting)
	return nil
}

func displaceExistingRuntimeDir(targetDir, backupDir string) (bool, error) {
	if _, statErr := os.Stat(targetDir); statErr != nil {
		return false, nil
	}
	if renErr := os.Rename(targetDir, backupDir); renErr == nil {
		return true, nil
	}
	if rmErr := os.RemoveAll(targetDir); rmErr != nil {
		return false, rmErr
	}
	return false, nil
}

func rollbackRuntimeActivation(activateErr error, backupDir, targetDir string, hadExisting bool) error {
	if !hadExisting {
		return activateErr
	}
	if restoreErr := os.Rename(backupDir, targetDir); restoreErr != nil {
		return errors.Join(activateErr, restoreErr)
	}
	return activateErr
}

func cleanupBackupRuntimeDir(backupDir string, hadExisting bool) {
	if !hadExisting {
		return
	}
	if rmErr := os.RemoveAll(backupDir); rmErr != nil {
		return
	}
}

func (LocalRuntimeFS) RemovePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return os.RemoveAll(path)
}

func (LocalRuntimeFS) DefaultHostInfo() (string, int) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "GPU-NODE"
	}
	return host, os.Getpid()
}

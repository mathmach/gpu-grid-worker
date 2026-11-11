package workerhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/tunnel"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workerhttp"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type stubHTTPWorkerHardware struct{}

func (stubHTTPWorkerHardware) MachineFingerprint() string {
	return "m-aaaa111122223333444455556666777788889999aaaabbbbccccddddeeeeffff"
}

func (stubHTTPWorkerHardware) ProbeGPUs(ctx context.Context, rtDir string) ([]protocol.GpuInfo, error) {
	return []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576, VramFreeMiB: 24000}}, nil
}

func (stubHTTPWorkerHardware) IsDedicatedGPU(name string) bool { return true }

func closeWorkerHTTPBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
}

func newWorkerHTTPTestServer(t *testing.T) (*httptest.Server, *worker.Engine, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	silence := slog.New(slog.NewTextHandler(io.Discard, nil))
	casAdapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), nil)
	eng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			DataDir: dir,
		},
		Hardware:      stubHTTPWorkerHardware{},
		Archive:       casAdapter,
		ModelCache:    casAdapter,
		ConfigStore:   workerconfig.NewWorkerConfigStore(dir, cfgPath),
		GatewayClient: cas.NewGatewayClient(),
		RuntimeFS:     cas.NewRuntimeFS(),
		Loopback:      tunnel.NewLoopbackTransport(),
		Logger:        silence,
	})
	t.Cleanup(eng.Shutdown)
	focusCh := make(chan struct{}, 2)
	srv := httptest.NewServer(workerhttp.NewHandler(eng, focusCh, silence))
	t.Cleanup(srv.Close)
	return srv, eng, focusCh
}

func doLocalIPCJSON(t *testing.T, method, url, rawJSON string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(rawJSON))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	req.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func TestWorkerHTTPSingletonPingFocusAndNoRootUI(t *testing.T) {
	srv, eng, focusCh := newWorkerHTTPTestServer(t)

	addr := strings.TrimPrefix(srv.URL, "http://")
	running, pid := workerhttp.CheckAlreadyRunning(addr)
	if !running || pid <= 0 {
		t.Fatalf("expected CheckAlreadyRunning=true, got running=%v pid=%d", running, pid)
	}

	select {
	case <-focusCh:
	default:
		t.Fatal("expected focus signal emitted by CheckAlreadyRunning")
	}

	respPing, err := http.Get(srv.URL + "/api/ping")
	if err != nil {
		t.Fatalf("GET /api/ping: %v", err)
	}
	var pingPayload map[string]any
	decodeErr := json.NewDecoder(respPing.Body).Decode(&pingPayload)
	closeWorkerHTTPBody(t, respPing)
	if decodeErr != nil {
		t.Fatalf("decode /api/ping: %v", decodeErr)
	}
	if pingPayload["machineId"] != eng.MachineID() || pingPayload["status"] != "running" {
		t.Fatalf("unexpected /api/ping payload: %+v", pingPayload)
	}

	respRoot, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	closeWorkerHTTPBody(t, respRoot)
	if respRoot.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found on root, got %d", respRoot.StatusCode)
	}
}

func TestWorkerHTTPStatusAndConfigLiveMutations(t *testing.T) {
	srv, eng, focusCh := newWorkerHTTPTestServer(t)
	if eng == nil || focusCh == nil {
		t.Fatal("expected initialized engine and focusCh")
	}

	respStatus, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	var initStatus map[string]any
	decodeErr := json.NewDecoder(respStatus.Body).Decode(&initStatus)
	closeWorkerHTTPBody(t, respStatus)
	if decodeErr != nil {
		t.Fatalf("decode /api/status: %v", decodeErr)
	}
	if respStatus.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on GET /api/status, got %d", respStatus.StatusCode)
	}
	for _, requiredKey := range []string{"enabledBackends", "soloEngineStatus", "soloModelId", "pauseDraining", "status", "sessionState"} {
		if _, exists := initStatus[requiredKey]; !exists {
			t.Fatalf("expected key %q in /api/status payload, got %+v", requiredKey, initStatus)
		}
	}
	for _, removedKey := range []string{"askPer1M", "autoPrice", "creditFloor"} {
		if _, exists := initStatus[removedKey]; exists {
			t.Fatalf("expected legacy key %q absent from /api/status payload, got %+v", removedKey, initStatus)
		}
	}

	secretTok := "wk_test_loopback_tok"
	patchPayload := map[string]any{
		"gatewayUrl":       "https://grid.example.com",
		"workerToken":      secretTok,
		"workerName":       "RIG-01",
		"selectedIds":      []string{"Vulkan0"},
		"scheduleMode":     "idle",
		"idleThresholdSec": 90,
		"manualPaused":     true,
		"enabledBackends":  []string{"LLAMA_LOCAL", "LLAMA_RPC"},
		"language":         "pt-BR",
		"uiMode":           "advanced",
	}
	patchBytes, err := json.Marshal(patchPayload)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	putResp := doLocalIPCJSON(t, http.MethodPut, srv.URL+"/api/config", string(patchBytes))
	var updatedCfg workerhttp.ConfigResponse
	putDecodeErr := json.NewDecoder(putResp.Body).Decode(&updatedCfg)
	closeWorkerHTTPBody(t, putResp)
	if putDecodeErr != nil {
		t.Fatalf("decode PUT /api/config: %v", putDecodeErr)
	}
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on PUT /api/config, got %d", putResp.StatusCode)
	}
	if !updatedCfg.HasWorkerToken || updatedCfg.WorkerName != "RIG-01" || !updatedCfg.ManualPaused || updatedCfg.Language != "pt-BR" || updatedCfg.UIMode != "advanced" {
		t.Fatalf("unexpected updated config: %+v", updatedCfg)
	}
	if len(updatedCfg.EnabledBackends) != 2 || len(updatedCfg.SelectedIDs) != 1 {
		t.Fatalf("expected 2 enabledBackends and 1 selectedId, got %+v", updatedCfg)
	}

	clearGPUsResp := doLocalIPCJSON(t, http.MethodPatch, srv.URL+"/api/config", `{"selectedIds":[]}`)
	var clearedCfg workerhttp.ConfigResponse
	clearDecodeErr := json.NewDecoder(clearGPUsResp.Body).Decode(&clearedCfg)
	closeWorkerHTTPBody(t, clearGPUsResp)
	if clearDecodeErr != nil {
		t.Fatalf("decode clear selectedIds: %v", clearDecodeErr)
	}
	if len(clearedCfg.SelectedIDs) != 0 {
		t.Fatalf("expected empty selectedIds after explicit patch, got %v", clearedCfg.SelectedIDs)
	}

	postStatus, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status after token set: %v", err)
	}
	rawStatusBytes, readErr := io.ReadAll(postStatus.Body)
	closeWorkerHTTPBody(t, postStatus)
	if readErr != nil {
		t.Fatalf("read /api/status body: %v", readErr)
	}
	if bytes.Contains(rawStatusBytes, []byte(secretTok)) || bytes.Contains(rawStatusBytes, []byte(`"workerToken"`)) {
		t.Fatalf("/api/status leaked workerToken: %s", string(rawStatusBytes))
	}

	badResp := doLocalIPCJSON(t, http.MethodPost, srv.URL+"/api/config", `{"enabledBackends":["UNKNOWN_ENGINE"]}`)
	closeWorkerHTTPBody(t, badResp)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on unknown backend, got %d", badResp.StatusCode)
	}

	badUIModeResp := doLocalIPCJSON(t, http.MethodPost, srv.URL+"/api/config", `{"uiMode":"expert"}`)
	closeWorkerHTTPBody(t, badUIModeResp)
	if badUIModeResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid uiMode, got %d", badUIModeResp.StatusCode)
	}

	for _, invalidPatch := range []string{
		`{"language":"fr"}`,
		`{"language":""}`,
		`{"askPer1M":0.42}`,
		`{"autoPrice":true}`,
		`{"creditFloor":0.25}`,
		`{"idleThresholdSec":5}`,
		`{"idleThresholdSec":7200}`,
	} {
		invalidResp := doLocalIPCJSON(t, http.MethodPatch, srv.URL+"/api/config", invalidPatch)
		closeWorkerHTTPBody(t, invalidResp)
		if invalidResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid config patch %s, got %d", invalidPatch, invalidResp.StatusCode)
		}
	}
}

func TestWorkerHTTPLoopbackSecurityRejectsDNSRebindingAndBrowserCSRF(t *testing.T) {
	srv, eng, focusCh := newWorkerHTTPTestServer(t)
	if focusCh == nil {
		t.Fatal("expected focusCh")
	}

	rebindReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/status", nil)
	if err != nil {
		t.Fatalf("build rebind request: %v", err)
	}
	rebindReq.Host = "evil-rebind.attacker.tld:17771"
	rebindResp, err := http.DefaultClient.Do(rebindReq)
	if err != nil {
		t.Fatalf("do rebind request: %v", err)
	}
	closeWorkerHTTPBody(t, rebindResp)
	if rebindResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on DNS-rebound Host header, got %d", rebindResp.StatusCode)
	}

	originReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/config", strings.NewReader(`{"gatewayUrl":"https://attacker.tld"}`))
	if err != nil {
		t.Fatalf("build browser Origin request: %v", err)
	}
	originReq.Header.Set("Origin", "https://evil.example")
	originReq.Header.Set("Content-Type", "application/json")
	originReq.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	originResp, err := http.DefaultClient.Do(originReq)
	if err != nil {
		t.Fatalf("do browser Origin request: %v", err)
	}
	closeWorkerHTTPBody(t, originResp)
	if originResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when browser Origin header is present, got %d", originResp.StatusCode)
	}

	fetchSiteReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/config", strings.NewReader(`{"gatewayUrl":"https://attacker.tld"}`))
	if err != nil {
		t.Fatalf("build Sec-Fetch-Site request: %v", err)
	}
	fetchSiteReq.Header.Set("Sec-Fetch-Site", "cross-site")
	fetchSiteReq.Header.Set("Content-Type", "application/json")
	fetchSiteResp, err := http.DefaultClient.Do(fetchSiteReq)
	if err != nil {
		t.Fatalf("do Sec-Fetch-Site request: %v", err)
	}
	closeWorkerHTTPBody(t, fetchSiteResp)
	if fetchSiteResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on cross-site Sec-Fetch-Site, got %d", fetchSiteResp.StatusCode)
	}

	noHdrResp, err := http.Post(srv.URL+"/api/config", "application/json", strings.NewReader(`{"gatewayUrl":"https://attacker.tld"}`))
	if err != nil {
		t.Fatalf("POST without X-Grid-Worker-Local: %v", err)
	}
	closeWorkerHTTPBody(t, noHdrResp)
	if noHdrResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when X-Grid-Worker-Local header is missing, got %d", noHdrResp.StatusCode)
	}

	plainReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/config", strings.NewReader(`{"gatewayUrl":"https://attacker.tld"}`))
	if err != nil {
		t.Fatalf("build text/plain request: %v", err)
	}
	plainReq.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	plainReq.Header.Set("Content-Type", "text/plain")
	plainResp, err := http.DefaultClient.Do(plainReq)
	if err != nil {
		t.Fatalf("do text/plain request: %v", err)
	}
	closeWorkerHTTPBody(t, plainResp)
	if plainResp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 on text/plain Content-Type, got %d", plainResp.StatusCode)
	}

	if gotGW := eng.Snapshot().GatewayURL; gotGW != "http://127.0.0.1:8000" {
		t.Fatalf("gatewayUrl was mutated despite rejected requests: %s", gotGW)
	}
}

func TestWorkerHTTPBrowserAuthCallbackPKCEAndFocusPulse(t *testing.T) {
	var expectedChallenge string
	mockGW := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.Path == "/api/models" {
			rw.Header().Set("Content-Type", "application/json")
			if _, err := rw.Write([]byte(`{"models":[]}`)); err != nil {
				t.Errorf("write models: %v", err)
			}
			return
		}
		if req.Method != http.MethodPost || req.URL.Path != "/api/auth/worker-cli/exchange" {
			http.NotFound(rw, req)
			return
		}
		var body struct {
			Code         string `json:"code"`
			CodeVerifier string `json:"codeVerifier"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(rw, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		if body.Code != "wac_loopback_test_code" || !nodecore.VerifyPKCES256(body.CodeVerifier, expectedChallenge) {
			http.Error(rw, `{"error":"invalid verifier","code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		if _, err := rw.Write([]byte(`{"account":{"email":"loopback-user@example.com","workerToken":"wk_test_callback_token_888"}}`)); err != nil {
			t.Errorf("write callback exchange: %v", err)
		}
	}))
	t.Cleanup(mockGW.Close)

	srv, eng, focusCh := newWorkerHTTPTestServer(t)
	authURL, err := eng.StartBrowserLogin(mockGW.URL)
	if err != nil {
		t.Fatalf("StartBrowserLogin: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	state := parsed.Query().Get("state")
	expectedChallenge = parsed.Query().Get("code_challenge")

	rebindReq, err := http.NewRequest(http.MethodGet, srv.URL+"/auth/callback?code=wac_loopback_test_code&state="+state, nil)
	if err != nil {
		t.Fatalf("new rebind callback req: %v", err)
	}
	rebindReq.Host = "evil.attacker.tld:17771"
	rebindResp, err := http.DefaultClient.Do(rebindReq)
	if err != nil {
		t.Fatalf("do rebind callback: %v", err)
	}
	closeWorkerHTTPBody(t, rebindResp)
	if rebindResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on DNS-rebound /auth/callback Host, got %d", rebindResp.StatusCode)
	}

	badStateResp, err := http.Get(srv.URL + "/auth/callback?code=wac_loopback_test_code&state=wrong_state_value_abcdef")
	if err != nil {
		t.Fatalf("GET /auth/callback wrong state: %v", err)
	}
	badStateHTML, badReadErr := io.ReadAll(badStateResp.Body)
	closeWorkerHTTPBody(t, badStateResp)
	if badReadErr != nil {
		t.Fatalf("read wrong state HTML: %v", badReadErr)
	}
	if badStateResp.StatusCode != http.StatusForbidden || !strings.Contains(string(badStateHTML), `<html lang="en">`) {
		t.Fatalf("expected 403 HTML with lang=en on wrong state, got %d body=%s", badStateResp.StatusCode, string(badStateHTML))
	}

	langResp := doLocalIPCJSON(t, http.MethodPatch, srv.URL+"/api/config", `{"language":"pt-BR"}`)
	closeWorkerHTTPBody(t, langResp)
	if langResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 setting language=pt-BR, got %d", langResp.StatusCode)
	}

	okReq, err := http.NewRequest(http.MethodGet, srv.URL+"/auth/callback?code=wac_loopback_test_code&state="+url.QueryEscape(state), nil)
	if err != nil {
		t.Fatalf("new ok callback req: %v", err)
	}
	okReq.Header.Set("Sec-Fetch-Site", "cross-site")
	okResp, err := http.DefaultClient.Do(okReq)
	if err != nil {
		t.Fatalf("do ok callback: %v", err)
	}
	htmlBytes, readErr := io.ReadAll(okResp.Body)
	closeWorkerHTTPBody(t, okResp)
	if readErr != nil {
		t.Fatalf("read callback HTML: %v", readErr)
	}
	if okResp.StatusCode != http.StatusOK || !strings.Contains(string(htmlBytes), "loopback-user@example.com") || !strings.Contains(string(htmlBytes), `<html lang="pt-BR">`) {
		t.Fatalf("expected 200 HTML with lang=pt-BR and linked email, got status=%d body=%s", okResp.StatusCode, string(htmlBytes))
	}

	select {
	case <-focusCh:
	default:
		t.Fatal("expected focusCh signal after successful /auth/callback")
	}

	snap := eng.Snapshot()
	if !snap.HasWorkerToken || snap.AccountEmail != "loopback-user@example.com" || snap.GatewayURL != mockGW.URL {
		t.Fatalf("unexpected worker snapshot after /auth/callback: %+v", snap)
	}
}

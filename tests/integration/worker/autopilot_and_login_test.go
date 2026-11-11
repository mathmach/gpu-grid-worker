package worker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type hybridGamerHardware struct {
	onlyIntegrated bool
}

func (hybridGamerHardware) MachineFingerprint() string {
	return "m-22223333444455556666777788889999aaaabbbbccccddddeeeeffff22223333"
}

func (h hybridGamerHardware) ProbeGPUs(ctx context.Context, binDir string) ([]protocol.GpuInfo, error) {
	if h.onlyIntegrated {
		return []protocol.GpuInfo{
			{DeviceID: "Vulkan0", Name: "Intel UHD Graphics 770", VramTotalMiB: 2048, VramFreeMiB: 1800},
		}, nil
	}
	return []protocol.GpuInfo{
		{DeviceID: "Vulkan0", Name: "Intel UHD Graphics 770", VramTotalMiB: 2048, VramFreeMiB: 1800},
		{DeviceID: "Vulkan1", Name: "NVIDIA GeForce RTX 4080", VramTotalMiB: 16384, VramFreeMiB: 15800},
		{DeviceID: "Vulkan2", Name: "AMD Radeon RX 7900 XTX", VramTotalMiB: 24576, VramFreeMiB: 24000},
	}, nil
}

func (hybridGamerHardware) IsDedicatedGPU(name string) bool {
	return !strings.Contains(strings.ToLower(name), "intel uhd")
}

func TestUnlinkAccountAndStartSimpleSelectsDedicatedGPUs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeFakeRuntimeDir(t, dir)

	rpcSup := &fakeRPCSupervisor{}
	idleDet := &fakeIdleDetector{}
	idleDet.sec.Store(300)
	dialer := newFakeTunnelDialer()

	deps := baseTestDeps(dir, cfgPath)
	deps.Hardware = hybridGamerHardware{}
	deps.RPC = rpcSup
	deps.Idle = idleDet
	deps.Tunnel = dialer
	deps.Boot = worker.BootConfig{
		GatewayURL:      "http://127.0.0.1:8000",
		Token:           "wk_test_gamer_123",
		DataDir:         dir,
		EnabledBackends: []string{"LLAMA_RPC"},
		ReconnectDelay:  15 * time.Millisecond,
	}
	eng := worker.NewEngine(deps)
	t.Cleanup(func() { eng.Shutdown() })

	if err := eng.StartSimple(context.Background(), protocol.ModelModeAuto, nil); err != nil {
		t.Fatalf("StartSimple failed: %v", err)
	}

	var sock *fakeTunnelSocket
	select {
	case sock = <-dialer.dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for simple-start worker to dial gateway")
	}

	waitUntil(t, func() bool { return len(sock.sentMessages()) >= 1 })
	hello := sock.sentMessages()[0]
	if hello.Type != "hello" || hello.ModelMode != protocol.ModelModeAuto || hello.ScheduleMode != "idle" {
		t.Fatalf("unexpected simple-start hello frame: %+v", hello)
	}
	if len(hello.GPUs) != 2 || hello.GPUs[0].DeviceID != "Vulkan1" || hello.GPUs[1].DeviceID != "Vulkan2" {
		t.Fatalf("expected dedicated GPUs [Vulkan1, Vulkan2] only, got %+v", hello.GPUs)
	}

	snap := eng.Snapshot()
	if !reflect.DeepEqual(snap.SelectedIDs, []string{"Vulkan1", "Vulkan2"}) {
		t.Fatalf("expected SelectedIDs [Vulkan1 Vulkan2], got %v", snap.SelectedIDs)
	}

	if err := eng.UnlinkAccount(); derr.StatusOf(err) != 409 {
		t.Fatalf("expected UnlinkAccount while running to return 409 ILLEGAL_STATE_TRANSITION, got %v", err)
	}

	eng.Stop("test done")

	if err := eng.UnlinkAccount(); err != nil {
		t.Fatalf("UnlinkAccount after stop failed: %v", err)
	}
	afterUnlink := worker.NewEngine(baseTestDeps(dir, cfgPath)).Snapshot()
	if afterUnlink.HasWorkerToken || afterUnlink.WorkerToken != "" || afterUnlink.AccountEmail != "" {
		t.Fatalf("expected UnlinkAccount to clear persisted credentials, got %+v", afterUnlink)
	}

	igpuDir := t.TempDir()
	writeFakeRuntimeDir(t, igpuDir)
	igpuDeps := baseTestDeps(igpuDir, filepath.Join(igpuDir, "config.json"))
	igpuDeps.Hardware = hybridGamerHardware{onlyIntegrated: true}
	igpuDeps.RPC = &fakeRPCSupervisor{}
	igpuDeps.Idle = idleDet
	igpuDeps.Tunnel = newFakeTunnelDialer()
	igpuDeps.Boot.GatewayURL = "http://127.0.0.1:8000"
	igpuDeps.Boot.Token = "wk_test_igpu_123"
	igpuEng := worker.NewEngine(igpuDeps)
	t.Cleanup(func() { igpuEng.Shutdown() })

	if err := igpuEng.StartSimple(context.Background(), protocol.ModelModeAuto, nil); err != nil {
		t.Fatalf("StartSimple on integrated-only Vulkan GPU failed: %v", err)
	}
	if !reflect.DeepEqual(igpuEng.Snapshot().SelectedIDs, []string{"Vulkan0"}) {
		t.Fatalf("expected integrated fallback to [Vulkan0], got %v", igpuEng.Snapshot().SelectedIDs)
	}
}

func TestSetUIModePersistenceAndClearLogs(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	cfgPath := filepath.Join(dir, "config.json")
	eng := worker.NewEngine(baseTestDeps(dir, cfgPath))

	if got := eng.Snapshot().UIMode; got != "simple" {
		t.Fatalf("expected default uiMode simple, got %q", got)
	}
	if resolved := eng.SetUIMode("ADVANCED"); resolved != "advanced" {
		t.Fatalf("expected SetUIMode(ADVANCED) -> advanced, got %q", resolved)
	}

	reloaded := worker.NewEngine(baseTestDeps(dir, cfgPath))
	if got := reloaded.Snapshot().UIMode; got != "advanced" {
		t.Fatalf("expected persisted uiMode advanced across reload, got %q", got)
	}

	if err := reloaded.ScanGPUs(context.Background(), ""); err != nil {
		t.Fatalf("ScanGPUs failed: %v", err)
	}
	if len(reloaded.Snapshot().Logs) == 0 {
		t.Fatal("expected non-empty worker logs before ClearLogs")
	}
	reloaded.ClearLogs()
	if len(reloaded.Snapshot().Logs) != 0 {
		t.Fatalf("expected 0 logs after ClearLogs, got %d", len(reloaded.Snapshot().Logs))
	}
}

type recordingBrowserLauncher struct {
	mu   sync.Mutex
	urls []string
}

func (r *recordingBrowserLauncher) OpenExternalURL(u string) error {
	r.mu.Lock()
	r.urls = append(r.urls, u)
	r.mu.Unlock()
	return nil
}

func (r *recordingBrowserLauncher) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

func TestBrowserPKCELoginFlowWithdrawRedirectAndSelectivePreDownload(t *testing.T) {
	modelBytes := []byte("GGUF_MOCK_PRE_DOWNLOAD_WEIGHTS_PAYLOAD_0123456789")
	sum := sha256.Sum256(modelBytes)
	modelSHA := hex.EncodeToString(sum[:])

	var activeChallenge atomic.Value
	var resolveCalls atomic.Int64
	var gwSrv *httptest.Server
	gwSrv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/api/auth/worker-cli/exchange":
			var body struct {
				Code         string `json:"code"`
				CodeVerifier string `json:"codeVerifier"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(rw, `{"error":"bad json"}`, http.StatusBadRequest)
				return
			}
			expectedCh, _ := activeChallenge.Load().(string)
			if body.Code != "wac_valid_single_use_code" || !nodecore.VerifyPKCES256(body.CodeVerifier, expectedCh) {
				http.Error(rw, `{"error":"invalid pkce","code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			rw.Header().Set("Content-Type", "application/json")
			if _, err := rw.Write([]byte(`{"account":{"email":"browser-provider@example.com","workerToken":"wk_test_browser_pkce_777"}}`)); err != nil {
				t.Errorf("write exchange response: %v", err)
			}
		case req.Method == http.MethodPost && req.URL.Path == "/api/worker/models/resolve":
			resolveCalls.Add(1)
			if req.Header.Get("Authorization") != "Bearer wk_test_browser_pkce_777" {
				http.Error(rw, `{"error":"unauthorized","code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			rw.Header().Set("Content-Type", "application/json")
			respPayload, mErr := json.Marshal(map[string]any{
				"artifacts": []protocol.WorkerModelArtifactView{{
					ModelID:     "qwen2.5-7b-q4_k_m",
					FileName:    "qwen2.5-7b-q4_k_m.gguf",
					ModelSHA256: modelSHA,
					DownloadURL: gwSrv.URL + "/models/qwen2.5-7b-q4_k_m.gguf",
					FileSizeMiB: 1,
				}},
			})
			if mErr != nil {
				t.Errorf("marshal resolve response: %v", mErr)
				return
			}
			if _, err := rw.Write(respPayload); err != nil {
				t.Errorf("write resolve response: %v", err)
			}
		case req.Method == http.MethodGet && req.URL.Path == "/models/qwen2.5-7b-q4_k_m.gguf":
			rw.Header().Set("X-Content-SHA256", modelSHA)
			if _, err := rw.Write(modelBytes); err != nil {
				t.Errorf("write model bytes: %v", err)
			}
		case req.Method == http.MethodGet && req.URL.Path == "/api/models":
			rw.Header().Set("Content-Type", "application/json")
			if _, err := rw.Write([]byte(`{"models":[]}`)); err != nil {
				t.Errorf("write models list: %v", err)
			}
		default:
			http.NotFound(rw, req)
		}
	}))
	t.Cleanup(gwSrv.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeFakeRuntimeDir(t, dir)

	browser := &recordingBrowserLauncher{}
	idleDet := &fakeIdleDetector{}
	idleDet.sec.Store(300)
	dialer := newFakeTunnelDialer()
	casAdapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), gwSrv.Client())
	gwClient := cas.NewGatewayClient()

	deps := baseTestDeps(dir, cfgPath)
	deps.Boot.EmbeddedGatewayURL = gwSrv.URL
	deps.Boot.ReconnectDelay = 15 * time.Millisecond
	deps.Hardware = hybridGamerHardware{}
	deps.RPC = &fakeRPCSupervisor{}
	deps.Idle = idleDet
	deps.Tunnel = dialer
	deps.Archive = casAdapter
	deps.ModelCache = casAdapter
	deps.GatewayClient = gwClient
	deps.Portal = gwClient
	deps.Browser = browser

	eng := worker.NewEngine(deps)
	t.Cleanup(eng.Shutdown)

	if gotGW := eng.Snapshot().GatewayURL; gotGW != gwSrv.URL {
		t.Fatalf("expected EmbeddedGatewayURL %s applied on fresh boot, got %s", gwSrv.URL, gotGW)
	}

	authURL, err := eng.StartBrowserLogin("")
	if err != nil {
		t.Fatalf("StartBrowserLogin failed: %v", err)
	}
	if !eng.Snapshot().BrowserAuthPending {
		t.Fatal("expected BrowserAuthPending=true after StartBrowserLogin")
	}
	if err := eng.ReopenBrowserLogin(); err != nil {
		t.Fatalf("ReopenBrowserLogin failed: %v", err)
	}
	eng.CancelBrowserLogin()
	if eng.Snapshot().BrowserAuthPending {
		t.Fatal("expected BrowserAuthPending=false after CancelBrowserLogin")
	}

	authURL, err = eng.StartBrowserLogin("")
	if err != nil {
		t.Fatalf("second StartBrowserLogin failed: %v", err)
	}
	parsedAuth, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	stateParam := parsedAuth.Query().Get("state")
	activeChallenge.Store(parsedAuth.Query().Get("code_challenge"))

	if _, badStateErr := eng.CompleteBrowserLogin(context.Background(), "wac_valid_single_use_code", "wrong_state_value_123456"); badStateErr == nil {
		t.Fatal("expected mismatched state to fail CompleteBrowserLogin")
	}
	linkedSnap, err := eng.CompleteBrowserLogin(context.Background(), "wac_valid_single_use_code", stateParam)
	if err != nil {
		t.Fatalf("CompleteBrowserLogin failed: %v", err)
	}
	if !linkedSnap.HasWorkerToken || linkedSnap.AccountEmail != "browser-provider@example.com" || linkedSnap.BrowserAuthPending {
		t.Fatalf("unexpected snapshot after browser PKCE completion: %+v", linkedSnap)
	}
	if _, replayErr := eng.CompleteBrowserLogin(context.Background(), "wac_valid_single_use_code", stateParam); replayErr == nil {
		t.Fatal("expected consumed browser login state to reject replay")
	}

	if err := eng.OpenWithdrawPage(); err != nil {
		t.Fatalf("OpenWithdrawPage failed: %v", err)
	}
	opened := browser.snapshot()
	if len(opened) == 0 || opened[len(opened)-1] != gwSrv.URL+"/?tab=wallet#withdraw" {
		t.Fatalf("expected withdraw redirect URL %s/?tab=wallet#withdraw, got %v", gwSrv.URL, opened)
	}

	if err := eng.StartSimple(context.Background(), protocol.ModelModeAuto, nil); err != nil {
		t.Fatalf("StartSimple(auto) failed: %v", err)
	}
	select {
	case autoSock := <-dialer.dialed:
		waitUntil(t, func() bool { return len(autoSock.sentMessages()) >= 1 })
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for auto-mode hello")
	}
	if resolveCalls.Load() != 0 {
		t.Fatalf("expected 0 pre-download resolve calls in auto mode, got %d", resolveCalls.Load())
	}
	eng.Stop("switch to manual selection")

	if err := eng.StartSimple(context.Background(), protocol.ModelModeMulti, []string{"qwen2.5-7b-q4_k_m"}); err != nil {
		t.Fatalf("StartSimple(multi) failed: %v", err)
	}
	var multiSock *fakeTunnelSocket
	select {
	case multiSock = <-dialer.dialed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for multi-mode worker to pre-download and dial gateway")
	}
	waitUntil(t, func() bool { return len(multiSock.sentMessages()) >= 1 })
	hello := multiSock.sentMessages()[0]
	if resolveCalls.Load() != 1 {
		t.Fatalf("expected 1 pre-download resolve call in multi mode, got %d", resolveCalls.Load())
	}
	if hello.ModelMode != protocol.ModelModeMulti || len(hello.CachedModelSHA256s) != 1 || hello.CachedModelSHA256s[0] != modelSHA {
		t.Fatalf("expected pre-downloaded SHA-256 advertised in hello, got %+v", hello)
	}

	pauseTrue := true
	eng.UpdateSchedule("idle", worker.DefaultIdleThresholdSec, &pauseTrue)
	waitUntil(t, func() bool { return eng.Snapshot().ManualPaused && eng.Snapshot().Paused })
	pauseFalse := false
	eng.UpdateSchedule("idle", worker.DefaultIdleThresholdSec, &pauseFalse)
	waitUntil(t, func() bool { return !eng.Snapshot().ManualPaused && eng.Snapshot().Running })
}

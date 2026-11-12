package worker_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workerhttp"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type dialedTunnelSession struct {
	wsURL string
	sock  *fakeTunnelSocket
}

type migrationTunnelDialer struct {
	dialed chan dialedTunnelSession
}

func newMigrationTunnelDialer() *migrationTunnelDialer {
	return &migrationTunnelDialer{dialed: make(chan dialedTunnelSession, 16)}
}

func (d *migrationTunnelDialer) Dial(_ context.Context, wsURL string) (worker.TunnelSocket, error) {
	sock := newFakeTunnelSocket()
	d.dialed <- dialedTunnelSession{wsURL: wsURL, sock: sock}
	return sock, nil
}

func signLocalIntegrationManifest(t *testing.T, priv ed25519.PrivateKey, version uint64, gwURL string) []byte {
	t.Helper()
	_, raw, err := nodecore.SignNetworkBootstrapManifest(priv, nodecore.NetworkBootstrapSignInput{
		Version:           version,
		GatewayURL:        gwURL,
		IssuedAt:          time.Now().UTC().Add(-time.Minute),
		AllowLocalTargets: true,
	})
	if err != nil {
		t.Fatalf("SignNetworkBootstrapManifest v%d: %v", version, err)
	}
	return raw
}

func waitSocketHello(t *testing.T, sock *fakeTunnelSocket) protocol.ControlMsg {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range sock.sentMessages() {
			if m.Type == protocol.ControlHello {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for worker hello control message")
	return protocol.ControlMsg{}
}

func TestLiveReconnectDomainMigrationPreservesTokenAndPersistsBootstrapVersion(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)

	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x44}, ed25519.SeedSize))
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	var manifestVal atomic.Value
	manifestVal.Store(signLocalIntegrationManifest(t, priv, 1, "http://127.0.0.1:18101"))

	mirrorSrv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		payload, _ := manifestVal.Load().([]byte)
		rw.Header().Set("Content-Type", "application/json")
		if _, err := rw.Write(payload); err != nil {
			t.Errorf("write bootstrap response: %v", err)
		}
	}))
	defer mirrorSrv.Close()

	dialer := newMigrationTunnelDialer()
	gwCli := cas.NewGatewayClient()
	deps := baseTestDeps(dir, "")
	deps.Boot.AllowInsecureWS = true
	deps.Boot.ReconnectDelay = 25 * time.Millisecond
	deps.Boot.BootstrapURLs = []string{mirrorSrv.URL + "/network.json"}
	deps.Boot.BootstrapPublicKeys = []string{pubHex}
	deps.Boot.Token = "wk_test_migration_999"
	deps.Boot.Devices = []string{"Vulkan0"}
	deps.Bootstrap = gwCli
	deps.Tunnel = dialer
	deps.RPC = &fakeRPCSupervisor{}

	eng := worker.NewEngine(deps)
	defer eng.Shutdown()

	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Engine.Start failed: %v", err)
	}
	defer eng.Stop("migration_test_done")

	var firstSess dialedTunnelSession
	select {
	case firstSess = <-dialer.dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial tunnel dial")
	}
	if firstSess.wsURL != "ws://127.0.0.1:18101/ws/worker" {
		t.Fatalf("expected initial dial to ws://127.0.0.1:18101/ws/worker, got %q", firstSess.wsURL)
	}
	firstHello := waitSocketHello(t, firstSess.sock)
	if firstHello.WorkerToken != "wk_test_migration_999" {
		t.Fatalf("expected initial hello to carry preserved token, got %q", firstHello.WorkerToken)
	}

	manifestVal.Store(signLocalIntegrationManifest(t, priv, 2, "http://127.0.0.1:18102"))
	if err := firstSess.sock.Close(); err != nil {
		t.Fatalf("close first tunnel socket: %v", err)
	}

	var secondSess dialedTunnelSession
	select {
	case secondSess = <-dialer.dialed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for migrated reconnect tunnel dial")
	}
	if secondSess.wsURL != "ws://127.0.0.1:18102/ws/worker" {
		t.Fatalf("expected migrated reconnect dial to ws://127.0.0.1:18102/ws/worker, got %q", secondSess.wsURL)
	}
	secondHello := waitSocketHello(t, secondSess.sock)
	if secondHello.WorkerToken != "wk_test_migration_999" {
		t.Fatalf("expected migrated hello to preserve wk_ token, got %q", secondHello.WorkerToken)
	}

	store := workerconfig.NewWorkerConfigStore(dir, "")
	persisted, exists, err := store.LoadConfig()
	if err != nil || !exists {
		t.Fatalf("LoadConfig failed: exists=%v err=%v", exists, err)
	}
	if persisted.GatewayURL != "http://127.0.0.1:18102" || persisted.BootstrapVersion != 2 || persisted.GatewayPinned || persisted.WorkerToken != "wk_test_migration_999" {
		t.Fatalf("unexpected persisted config after live migration: %+v", persisted)
	}
}

func TestWorkerHTTPConfigGatewayPinnedPatchAndUnpinRefresh(t *testing.T) {
	dir := t.TempDir()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x55}, ed25519.SeedSize))
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	var mu sync.Mutex
	currentManifest := signLocalIntegrationManifest(t, priv, 3, "http://127.0.0.1:18203")
	mirrorSrv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := append([]byte(nil), currentManifest...)
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		if _, err := rw.Write(body); err != nil {
			t.Errorf("write mirror body: %v", err)
		}
	}))
	defer mirrorSrv.Close()

	gwCli := cas.NewGatewayClient()
	deps := baseTestDeps(dir, "")
	deps.Boot.AllowInsecureWS = true
	deps.Boot.BootstrapURLs = []string{mirrorSrv.URL + "/network.json"}
	deps.Boot.BootstrapPublicKeys = []string{pubHex}
	deps.Bootstrap = gwCli

	eng := worker.NewEngine(deps)
	defer eng.Shutdown()

	silence := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiSrv := httptest.NewServer(workerhttp.NewHandler(eng, make(chan struct{}, 1), silence))
	defer apiSrv.Close()

	patchPinReq, err := http.NewRequest(http.MethodPatch, apiSrv.URL+"/api/config", strings.NewReader(`{"gatewayUrl":"http://127.0.0.1:19999"}`))
	if err != nil {
		t.Fatalf("build PATCH /api/config: %v", err)
	}
	patchPinReq.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	patchPinReq.Header.Set("Content-Type", "application/json")
	pinResp, err := http.DefaultClient.Do(patchPinReq)
	if err != nil {
		t.Fatalf("PATCH /api/config pin: %v", err)
	}
	var pinDoc map[string]any
	decErr := json.NewDecoder(pinResp.Body).Decode(&pinDoc)
	if closeErr := pinResp.Body.Close(); closeErr != nil {
		t.Fatalf("close pinResp: %v", closeErr)
	}
	if decErr != nil || pinResp.StatusCode != http.StatusOK || pinDoc["gatewayPinned"] != true {
		t.Fatalf("expected gatewayPinned=true on manual gatewayUrl PATCH, status=%d err=%v body=%+v", pinResp.StatusCode, decErr, pinDoc)
	}

	patchUnpinReq, err := http.NewRequest(http.MethodPatch, apiSrv.URL+"/api/config", strings.NewReader(`{"gatewayPinned":false}`))
	if err != nil {
		t.Fatalf("build PATCH unpin: %v", err)
	}
	patchUnpinReq.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	patchUnpinReq.Header.Set("Content-Type", "application/json")
	unpinResp, err := http.DefaultClient.Do(patchUnpinReq)
	if err != nil {
		t.Fatalf("PATCH /api/config unpin: %v", err)
	}
	var unpinDoc map[string]any
	unpinDecErr := json.NewDecoder(unpinResp.Body).Decode(&unpinDoc)
	if closeErr := unpinResp.Body.Close(); closeErr != nil {
		t.Fatalf("close unpinResp: %v", closeErr)
	}
	if unpinDecErr != nil || unpinResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on unpin PATCH, status=%d err=%v", unpinResp.StatusCode, unpinDecErr)
	}
	if unpinDoc["gatewayPinned"] != false || unpinDoc["gatewayUrl"] != "http://127.0.0.1:18203" {
		t.Fatalf("expected unpinning via PATCH /api/config to auto-discover http://127.0.0.1:18203, got %+v", unpinDoc)
	}
}

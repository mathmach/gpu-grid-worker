package worker_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type fakeMirrorEntry struct {
	payload []byte
	err     error
}

type fakeNetworkBootstrapPort struct {
	mu      sync.Mutex
	mirrors map[string]fakeMirrorEntry
	hits    int
}

func newFakeNetworkBootstrapPort() *fakeNetworkBootstrapPort {
	return &fakeNetworkBootstrapPort{mirrors: make(map[string]fakeMirrorEntry)}
}

func (f *fakeNetworkBootstrapPort) setMirror(url string, payload []byte, err error) {
	f.mu.Lock()
	f.mirrors[url] = fakeMirrorEntry{payload: append([]byte(nil), payload...), err: err}
	f.mu.Unlock()
}

func (f *fakeNetworkBootstrapPort) FetchNetworkBootstrap(_ context.Context, targetURL string) ([]byte, error) {
	f.mu.Lock()
	f.hits++
	entry, ok := f.mirrors[targetURL]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("mirror 404")
	}
	if entry.err != nil {
		return nil, entry.err
	}
	return append([]byte(nil), entry.payload...), nil
}

type unitBrowserRecorder struct {
	mu      sync.Mutex
	lastURL string
}

func (b *unitBrowserRecorder) OpenExternalURL(u string) error {
	b.mu.Lock()
	b.lastURL = u
	b.mu.Unlock()
	return nil
}

func (b *unitBrowserRecorder) openedURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastURL
}

func mustSignUnitManifest(t *testing.T, priv ed25519.PrivateKey, version uint64, gwURL string, mirrors []string) []byte {
	t.Helper()
	_, raw, err := nodecore.SignNetworkBootstrapManifest(priv, nodecore.NetworkBootstrapSignInput{
		Version:       version,
		GatewayURL:    gwURL,
		BootstrapURLs: mirrors,
		IssuedAt:      time.Now().UTC().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("SignNetworkBootstrapManifest v%d: %v", version, err)
	}
	return raw
}

func TestDynamicNetworkBootstrapUnpinnedLoginWithdrawAndHighestVersionSelection(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x24}, ed25519.SeedSize))
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	const (
		mirrorBroken   = "https://mirror-down.example.com/network.json"
		mirrorTampered = "https://mirror-tampered.example.com/network.json"
		mirrorStale    = "https://mirror-v1.example.com/network.json"
		mirrorFresh    = "https://mirror-v2.example.com/network.json"
	)

	bootPort := newFakeNetworkBootstrapPort()
	bootPort.setMirror(mirrorBroken, nil, errors.New("connection refused"))
	tampered := bytes.ReplaceAll(
		mustSignUnitManifest(t, priv, 99, "https://legit.example.com", nil),
		[]byte("https://legit.example.com"),
		[]byte("https://attacker.example.com"),
	)
	bootPort.setMirror(mirrorTampered, tampered, nil)
	bootPort.setMirror(mirrorStale, mustSignUnitManifest(t, priv, 1, "https://gw-v1.example.com", nil), nil)
	bootPort.setMirror(mirrorFresh, mustSignUnitManifest(t, priv, 2, "https://gw-v2.example.com", nil), nil)

	cfgStore := &fakeConfigStore{}
	browserRec := &unitBrowserRecorder{}
	eng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			DataDir:             "/virtual/worker-cache",
			BootstrapURLs:       []string{mirrorBroken, mirrorTampered, mirrorStale, mirrorFresh},
			BootstrapPublicKeys: []string{pubHex},
		},
		Hardware:    &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}}},
		Bootstrap:   bootPort,
		Browser:     browserRec,
		ConfigStore: cfgStore,
		RuntimeFS:   fakeRuntimeFS{},
		Loopback:    fakeLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	defer eng.Shutdown()

	authURL, err := eng.StartBrowserLogin("")
	if err != nil {
		t.Fatalf("StartBrowserLogin failed: %v", err)
	}
	if !strings.HasPrefix(authURL, "https://gw-v2.example.com/?") || !strings.Contains(authURL, "worker_auth=1") {
		t.Fatalf("expected PKCE login to discover highest valid v2 domain, got %q", authURL)
	}
	snap := eng.Snapshot()
	if snap.GatewayURL != "https://gw-v2.example.com" || snap.BootstrapVersion != 2 || snap.GatewayPinned {
		t.Fatalf("unexpected snapshot after v2 bootstrap discovery: %+v", snap)
	}
	if cfgStore.saved.GatewayURL != "https://gw-v2.example.com" || cfgStore.saved.BootstrapVersion != 2 {
		t.Fatalf("expected persisted config to store v2 gateway, got %+v", cfgStore.saved)
	}

	bootPort.setMirror(mirrorStale, mustSignUnitManifest(t, priv, 3, "https://gw-v3.example.com", nil), nil)
	if err := eng.OpenWithdrawPage(); err != nil {
		t.Fatalf("OpenWithdrawPage failed: %v", err)
	}
	if got := browserRec.openedURL(); got != "https://gw-v3.example.com/?tab=wallet#withdraw" {
		t.Fatalf("expected OpenWithdrawPage to cut over to v3 domain, got %q", got)
	}

	bootPort.setMirror(mirrorStale, mustSignUnitManifest(t, priv, 2, "https://gw-rollback.example.com", nil), nil)
	bootPort.setMirror(mirrorFresh, mustSignUnitManifest(t, priv, 2, "https://gw-rollback.example.com", nil), nil)
	resolvedGW, updated := eng.RefreshNetworkBootstrap(context.Background())
	if updated || resolvedGW != "https://gw-v3.example.com" || eng.Snapshot().BootstrapVersion != 3 {
		t.Fatalf("expected rollback to v2 to be rejected, got gw=%q updated=%v ver=%d", resolvedGW, updated, eng.Snapshot().BootstrapVersion)
	}
}

func TestDynamicNetworkBootstrapPinImmunityAndOfflineSilentFallback(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x66}, ed25519.SeedSize))
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	const mirrorURL = "https://mirror-main.example.com/network.json"

	bootPort := newFakeNetworkBootstrapPort()
	bootPort.setMirror(mirrorURL, mustSignUnitManifest(t, priv, 4, "https://official-prod.example.com", nil), nil)

	cliPinnedEng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			GatewayURL:          "https://selfhosted-cli.example.com",
			DataDir:             "/virtual/worker-cache",
			BootstrapURLs:       []string{mirrorURL},
			BootstrapPublicKeys: []string{pubHex},
		},
		Hardware:    &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}}},
		Bootstrap:   bootPort,
		ConfigStore: &fakeConfigStore{},
		RuntimeFS:   fakeRuntimeFS{},
		Loopback:    fakeLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	defer cliPinnedEng.Shutdown()

	if gw, changed := cliPinnedEng.RefreshNetworkBootstrap(context.Background()); changed || gw != "https://selfhosted-cli.example.com" || !cliPinnedEng.Snapshot().GatewayPinned {
		t.Fatalf("expected --gateway CLI flag to remain pinned and immune to bootstrap, got gw=%q changed=%v pinned=%v", gw, changed, cliPinnedEng.Snapshot().GatewayPinned)
	}

	cfgStore := &fakeConfigStore{}
	sock := newFakeTunnelSocket()
	dynamicEng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			Token:               "wk_unit_pin_token",
			Devices:             []string{"Vulkan0"},
			DataDir:             "/virtual/worker-cache",
			BootstrapURLs:       []string{mirrorURL},
			BootstrapPublicKeys: []string{pubHex},
		},
		Hardware:    &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}}},
		Tunnel:      &fakeTunnelDialer{sock: sock},
		Bootstrap:   bootPort,
		ConfigStore: cfgStore,
		RuntimeFS:   fakeRuntimeFS{},
		Loopback:    fakeLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	defer dynamicEng.Shutdown()

	unchangedDefaultGW := dynamicEng.Snapshot().GatewayURL
	if err := dynamicEng.Start(worker.StartParams{GatewayURL: unchangedDefaultGW}); err != nil {
		t.Fatalf("Start with unchanged GatewayURL failed: %v", err)
	}
	dynamicEng.Stop("unit_stop")
	if dynamicEng.Snapshot().GatewayPinned || dynamicEng.Snapshot().GatewayURL != "https://official-prod.example.com" {
		t.Fatalf("expected unchanged Advanced form GatewayURL not to pin and to discover official-prod, got %+v", dynamicEng.Snapshot())
	}

	if err := dynamicEng.ApplySettings(worker.UpdateSettings{GatewayURL: "https://manual-override.example.com"}); err != nil {
		t.Fatalf("ApplySettings manual GatewayURL failed: %v", err)
	}
	bootPort.setMirror(mirrorURL, mustSignUnitManifest(t, priv, 5, "https://official-v5.example.com", nil), nil)
	if gw, changed := dynamicEng.RefreshNetworkBootstrap(context.Background()); changed || gw != "https://manual-override.example.com" || !dynamicEng.Snapshot().GatewayPinned {
		t.Fatalf("expected manual GatewayURL override to pin and block v5 overwrite, got gw=%q changed=%v", gw, changed)
	}

	unpin := false
	if err := dynamicEng.ApplySettings(worker.UpdateSettings{GatewayPinned: &unpin}); err != nil {
		t.Fatalf("ApplySettings unpin failed: %v", err)
	}
	if snap := dynamicEng.Snapshot(); snap.GatewayPinned || snap.GatewayURL != "https://official-v5.example.com" || snap.BootstrapVersion != 5 {
		t.Fatalf("expected unpinning gatewayPinned=false to resume dynamic discovery to v5, got %+v", snap)
	}

	offlinePort := newFakeNetworkBootstrapPort()
	offlineEng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			EmbeddedGatewayURL:  "https://seed-fallback.example.com",
			DataDir:             "/virtual/worker-cache",
			BootstrapURLs:       []string{mirrorURL},
			BootstrapPublicKeys: []string{pubHex},
		},
		Hardware:    &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}}},
		Bootstrap:   offlinePort,
		ConfigStore: &fakeConfigStore{},
		RuntimeFS:   fakeRuntimeFS{},
		Loopback:    fakeLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	defer offlineEng.Shutdown()

	if gw, changed := offlineEng.RefreshNetworkBootstrap(context.Background()); changed || gw != "https://seed-fallback.example.com" || offlineEng.Snapshot().LastError != "" {
		t.Fatalf("expected offline mirrors to degrade silently without LastError, got gw=%q changed=%v lastErr=%q", gw, changed, offlineEng.Snapshot().LastError)
	}
}

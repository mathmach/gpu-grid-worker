package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/tunnel"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	"github.com/mathmach/gpu-grid-worker/tests/integration/testkit"
)

type isolatedRuntimeFS struct {
	worker.RuntimeFS
}

func (f isolatedRuntimeFS) FindExistingRuntime(dataDir, binaryName string) string {
	if dataDir == "" || binaryName == "" {
		return ""
	}
	direct := filepath.Join(dataDir, binaryName)
	if st, err := os.Stat(direct); err == nil && !st.IsDir() {
		return filepath.Dir(direct)
	}
	var found string
	walkErr := filepath.WalkDir(filepath.Join(dataDir, "runtime-"+runtime.GOOS), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == binaryName {
			found = filepath.Dir(p)
			return io.EOF
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, io.EOF) && !errors.Is(walkErr, os.ErrNotExist) {
		return ""
	}
	return found
}

func baseTestDeps(dir, cfgPath string) worker.Dependencies {
	if cfgPath == "" {
		cfgPath = filepath.Join(dir, "config.json")
	}
	casAdapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), nil)
	return worker.Dependencies{
		Boot: worker.BootConfig{
			DataDir: dir,
		},
		Hardware:      fakeHardware{},
		Archive:       casAdapter,
		ModelCache:    casAdapter,
		ConfigStore:   workerconfig.NewWorkerConfigStore(dir, cfgPath),
		GatewayClient: cas.NewGatewayClient(),
		RuntimeFS:     isolatedRuntimeFS{RuntimeFS: cas.NewRuntimeFS()},
		Loopback:      tunnel.NewLoopbackTransport(),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

type fakeRPCProc struct {
	stopped atomic.Int32
	done    chan struct{}
}

func newFakeRPCProc() *fakeRPCProc {
	return &fakeRPCProc{done: make(chan struct{})}
}

func (p *fakeRPCProc) Stop() {
	if p.stopped.CompareAndSwap(0, 1) {
		close(p.done)
	}
}

func (p *fakeRPCProc) Done() <-chan struct{} {
	return p.done
}

type fakeRPCSupervisor struct {
	mu         sync.Mutex
	spawnCount atomic.Int32
	lastProc   *fakeRPCProc
}

func (s *fakeRPCSupervisor) SpawnRPC(_ context.Context, _, _ string, _ []protocol.GpuInfo, _ int) (worker.RunningRPCProcess, error) {
	s.spawnCount.Add(1)
	proc := newFakeRPCProc()
	s.mu.Lock()
	s.lastProc = proc
	s.mu.Unlock()
	return proc, nil
}

func (s *fakeRPCSupervisor) currentProc() *fakeRPCProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastProc
}

type fakeIdleDetector struct {
	sec atomic.Int32
}

func (d *fakeIdleDetector) UserIdleSeconds() int {
	return int(d.sec.Load())
}

type fakeHardware struct{}

func (fakeHardware) MachineFingerprint() string {
	return "m-111122223333444455556666777788889999aaaabbbbccccddddeeeeffff1111"
}

func (fakeHardware) ProbeGPUs(_ context.Context, _ string) ([]protocol.GpuInfo, error) {
	return []protocol.GpuInfo{
		{DeviceID: "Vulkan0", Name: "NVIDIA RTX 4090", VramTotalMiB: 24576, VramFreeMiB: 24000},
	}, nil
}

func (fakeHardware) IsDedicatedGPU(_ string) bool { return true }

type fakeTunnelSocket struct {
	mu      sync.Mutex
	sent    []protocol.ControlMsg
	inbound chan []byte
	closed  chan struct{}
	once    sync.Once
}

func newFakeTunnelSocket() *fakeTunnelSocket {
	return &fakeTunnelSocket{
		inbound: make(chan []byte, 32),
		closed:  make(chan struct{}),
	}
}

func (s *fakeTunnelSocket) ReadFrame() (bool, []byte, error) {
	select {
	case raw := <-s.inbound:
		return false, raw, nil
	case <-s.closed:
		return false, nil, io.EOF
	}
}

func (s *fakeTunnelSocket) SendJSON(v any, _ time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var msg protocol.ControlMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return err
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return nil
}

func (s *fakeTunnelSocket) SendStreamChunk(_ uint32, _ []byte, _ time.Duration) error {
	return nil
}

func (s *fakeTunnelSocket) Close() error {
	s.once.Do(func() {
		close(s.closed)
	})
	return nil
}

func (s *fakeTunnelSocket) pushControl(t *testing.T, msg protocol.ControlMsg) {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	s.inbound <- raw
}

func (s *fakeTunnelSocket) sentMessages() []protocol.ControlMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.ControlMsg(nil), s.sent...)
}

type fakeTunnelDialer struct {
	mu      sync.Mutex
	sockets []*fakeTunnelSocket
	dialed  chan *fakeTunnelSocket
}

func newFakeTunnelDialer() *fakeTunnelDialer {
	return &fakeTunnelDialer{dialed: make(chan *fakeTunnelSocket, 16)}
}

func (d *fakeTunnelDialer) Dial(_ context.Context, _ string) (worker.TunnelSocket, error) {
	sock := newFakeTunnelSocket()
	d.mu.Lock()
	d.sockets = append(d.sockets, sock)
	d.mu.Unlock()
	d.dialed <- sock
	return sock, nil
}

func writeFakeRuntimeDir(t *testing.T, dataDir string) {
	t.Helper()
	binName := "ggml-rpc-server"
	if runtime.GOOS == "windows" {
		binName = "ggml-rpc-server.exe"
	}
	rtDir := filepath.Join(dataDir, "runtime-"+runtime.GOOS)
	if err := os.MkdirAll(rtDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rtDir, binName), []byte("stub"), 0o755); err != nil {
		t.Fatalf("write stub rpc binary: %v", err)
	}
}

func waitUntil(t *testing.T, check func() bool) {
	t.Helper()
	testkit.WaitUntil(t, 2*time.Second, "worker condition", check)
}

func startConnectedTestEngine(t *testing.T) (*worker.Engine, string, *fakeRPCSupervisor, *fakeIdleDetector, *fakeTunnelDialer, *fakeTunnelSocket) {
	t.Helper()
	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	rpcSup := &fakeRPCSupervisor{}
	idleDet := &fakeIdleDetector{}
	idleDet.sec.Store(300)
	dialer := newFakeTunnelDialer()

	deps := baseTestDeps(dir, filepath.Join(dir, "config.json"))
	deps.Boot = worker.BootConfig{
		GatewayURL:       "http://127.0.0.1:8000",
		Token:            "wk_test_token",
		WorkerName:       "TestNode",
		Devices:          []string{"Vulkan0"},
		DataDir:          dir,
		ReconnectDelay:   15 * time.Millisecond,
		IdlePollInterval: 15 * time.Millisecond,
	}
	deps.Idle = idleDet
	deps.RPC = rpcSup
	deps.Tunnel = dialer

	eng := worker.NewEngine(deps)
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { eng.Stop("cleanup") })

	var sock *fakeTunnelSocket
	select {
	case sock = <-dialer.dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for TunnelDialer.Dial")
	}
	waitUntil(t, func() bool {
		msgs := sock.sentMessages()
		return len(msgs) >= 1 && msgs[0].Type == "hello"
	})
	return eng, dir, rpcSup, idleDet, dialer, sock
}

func TestModelPreferencesPersistAndLiveWSUpdate(t *testing.T) {
	eng, dir, _, _, _, sock := startConnectedTestEngine(t)

	if err := eng.UpdateModelPreferences("fixed", []string{"qwen2.5-7b"}); err == nil {
		t.Fatalf("expected error when deprecated fixed mode is passed explicitly")
	}
	if err := eng.UpdateModelPreferences("multi", nil); err == nil {
		t.Fatalf("expected error when multi mode has zero selected models")
	}

	if err := eng.UpdateModelPreferences("multi", []string{"Qwen2.5-7B.gguf"}); err != nil {
		t.Fatalf("UpdateModelPreferences single-checkbox multi failed: %v", err)
	}
	if err := eng.UpdateModelPreferences("multi", []string{"Qwen2.5-7B.gguf", "llama-3.2-3b"}); err != nil {
		t.Fatalf("UpdateModelPreferences multi failed: %v", err)
	}

	msgs := sock.sentMessages()
	if len(msgs) != 3 || msgs[2].Type != "update_models" || msgs[2].ModelMode != protocol.ModelModeMulti {
		t.Fatalf("expected hello + 2 live update_models frames, got %+v", msgs)
	}
	if len(msgs[2].SelectedModels) != 2 || msgs[2].SelectedModels[0] != "qwen2.5-7b" {
		t.Fatalf("expected normalized selected models, got %+v", msgs[2].SelectedModels)
	}

	eng.Stop("stop before reload")
	reloaded := worker.NewEngine(baseTestDeps(dir, filepath.Join(dir, "config.json")))
	snap := reloaded.Snapshot()
	if snap.ModelMode != protocol.ModelModeMulti || len(snap.SelectedModels) != 2 {
		t.Fatalf("expected persisted multi mode on reload, got mode=%q models=%v", snap.ModelMode, snap.SelectedModels)
	}
}

func TestInvalidPersistedFixedConfigRecordsFieldWarning(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	legacyJSON := `{"modelMode":"fixed","selectedModels":["Llama-3.1-8B"]}`
	if err := os.WriteFile(cfgPath, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	eng := worker.NewEngine(baseTestDeps(dir, cfgPath))

	snap := eng.Snapshot()
	if snap.ModelMode != protocol.ModelModeAuto || len(snap.SelectedModels) != 0 || snap.LastError == "" {
		t.Fatalf("expected unsupported fixed config to record LastError and keep auto mode, got mode=%q models=%v lastErr=%q", snap.ModelMode, snap.SelectedModels, snap.LastError)
	}
}

func TestZeroVRAMPauseKillsAndRespawnsRPCProcess(t *testing.T) {
	eng, _, rpcSup, idleDet, _, sock := startConnectedTestEngine(t)
	initialProc := rpcSup.currentProc()
	if initialProc == nil || initialProc.stopped.Load() != 0 {
		t.Fatal("expected initial ggml-rpc-server running")
	}

	pauseTrue := true
	eng.UpdateSchedule("always", 120, &pauseTrue)
	if initialProc.stopped.Load() != 1 {
		t.Fatal("expected ggml-rpc-server process terminated immediately on manual pause (0 MiB VRAM)")
	}

	pauseFalse := false
	eng.UpdateSchedule("always", 120, &pauseFalse)
	respawned := rpcSup.currentProc()
	if rpcSup.spawnCount.Load() < 2 || respawned == nil || respawned.stopped.Load() != 0 {
		t.Fatalf("expected ggml-rpc-server respawned before unpausing, spawnCount=%d", rpcSup.spawnCount.Load())
	}

	msgs := sock.sentMessages()
	if len(msgs) < 3 || !msgs[1].Paused || msgs[2].Paused {
		t.Fatalf("expected pause_state true then false sent to gateway, got %+v", msgs)
	}

	eng.UpdateSchedule("idle", 60, nil)
	idleDet.sec.Store(1)
	waitUntil(t, func() bool {
		return respawned.stopped.Load() == 1
	})
}

func TestJobCreditAndGatewayStateCountersAreOrderIndependent(t *testing.T) {
	eng, _, _, _, dialer, sock1 := startConnectedTestEngine(t)

	sock1.pushControl(t, protocol.ControlMsg{
		Type: "state",
		Worker: &protocol.WorkerView{
			Status:          "ONLINE",
			BlocksProcessed: 12,
			TokensProcessed: 240,
			Balance:         0.15,
		},
	})
	sock1.pushControl(t, protocol.ControlMsg{
		Type:    "job_credit",
		Blocks:  12,
		Tokens:  240,
		Earned:  0.15,
		Balance: 0.15,
	})
	waitUntil(t, func() bool {
		snap := eng.Snapshot()
		return snap.BlocksProcessed == 12 && snap.TokensProcessed == 240
	})

	if err := sock1.Close(); err != nil {
		t.Fatalf("close sock1: %v", err)
	}
	var sock2 *fakeTunnelSocket
	select {
	case sock2 = <-dialer.dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reconnect dial")
	}

	sock2.pushControl(t, protocol.ControlMsg{
		Type:    "job_credit",
		Blocks:  8,
		Tokens:  160,
		Earned:  0.10,
		Balance: 0.25,
	})
	sock2.pushControl(t, protocol.ControlMsg{
		Type: "state",
		Worker: &protocol.WorkerView{
			Status:          "ONLINE",
			BlocksProcessed: 8,
			TokensProcessed: 160,
			Balance:         0.25,
		},
	})
	waitUntil(t, func() bool {
		snap := eng.Snapshot()
		return snap.BlocksProcessed == 20 && snap.TokensProcessed == 400
	})
}

func TestEnsureRuntimeFailsClosedWithoutManifestDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		http.NotFound(rw, req)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	eng := worker.NewEngine(baseTestDeps(dir, filepath.Join(dir, "config.json")))

	_, err := eng.EnsureRuntime(context.Background(), srv.URL)
	appErr, ok := derr.AsAppError(err)
	if !ok || appErr.Code != derr.CodeDigestMismatch {
		t.Fatalf("expected DIGEST_MISMATCH fail-closed error when runtime manifest is missing, got %v", err)
	}
}

func TestRefreshCatalogSyncsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/models" {
			http.NotFound(rw, req)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(rw).Encode(map[string]any{
			"models": []protocol.CatalogModelView{
				{ID: "qwen2.5-0.5b-instruct-q4_k_m", Name: "Qwen 0.5B", TargetVramMiB: 1024, Active: true},
				{ID: "llama-3.1-8b-q4_k_m", Name: "Llama 8B", TargetVramMiB: 6144, Active: false},
			},
		}); err != nil {
			t.Errorf("encode mock catalog: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	eng := worker.NewEngine(baseTestDeps(dir, filepath.Join(dir, "config.json")))
	models, err := eng.RefreshCatalog(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("RefreshCatalog failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 catalog models, got %d", len(models))
	}
	snap := eng.Snapshot()
	if len(snap.CatalogModels) != 2 {
		t.Fatalf("expected 2 cached models, got len=%d", len(snap.CatalogModels))
	}
}

func TestSelectedGPUsVRAMCalculation(t *testing.T) {
	gpus := []protocol.GpuInfo{
		{DeviceID: "Vulkan0", Name: "RTX 3060", VramTotalMiB: 12288, VramFreeMiB: 11800},
		{DeviceID: "Vulkan1", Name: "RTX 4060", VramTotalMiB: 8192, VramFreeMiB: 7900},
	}
	vram := worker.SelectedGPUsVRAM(gpus, []string{"Vulkan1"})
	if vram != 7900 {
		t.Fatalf("expected 7900 MiB for Vulkan1, got %d", vram)
	}
	both := worker.SelectedGPUsVRAM(gpus, []string{"Vulkan0", "Vulkan1"})
	if both != 19700 {
		t.Fatalf("expected 19700 MiB for both GPUs, got %d", both)
	}
}

func TestLanguageAutoDetectionAndManualPersistence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	depsEN := baseTestDeps(dir, cfgPath)
	depsEN.Boot.DefaultLanguage = "en"
	engAutoEN := worker.NewEngine(depsEN)
	if got := engAutoEN.Snapshot().Language; got != "en" {
		t.Fatalf("expected auto-detected en, got %q", got)
	}

	if got := engAutoEN.SetLanguage("pt-BR"); got != "pt-BR" {
		t.Fatalf("expected SetLanguage pt-BR, got %q", got)
	}

	depsReload := baseTestDeps(dir, cfgPath)
	depsReload.Boot.DefaultLanguage = "en"
	engReloaded := worker.NewEngine(depsReload)
	if got := engReloaded.Snapshot().Language; got != "pt-BR" {
		t.Fatalf("expected persisted manual override pt-BR over DefaultLanguage=en, got %q", got)
	}
}

func TestZeroConfigStartHardwareRequirementAndSessionFSM(t *testing.T) {
	dir := t.TempDir()
	noTokenDeps := baseTestDeps(dir, filepath.Join(dir, "notoken.json"))
	noTokenEng := worker.NewEngine(noTokenDeps)
	if err := noTokenEng.Start(worker.StartParams{}); err == nil {
		t.Fatal("expected Start without worker token to fail with CodeValidation")
	} else if appErr, ok := derr.AsAppError(err); !ok || appErr.Code != derr.CodeValidation {
		t.Fatalf("expected CodeValidation on missing-token Start, got %v", err)
	}

	noHWDeps := baseTestDeps(dir, filepath.Join(dir, "nohw.json"))
	noHWDeps.Hardware = nil
	noHWDeps.Boot.Token = "wk_nohw"
	noHWEng := worker.NewEngine(noHWDeps)
	if err := noHWEng.Start(worker.StartParams{}); err == nil {
		t.Fatal("expected Start to fail with CodeNoVulkanGPU when Hardware is nil")
	} else if appErr, ok := derr.AsAppError(err); !ok || appErr.Code != derr.CodeNoVulkanGPU {
		t.Fatalf("expected CodeNoVulkanGPU, got %v", err)
	}

	writeFakeRuntimeDir(t, dir)
	rpcSup := &fakeRPCSupervisor{}
	dialer := newFakeTunnelDialer()
	deps := baseTestDeps(dir, filepath.Join(dir, "fsm.json"))
	deps.Boot.Token = "wk_fsm"
	deps.Boot.Devices = []string{"Vulkan0"}
	deps.RPC = rpcSup
	deps.Tunnel = dialer

	eng := worker.NewEngine(deps)
	if eng.Snapshot().SessionState != "CLOSED" {
		t.Fatalf("expected initial SessionState=CLOSED, got %q", eng.Snapshot().SessionState)
	}
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { eng.Stop("cleanup") })

	sock := <-dialer.dialed
	waitUntil(t, func() bool {
		msgs := sock.sentMessages()
		return len(msgs) >= 1 && msgs[0].Type == "hello"
	})
	if eng.Snapshot().SessionState != "IDLE" {
		t.Fatalf("expected SessionState=IDLE after zero-config hello, got %q", eng.Snapshot().SessionState)
	}

	sock.pushControl(t, protocol.ControlMsg{
		Type:   "state",
		PoolID: "pool-fsm-1",
		Worker: &protocol.WorkerView{Status: "READY", PoolID: "pool-fsm-1"},
	})
	waitUntil(t, func() bool {
		return eng.Snapshot().SessionState == "IN_POOL"
	})

	eng.Stop("done")
	if eng.Snapshot().SessionState != "CLOSED" {
		t.Fatalf("expected SessionState=CLOSED after Stop, got %q", eng.Snapshot().SessionState)
	}
}

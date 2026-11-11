package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type fakeWorkerHardware struct {
	gpus []protocol.GpuInfo
}

func (h *fakeWorkerHardware) MachineFingerprint() string { return "m-unit-worker-01" }

func (h *fakeWorkerHardware) ProbeGPUs(_ context.Context, _ string) ([]protocol.GpuInfo, error) {
	return h.gpus, nil
}

func (h *fakeWorkerHardware) IsDedicatedGPU(_ string) bool { return true }

type fakeWorkerIdle struct {
	sec atomic.Int64
}

func (i *fakeWorkerIdle) UserIdleSeconds() int { return int(i.sec.Load()) }

type fakeRunningProc struct {
	done    chan struct{}
	once    sync.Once
	stopped atomic.Bool
}

func newFakeRunningProc() *fakeRunningProc {
	return &fakeRunningProc{done: make(chan struct{})}
}

func (p *fakeRunningProc) Stop() {
	p.stopped.Store(true)
	p.once.Do(func() { close(p.done) })
}

func (p *fakeRunningProc) Done() <-chan struct{} { return p.done }

type fakeRPCSupervisor struct {
	mu     sync.Mutex
	spawns int
	procs  []*fakeRunningProc
}

func (r *fakeRPCSupervisor) SpawnRPC(_ context.Context, _, _ string, _ []protocol.GpuInfo, _ int) (worker.RunningRPCProcess, error) {
	p := newFakeRunningProc()
	r.mu.Lock()
	r.spawns++
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	return p, nil
}

func (r *fakeRPCSupervisor) stats() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stopped := 0
	for _, p := range r.procs {
		if p.stopped.Load() {
			stopped++
		}
	}
	return r.spawns, stopped
}

type fakeTunnelSocket struct {
	mu      sync.Mutex
	inbound chan []byte
	sent    chan protocol.ControlMsg
	closed  chan struct{}
	once    sync.Once
}

func newFakeTunnelSocket() *fakeTunnelSocket {
	return &fakeTunnelSocket{
		inbound: make(chan []byte, 8),
		sent:    make(chan protocol.ControlMsg, 16),
		closed:  make(chan struct{}),
	}
}

func (s *fakeTunnelSocket) ReadFrame() (bool, []byte, error) {
	select {
	case b := <-s.inbound:
		return false, b, nil
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
	select {
	case s.sent <- msg:
	default:
	}
	s.mu.Unlock()
	return nil
}

func (s *fakeTunnelSocket) SendStreamChunk(_ uint32, _ []byte, _ time.Duration) error { return nil }

func (s *fakeTunnelSocket) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

type fakeTunnelDialer struct {
	sock *fakeTunnelSocket
}

func (d *fakeTunnelDialer) Dial(_ context.Context, _ string) (worker.TunnelSocket, error) {
	return d.sock, nil
}

type fakeRuntimeFS struct{}

func (fakeRuntimeFS) FindExistingRuntime(dataDir, _ string) string { return dataDir + "/runtime" }
func (fakeRuntimeFS) PrepareStagingDir(dataDir string) (string, error) {
	return dataDir + "/stage", nil
}
func (fakeRuntimeFS) ActivateStagedRuntime(_, _, _ string) error { return nil }
func (fakeRuntimeFS) RemovePath(_ string) error                  { return nil }
func (fakeRuntimeFS) DefaultHostInfo() (string, int)             { return "unit-host", 4242 }

type fakeLoopback struct{}

func (fakeLoopback) AllocateFreePort() (int, error) { return 29991, nil }
func (fakeLoopback) DialStream(_ int, _ time.Duration) (io.ReadWriteCloser, error) {
	return nil, errors.New("not dialed")
}
func (fakeLoopback) ProbeHTTPHealth(_ context.Context, _ int) bool { return true }

type fakeConfigStore struct {
	mu      sync.Mutex
	saved   nodecore.WorkerPersistedConfig
	hasCfg  bool
	saveErr error
}

func (c *fakeConfigStore) DataDir() string { return "/virtual/worker-cache" }

func (c *fakeConfigStore) LoadConfig() (nodecore.WorkerPersistedConfig, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saved, c.hasCfg, nil
}

func (c *fakeConfigStore) SaveConfig(cfg nodecore.WorkerPersistedConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saveErr != nil {
		return c.saveErr
	}
	c.saved = cfg
	c.hasCfg = true
	return nil
}

type fakeModelCache struct {
	mu    sync.Mutex
	calls int
}

func (m *fakeModelCache) EnsureModelCached(_ context.Context, _, _, _, _, _ string, _ func(int64, int64)) (string, string, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return "/virtual/model.gguf", strings.Repeat("a", 64), nil
}

func waitControlType(t *testing.T, ch <-chan protocol.ControlMsg, wantType string) protocol.ControlMsg {
	t.Helper()
	for {
		select {
		case msg := <-ch:
			if msg.Type == wantType {
				return msg
			}
		case <-time.After(40 * time.Millisecond):
			t.Fatalf("timed out waiting for control frame %q", wantType)
		}
	}
}

func TestValidateWorkerDownloadURLAndSoloControlURLGuard(t *testing.T) {
	if _, err := nodecore.ValidateWorkerDownloadURL("http://cdn.example.com/model.gguf", "https://grid.example.com", false); err == nil {
		t.Fatal("expected plain HTTP download URL to be rejected when allowPlainHTTP=false")
	}
	if _, err := nodecore.ValidateWorkerDownloadURL("https://169.254.169.254/latest/meta-data", "https://grid.example.com", false); err == nil {
		t.Fatal("expected link-local metadata IP SSRF download URL to be rejected")
	}
	if _, err := nodecore.ValidateWorkerDownloadURL("https://user:pass@cdn.example.com/model.gguf", "https://grid.example.com", false); err == nil {
		t.Fatal("expected embedded userinfo credentials in download URL to be rejected")
	}
	validURL, err := nodecore.ValidateWorkerDownloadURL("https://cdn.example.com/models/qwen-7b.gguf", "https://grid.example.com", false)
	if err != nil || validURL != "https://cdn.example.com/models/qwen-7b.gguf" {
		t.Fatalf("expected valid HTTPS CDN download URL, got %q err=%v", validURL, err)
	}

	sock := newFakeTunnelSocket()
	cache := &fakeModelCache{}
	rpc := &fakeRPCSupervisor{}
	eng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			GatewayURL: "https://grid.example.com",
			Token:      "wk_secret_unit",
			WorkerName: "UnitNode",
			Devices:    []string{"Vulkan0"},
			DataDir:    "/virtual/worker-cache",
		},
		Hardware:    &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}}},
		RPC:         rpc,
		Tunnel:      &fakeTunnelDialer{sock: sock},
		ModelCache:  cache,
		ConfigStore: &fakeConfigStore{},
		RuntimeFS:   fakeRuntimeFS{},
		Loopback:    fakeLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})

	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Engine.Start failed: %v", err)
	}
	defer eng.Stop("test_cleanup")

	waitControlType(t, sock.sent, protocol.ControlHello)

	ssrfCtrl, mErr := json.Marshal(protocol.ControlMsg{
		Type:             protocol.ControlConfigureSoloEngine,
		PoolID:           "pool-solo-1",
		ModelID:          "qwen-7b",
		TargetBackend:    "llama_local",
		ModelDownloadURL: "https://127.0.0.1:17771/api/status",
	})
	if mErr != nil {
		t.Fatalf("json.Marshal failed: %v", mErr)
	}
	sock.inbound <- ssrfCtrl

	errFrame := waitControlType(t, sock.sent, protocol.ControlSoloEngineError)
	if errFrame.PoolID != "pool-solo-1" || cache.calls != 0 {
		t.Fatalf("expected SSRF solo download URL to be blocked before ModelCacheVerifier, frame=%+v cacheCalls=%d", errFrame, cache.calls)
	}
}

func TestWorkerPauseResumeStateMachineAndIdleScheduleTransitions(t *testing.T) {
	sock := newFakeTunnelSocket()
	rpc := &fakeRPCSupervisor{}
	idleSensor := &fakeWorkerIdle{}
	idleSensor.sec.Store(300)

	eng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			GatewayURL: "https://grid.example.com",
			Token:      "wk_pause_unit",
			WorkerName: "PauseNode",
			Devices:    []string{"Vulkan0"},
			DataDir:    "/virtual/worker-cache",
		},
		Hardware:    &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4080", VramTotalMiB: 16384}}},
		Idle:        idleSensor,
		RPC:         rpc,
		Tunnel:      &fakeTunnelDialer{sock: sock},
		ConfigStore: &fakeConfigStore{},
		RuntimeFS:   fakeRuntimeFS{},
		Loopback:    fakeLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})

	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Engine.Start failed: %v", err)
	}
	defer eng.Stop("test_cleanup")

	waitControlType(t, sock.sent, protocol.ControlHello)
	spawns1, stops1 := rpc.stats()
	if spawns1 != 1 || stops1 != 0 {
		t.Fatalf("expected 1 active RPC process after start, spawns=%d stops=%d", spawns1, stops1)
	}

	pausedTrue := true
	eng.UpdateSchedule("", 0, &pausedTrue)
	pauseMsg := waitControlType(t, sock.sent, protocol.ControlPauseState)
	snapPaused := eng.Snapshot()
	spawnsDuringPause, stopsAfterPause := rpc.stats()
	if !pauseMsg.Paused || !snapPaused.Paused || snapPaused.Status != "PAUSED" || snapPaused.SessionState != string(nodecore.WorkerPaused) || spawnsDuringPause != 1 || stopsAfterPause != 1 {
		t.Fatalf("expected manual pause to stop RPC and transition to PAUSED, msg=%+v snap=%+v spawns=%d stops=%d", pauseMsg, snapPaused, spawnsDuringPause, stopsAfterPause)
	}

	pausedFalse := false
	eng.UpdateSchedule("idle", 60, &pausedFalse)
	resumeMsg := waitControlType(t, sock.sent, protocol.ControlPauseState)
	snapResumed := eng.Snapshot()
	spawnsAfterResume, stopsAfterResume := rpc.stats()
	if resumeMsg.Paused || snapResumed.Paused || snapResumed.Status != "CONNECTED" || snapResumed.SessionState != string(nodecore.WorkerIdle) || spawnsAfterResume != 2 || stopsAfterResume != 1 {
		t.Fatalf("expected resume to respawn RPC and transition to CONNECTED/IDLE, msg=%+v snap=%+v spawns=%d stops=%d", resumeMsg, snapResumed, spawnsAfterResume, stopsAfterResume)
	}

	idleSensor.sec.Store(1)
	eng.UpdateSchedule("idle", 60, nil)
	autoPauseMsg := waitControlType(t, sock.sent, protocol.ControlPauseState)
	snapAutoPaused := eng.Snapshot()
	if !autoPauseMsg.Paused || !snapAutoPaused.AutoPaused || snapAutoPaused.Status != "PAUSED" {
		t.Fatalf("expected desktop activity (idle=1s < 5s) to trigger Smart Auto-Pause, msg=%+v snap=%+v", autoPauseMsg, snapAutoPaused)
	}
}

func TestTransactionalApplySettingsRollbackAndRunningLockGuard(t *testing.T) {
	store := &fakeConfigStore{}
	sock := newFakeTunnelSocket()
	eng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			GatewayURL: "https://grid.example.com",
			Token:      "wk_tx_unit",
			WorkerName: "TxNode",
			Devices:    []string{"Vulkan0"},
			DataDir:    "/virtual/worker-cache",
		},
		Hardware:    &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}}},
		RPC:         &fakeRPCSupervisor{},
		Tunnel:      &fakeTunnelDialer{sock: sock},
		ConfigStore: store,
		RuntimeFS:   fakeRuntimeFS{},
		Loopback:    fakeLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})

	if err := eng.ApplySettings(worker.UpdateSettings{
		WorkerName:    "CommittedNode",
		WorkerNameSet: true,
		Language:      "pt-BR",
		LanguageSet:   true,
		UIMode:        "advanced",
		UIModeSet:     true,
	}); err != nil {
		t.Fatalf("baseline ApplySettings failed: %v", err)
	}
	before := eng.Snapshot()
	if before.WorkerName != "CommittedNode" || before.Language != "pt-BR" || before.UIMode != "advanced" {
		t.Fatalf("unexpected committed baseline snapshot: %+v", before)
	}

	store.mu.Lock()
	store.saveErr = errors.New("simulated EIO config write failure")
	store.mu.Unlock()

	err := eng.ApplySettings(worker.UpdateSettings{
		WorkerName:        "FailedNode",
		WorkerNameSet:     true,
		Language:          "en",
		LanguageSet:       true,
		UIMode:            "simple",
		UIModeSet:         true,
		ModelMode:         "multi",
		ModelModeSet:      true,
		SelectedModels:    []string{"qwen-7b"},
		SelectedModelsSet: true,
	})
	if derr.StatusOf(err) != 500 {
		t.Fatalf("expected 500 CodeInternal when SaveConfig fails, got %v", err)
	}
	afterRollback := eng.Snapshot()
	if afterRollback.WorkerName != "CommittedNode" || afterRollback.Language != "pt-BR" || afterRollback.UIMode != "advanced" || afterRollback.ModelMode != "auto" {
		t.Fatalf("expected transactional rollback to restore exact prior in-memory config, got %+v", afterRollback)
	}

	store.mu.Lock()
	store.saveErr = nil
	store.mu.Unlock()
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Engine.Start failed: %v", err)
	}
	defer eng.Stop("test_cleanup")
	waitControlType(t, sock.sent, protocol.ControlHello)

	lockErr := eng.ApplySettings(worker.UpdateSettings{
		GatewayURL:    "https://other.example.com",
		GatewayURLSet: true,
	})
	if derr.StatusOf(lockErr) != 409 {
		t.Fatalf("expected 409 CodeIllegalState when mutating gatewayURL while running, got %v", lockErr)
	}
}

type fakeSoloSupervisor struct{}

func (fakeSoloSupervisor) SupportedBackends(_ string) []string {
	return []string{string(nodecore.BackendSafetensors)}
}

func (fakeSoloSupervisor) SpawnSolo(_ context.Context, _ worker.SoloEngineSpec) (worker.RunningRPCProcess, error) {
	return newFakeRunningProc(), nil
}

type deadlineCapturingLoopback struct {
	fakeLoopback
	observedBudget chan time.Duration
}

func (l *deadlineCapturingLoopback) ProbeHTTPHealth(ctx context.Context, _ int) bool {
	if dl, ok := ctx.Deadline(); ok {
		select {
		case l.observedBudget <- time.Until(dl):
		default:
		}
	}
	return true
}

func TestWorkerBootConfigSoloEngineBootTimeoutPropagatesToHealthProbe(t *testing.T) {
	sock := newFakeTunnelSocket()
	loopback := &deadlineCapturingLoopback{observedBudget: make(chan time.Duration, 1)}
	eng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			GatewayURL:            "https://grid.example.com",
			Token:                 "wk_secret_unit",
			WorkerName:            "SoloTimeoutNode",
			Devices:               []string{"Vulkan0"},
			EnabledBackends:       []string{string(nodecore.BackendSafetensors)},
			SoloEngineBootTimeout: 5 * time.Minute,
			DataDir:               "/virtual/worker-cache",
		},
		Hardware:   &fakeWorkerHardware{gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}}},
		RPC:        &fakeRPCSupervisor{},
		Solo:       fakeSoloSupervisor{},
		Tunnel:     &fakeTunnelDialer{sock: sock},
		ModelCache: &fakeModelCache{},
		RuntimeFS:  fakeRuntimeFS{},
		Loopback:   loopback,
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Engine.Start failed: %v", err)
	}
	defer eng.Stop("test_cleanup")
	waitControlType(t, sock.sent, protocol.ControlHello)

	cfgBytes, err := json.Marshal(protocol.ControlMsg{
		Type:             protocol.ControlConfigureSoloEngine,
		PoolID:           "pool-solo-timeout",
		ModelID:          "qwen-safetensors",
		ModelSHA256:      strings.Repeat("a", 64),
		ModelDownloadURL: "https://grid.example.com/models/qwen-safetensors",
		TargetBackend:    string(nodecore.BackendSafetensors),
	})
	if err != nil {
		t.Fatalf("marshal configure_solo_engine: %v", err)
	}
	sock.inbound <- cfgBytes

	readyMsg := waitControlType(t, sock.sent, protocol.ControlSoloEngineReady)
	if readyMsg.PoolID != "pool-solo-timeout" {
		t.Fatalf("unexpected solo_engine_ready poolId: %+v", readyMsg)
	}
	select {
	case budget := <-loopback.observedBudget:
		if budget < 4*time.Minute {
			t.Fatalf("expected ProbeHTTPHealth deadline budget >= 4m from SoloEngineBootTimeout=5m, got %v", budget)
		}
	default:
		t.Fatal("expected ProbeHTTPHealth to be invoked with configured SoloEngineBootTimeout deadline")
	}
}

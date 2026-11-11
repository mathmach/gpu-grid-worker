package worker_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type duplexFrame struct {
	binary bool
	data   []byte
}

type duplexTunnelSocket struct {
	mu          sync.Mutex
	sent        []protocol.ControlMsg
	streamBytes map[uint32][]byte
	inbound     chan duplexFrame
	closed      chan struct{}
	once        sync.Once
}

func newDuplexTunnelSocket() *duplexTunnelSocket {
	return &duplexTunnelSocket{
		streamBytes: make(map[uint32][]byte),
		inbound:     make(chan duplexFrame, 256),
		closed:      make(chan struct{}),
	}
}

func (s *duplexTunnelSocket) ReadFrame() (bool, []byte, error) {
	select {
	case fr := <-s.inbound:
		return fr.binary, fr.data, nil
	case <-s.closed:
		return false, nil, io.EOF
	}
}

func (s *duplexTunnelSocket) SendJSON(v any, timeout time.Duration) error {
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

func (s *duplexTunnelSocket) SendStreamChunk(streamID uint32, payload []byte, timeout time.Duration) error {
	s.mu.Lock()
	s.streamBytes[streamID] = append(s.streamBytes[streamID], payload...)
	s.mu.Unlock()
	return nil
}

func (s *duplexTunnelSocket) Close() error {
	s.once.Do(func() {
		close(s.closed)
	})
	return nil
}

func (s *duplexTunnelSocket) pushControl(t *testing.T, msg protocol.ControlMsg) {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	s.inbound <- duplexFrame{binary: false, data: raw}
}

func (s *duplexTunnelSocket) pushStream(sid uint32, payload []byte) {
	s.inbound <- duplexFrame{binary: true, data: protocol.EncodeStreamFrame(sid, payload)}
}

func (s *duplexTunnelSocket) sentMessages() []protocol.ControlMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.ControlMsg(nil), s.sent...)
}

func (s *duplexTunnelSocket) receivedStreamData(sid uint32) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.streamBytes[sid])
}

type duplexTunnelDialer struct {
	dialed chan *duplexTunnelSocket
}

func newDuplexTunnelDialer() *duplexTunnelDialer {
	return &duplexTunnelDialer{dialed: make(chan *duplexTunnelSocket, 8)}
}

func (d *duplexTunnelDialer) Dial(ctx context.Context, gatewayURL string) (worker.TunnelSocket, error) {
	sock := newDuplexTunnelSocket()
	d.dialed <- sock
	return sock, nil
}

type mockHTTPServerSoloProc struct {
	t       testing.TB
	srv     *http.Server
	done    chan struct{}
	stopped atomic.Int32
	once    sync.Once
}

func (p *mockHTTPServerSoloProc) Stop() {
	p.once.Do(func() {
		p.stopped.Store(1)
		if err := p.srv.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) && p.t != nil {
			p.t.Logf("close mock solo http server: %v", err)
		}
		close(p.done)
	})
}

func (p *mockHTTPServerSoloProc) Done() <-chan struct{} {
	return p.done
}

type mockSoloSupervisor struct {
	t        testing.TB
	mu       sync.Mutex
	backends []string
	lastSpec worker.SoloEngineSpec
	lastProc *mockHTTPServerSoloProc
}

func (m *mockSoloSupervisor) SupportedBackends(runtimeDir string) []string {
	return append([]string(nil), m.backends...)
}

func (m *mockSoloSupervisor) SpawnSolo(ctx context.Context, spec worker.SoloEngineSpec) (worker.RunningRPCProcess, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", spec.ListenPort))
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, wErr := w.Write([]byte(`{"status":"ok"}`)); wErr != nil && m.t != nil {
			m.t.Errorf("write health response: %v", wErr)
		}
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusOK)
		if _, wErr := w.Write([]byte(`{"id":"solo-resp","model":"` + spec.ModelID + `"}`)); wErr != nil && m.t != nil {
			m.t.Errorf("write chat response: %v", wErr)
		}
	})
	srv := &http.Server{Handler: mux}
	proc := &mockHTTPServerSoloProc{
		t:    m.t,
		srv:  srv,
		done: make(chan struct{}),
	}
	go func() {
		if srvErr := srv.Serve(ln); srvErr != nil && !errors.Is(srvErr, http.ErrServerClosed) && !errors.Is(srvErr, net.ErrClosed) && m.t != nil {
			m.t.Logf("serve mock solo http: %v", srvErr)
		}
	}()

	m.mu.Lock()
	m.lastSpec = spec
	m.lastProc = proc
	m.mu.Unlock()
	return proc, nil
}

func (m *mockSoloSupervisor) snapshotLast() (worker.SoloEngineSpec, *mockHTTPServerSoloProc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSpec, m.lastProc
}

type portBoundRPCSupervisor struct {
	t       testing.TB
	handler func(connIndex int, c net.Conn)
	procs   []*listenerRPCProc
	mu      sync.Mutex
}

type listenerRPCProc struct {
	t       testing.TB
	ln      net.Listener
	done    chan struct{}
	stopped atomic.Int32
	once    sync.Once
}

func (p *listenerRPCProc) Stop() {
	p.once.Do(func() {
		p.stopped.Store(1)
		if err := p.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) && p.t != nil {
			p.t.Logf("close rpc listener: %v", err)
		}
		close(p.done)
	})
}

func (p *listenerRPCProc) Done() <-chan struct{} {
	return p.done
}

func (s *portBoundRPCSupervisor) SpawnRPC(ctx context.Context, runtimeDir, binaryName string, gpus []protocol.GpuInfo, port int) (worker.RunningRPCProcess, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	proc := &listenerRPCProc{t: s.t, ln: ln, done: make(chan struct{})}
	s.mu.Lock()
	s.procs = append(s.procs, proc)
	s.mu.Unlock()

	var idx atomic.Int32
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			connNum := int(idx.Add(1))
			go s.handler(connNum, c)
		}
	}()
	return proc, nil
}

func (s *portBoundRPCSupervisor) latestProc() *listenerRPCProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.procs) == 0 {
		return nil
	}
	return s.procs[len(s.procs)-1]
}

func TestObserverSubscribePushesLatestSnapshotWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	deps := baseTestDeps(dir, filepath.Join(dir, "config.json"))
	deps.Boot.DefaultLanguage = "en"
	eng := worker.NewEngine(deps)

	subCh, unsub := eng.Subscribe()
	eng.SetLanguage("en")
	eng.UpdateSelectedGPUs([]string{"Vulkan0"})
	eng.SetLanguage("pt-BR")

	select {
	case snap := <-subCh:
		if snap.Language != "pt-BR" || len(snap.SelectedIDs) != 1 || snap.SelectedIDs[0] != "Vulkan0" {
			t.Fatalf("expected latest coalesced snapshot pushed to subscriber, got lang=%q gpus=%v", snap.Language, snap.SelectedIDs)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observer push snapshot")
	}

	unsub()
	unsub()

	_, stillOpen := <-subCh
	if stillOpen {
		t.Fatal("expected subscriber channel closed on unsubscribe")
	}
	eng.SetLanguage("en")
}

func TestPersistedConfigAtomicWritePermissionsAndReload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	deps := baseTestDeps(dir, cfgPath)
	deps.Solo = &mockSoloSupervisor{t: t, backends: []string{"llama_local", "onnx_genai"}}
	eng := worker.NewEngine(deps)
	eng.UpdateSelectedGPUs([]string{"Vulkan0"})
	if err := eng.ApplySettings(worker.UpdateSettings{
		WorkerName:      "PersistedNode",
		WorkerNameSet:   true,
		UIMode:          "advanced",
		UIModeSet:       true,
		EnabledBackends: []string{"llama_local"},
		BackendsSet:     true,
	}); err != nil {
		t.Fatalf("ApplySettings failed: %v", err)
	}

	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat config.json: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions on config.json, got %o", st.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("unexpected leftover temp config file: %s", entry.Name())
		}
	}

	reloadDeps := baseTestDeps(dir, cfgPath)
	reloadDeps.Solo = &mockSoloSupervisor{t: t, backends: []string{"llama_local", "onnx_genai"}}
	reloaded := worker.NewEngine(reloadDeps)
	snap := reloaded.Snapshot()
	if len(snap.EnabledBackends) != 1 || snap.EnabledBackends[0] != "llama_local" {
		t.Fatalf("expected reloaded EnabledBackends=[llama_local], got %v", snap.EnabledBackends)
	}
	if len(snap.SupportedBackends) != 1 || snap.SupportedBackends[0] != "llama_local" {
		t.Fatalf("expected filtered SupportedBackends=[llama_local], got %v", snap.SupportedBackends)
	}
}

func TestRuntimeBootstrapperRetriesTransientFailureAndAdvertisesBackends(t *testing.T) {
	archiveBytes, archiveSHA := buildTestRuntimeArchive(t)
	archiveName := "worker-runtime-linux-amd64.tar.gz"
	if runtime.GOOS == "windows" {
		archiveName = "worker-runtime-windows-amd64.zip"
	}

	var failManifest atomic.Bool
	failManifest.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failManifest.Load() {
			http.Error(w, "temporary outage", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/runtime/manifest" {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"artifacts": map[string]any{
					archiveName: map[string]string{"sha256": archiveSHA},
				},
			}); err != nil {
				t.Errorf("encode runtime manifest: %v", err)
			}
			return
		}
		if r.URL.Path == "/assets/"+archiveName {
			if _, err := w.Write(archiveBytes); err != nil {
				t.Errorf("write runtime archive: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	eng := worker.NewEngine(baseTestDeps(dir, filepath.Join(dir, "config.json")))

	if _, err := eng.EnsureRuntime(context.Background(), srv.URL); err == nil {
		t.Fatal("expected transient manifest failure on first EnsureRuntime attempt")
	}

	failManifest.Store(false)
	rtDir, err := eng.EnsureRuntime(context.Background(), srv.URL)
	if err != nil || rtDir == "" {
		t.Fatalf("expected EnsureRuntime retry to succeed after transient error, dir=%q err=%v", rtDir, err)
	}

	backends := eng.Snapshot().SupportedBackends
	if len(backends) != 2 || backends[0] != "llama_rpc" || backends[1] != "llama_local" {
		t.Fatalf("expected [llama_rpc llama_local] advertised when llama-server binary exists, got %v", backends)
	}
}

func TestWarmCacheEnsureRuntimeProbesRemoteModularPacks(t *testing.T) {
	ext := ".tar.gz"
	osArch := "linux-amd64"
	if runtime.GOOS == "windows" {
		ext = ".zip"
		osArch = "windows-amd64"
	}
	diffArchive := "worker-runtime-diffusion-" + osArch + ext
	audioArchive := "worker-runtime-audio-" + osArch + ext
	dummySHA := strings.Repeat("a", 64)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runtime/manifest" {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"artifacts": map[string]any{
					diffArchive:  map[string]string{"sha256": dummySHA},
					audioArchive: map[string]string{"sha256": dummySHA},
				},
			}); err != nil {
				t.Errorf("encode modular runtime manifest: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	eng := worker.NewEngine(baseTestDeps(dir, filepath.Join(dir, "config.json")))

	if _, err := eng.EnsureRuntime(context.Background(), srv.URL); err != nil {
		t.Fatalf("warm-cache EnsureRuntime failed: %v", err)
	}
	got := strings.Join(eng.Snapshot().SupportedBackends, ",")
	if !strings.Contains(got, "diffusion_local") || !strings.Contains(got, "whisper_local") {
		t.Fatalf("expected warm-cache EnsureRuntime to advertise remote modular packs [diffusion_local, whisper_local], got %q", got)
	}
}

func buildTestRuntimeArchive(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		for _, name := range []string{"ggml-rpc-server.exe", "llama-server.exe"} {
			fw, err := zw.Create(name)
			if err != nil {
				t.Fatalf("zip create %s: %v", name, err)
			}
			if _, err := fw.Write([]byte("binary-stub")); err != nil {
				t.Fatalf("zip write %s: %v", name, err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
	} else {
		gzw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gzw)
		for _, name := range []string{"ggml-rpc-server", "llama-server"} {
			payload := []byte("binary-stub")
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatalf("tar header %s: %v", name, err)
			}
			if _, err := tw.Write(payload); err != nil {
				t.Fatalf("tar write %s: %v", name, err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("tar close: %v", err)
		}
		if err := gzw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestSoloEngineLifecycleHttpTunnelForwardingAndStopCleanup(t *testing.T) {
	modelBytes := []byte("solo-gguf-verified-weights")
	sum := sha256.Sum256(modelBytes)
	modelSHA := hex.EncodeToString(sum[:])

	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models/qwen-solo.gguf" {
			if _, err := w.Write(modelBytes); err != nil {
				t.Errorf("write solo model bytes: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(modelSrv.Close)

	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	soloSup := &mockSoloSupervisor{t: t, backends: []string{"llama_local"}}
	dialer := newDuplexTunnelDialer()

	soloDeps := baseTestDeps(dir, filepath.Join(dir, "config.json"))
	soloDeps.Boot = worker.BootConfig{
		GatewayURL:     modelSrv.URL,
		Token:          "wk_solo_test",
		WorkerName:     "SoloNode",
		Devices:        []string{"Vulkan0"},
		DataDir:        dir,
		ReconnectDelay: 20 * time.Millisecond,
	}
	soloDeps.Idle = &fakeIdleDetector{}
	soloDeps.RPC = &fakeRPCSupervisor{}
	soloDeps.Solo = soloSup
	soloDeps.Tunnel = dialer
	eng := worker.NewEngine(soloDeps)
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { eng.Stop("cleanup") })

	sock := <-dialer.dialed
	waitUntil(t, func() bool {
		msgs := sock.sentMessages()
		return len(msgs) >= 1 && msgs[0].Type == "hello" && len(msgs[0].SupportedBackends) == 2
	})

	sock.pushControl(t, protocol.ControlMsg{
		Type:             "configure_solo_engine",
		PoolID:           "pool-solo-1",
		ModelID:          "qwen-solo",
		ModelSHA256:      strings.Repeat("f", 64),
		ModelDownloadURL: modelSrv.URL + "/models/qwen-solo.gguf",
		ModelFileName:    "qwen-solo.gguf",
		TargetBackend:    "llama_local",
	})
	waitUntil(t, func() bool {
		for _, m := range sock.sentMessages() {
			if m.Type == "solo_engine_error" && m.PoolID == "pool-solo-1" && m.Reason != "" {
				return true
			}
		}
		return false
	})

	sock.pushControl(t, protocol.ControlMsg{
		Type:             "configure_solo_engine",
		PoolID:           "pool-solo-2",
		ModelID:          "qwen-solo",
		ModelSHA256:      modelSHA,
		ModelDownloadURL: "/models/qwen-solo.gguf",
		ModelFileName:    "qwen-solo.gguf",
		TargetBackend:    "llama_local",
		ContextLength:    4096,
		ParallelSlots:    2,
	})
	waitUntil(t, func() bool {
		for _, m := range sock.sentMessages() {
			if m.Type == "solo_engine_ready" && m.PoolID == "pool-solo-2" && m.TargetBackend == "llama_local" {
				return true
			}
		}
		return false
	})

	if eng.Snapshot().ExecutionMode != protocol.ExecutionModeSolo {
		t.Fatalf("expected Snapshot ExecutionMode=SOLO, got %q", eng.Snapshot().ExecutionMode)
	}
	spec, soloProc := soloSup.snapshotLast()
	wantModelPath := filepath.Join(dir, "models", modelSHA, "qwen-solo.gguf")
	if spec.ModelPath != wantModelPath {
		t.Fatalf("expected SOLO spec ModelPath=%s, got %s", wantModelPath, spec.ModelPath)
	}

	sock.pushControl(t, protocol.ControlMsg{
		Type:         "tunnel_open",
		StreamID:     77,
		TunnelTarget: protocol.TunnelTargetSoloHTTP,
	})
	httpReq := "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"
	sock.pushStream(77, []byte(httpReq))

	waitUntil(t, func() bool {
		return strings.Contains(sock.receivedStreamData(77), `"id":"solo-resp"`)
	})

	sock.pushControl(t, protocol.ControlMsg{
		Type:   "stop_solo_engine",
		PoolID: "pool-solo-2",
	})
	waitUntil(t, func() bool {
		return soloProc != nil && soloProc.stopped.Load() == 1
	})
}

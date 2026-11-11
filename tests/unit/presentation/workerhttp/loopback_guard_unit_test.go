package workerhttp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workerhttp"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type ipcHardwareStub struct {
	gpus []protocol.GpuInfo
}

func (h ipcHardwareStub) MachineFingerprint() string { return "m-unit-ipc-01" }

func (h ipcHardwareStub) ProbeGPUs(context.Context, string) ([]protocol.GpuInfo, error) {
	return h.gpus, nil
}

func (h ipcHardwareStub) IsDedicatedGPU(string) bool { return true }

type ipcIdleStub struct{}

func (ipcIdleStub) UserIdleSeconds() int { return 0 }

type ipcConfigStore struct {
	mu         sync.Mutex
	savedCount int
	lastSaved  nodecore.WorkerPersistedConfig
}

func (s *ipcConfigStore) DataDir() string { return "/virtual/ipc-data" }

func (s *ipcConfigStore) LoadConfig() (nodecore.WorkerPersistedConfig, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.savedCount == 0 {
		return nodecore.WorkerPersistedConfig{}, false, nil
	}
	return s.lastSaved, true, nil
}

func (s *ipcConfigStore) SaveConfig(cfg nodecore.WorkerPersistedConfig) error {
	s.mu.Lock()
	s.savedCount++
	s.lastSaved = cfg
	s.mu.Unlock()
	return nil
}

func (s *ipcConfigStore) stats() (int, nodecore.WorkerPersistedConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.savedCount, s.lastSaved
}

type ipcRuntimeFS struct{}

func (ipcRuntimeFS) FindExistingRuntime(dataDir, _ string) string { return dataDir + "/runtime" }

func (ipcRuntimeFS) PrepareStagingDir(dataDir string) (string, error) {
	return dataDir + "/stage", nil
}

func (ipcRuntimeFS) ActivateStagedRuntime(_, _, _ string) error { return nil }

func (ipcRuntimeFS) RemovePath(string) error { return nil }

func (ipcRuntimeFS) DefaultHostInfo() (string, int) { return "ipc-host", 4242 }

type ipcLoopback struct{}

func (ipcLoopback) AllocateFreePort() (int, error) { return 18999, nil }

func (ipcLoopback) DialStream(int, time.Duration) (io.ReadWriteCloser, error) {
	return nil, derr.New(derr.CodeUpstreamFailure, 502, "unused", nil)
}

func (ipcLoopback) ProbeHTTPHealth(context.Context, int) bool { return true }

func newTestWorkerHandler(t *testing.T) (http.Handler, chan struct{}, *ipcConfigStore) {
	t.Helper()
	store := &ipcConfigStore{}
	eng := worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			GatewayURL: "https://grid.example.com",
			Token:      "wk_unit_ipc_secret",
			WorkerName: "IPCNode",
			Devices:    []string{"Vulkan0"},
			DataDir:    "/virtual/ipc-data",
		},
		Hardware: ipcHardwareStub{
			gpus: []protocol.GpuInfo{{DeviceID: "Vulkan0", Name: "RTX 4090", VramTotalMiB: 24576}},
		},
		Idle:        ipcIdleStub{},
		ConfigStore: store,
		RuntimeFS:   ipcRuntimeFS{},
		Loopback:    ipcLoopback{},
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	focusCh := make(chan struct{}, 1)
	return workerhttp.NewHandler(eng, focusCh, nil), focusCh, store
}

func TestLoopbackSecurityGuardRejectsDNSRebindingBrowserOriginsAndMissingIPCHeader(t *testing.T) {
	handler, focusCh, store := newTestWorkerHandler(t)
	initSaves, initCfg := store.stats()
	if len(focusCh) != 0 || initSaves != 0 || initCfg.WorkerName != "" {
		t.Fatalf("expected clean initial worker handler state, saves=%d", initSaves)
	}

	for _, badHost := range []string{
		"",
		"evil.example:17771",
		"0.0.0.0:17771",
		"192.168.1.50:17771",
		"127.0.0.1:0",
		"127.0.0.1:70000",
		"127.0.0.1:notaport",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17771/api/ping", nil)
		req.Host = badHost
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for Host=%q, got %d", badHost, rec.Code)
		}
	}

	for _, okHost := range []string{"127.0.0.1:17771", "[::1]:17771", "localhost:17771", "127.0.0.1", "LOCALHOST"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17771/api/ping", nil)
		req.Host = okHost
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for loopback Host=%q, got %d (%s)", okHost, rec.Code, rec.Body.String())
		}
	}

	for _, origin := range []string{"http://127.0.0.1:17771", "https://evil.example", "null"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17771/api/status", nil)
		req.Host = "127.0.0.1:17771"
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden when Origin=%q is present on loopback IPC, got %d", origin, rec.Code)
		}
	}

	for _, secSite := range []string{"cross-site", "same-site", "same-origin"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17771/api/status", nil)
		req.Host = "127.0.0.1:17771"
		req.Header.Set("Sec-Fetch-Site", secSite)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for Sec-Fetch-Site=%q, got %d", secSite, rec.Code)
		}
	}

	reqNoneSite := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17771/api/status", nil)
	reqNoneSite.Host = "127.0.0.1:17771"
	reqNoneSite.Header.Set("Sec-Fetch-Site", "none")
	recNoneSite := httptest.NewRecorder()
	handler.ServeHTTP(recNoneSite, reqNoneSite)
	if recNoneSite.Code != http.StatusOK {
		t.Fatalf("expected Sec-Fetch-Site=none allowed, got %d", recNoneSite.Code)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "http://127.0.0.1:17771/api/config", strings.NewReader(`{}`))
		req.Host = "127.0.0.1:17771"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden on %s without %s: 1, got %d", method, workerhttp.LocalIPCAuthHeader, rec.Code)
		}
	}
}

func TestWorkerHTTPPingFocusStatusAndStrictConfigPatchContracts(t *testing.T) {
	handler, focusCh, store := newTestWorkerHandler(t)

	pingRec := httptest.NewRecorder()
	pingReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17771/api/ping", nil)
	pingReq.Host = "127.0.0.1:17771"
	handler.ServeHTTP(pingRec, pingReq)
	var pingBody map[string]any
	if err := json.Unmarshal(pingRec.Body.Bytes(), &pingBody); err != nil {
		t.Fatalf("unmarshal /api/ping: %v", err)
	}
	if pingRec.Code != http.StatusOK || pingBody["status"] != "running" || pingBody["machineId"] != "m-unit-ipc-01" {
		t.Fatalf("unexpected /api/ping payload: code=%d body=%+v", pingRec.Code, pingBody)
	}

	badPingMethod := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17771/api/ping", nil)
	badPingMethod.Host = "127.0.0.1:17771"
	badPingMethod.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	recPing405 := httptest.NewRecorder()
	handler.ServeHTTP(recPing405, badPingMethod)
	if recPing405.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on POST /api/ping, got %d", recPing405.Code)
	}

	focusReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17771/api/focus", nil)
	focusReq.Host = "127.0.0.1:17771"
	focusReq.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	focusRec := httptest.NewRecorder()
	handler.ServeHTTP(focusRec, focusReq)
	if focusRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on POST /api/focus, got %d", focusRec.Code)
	}
	select {
	case <-focusCh:
	default:
		t.Fatal("expected POST /api/focus to signal focusCh")
	}

	cfgGetReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17771/api/config", nil)
	cfgGetReq.Host = "127.0.0.1:17771"
	cfgGetRec := httptest.NewRecorder()
	handler.ServeHTTP(cfgGetRec, cfgGetReq)
	if cfgGetRec.Code != http.StatusOK || strings.Contains(cfgGetRec.Body.String(), "wk_unit_ipc_secret") {
		t.Fatalf("expected GET /api/config 200 without raw token leak, got %d: %s", cfgGetRec.Code, cfgGetRec.Body.String())
	}
	var cfgResp workerhttp.ConfigResponse
	if err := json.Unmarshal(cfgGetRec.Body.Bytes(), &cfgResp); err != nil || !cfgResp.HasWorkerToken || cfgResp.WorkerName != "IPCNode" {
		t.Fatalf("unexpected ConfigResponse: %+v err=%v", cfgResp, err)
	}

	noCT := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1:17771/api/config", strings.NewReader(`{}`))
	noCT.Host = "127.0.0.1:17771"
	noCT.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	rec415 := httptest.NewRecorder()
	handler.ServeHTTP(rec415, noCT)
	if rec415.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 without application/json Content-Type, got %d", rec415.Code)
	}

	for _, badBody := range []string{
		`{"unknownField":123}`,
		`{"workerName":"A"}{"trailing":1}`,
		`{"askPer1M":0.42}`,
		`{"autoPrice":true}`,
		`{"creditFloor":0.1}`,
		`{"idleThresholdSec":5}`,
	} {
		req := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1:17771/api/config", strings.NewReader(badBody))
		req.Host = "127.0.0.1:17771"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid config patch %s, got %d (%s)", badBody, rec.Code, rec.Body.String())
		}
	}

	validPatch := `{"workerName":"UpdatedNode","scheduleMode":"idle","idleThresholdSec":120,"language":"pt-BR","uiMode":"advanced"}`
	patchReq := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1:17771/api/config", strings.NewReader(validPatch))
	patchReq.Host = "127.0.0.1:17771"
	patchReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	patchReq.Header.Set(workerhttp.LocalIPCAuthHeader, "1")
	patchRec := httptest.NewRecorder()
	handler.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid PATCH /api/config, got %d (%s)", patchRec.Code, patchRec.Body.String())
	}

	var updated workerhttp.ConfigResponse
	if err := json.Unmarshal(patchRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal updated ConfigResponse: %v", err)
	}
	saves, lastCfg := store.stats()
	if saves != 1 || updated.WorkerName != "UpdatedNode" || lastCfg.WorkerName != "UpdatedNode" || updated.ScheduleMode != "idle" || updated.IdleThresholdSec != 120 || updated.Language != "pt-BR" || updated.UIMode != "advanced" {
		t.Fatalf("unexpected patched worker config: resp=%+v saves=%d persisted=%+v", updated, saves, lastCfg)
	}
}

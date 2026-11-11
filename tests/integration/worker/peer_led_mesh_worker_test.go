package worker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type controllableCacheInventory struct {
	inner       *cas.WorkerCASAdapter
	freeDiskMiB atomic.Int64
}

func newControllableCacheInventory(freeDiskMiB int) *controllableCacheInventory {
	c := &controllableCacheInventory{
		inner: cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), nil),
	}
	c.freeDiskMiB.Store(int64(freeDiskMiB))
	return c
}

func (c *controllableCacheInventory) EnsureModelCached(
	ctx context.Context,
	dataDir, modelID, fileName, downloadURL, expectedSHA256 string,
	onProgress func(downloaded, total int64),
) (string, string, error) {
	return c.inner.EnsureModelCached(ctx, dataDir, modelID, fileName, downloadURL, expectedSHA256, onProgress)
}

func (c *controllableCacheInventory) AvailableDiskMiB(dataDir string) (int, error) {
	return int(c.freeDiskMiB.Load()), nil
}

func (c *controllableCacheInventory) ListCachedDigests(dataDir string) ([]string, error) {
	return c.inner.ListCachedDigests(dataDir)
}

func TestWorkerEnsureMeshPeerModelEnforcesMandatoryStorageAndReportsReadyOrError(t *testing.T) {
	modelBytes := []byte("GGUF-PEER-LED-MESH-MANDATORY-WEIGHTS-V1")
	sum := sha256.Sum256(modelBytes)
	modelSHA := hex.EncodeToString(sum[:])

	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models/qwen-mesh-peer.gguf" {
			if _, err := w.Write(modelBytes); err != nil {
				t.Errorf("write mesh peer model bytes: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(modelSrv.Close)

	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	cacheInv := newControllableCacheInventory(512)
	dialer := newDuplexTunnelDialer()

	deps := baseTestDeps(dir, filepath.Join(dir, "config.json"))
	deps.Boot = worker.BootConfig{
		GatewayURL:     modelSrv.URL,
		Token:          "wk_mesh_peer_1",
		WorkerName:     "MeshPeerNode",
		Devices:        []string{"Vulkan0"},
		DataDir:        dir,
		ReconnectDelay: 20 * time.Millisecond,
	}
	deps.ModelCache = cacheInv
	deps.Idle = &fakeIdleDetector{}
	deps.RPC = &fakeRPCSupervisor{}
	deps.Tunnel = dialer

	eng := worker.NewEngine(deps)
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { eng.Stop("test_cleanup") })

	var sock *duplexTunnelSocket
	select {
	case sock = <-dialer.dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel dial")
	}

	waitUntil(t, func() bool {
		msgs := sock.sentMessages()
		return len(msgs) >= 1 && msgs[0].Type == "hello" && msgs[0].DiskFreeProbed && msgs[0].DiskFreeMiB == 512
	})

	sock.pushControl(t, protocol.ControlMsg{
		Type:             protocol.ControlEnsureMeshModel,
		PoolID:           "pool-mesh-lowdisk",
		ModelID:          "qwen-mesh-peer",
		ModelSHA256:      modelSHA,
		ModelDownloadURL: modelSrv.URL + "/models/qwen-mesh-peer.gguf",
		ModelFileName:    "qwen-mesh-peer.gguf",
		BundleComponents: []protocol.BundleComponentWire{
			{
				Role:        "primary",
				FileName:    "qwen-mesh-peer.gguf",
				SHA256:      modelSHA,
				DownloadURL: modelSrv.URL + "/models/qwen-mesh-peer.gguf",
				SizeBytes:   int64(2048) << 20,
			},
		},
	})

	waitUntil(t, func() bool {
		for _, m := range sock.sentMessages() {
			if m.Type == protocol.ControlMeshPeerError &&
				m.PoolID == "pool-mesh-lowdisk" &&
				strings.Contains(m.Reason, "insufficient free disk space") {
				return true
			}
		}
		return false
	})

	cacheInv.freeDiskMiB.Store(16384)
	sock.pushControl(t, protocol.ControlMsg{
		Type:             protocol.ControlEnsureMeshModel,
		PoolID:           "pool-mesh-ok",
		ModelID:          "qwen-mesh-peer",
		ModelSHA256:      modelSHA,
		ModelDownloadURL: modelSrv.URL + "/models/qwen-mesh-peer.gguf",
		ModelFileName:    "qwen-mesh-peer.gguf",
	})

	waitUntil(t, func() bool {
		for _, m := range sock.sentMessages() {
			if m.Type == protocol.ControlMeshPeerReady &&
				m.PoolID == "pool-mesh-ok" &&
				m.ModelSHA256 == modelSHA &&
				len(m.CachedModelSHA256s) == 1 &&
				m.CachedModelSHA256s[0] == modelSHA {
				return true
			}
		}
		return false
	})
}

func TestWorkerLeaderMeshRelayListenersMultiplexBidirectionalRPCFramesAndCleanUp(t *testing.T) {
	modelBytes := []byte("GGUF-LEADER-MESH-WEIGHTS-V1")
	sum := sha256.Sum256(modelBytes)
	modelSHA := hex.EncodeToString(sum[:])

	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models/qwen-leader.gguf" {
			if _, err := w.Write(modelBytes); err != nil {
				t.Errorf("write leader model bytes: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(modelSrv.Close)

	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	dialer := newDuplexTunnelDialer()
	soloSup := &mockSoloSupervisor{t: t, backends: []string{"llama_local"}}

	deps := baseTestDeps(dir, filepath.Join(dir, "config.json"))
	deps.Boot = worker.BootConfig{
		GatewayURL:     modelSrv.URL,
		Token:          "wk_mesh_leader",
		WorkerName:     "MeshLeaderNode",
		Devices:        []string{"Vulkan0"},
		DataDir:        dir,
		ReconnectDelay: 20 * time.Millisecond,
	}
	deps.Idle = &fakeIdleDetector{}
	deps.RPC = &fakeRPCSupervisor{}
	deps.Solo = soloSup
	deps.Tunnel = dialer

	eng := worker.NewEngine(deps)
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { eng.Stop("test_cleanup") })

	var sock *duplexTunnelSocket
	select {
	case sock = <-dialer.dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel dial")
	}

	waitUntil(t, func() bool {
		msgs := sock.sentMessages()
		return len(msgs) >= 1 && msgs[0].Type == "hello"
	})

	sock.pushControl(t, protocol.ControlMsg{
		Type:             "configure_solo_engine",
		ExecutionMode:    protocol.ExecutionModeMesh,
		PoolID:           "pool-leader-mesh-1",
		ModelID:          "qwen-leader",
		ModelSHA256:      modelSHA,
		ModelDownloadURL: "/models/qwen-leader.gguf",
		ModelFileName:    "qwen-leader.gguf",
		TargetBackend:    "llama_local",
		ContextLength:    4096,
		ParallelSlots:    2,
		VramTotalMiB:     12288,
		MeshPeers: []protocol.MeshPeerWire{
			{WorkerID: "w-peer-remote-1", GPUCount: 2},
		},
		TensorWeights: []int{4096, 8192},
	})

	waitUntil(t, func() bool {
		for _, m := range sock.sentMessages() {
			if m.Type == "solo_engine_ready" &&
				m.PoolID == "pool-leader-mesh-1" &&
				m.ExecutionMode == protocol.ExecutionModeMesh {
				return true
			}
		}
		return false
	})

	if eng.Snapshot().ExecutionMode != protocol.ExecutionModeMesh {
		t.Fatalf("expected Snapshot ExecutionMode=MESH, got %q", eng.Snapshot().ExecutionMode)
	}
	spec, soloProc := soloSup.snapshotLast()
	if len(spec.RPCEndpoints) != 2 || spec.TotalGPUs != 3 || len(spec.TensorWeights) != 2 {
		t.Fatalf("unexpected Leader MESH SoloEngineSpec: %+v", spec)
	}

	relayAddr := spec.RPCEndpoints[1]
	conn, err := net.DialTimeout("tcp", relayAddr, time.Second)
	if err != nil {
		t.Fatalf("dial peer relay listener %s: %v", relayAddr, err)
	}
	var closeRelayOnce sync.Once
	t.Cleanup(func() {
		closeRelayOnce.Do(func() {
			if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				t.Logf("close relay conn: %v", closeErr)
			}
		})
	})

	var relaySID uint32
	waitUntil(t, func() bool {
		for _, m := range sock.sentMessages() {
			if m.Type == protocol.ControlMeshRelayOpen &&
				m.PoolID == "pool-leader-mesh-1" &&
				m.PeerWorkerID == "w-peer-remote-1" {
				relaySID = m.StreamID
				return true
			}
		}
		return false
	})
	if relaySID&protocol.WorkerInitiatedStreamMask == 0 {
		t.Fatalf("expected worker-initiated StreamID bitmask on %#x", relaySID)
	}

	if _, err := conn.Write([]byte("rpc-tensor-ping")); err != nil {
		t.Fatalf("write to peer relay conn: %v", err)
	}
	waitUntil(t, func() bool {
		return sock.receivedStreamData(relaySID) == "rpc-tensor-ping"
	})

	sock.pushStream(relaySID, []byte("rpc-tensor-pong"))
	reply := make([]byte, 32)
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, err := conn.Read(reply)
	if err != nil || string(reply[:n]) != "rpc-tensor-pong" {
		t.Fatalf("expected rpc-tensor-pong from peer relay, got %q err=%v", string(reply[:n]), err)
	}

	sock.pushControl(t, protocol.ControlMsg{
		Type:   "stop_solo_engine",
		PoolID: "pool-leader-mesh-1",
	})
	waitUntil(t, func() bool {
		return soloProc != nil && soloProc.stopped.Load() == 1 && eng.Snapshot().ExecutionMode == ""
	})

	if postConn, dErr := net.DialTimeout("tcp", relayAddr, 150*time.Millisecond); dErr == nil {
		if closeErr := postConn.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Logf("close unexpected postConn: %v", closeErr)
		}
		t.Fatalf("expected peer relay listener %s to close on stop_solo_engine", relayAddr)
	}
}

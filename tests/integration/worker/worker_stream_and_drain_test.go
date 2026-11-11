package worker_test

import (
	"bytes"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func TestPerStreamRouterEliminatesHeadOfLineBlocking(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	dialer := newDuplexTunnelDialer()

	blockCh := make(chan struct{})
	var closeBlockOnce sync.Once
	unblockConn1 := func() {
		closeBlockOnce.Do(func() {
			close(blockCh)
		})
	}
	conn1Accepted := make(chan struct{})

	rpcSup := &portBoundRPCSupervisor{
		t: t,
		handler: func(connIndex int, c net.Conn) {
			defer func() {
				if closeErr := c.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
					t.Logf("close rpc conn: %v", closeErr)
				}
			}()
			if connIndex == 1 {
				close(conn1Accepted)
				<-blockCh
				return
			}
			buf := make([]byte, 64)
			n, err := c.Read(buf)
			if err == nil && n > 0 {
				if _, wErr := c.Write([]byte("pong:" + string(buf[:n]))); wErr != nil {
					t.Errorf("write rpc pong: %v", wErr)
				}
			}
		},
	}

	holDeps := baseTestDeps(dir, filepath.Join(dir, "config.json"))
	holDeps.Boot = worker.BootConfig{
		GatewayURL:     "http://127.0.0.1:8000",
		Token:          "wk_hol_test",
		WorkerName:     "HoLNode",
		Devices:        []string{"Vulkan0"},
		DataDir:        dir,
		ReconnectDelay: 20 * time.Millisecond,
	}
	holDeps.Idle = &fakeIdleDetector{}
	holDeps.RPC = rpcSup
	holDeps.Tunnel = dialer
	eng := worker.NewEngine(holDeps)
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() {
		unblockConn1()
		eng.Stop("cleanup")
	})

	sock := <-dialer.dialed
	waitUntil(t, func() bool {
		return len(sock.sentMessages()) >= 1
	})

	sock.pushControl(t, protocol.ControlMsg{Type: "tunnel_open", StreamID: 1, TunnelTarget: protocol.TunnelTargetRPC})
	<-conn1Accepted
	sock.pushControl(t, protocol.ControlMsg{Type: "tunnel_open", StreamID: 2, TunnelTarget: protocol.TunnelTargetRPC})

	stalledPayload := bytes.Repeat([]byte("X"), 64*1024)
	for i := 0; i < 40; i++ {
		sock.pushStream(1, stalledPayload)
	}
	sock.pushStream(2, []byte("fast"))
	sock.pushControl(t, protocol.ControlMsg{
		Type:   "state",
		PoolID: "pool-fast",
		Worker: &protocol.WorkerView{Status: "READY", PoolID: "pool-fast"},
	})

	waitUntil(t, func() bool {
		return strings.Contains(sock.receivedStreamData(2), "pong:fast") && eng.Snapshot().PoolID == "pool-fast"
	})
}

func TestMonotonicStateSeqPreventsStaleStateBalanceRegression(t *testing.T) {
	eng, _, _, _, _, sock := startConnectedTestEngine(t)

	sock.pushControl(t, protocol.ControlMsg{
		Type:     "job_credit",
		StateSeq: 10,
		Blocks:   4,
		Tokens:   100,
		Earned:   0.50,
		Balance:  2.50,
	})
	sock.pushControl(t, protocol.ControlMsg{
		Type:     "state",
		StateSeq: 9,
		Worker: &protocol.WorkerView{
			Status:  "READY",
			Balance: 1.00,
		},
	})
	waitUntil(t, func() bool {
		snap := eng.Snapshot()
		return snap.Status == "READY" && snap.Balance == 2.50
	})

	sock.pushControl(t, protocol.ControlMsg{
		Type:     "state",
		StateSeq: 11,
		Worker: &protocol.WorkerView{
			Status:  "READY",
			Balance: 0.75,
		},
	})
	waitUntil(t, func() bool {
		return eng.Snapshot().Balance == 0.75
	})
}

func TestGracefulPauseDrainingAndUnavailableIdleBackendStatus(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntimeDir(t, dir)
	dialer := newDuplexTunnelDialer()
	idleDet := &fakeIdleDetector{}
	idleDet.sec.Store(300)

	holdConn := make(chan struct{})
	var closeHoldOnce sync.Once
	releaseHoldConn := func() {
		closeHoldOnce.Do(func() {
			close(holdConn)
		})
	}
	connOpened := make(chan struct{}, 1)
	rpcSup := &portBoundRPCSupervisor{
		t: t,
		handler: func(connIndex int, c net.Conn) {
			defer func() {
				if closeErr := c.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
					t.Logf("close rpc conn: %v", closeErr)
				}
			}()
			select {
			case connOpened <- struct{}{}:
			default:
			}
			<-holdConn
		},
	}

	drainDeps := baseTestDeps(dir, filepath.Join(dir, "config.json"))
	drainDeps.Boot = worker.BootConfig{
		GatewayURL:        "http://127.0.0.1:8000",
		Token:             "wk_drain_test",
		WorkerName:        "DrainNode",
		Devices:           []string{"Vulkan0"},
		DataDir:           dir,
		ReconnectDelay:    20 * time.Millisecond,
		IdlePollInterval:  20 * time.Millisecond,
		PauseDrainTimeout: 3 * time.Second,
	}
	drainDeps.Idle = idleDet
	drainDeps.RPC = rpcSup
	drainDeps.Tunnel = dialer
	eng := worker.NewEngine(drainDeps)
	if err := eng.Start(worker.StartParams{}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() {
		releaseHoldConn()
		eng.Stop("cleanup")
	})

	sock := <-dialer.dialed
	waitUntil(t, func() bool { return len(sock.sentMessages()) >= 1 })

	sock.pushControl(t, protocol.ControlMsg{Type: "tunnel_open", StreamID: 42, TunnelTarget: protocol.TunnelTargetRPC})
	<-connOpened

	proc := rpcSup.latestProc()
	pauseTrue := true
	eng.UpdateSchedule("always", 120, &pauseTrue)

	if eng.Snapshot().Status != "PAUSE_DRAINING" {
		t.Fatalf("expected status PAUSE_DRAINING while stream 42 is active, got %q", eng.Snapshot().Status)
	}
	if proc.stopped.Load() != 0 {
		t.Fatal("expected local RPC process kept alive during PAUSE_DRAINING while stream 42 drains")
	}
	waitUntil(t, func() bool {
		for _, m := range sock.sentMessages() {
			if m.Type == "pause_state" && m.Paused {
				return true
			}
		}
		return false
	})

	releaseHoldConn()
	waitUntil(t, func() bool {
		return proc.stopped.Load() == 1 && eng.Snapshot().Status == "PAUSED"
	})

	idleDet.sec.Store(-1)
	pauseFalse := false
	eng.UpdateSchedule("idle", 60, &pauseFalse)
	snap := eng.Snapshot()
	if snap.IdleStatus != worker.IdleStatusBackendUnavailable || snap.IdleSeconds != -1 {
		t.Fatalf("expected IdleStatus=%q and IdleSeconds=-1, got status=%q sec=%d", worker.IdleStatusBackendUnavailable, snap.IdleStatus, snap.IdleSeconds)
	}
}

func TestExponentialReconnectBackoffWithBoundedCeiling(t *testing.T) {
	b0 := worker.ComputeReconnectBackoff(20*time.Millisecond, 0)
	if b0 < 20*time.Millisecond || b0 > 25*time.Millisecond {
		t.Fatalf("expected attempt 0 in [20ms, 25ms], got %v", b0)
	}

	b2 := worker.ComputeReconnectBackoff(20*time.Millisecond, 2)
	if b2 < 80*time.Millisecond || b2 > 100*time.Millisecond {
		t.Fatalf("expected attempt 2 in [80ms, 100ms], got %v", b2)
	}

	capped := worker.ComputeReconnectBackoff(time.Second, 8)
	if capped < worker.MaxReconnectDelay || capped > worker.MaxReconnectDelay+worker.MaxReconnectDelay/4 {
		t.Fatalf("expected attempt 8 capped around 30s ceiling (+0..25%% jitter), got %v", capped)
	}
}

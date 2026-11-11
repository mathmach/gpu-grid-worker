package worker

import (
	"context"
	"fmt"
	"io"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func (e *Engine) openMeshPeerRelayListeners(
	poolID string,
	peers []protocol.MeshPeerWire,
) ([]string, []func(), int, error) {
	if len(peers) == 0 {
		return nil, nil, 0, nil
	}
	relayTransport := e.loopbackRelay
	if relayTransport == nil {
		return nil, nil, 0, derr.New(derr.CodeUpstreamFailure, 503, "loopback relay listener transport is not configured", nil)
	}
	e.mu.Lock()
	ts := e.activeStreams
	e.mu.Unlock()
	if ts == nil {
		return nil, nil, 0, derr.New(derr.CodeUpstreamFailure, 503, "worker tunnel session is not active for MESH relay", nil)
	}

	endpoints := make([]string, 0, len(peers))
	stops := make([]func(), 0, len(peers))
	peerGPUs := 0
	sem := make(chan struct{}, maxConcurrentTunnelStreams)

	for _, peer := range peers {
		peerID := peer.WorkerID
		port, stopFn, err := relayTransport.ListenLoopback(func(conn io.ReadWriteCloser) {
			select {
			case sem <- struct{}{}:
			default:
				closeStreamConnQuietly(conn)
				return
			}
			releaseSem := func() { <-sem }
			go func() {
				defer releaseSem()
				ts.attachAcceptedRelayStream(conn, poolID, peerID)
			}()
		})
		if err != nil {
			invokeRelayStops(stops)
			return nil, nil, 0, err
		}
		endpoints = append(endpoints, fmt.Sprintf("127.0.0.1:%d", port))
		stops = append(stops, stopFn)
		peerGPUs += max(1, peer.GPUCount)
	}
	return endpoints, stops, peerGPUs, nil
}

func invokeRelayStops(stops []func()) {
	for _, fn := range stops {
		if fn != nil {
			fn()
		}
	}
}

func (ts *tunnelStreams) attachAcceptedRelayStream(conn io.ReadWriteCloser, poolID, peerWorkerID string) {
	if ts == nil || conn == nil {
		closeStreamConnQuietly(conn)
		return
	}
	if rejectReason := ts.checkRelayStreamAllowed(); rejectReason != "" {
		closeStreamConnQuietly(conn)
		return
	}

	seq := ts.nextWorkerSeq.Add(1) &^ protocol.WorkerInitiatedStreamMask
	if seq == 0 {
		seq = ts.nextWorkerSeq.Add(1) &^ protocol.WorkerInitiatedStreamMask
	}
	sid := protocol.WorkerInitiatedStreamMask | seq
	openMsg := protocol.ControlMsg{
		Type:         protocol.ControlMeshRelayOpen,
		PoolID:       poolID,
		StreamID:     sid,
		PeerWorkerID: peerWorkerID,
	}
	protocol.InjectTraceParent(context.Background(), &openMsg)
	st := newActiveStream(sid, protocol.TunnelTargetMeshRelay, openMsg.TraceParent)
	if !st.attachConn(conn) {
		closeStreamConnQuietly(conn)
		return
	}

	displaced, admitted := ts.admitStream(sid, st)
	if !admitted {
		st.shutdown()
		return
	}
	if displaced != nil {
		displaced.shutdown()
	}

	if ts.sendJSON == nil {
		ts.closeStream(sid, "missing_gateway_sender")
		return
	}
	if err := ts.sendJSON(openMsg); err != nil {
		ts.closeStream(sid, "mesh_relay_open_failed")
		return
	}

	go ts.pumpStreamWrites(st, conn)
	ts.pumpLocalToGateway(st, conn)
}

func (ts *tunnelStreams) checkRelayStreamAllowed() string {
	if ts.eng == nil {
		return ""
	}
	ts.eng.mu.Lock()
	paused, _ := ts.eng.effectivePauseLocked()
	draining := ts.eng.pauseDraining
	running := ts.eng.running
	ts.eng.mu.Unlock()
	if !running || paused || draining {
		return "worker unavailable"
	}
	return ""
}

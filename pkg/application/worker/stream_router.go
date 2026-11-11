package worker

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/trace"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const (
	streamSendQueueCap         = 256
	maxConcurrentTunnelStreams = 256
	streamDialTimeout          = 3 * time.Second
	streamChunkSendTimeout     = 30 * time.Second
)

type activeStream struct {
	id           uint32
	target       string
	traceParent  string
	inMu         sync.Mutex
	writeClosed  bool
	writeCh      chan []byte
	stopCh       chan struct{}
	lastActiveNs atomic.Int64
	connMu       sync.Mutex
	conn         io.ReadWriteCloser
	closed       bool
	closeOnce    sync.Once
}

func newActiveStream(id uint32, target, traceParent string) *activeStream {
	st := &activeStream{
		id:          id,
		target:      target,
		traceParent: traceParent,
		writeCh:     make(chan []byte, streamSendQueueCap),
		stopCh:      make(chan struct{}),
	}
	st.touch()
	return st
}

func (s *activeStream) touch() {
	s.lastActiveNs.Store(time.Now().UnixNano())
}

func (s *activeStream) isBusyAt(nowNs, cutoffNs int64) bool {
	if cutoffNs <= 0 {
		return true
	}
	return (nowNs - s.lastActiveNs.Load()) <= cutoffNs
}

func (s *activeStream) enqueueChunk(chunk []byte) (bool, bool) {
	s.inMu.Lock()
	defer s.inMu.Unlock()
	if s.writeClosed {
		return false, false
	}
	select {
	case s.writeCh <- chunk:
		return true, false
	default:
		return false, true
	}
}

func (s *activeStream) closeWriteQueue() {
	s.inMu.Lock()
	if !s.writeClosed {
		s.writeClosed = true
		close(s.writeCh)
	}
	s.inMu.Unlock()
}

func (s *activeStream) drainWriteQueue() {
	for range s.writeCh {
	}
}

func (s *activeStream) attachConn(c io.ReadWriteCloser) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.closed {
		return false
	}
	s.conn = c
	return true
}

func (s *activeStream) shutdown() {
	s.closeOnce.Do(func() {
		s.closeWriteQueue()
		close(s.stopCh)
		s.connMu.Lock()
		s.closed = true
		c := s.conn
		s.conn = nil
		s.connMu.Unlock()
		closeStreamConnQuietly(c)
		s.drainWriteQueue()
	})
}

func closeStreamConnQuietly(c io.Closer) {
	if c != nil {
		if err := c.Close(); err != nil {
			return
		}
	}
}

type tunnelStreams struct {
	eng           *Engine
	localPort     int
	sock          TunnelSocket
	sendJSON      func(protocol.ControlMsg) error
	nextWorkerSeq atomic.Uint32
	mu            sync.Mutex
	streams       map[uint32]*activeStream
	hasActive     bool
	drainedCh     chan struct{}
}

func newTunnelStreams(eng *Engine, localPort int, sock TunnelSocket, sendJSON func(protocol.ControlMsg) error) *tunnelStreams {
	closedCh := make(chan struct{})
	close(closedCh)
	return &tunnelStreams{
		eng:       eng,
		localPort: localPort,
		sock:      sock,
		sendJSON:  sendJSON,
		streams:   make(map[uint32]*activeStream),
		drainedCh: closedCh,
	}
}

func parseInboundTunnelTarget(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	switch {
	case trimmed == "" || strings.EqualFold(trimmed, protocol.TunnelTargetRPC):
		return protocol.TunnelTargetRPC, true
	case strings.EqualFold(trimmed, protocol.TunnelTargetSoloHTTP):
		return protocol.TunnelTargetSoloHTTP, true
	default:
		return "", false
	}
}

func normalizeTunnelTarget(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.EqualFold(trimmed, protocol.TunnelTargetSoloHTTP) {
		return protocol.TunnelTargetSoloHTTP
	}
	if strings.EqualFold(trimmed, protocol.TunnelTargetMeshRelay) {
		return protocol.TunnelTargetMeshRelay
	}
	return protocol.TunnelTargetRPC
}

func (ts *tunnelStreams) activeBusyCount(idleCutoff time.Duration) int {
	if ts == nil {
		return 0
	}
	nowNs := time.Now().UnixNano()
	cutoffNs := int64(idleCutoff)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	busy := 0
	for _, st := range ts.streams {
		if st.isBusyAt(nowNs, cutoffNs) {
			busy++
		}
	}
	return busy
}

func (ts *tunnelStreams) WaitDrained(ctx context.Context, idleCutoff time.Duration) bool {
	if ts == nil || ts.activeBusyCount(idleCutoff) == 0 {
		return true
	}
	ts.mu.Lock()
	ch := ts.drainedCh
	ts.mu.Unlock()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ch:
			return true
		case <-ticker.C:
			if ts.activeBusyCount(idleCutoff) == 0 {
				return true
			}
		case <-ctx.Done():
			return false
		}
	}
}

func (ts *tunnelStreams) setLocalPort(port int) {
	if ts == nil || port <= 0 {
		return
	}
	ts.mu.Lock()
	ts.localPort = port
	ts.mu.Unlock()
}

func (ts *tunnelStreams) getLocalPort() int {
	if ts == nil {
		return 0
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.localPort
}

func (ts *tunnelStreams) openStream(ctx context.Context, sid uint32, rawTarget string) {
	tp, _ := trace.LookupTraceParent(ctx)
	if sid == 0 || (sid&protocol.WorkerInitiatedStreamMask) != 0 {
		ts.sendTunnelClose(sid, strings.TrimSpace(rawTarget), "invalid_stream_id", tp)
		return
	}
	target, ok := parseInboundTunnelTarget(rawTarget)
	if !ok {
		ts.sendTunnelClose(sid, strings.TrimSpace(rawTarget), "unsupported_tunnel_target", tp)
		return
	}
	if target == protocol.TunnelTargetRPC && ts.eng != nil {
		if err := ts.eng.ensureConnectedIdleRPC(ctx); err != nil {
			ts.sendTunnelClose(sid, target, "rpc engine not ready", tp)
			return
		}
	}
	port, rejectReason := ts.resolveTargetPort(target)
	if rejectReason != "" {
		ts.sendTunnelClose(sid, target, rejectReason, tp)
		return
	}

	st := newActiveStream(sid, target, tp)
	displaced, admitted := ts.admitStream(sid, st)
	if !admitted {
		st.shutdown()
		ts.sendTunnelClose(sid, target, "too_many_streams", tp)
		return
	}
	if displaced != nil {
		displaced.shutdown()
	}
	go ts.runStreamLifecycle(st, port)
}

func (ts *tunnelStreams) admitStream(sid uint32, st *activeStream) (*activeStream, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	displaced, exists := ts.streams[sid]
	if !exists && len(ts.streams) >= maxConcurrentTunnelStreams {
		return nil, false
	}
	ts.streams[sid] = st
	if !ts.hasActive {
		ts.hasActive = true
		ts.drainedCh = make(chan struct{})
	}
	return displaced, true
}

func (ts *tunnelStreams) resolveTargetPort(target string) (int, string) {
	rpcPort := ts.getLocalPort()
	if ts.eng == nil {
		if target == protocol.TunnelTargetSoloHTTP {
			return 0, "solo engine unavailable"
		}
		if rpcPort <= 0 {
			return 0, "rpc port unavailable"
		}
		return rpcPort, ""
	}

	ts.eng.mu.Lock()
	paused, _ := ts.eng.effectivePauseLocked()
	draining := ts.eng.pauseDraining
	soloPort := ts.eng.soloPort
	if ts.eng.localPort > 0 {
		rpcPort = ts.eng.localPort
	}
	ts.eng.mu.Unlock()

	if paused || draining {
		return 0, "worker paused"
	}
	if target == protocol.TunnelTargetSoloHTTP {
		if soloPort <= 0 {
			return 0, "solo engine not ready"
		}
		return soloPort, ""
	}
	if rpcPort <= 0 {
		return 0, "rpc engine not ready"
	}
	return rpcPort, ""
}

func (ts *tunnelStreams) runStreamLifecycle(st *activeStream, port int) {
	if ts.eng == nil || ts.eng.loopback == nil {
		ts.abortStreamAndNotify(st, "loopback transport unavailable")
		return
	}
	conn, err := ts.eng.loopback.DialStream(port, streamDialTimeout)
	if err != nil {
		ts.abortStreamAndNotify(st, err.Error())
		return
	}
	if !st.attachConn(conn) {
		closeStreamConnQuietly(conn)
		return
	}
	go ts.pumpStreamWrites(st, conn)
	ts.pumpLocalToGateway(st, conn)
}

func (ts *tunnelStreams) forwardInbound(raw []byte) {
	sid, payload, ok := protocol.DecodeStreamFrame(raw)
	if !ok {
		return
	}
	ts.mu.Lock()
	st := ts.streams[sid]
	ts.mu.Unlock()
	if st == nil {
		return
	}

	st.touch()
	if _, saturated := st.enqueueChunk(payload); saturated {
		go ts.abortStreamAndNotify(st, "stream write queue saturated")
	}
}

func (ts *tunnelStreams) pumpStreamWrites(st *activeStream, conn io.Writer) {
	for {
		select {
		case <-st.stopCh:
			st.drainWriteQueue()
			return
		case chunk, ok := <-st.writeCh:
			if !ok {
				return
			}
			st.touch()
			if _, err := conn.Write(chunk); err != nil {
				ts.abortStreamAndNotify(st, err.Error())
				return
			}
		}
	}
}

func (ts *tunnelStreams) pumpLocalToGateway(st *activeStream, conn io.Reader) {
	buf := make([]byte, 64*1024)
	for {
		n, rErr := conn.Read(buf)
		if n > 0 {
			st.touch()
			if sErr := ts.sock.SendStreamChunk(st.id, buf[:n], streamChunkSendTimeout); sErr != nil {
				break
			}
		}
		if rErr != nil {
			break
		}
	}
	ts.abortStreamAndNotify(st, "")
}

func (ts *tunnelStreams) abortStreamAndNotify(st *activeStream, reason string) {
	removed := ts.detachIfMatching(st)
	st.shutdown()
	if removed {
		ts.sendTunnelClose(st.id, st.target, reason, st.traceParent)
	}
}

func (ts *tunnelStreams) detachIfMatching(st *activeStream) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	cur, ok := ts.streams[st.id]
	if !ok || cur != st {
		return false
	}
	delete(ts.streams, st.id)
	ts.markDrainedIfEmptyLocked()
	return true
}

func (ts *tunnelStreams) closeStream(sid uint32, reason string) {
	ts.mu.Lock()
	st, ok := ts.streams[sid]
	if ok {
		delete(ts.streams, sid)
		ts.markDrainedIfEmptyLocked()
	}
	ts.mu.Unlock()

	if !ok {
		return
	}
	if reason != "" && ts.eng != nil {
		ts.eng.logger.Debug("worker_tunnel_stream_closed_by_peer", "stream_id", sid, "reason", reason)
	}
	st.shutdown()
}

func (ts *tunnelStreams) closeTargetStreams(target string) {
	norm := normalizeTunnelTarget(target)
	ts.mu.Lock()
	var matched []*activeStream
	for sid, st := range ts.streams {
		if st.target == norm {
			matched = append(matched, st)
			delete(ts.streams, sid)
		}
	}
	ts.markDrainedIfEmptyLocked()
	ts.mu.Unlock()

	for _, st := range matched {
		st.shutdown()
	}
}

func (ts *tunnelStreams) closeAll() {
	ts.mu.Lock()
	all := make([]*activeStream, 0, len(ts.streams))
	for _, st := range ts.streams {
		all = append(all, st)
	}
	ts.streams = make(map[uint32]*activeStream)
	ts.markDrainedIfEmptyLocked()
	ts.mu.Unlock()

	for _, st := range all {
		st.shutdown()
	}
}

func (ts *tunnelStreams) markDrainedIfEmptyLocked() {
	if len(ts.streams) == 0 && ts.hasActive {
		ts.hasActive = false
		close(ts.drainedCh)
	}
}

func (ts *tunnelStreams) sendTunnelClose(sid uint32, target, reason, traceParent string) {
	if ts.sendJSON == nil {
		return
	}
	msg := protocol.ControlMsg{
		Type:         protocol.ControlTunnelClose,
		StreamID:     sid,
		TunnelTarget: target,
		Reason:       reason,
		TraceParent:  traceParent,
	}
	if err := ts.sendJSON(msg); err != nil && ts.eng != nil {
		ts.eng.logger.Debug("tunnel_close_send_failed", "stream_id", sid, "error", err.Error())
	}
}

package tunnel_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/trace"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/tunnel"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func TestBinaryStreamFrameCodecAndNilSafety(t *testing.T) {
	payload := []byte{0xde, 0xad, 0xbe, 0xef, 0x42}
	encoded := protocol.EncodeStreamFrame(987654, payload)
	sid, body, ok := protocol.DecodeStreamFrame(encoded)
	if !ok || sid != 987654 || !bytes.Equal(body, payload) {
		t.Fatalf("frame roundtrip mismatch: sid=%d ok=%v body=%x", sid, ok, body)
	}
	if _, _, ok := protocol.DecodeStreamFrame([]byte{1, 2, 3}); ok {
		t.Fatal("expected short frame < 4 bytes to be rejected")
	}

	nilConn := tunnel.NewSafeWSConn(nil)
	if err := nilConn.SendJSON(map[string]string{"type": "state"}, 10*time.Millisecond); err == nil {
		t.Fatal("expected nil SafeWSConn to return ErrSocketClosed instead of panicking")
	}
}

func TestTraceParentInjectAndExtractAcrossControlFrames(t *testing.T) {
	wantTP := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := trace.WithTraceParent(context.Background(), wantTP)

	msg := protocol.ControlMsg{Type: "tunnel_open", StreamID: 42}
	protocol.InjectTraceParent(ctx, &msg)
	if msg.TraceParent != wantTP {
		t.Fatalf("expected injected traceparent %q, got %q", wantTP, msg.TraceParent)
	}

	extractedCtx := protocol.ExtractTraceParent(context.Background(), msg)
	if got := trace.TraceParentFromContext(extractedCtx); got != wantTP {
		t.Fatalf("expected domain traceparent %q, got %q", wantTP, got)
	}

	childCtx := trace.WithTraceParent(context.Background(), wantTP)
	var fromDomain protocol.ControlMsg
	protocol.InjectTraceParent(childCtx, &fromDomain)
	if fromDomain.TraceParent != wantTP {
		t.Fatalf("expected domain-context traceparent %q, got %q", wantTP, fromDomain.TraceParent)
	}
}

type stalledWriteCloser struct {
	unblock chan struct{}
	closed  atomic.Bool
}

func newStalledWriteCloser() *stalledWriteCloser {
	return &stalledWriteCloser{unblock: make(chan struct{})}
}

func (s *stalledWriteCloser) Write(p []byte) (int, error) {
	<-s.unblock
	if s.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func (s *stalledWriteCloser) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		close(s.unblock)
	}
	return nil
}

type collectorWriteCloser struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	doneCh chan struct{}
	once   sync.Once
}

func newCollectorWriteCloser() *collectorWriteCloser {
	return &collectorWriteCloser{doneCh: make(chan struct{})}
}

func (c *collectorWriteCloser) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *collectorWriteCloser) Close() error {
	c.once.Do(func() {
		close(c.doneCh)
	})
	return nil
}

func (c *collectorWriteCloser) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func TestStreamDemuxerEliminatesHeadOfLineBlockingAndEvictsOverflow(t *testing.T) {
	var evictedID atomic.Uint32
	evictedCh := make(chan error, 1)

	demux := tunnel.NewStreamDemuxer(tunnel.DemuxerOptions{
		BufferCapacity: 8,
		EnqueueTimeout: 2 * time.Millisecond,
		WriteTimeout:   50 * time.Millisecond,
		OnStreamEvict: func(streamID uint32, reason error) {
			evictedID.Store(streamID)
			select {
			case evictedCh <- reason:
			default:
			}
		},
	})
	defer demux.CloseAll()

	stalled := newStalledWriteCloser()
	fast := newCollectorWriteCloser()
	demux.Register(1, stalled)
	demux.Register(2, fast)

	assertFastStreamBypassesStalledPeer(t, demux, fast)
	assertStalledStreamEvictedOnOverflow(t, demux, &evictedID, evictedCh)
}

func assertFastStreamBypassesStalledPeer(t *testing.T, demux *tunnel.StreamDemuxer, fast *collectorWriteCloser) {
	t.Helper()
	dispatchRepeatedFrames(t, demux, 1, 6, []byte("stalled-rpc-tensor"))
	dispatchRepeatedFrames(t, demux, 2, 20, []byte("fast|"))
	demux.CloseStream(2)

	select {
	case <-fast.doneCh:
	case <-time.After(20 * time.Millisecond):
		t.Fatal("stream 2 blocked by stalled stream 1 (Head-of-Line blocking regression)")
	}
	wantFast := string(bytes.Repeat([]byte("fast|"), 20))
	if fast.String() != wantFast {
		t.Fatalf("expected FIFO payload %q, got %q", wantFast, fast.String())
	}
}

func dispatchRepeatedFrames(t *testing.T, demux *tunnel.StreamDemuxer, streamID uint32, count int, body []byte) {
	t.Helper()
	for range count {
		if !demux.DispatchFrame(protocol.EncodeStreamFrame(streamID, body)) {
			t.Fatalf("expected stream %d frame to enqueue without blocking", streamID)
		}
	}
}

func assertStalledStreamEvictedOnOverflow(t *testing.T, demux *tunnel.StreamDemuxer, evictedID *atomic.Uint32, evictedCh <-chan error) {
	t.Helper()
	for range 10 {
		demux.DispatchFrame(protocol.EncodeStreamFrame(1, []byte("overflow")))
	}
	select {
	case reason := <-evictedCh:
		if !errors.Is(reason, tunnel.ErrStreamBufferFull) || evictedID.Load() != 1 {
			t.Fatalf("expected stream 1 evicted with ErrStreamBufferFull, got id=%d reason=%v", evictedID.Load(), reason)
		}
	case <-time.After(20 * time.Millisecond):
		t.Fatal("expected stalled stream 1 evicted once buffer capacity filled")
	}
	if demux.HasStream(1) {
		t.Fatal("expected evicted stream 1 removed from active streams")
	}
}

func TestStreamDemuxerHalfCloseDrainsQueuedFramesOverNetPipe(t *testing.T) {
	readerConn, writerConn := net.Pipe()
	defer readerConn.Close()

	demux := tunnel.NewStreamDemuxer(tunnel.DemuxerOptions{
		BufferCapacity: 16,
		EnqueueTimeout: 5 * time.Millisecond,
		WriteTimeout:   50 * time.Millisecond,
	})
	defer demux.CloseAll()

	demux.Register(77, writerConn)
	chunks := [][]byte{
		[]byte("HTTP/1.1 200 OK\r\n\r\n"),
		[]byte("data: {\"delta\":\"a\"}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}
	for _, ch := range chunks {
		if !demux.Enqueue(77, ch) {
			t.Fatal("expected Enqueue before CloseStream to succeed")
		}
	}
	demux.CloseStream(77)
	if demux.Enqueue(77, []byte("post-close-frame")) {
		t.Fatal("expected Enqueue after CloseStream to return false")
	}

	readDone := make(chan []byte, 1)
	readErrCh := make(chan error, 1)
	go func() {
		all, err := io.ReadAll(readerConn)
		if err != nil {
			readErrCh <- err
			return
		}
		readDone <- all
	}()

	select {
	case err := <-readErrCh:
		t.Fatalf("unexpected net.Pipe read error: %v", err)
	case got := <-readDone:
		want := string(bytes.Join(chunks, nil))
		if string(got) != want {
			t.Fatalf("expected half-close drain to deliver all queued chunks %q, got %q", want, string(got))
		}
	case <-time.After(20 * time.Millisecond):
		t.Fatal("timed out waiting for half-close stream drain")
	}
}

func TestStreamDemuxerDispatchOwnedFrameAndReserveLoopbackPort(t *testing.T) {
	demux := tunnel.NewStreamDemuxer(tunnel.DemuxerOptions{BufferCapacity: 4})
	defer demux.CloseAll()

	dst := newCollectorWriteCloser()
	demux.Register(9, dst)
	owned := protocol.EncodeStreamFrame(9, []byte("zero-copy-payload"))
	if !demux.DispatchOwnedFrame(owned) {
		t.Fatal("expected DispatchOwnedFrame to succeed")
	}
	demux.CloseStream(9)
	select {
	case <-dst.doneCh:
	case <-time.After(20 * time.Millisecond):
		t.Fatal("timed out waiting for DispatchOwnedFrame delivery")
	}
	if got := dst.String(); got != "zero-copy-payload" {
		t.Fatalf("unexpected DispatchOwnedFrame payload %q", got)
	}

	port, err := tunnel.ReserveLoopbackPort()
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("expected valid loopback port, got port=%d err=%v", port, err)
	}
}

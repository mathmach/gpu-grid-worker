package wsconn_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
	"github.com/mathmach/gpu-grid-worker/pkg/wsconn"
)

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

type inMemoryPipeListener struct {
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newInMemoryPipeListener() *inMemoryPipeListener {
	return &inMemoryPipeListener{
		ch:     make(chan net.Conn, 8),
		closed: make(chan struct{}),
	}
}

func (l *inMemoryPipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *inMemoryPipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *inMemoryPipeListener) Addr() net.Addr { return pipeAddr("inmemory") }

type deadlineBlockingConn struct {
	net.Conn
	blockWrites  atomic.Bool
	enteredWrite chan struct{}
	enterOnce    sync.Once
	writeBlock   chan struct{}
	mu           sync.Mutex
	writeDead    time.Time
}

func (c *deadlineBlockingConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDead = t
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}

func (c *deadlineBlockingConn) Write(p []byte) (int, error) {
	if !c.blockWrites.Load() {
		return c.Conn.Write(p)
	}
	c.enterOnce.Do(func() { close(c.enteredWrite) })
	c.mu.Lock()
	dl := c.writeDead
	c.mu.Unlock()
	var timerCh <-chan time.Time
	if !dl.IsZero() {
		wait := time.Until(dl)
		if wait <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timerCh = timer.C
	}
	select {
	case <-c.writeBlock:
		return 0, net.ErrClosed
	case <-timerCh:
		return 0, os.ErrDeadlineExceeded
	}
}

func (c *deadlineBlockingConn) Close() error {
	select {
	case <-c.writeBlock:
	default:
		close(c.writeBlock)
	}
	return c.Conn.Close()
}

func closeQuietly(c interface{ Close() error }) {
	if err := c.Close(); err != nil {
		return
	}
}

func openInMemoryWSPair(t *testing.T, wrapClient func(net.Conn) net.Conn) (*wsconn.Conn, *wsconn.Conn, func()) {
	t.Helper()
	ln := newInMemoryPipeListener()
	serverCh := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sc, err := upgrader.Upgrade(w, r, nil)
			if err == nil {
				serverCh <- sc
			}
		}),
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			return
		}
	}()

	dialer := websocket.Dialer{
		NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			cSide, sSide := net.Pipe()
			ln.ch <- sSide
			if wrapClient != nil {
				return wrapClient(cSide), nil
			}
			return cSide, nil
		},
	}
	clientRaw, resp, err := dialer.Dial("ws://inmemory.local/ws", nil)
	if resp != nil && resp.Body != nil {
		closeQuietly(resp.Body)
	}
	if err != nil {
		closeQuietly(ln)
		t.Fatalf("in-memory websocket dial failed: %v", err)
	}
	serverRaw := <-serverCh
	clientSafe := wsconn.New(clientRaw)
	serverSafe := wsconn.New(serverRaw)
	cleanup := func() {
		closeQuietly(clientSafe)
		closeQuietly(serverSafe)
		closeQuietly(ln)
	}
	return clientSafe, serverSafe, cleanup
}

func TestSafeConnSendStreamChunkFramingAndConcurrentWrites(t *testing.T) {
	client, server, cleanup := openInMemoryWSPair(t, nil)
	defer cleanup()

	go func() {
		for {
			if _, _, err := client.ReadFrame(); err != nil {
				return
			}
		}
	}()

	const workers = 6
	const framesPerWorker = 8
	total := workers*framesPerWorker + workers*2

	type rxResult struct {
		textCount int
		binCount  int
		err       error
	}
	rxDone := make(chan rxResult, 1)
	go func() {
		var res rxResult
		for i := 0; i < total; i++ {
			isBin, data, err := server.ReadFrame()
			if err != nil {
				res.err = err
				break
			}
			if isBin {
				id, payload, ok := protocol.DecodeStreamFrame(data)
				if !ok || id == 0 || !bytes.Equal(payload, []byte("chunk-data")) {
					res.err = errors.New("corrupted binary stream frame")
					break
				}
				res.binCount++
			} else {
				var msg protocol.ControlMsg
				if uErr := json.Unmarshal(data, &msg); uErr != nil || msg.Type != protocol.ControlState {
					res.err = errors.New("corrupted JSON text frame")
					break
				}
				res.textCount++
			}
		}
		rxDone <- res
	}()

	var wg sync.WaitGroup
	for w := 1; w <= workers; w++ {
		wg.Add(1)
		streamID := uint32(w)
		go func() {
			defer wg.Done()
			if err := client.SendPing([]byte("p"), time.Second); err != nil {
				t.Errorf("SendPing error: %v", err)
			}
			if err := client.SendJSON(protocol.ControlMsg{Type: protocol.ControlState, PoolID: "p1"}, time.Second); err != nil {
				t.Errorf("SendJSON error: %v", err)
			}
			if err := client.SendText([]byte(`{"type":"state","poolId":"p1"}`), time.Second); err != nil {
				t.Errorf("SendText error: %v", err)
			}
			for j := 0; j < framesPerWorker; j++ {
				if err := client.SendStreamChunk(streamID, []byte("chunk-data"), time.Second); err != nil {
					t.Errorf("SendStreamChunk error: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got := <-rxDone
	if got.err != nil || got.binCount != workers*framesPerWorker || got.textCount != workers*2 {
		t.Fatalf("unexpected concurrent framing result: %+v", got)
	}
}

func TestSafeConnClosedLatchSkipsJSONMarshalAndPreLockDeadlineEnforced(t *testing.T) {
	var blocker *deadlineBlockingConn
	client, server, cleanup := openInMemoryWSPair(t, func(raw net.Conn) net.Conn {
		blocker = &deadlineBlockingConn{
			Conn:         raw,
			enteredWrite: make(chan struct{}),
			writeBlock:   make(chan struct{}),
		}
		return blocker
	})
	defer cleanup()

	go func() {
		for {
			if _, _, err := server.ReadFrame(); err != nil {
				return
			}
		}
	}()

	blocker.blockWrites.Store(true)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.SendBinary(bytes.Repeat([]byte{'x'}, 8192), 30*time.Millisecond)
	}()

	<-blocker.enteredWrite
	contendedErr := client.SendJSON(protocol.ControlMsg{Type: protocol.ControlState}, 5*time.Millisecond)
	firstErr := <-firstDone
	if firstErr == nil {
		t.Fatal("expected blocked SendBinary to fail on write deadline")
	}
	if !errors.Is(contendedErr, os.ErrDeadlineExceeded) && !errors.Is(contendedErr, wsconn.ErrSocketClosed) {
		t.Fatalf("expected contended SendJSON to fail with ErrDeadlineExceeded or ErrSocketClosed, got %v", contendedErr)
	}

	unmarshalable := struct {
		Fn func()
	}{Fn: func() {}}
	if err := client.SendJSON(unmarshalable, time.Second); !errors.Is(err, wsconn.ErrSocketClosed) {
		t.Fatalf("expected closed latch to return ErrSocketClosed before json.Marshal, got %v", err)
	}
	if err := client.SendStreamChunk(1, []byte("abc"), time.Second); !errors.Is(err, wsconn.ErrSocketClosed) {
		t.Fatalf("expected SendStreamChunk after write failure to return ErrSocketClosed, got %v", err)
	}
}

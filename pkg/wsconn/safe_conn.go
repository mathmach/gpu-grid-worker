package wsconn

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var ErrSocketClosed = errors.New("websocket connection is closed")

type Conn struct {
	writeMu sync.Mutex
	conn    *websocket.Conn
	closed  atomic.Bool
}

func New(conn *websocket.Conn) *Conn {
	return &Conn{conn: conn}
}

func (s *Conn) ReadFrame() (bool, []byte, error) {
	if s == nil || s.conn == nil || s.closed.Load() {
		return false, nil, ErrSocketClosed
	}
	msgType, data, err := s.conn.ReadMessage()
	if err != nil {
		return false, nil, err
	}
	return msgType == websocket.BinaryMessage, data, nil
}

func (s *Conn) SendJSON(v any, timeout time.Duration) error {
	if s == nil || s.conn == nil || s.closed.Load() {
		return ErrSocketClosed
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.writeMessage(websocket.TextMessage, b, timeout, 10*time.Second)
}

func (s *Conn) SendText(payload []byte, timeout time.Duration) error {
	return s.writeMessage(websocket.TextMessage, payload, timeout, 10*time.Second)
}

func (s *Conn) SendBinary(frame []byte, timeout time.Duration) error {
	return s.writeMessage(websocket.BinaryMessage, frame, timeout, 30*time.Second)
}

func (s *Conn) writeMessage(msgType int, payload []byte, timeout, defaultTimeout time.Duration) error {
	if s == nil || s.conn == nil || s.closed.Load() {
		return ErrSocketClosed
	}
	deadline := time.Now().Add(resolveTimeout(timeout, defaultTimeout))
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareWriteLocked(deadline); err != nil {
		return err
	}
	if err := s.conn.WriteMessage(msgType, payload); err != nil {
		s.markClosedAndShutdown()
		return err
	}
	return nil
}

func (s *Conn) SendStreamChunk(streamID uint32, payload []byte, timeout time.Duration) error {
	if s == nil || s.conn == nil || s.closed.Load() {
		return ErrSocketClosed
	}
	deadline := time.Now().Add(resolveTimeout(timeout, 30*time.Second))
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareWriteLocked(deadline); err != nil {
		return err
	}
	return s.writeStreamFramesLocked(streamID, payload)
}

func (s *Conn) SendPing(payload []byte, timeout time.Duration) error {
	if s == nil || s.conn == nil {
		return ErrSocketClosed
	}
	deadline := time.Now().Add(resolveTimeout(timeout, 5*time.Second))
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return ErrSocketClosed
	}
	if !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	if err := s.conn.WriteControl(websocket.PingMessage, payload, deadline); err != nil {
		s.markClosedAndShutdown()
		return err
	}
	return nil
}

func (s *Conn) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	s.closed.Store(true)
	return s.conn.Close()
}

func (s *Conn) prepareWriteLocked(deadline time.Time) error {
	if s.closed.Load() {
		return ErrSocketClosed
	}
	if !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	return s.conn.SetWriteDeadline(deadline)
}

func (s *Conn) writeStreamFramesLocked(streamID uint32, payload []byte) error {
	w, err := s.conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		s.markClosedAndShutdown()
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], streamID)
	if _, err := w.Write(hdr[:]); err != nil {
		return s.closeFrameWriterAndShutdown(w, err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return s.closeFrameWriterAndShutdown(w, err)
		}
	}
	if err := w.Close(); err != nil {
		s.markClosedAndShutdown()
		return err
	}
	return nil
}

func (s *Conn) closeFrameWriterAndShutdown(w interface{ Close() error }, writeErr error) error {
	closeErr := w.Close()
	s.markClosedAndShutdown()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (s *Conn) markClosedAndShutdown() {
	if s.closed.CompareAndSwap(false, true) {
		if err := s.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return
		}
	}
}

func resolveTimeout(timeout, fallback time.Duration) time.Duration {
	if timeout <= 0 {
		return fallback
	}
	return timeout
}

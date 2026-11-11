package tunnel

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type writeDeadliner interface {
	SetWriteDeadline(t time.Time) error
}

type streamPipe struct {
	id           uint32
	dst          io.WriteCloser
	sendMu       sync.RWMutex
	ch           chan []byte
	abortCh      chan struct{}
	closing      atomic.Bool
	drainOnce    sync.Once
	abortOnce    sync.Once
	dstCloseOnce sync.Once
	evictOnce    sync.Once
	writeTimeout time.Duration
}

func newStreamPipe(streamID uint32, dst io.WriteCloser, capacity int, writeTimeout time.Duration) *streamPipe {
	return &streamPipe{
		id:           streamID,
		dst:          dst,
		ch:           make(chan []byte, capacity),
		abortCh:      make(chan struct{}),
		writeTimeout: writeTimeout,
	}
}

func (p *streamPipe) closeDestination() {
	p.dstCloseOnce.Do(func() {
		if p.dst != nil {
			if err := p.dst.Close(); err != nil {
				return
			}
		}
	})
}

func (p *streamPipe) abort() {
	p.closing.Store(true)
	p.abortOnce.Do(func() {
		close(p.abortCh)
	})
	p.closeDestination()
}

func (p *streamPipe) signalDrain() {
	p.closing.Store(true)
	p.drainOnce.Do(func() {
		p.sendMu.Lock()
		close(p.ch)
		p.sendMu.Unlock()
	})
}

func writeAllBytes(dst io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := dst.Write(buf)
		if n > 0 {
			buf = buf[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

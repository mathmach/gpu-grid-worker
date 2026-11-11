package tunnel

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

const (
	DefaultStreamBufferCapacity = 128
	DefaultEnqueueTimeout       = 25 * time.Millisecond
	DefaultStreamWriteTimeout   = 15 * time.Second
)

var ErrStreamBufferFull = errors.New("tunnel stream buffer capacity exceeded")

type DemuxerOptions struct {
	BufferCapacity int
	EnqueueTimeout time.Duration
	WriteTimeout   time.Duration
	OnStreamEvict  func(streamID uint32, reason error)
}

type StreamDemuxer struct {
	mu             sync.RWMutex
	streams        map[uint32]*streamPipe
	bufferCapacity int
	enqueueTimeout time.Duration
	writeTimeout   time.Duration
	onEvict        func(streamID uint32, reason error)
}

func NewStreamDemuxer(opts DemuxerOptions) *StreamDemuxer {
	bufCap := opts.BufferCapacity
	if bufCap <= 0 {
		bufCap = DefaultStreamBufferCapacity
	}
	enqTimeout := opts.EnqueueTimeout
	if enqTimeout == 0 {
		enqTimeout = DefaultEnqueueTimeout
	} else if enqTimeout < 0 {
		enqTimeout = 0
	}
	wTimeout := opts.WriteTimeout
	if wTimeout <= 0 {
		wTimeout = DefaultStreamWriteTimeout
	}
	return &StreamDemuxer{
		streams:        make(map[uint32]*streamPipe),
		bufferCapacity: bufCap,
		enqueueTimeout: enqTimeout,
		writeTimeout:   wTimeout,
		onEvict:        opts.OnStreamEvict,
	}
}

func NewStreamDemuxerFactory(opts DemuxerOptions) nodecore.StreamDemuxerFactory {
	return func(onEvict func(streamID uint32, reason error)) nodecore.StreamDemuxer {
		cp := opts
		cp.OnStreamEvict = onEvict
		return NewStreamDemuxer(cp)
	}
}

func (d *StreamDemuxer) Register(streamID uint32, dst io.WriteCloser) {
	if d == nil || dst == nil {
		return
	}
	pipe := newStreamPipe(streamID, dst, d.bufferCapacity, d.writeTimeout)

	d.mu.Lock()
	prev := d.streams[streamID]
	d.streams[streamID] = pipe
	d.mu.Unlock()

	if prev != nil {
		prev.abort()
	}
	go d.runStreamWriter(pipe)
}

func (d *StreamDemuxer) DispatchFrame(rawFrame []byte) bool {
	if d == nil {
		return false
	}
	sid, payload, ok := protocol.DecodeStreamFrame(rawFrame)
	if !ok || len(payload) == 0 {
		return false
	}
	return d.enqueuePayload(sid, payload, true)
}

func (d *StreamDemuxer) DispatchOwnedFrame(ownedFrame []byte) bool {
	if d == nil {
		return false
	}
	sid, payload, ok := protocol.DecodeStreamFrame(ownedFrame)
	if !ok || len(payload) == 0 {
		return false
	}
	return d.enqueuePayload(sid, payload, false)
}

func (d *StreamDemuxer) Enqueue(streamID uint32, payload []byte) bool {
	return d.enqueuePayload(streamID, payload, true)
}

func (d *StreamDemuxer) enqueuePayload(streamID uint32, payload []byte, clone bool) bool {
	if d == nil || len(payload) == 0 {
		return false
	}
	d.mu.RLock()
	pipe := d.streams[streamID]
	d.mu.RUnlock()
	if pipe == nil || pipe.closing.Load() {
		return false
	}

	chunk := payload
	if clone {
		chunk = make([]byte, len(payload))
		copy(chunk, payload)
	}

	pipe.sendMu.RLock()
	if pipe.closing.Load() {
		pipe.sendMu.RUnlock()
		return false
	}
	select {
	case <-pipe.abortCh:
		pipe.sendMu.RUnlock()
		return false
	case pipe.ch <- chunk:
		pipe.sendMu.RUnlock()
		return true
	default:
	}
	ok, full := d.enqueueSlowPathLocked(pipe, chunk)
	pipe.sendMu.RUnlock()
	if full {
		d.evictPipe(pipe, ErrStreamBufferFull)
	}
	return ok
}

func (d *StreamDemuxer) enqueueSlowPathLocked(pipe *streamPipe, chunk []byte) (bool, bool) {
	if d.enqueueTimeout <= 0 {
		return false, true
	}
	timer := time.NewTimer(d.enqueueTimeout)
	defer timer.Stop()
	select {
	case <-pipe.abortCh:
		return false, false
	case pipe.ch <- chunk:
		return true, false
	case <-timer.C:
		return false, true
	}
}

var (
	_ nodecore.StreamDemuxer      = (*StreamDemuxer)(nil)
	_ nodecore.OwnedStreamDemuxer = (*StreamDemuxer)(nil)
	_ nodecore.StreamAborter      = (*StreamDemuxer)(nil)
)

func (d *StreamDemuxer) CloseStream(streamID uint32) {
	if d == nil {
		return
	}
	d.mu.Lock()
	pipe := d.streams[streamID]
	if pipe != nil {
		delete(d.streams, streamID)
	}
	d.mu.Unlock()

	if pipe != nil {
		pipe.signalDrain()
	}
}

func (d *StreamDemuxer) AbortStream(streamID uint32) {
	if d == nil {
		return
	}
	d.mu.Lock()
	pipe := d.streams[streamID]
	if pipe != nil {
		delete(d.streams, streamID)
	}
	d.mu.Unlock()

	if pipe != nil {
		pipe.abort()
	}
}

func (d *StreamDemuxer) CloseAll() {
	if d == nil {
		return
	}
	d.mu.Lock()
	pipes := make([]*streamPipe, 0, len(d.streams))
	for _, p := range d.streams {
		pipes = append(pipes, p)
	}
	d.streams = make(map[uint32]*streamPipe)
	d.mu.Unlock()

	for _, p := range pipes {
		p.abort()
	}
}

func (d *StreamDemuxer) HasStream(streamID uint32) bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	_, ok := d.streams[streamID]
	d.mu.RUnlock()
	return ok
}

func (d *StreamDemuxer) runStreamWriter(pipe *streamPipe) {
	for {
		select {
		case <-pipe.abortCh:
			return
		case chunk, ok := <-pipe.ch:
			if !ok {
				pipe.closeDestination()
				return
			}
			if !d.writePipeChunk(pipe, chunk) {
				return
			}
		}
	}
}

func (d *StreamDemuxer) writePipeChunk(pipe *streamPipe, chunk []byte) bool {
	if pipe.writeTimeout > 0 {
		if dl, ok := pipe.dst.(writeDeadliner); ok {
			if err := dl.SetWriteDeadline(time.Now().Add(pipe.writeTimeout)); err != nil {
				d.evictPipe(pipe, err)
				return false
			}
		}
	}
	if err := writeAllBytes(pipe.dst, chunk); err != nil {
		d.evictPipe(pipe, err)
		return false
	}
	return true
}

func (d *StreamDemuxer) evictPipe(pipe *streamPipe, reason error) {
	if pipe == nil {
		return
	}
	d.mu.Lock()
	if cur := d.streams[pipe.id]; cur == pipe {
		delete(d.streams, pipe.id)
	}
	d.mu.Unlock()

	pipe.abort()
	if d.onEvict != nil {
		pipe.evictOnce.Do(func() {
			d.onEvict(pipe.id, reason)
		})
	}
}

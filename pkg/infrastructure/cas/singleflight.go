package cas

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

const (
	maxMemoizedDigests           = 512
	minDownloadFreeHeadroomBytes = 64 << 20
)

type inflightCall struct {
	done   chan struct{}
	digest string
	err    error
}

type pathLockEntry struct {
	mu   sync.Mutex
	refs int
}

type downloadCoordinator struct {
	mu       sync.Mutex
	inflight map[string]*inflightCall
	locks    map[string]*pathLockEntry
}

var globalDownloadCoord = &downloadCoordinator{
	inflight: make(map[string]*inflightCall),
	locks:    make(map[string]*pathLockEntry),
}

var inFlightReservedDiskBytes atomic.Int64

func (c *downloadCoordinator) Do(key string, fn func() (string, error)) (string, error) {
	c.mu.Lock()
	if existing, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-existing.done
		return existing.digest, existing.err
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	digest, err := fn()
	call.digest = digest
	call.err = err

	c.mu.Lock()
	delete(c.inflight, key)
	close(call.done)
	c.mu.Unlock()
	return digest, err
}

func (c *downloadCoordinator) LockPath(pathKey string) func() {
	c.mu.Lock()
	entry, ok := c.locks[pathKey]
	if !ok {
		entry = &pathLockEntry{}
		c.locks[pathKey] = entry
	}
	entry.refs++
	c.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		c.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(c.locks, pathKey)
		}
		c.mu.Unlock()
	}
}

type memoizedDigestEntry struct {
	size        int64
	modTimeNano int64
	sha256Hex   string
}

type verifiedDigestStore struct {
	mu    sync.Mutex
	items map[string]memoizedDigestEntry
	order []string
}

var globalDigestMemo = &verifiedDigestStore{
	items: make(map[string]memoizedDigestEntry),
}

func (s *verifiedDigestStore) Lookup(path string) (string, int64, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, false
	}
	key := canonicalCASPathKey(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	if !ok || entry.size != st.Size() || entry.modTimeNano != st.ModTime().UnixNano() {
		return "", 0, false
	}
	return entry.sha256Hex, entry.size, true
}

func (s *verifiedDigestStore) Record(path, sha256Hex string) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return
	}
	key := canonicalCASPathKey(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[key]; !exists {
		if len(s.order) >= maxMemoizedDigests {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.items, oldest)
		}
		s.order = append(s.order, key)
	}
	s.items[key] = memoizedDigestEntry{
		size:        st.Size(),
		modTimeNano: st.ModTime().UnixNano(),
		sha256Hex:   sha256Hex,
	}
}

func (s *verifiedDigestStore) Invalidate(path string) {
	key := canonicalCASPathKey(path)
	s.mu.Lock()
	if _, exists := s.items[key]; exists {
		delete(s.items, key)
		s.order = slices.DeleteFunc(s.order, func(k string) bool { return k == key })
	}
	s.mu.Unlock()
}

func canonicalCASPathKey(p string) string {
	cleaned := filepath.Clean(strings.TrimSpace(p))
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = abs
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func reserveDiskHeadroomForDownload(destDir string, neededBytes int64) (func(), error) {
	freeBefore, err := rawAvailableDiskBytes(destDir)
	if err != nil {
		return func() {}, nil
	}
	delta := max(int64(0), neededBytes)
	reservedAfter := inFlightReservedDiskBytes.Add(delta)
	release := func() {
		if delta > 0 {
			inFlightReservedDiskBytes.Add(-delta)
		}
	}
	required := uint64(max(int64(0), reservedAfter)) + minDownloadFreeHeadroomBytes
	if freeBefore < required {
		release()
		return nil, derr.New(derr.CodeUpstreamFailure, 507,
			fmt.Sprintf("insufficient disk space for artifact download: need %d bytes free including headroom, have %d", required, freeBefore), nil)
	}
	return release, nil
}

func currentReservedDiskBytes() uint64 {
	v := inFlightReservedDiskBytes.Load()
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

func newRandomPartSuffix() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

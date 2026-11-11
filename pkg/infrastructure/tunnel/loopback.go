package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultStreamWriteDeadline = 15 * time.Second
	loopbackPortCooldown       = 2 * time.Second
	maxRecentLoopbackPorts     = 32
)

type recentLoopbackPort struct {
	port int
	at   time.Time
}

var (
	loopbackPortsMu sync.Mutex
	loopbackPorts   []recentLoopbackPort
)

type LocalLoopbackTransport struct {
	healthClient *http.Client
}

func NewLoopbackTransport() *LocalLoopbackTransport {
	return &LocalLoopbackTransport{
		healthClient: newNoRedirectLoopbackClient(1500 * time.Millisecond),
	}
}

func newNoRedirectLoopbackClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (LocalLoopbackTransport) AllocateFreePort() (int, error) {
	return ReserveLoopbackPort()
}

func ReserveLoopbackPort() (int, error) {
	var held []net.Listener
	defer func() {
		for _, ln := range held {
			closeCloserQuietly(ln)
		}
	}()

	var fallbackPort int
	for range 8 {
		ln, port, err := listenEphemeralLoopback()
		if err != nil {
			return finalizeReservedLoopbackPort(fallbackPort, err)
		}
		held = append(held, ln)
		if port <= 0 {
			continue
		}
		fallbackPort = port
		if !wasRecentLoopbackPort(port) {
			recordLoopbackPort(port)
			return port, nil
		}
	}
	return finalizeReservedLoopbackPort(fallbackPort, errors.New("failed to allocate free loopback TCP port"))
}

func listenEphemeralLoopback() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return ln, 0, nil
	}
	return ln, addr.Port, nil
}

func finalizeReservedLoopbackPort(fallbackPort int, fallbackErr error) (int, error) {
	if fallbackPort > 0 {
		recordLoopbackPort(fallbackPort)
		return fallbackPort, nil
	}
	return 0, fallbackErr
}

func wasRecentLoopbackPort(port int) bool {
	now := time.Now()
	loopbackPortsMu.Lock()
	defer loopbackPortsMu.Unlock()
	pruneLoopbackPortsLocked(now)
	for _, entry := range loopbackPorts {
		if entry.port == port {
			return true
		}
	}
	return false
}

func recordLoopbackPort(port int) {
	now := time.Now()
	loopbackPortsMu.Lock()
	defer loopbackPortsMu.Unlock()
	pruneLoopbackPortsLocked(now)
	if len(loopbackPorts) >= maxRecentLoopbackPorts {
		loopbackPorts = loopbackPorts[1:]
	}
	loopbackPorts = append(loopbackPorts, recentLoopbackPort{port: port, at: now})
}

func pruneLoopbackPortsLocked(now time.Time) {
	keep := loopbackPorts[:0]
	for _, entry := range loopbackPorts {
		if now.Sub(entry.at) < loopbackPortCooldown {
			keep = append(keep, entry)
		}
	}
	loopbackPorts = keep
}

func (LocalLoopbackTransport) ListenLoopback(onAccept func(io.ReadWriteCloser)) (int, func(), error) {
	ln, port, err := (LoopbackBridgeManager{}).ListenLoopback(onAccept)
	if err != nil {
		return 0, nil, err
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			closeCloserQuietly(ln)
		})
	}
	return port, stop, nil
}

func (LocalLoopbackTransport) DialStream(port int, timeout time.Duration) (io.ReadWriteCloser, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), timeout)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		if dErr := tc.SetNoDelay(true); dErr != nil {
			closeCloserQuietly(conn)
			return nil, dErr
		}
	}
	return &deadlineStreamConn{conn: conn, writeTimeout: defaultStreamWriteDeadline}, nil
}

func (t *LocalLoopbackTransport) ProbeHTTPHealth(ctx context.Context, port int) bool {
	var client *http.Client
	if t != nil {
		client = t.healthClient
	}
	return ProbeServerHealth(ctx, client, "127.0.0.1", port)
}

func ProbeServerHealth(ctx context.Context, client *http.Client, host string, port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = newNoRedirectLoopbackClient(1500 * time.Millisecond)
	}
	cleanHost := strings.TrimSpace(host)
	if cleanHost == "" {
		cleanHost = "127.0.0.1"
	}
	status := probeHTTPStatus(ctx, client, cleanHost, port, "/health")
	if status == http.StatusOK {
		return true
	}
	if status != http.StatusNotFound {
		return false
	}
	return probeHTTPStatus(ctx, client, cleanHost, port, "/v1/models") == http.StatusOK ||
		probeHTTPStatus(ctx, client, cleanHost, port, "/") == http.StatusOK
}

func probeHTTPStatus(ctx context.Context, client *http.Client, host string, port int, path string) int {
	hostPort := net.JoinHostPort(host, strconv.Itoa(port))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+hostPort+path, nil)
	if err != nil {
		return 0
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	if _, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); copyErr != nil {
		closeCloserQuietly(resp.Body)
		return 0
	}
	closeCloserQuietly(resp.Body)
	return resp.StatusCode
}

type deadlineStreamConn struct {
	conn                net.Conn
	writeTimeout        time.Duration
	hasExplicitDeadline atomic.Bool
}

func (c *deadlineStreamConn) Read(p []byte) (int, error) {
	return c.conn.Read(p)
}

func (c *deadlineStreamConn) SetWriteDeadline(t time.Time) error {
	c.hasExplicitDeadline.Store(!t.IsZero())
	return c.conn.SetWriteDeadline(t)
}

func (c *deadlineStreamConn) Write(p []byte) (int, error) {
	if !c.hasExplicitDeadline.Load() && c.writeTimeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
			return 0, err
		}
	}
	return c.conn.Write(p)
}

func (c *deadlineStreamConn) Close() error {
	return c.conn.Close()
}

func closeCloserQuietly(c io.Closer) {
	if c != nil {
		if err := c.Close(); err != nil {
			return
		}
	}
}

package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

const maxConcurrentBridgeHandshakes = 256

type LoopbackBridgeManager struct{}

var _ nodecore.LoopbackBridgeFactory = (*LoopbackBridgeManager)(nil)

func NewLoopbackBridgeFactory() *LoopbackBridgeManager {
	return &LoopbackBridgeManager{}
}

func (LoopbackBridgeManager) AllocatePort() (int, error) {
	return ReserveLoopbackPort()
}

func (m LoopbackBridgeManager) ListenLoopback(onConn func(io.ReadWriteCloser)) (io.Closer, int, error) {
	return m.ListenLoopbackContext(context.Background(), onConn)
}

func (LoopbackBridgeManager) ListenLoopbackContext(ctx context.Context, onConn func(io.ReadWriteCloser)) (io.Closer, int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	managed := newManagedLoopbackListener(ctx, ln, onConn)
	return managed, port, nil
}

type managedLoopbackListener struct {
	ln        net.Listener
	stopCh    chan struct{}
	closeOnce sync.Once
	closeErr  error
	stopCtxFn func() bool
	sem       chan struct{}
	acceptWG  sync.WaitGroup
}

func newManagedLoopbackListener(ctx context.Context, ln net.Listener, onConn func(io.ReadWriteCloser)) *managedLoopbackListener {
	m := &managedLoopbackListener{
		ln:     ln,
		stopCh: make(chan struct{}),
		sem:    make(chan struct{}, maxConcurrentBridgeHandshakes),
	}
	if ctx != nil && ctx.Done() != nil {
		m.stopCtxFn = context.AfterFunc(ctx, func() {
			closeCloserQuietly(m)
		})
	}
	m.acceptWG.Add(1)
	go m.acceptLoop(onConn)
	return m
}

func (m *managedLoopbackListener) Close() error {
	m.closeOnce.Do(func() {
		if m.stopCtxFn != nil {
			m.stopCtxFn()
		}
		close(m.stopCh)
		m.closeErr = m.ln.Close()
		m.acceptWG.Wait()
	})
	return m.closeErr
}

func (m *managedLoopbackListener) acceptLoop(onConn func(io.ReadWriteCloser)) {
	defer m.acceptWG.Done()
	for {
		conn, err := m.ln.Accept()
		if err != nil {
			if m.shouldExitAcceptLoop(err) {
				return
			}
			continue
		}
		m.handleAcceptedConn(conn, onConn)
	}
}

func (m *managedLoopbackListener) shouldExitAcceptLoop(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	select {
	case <-m.stopCh:
		return true
	default:
	}
	timer := time.NewTimer(5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-m.stopCh:
		return true
	case <-timer.C:
		return false
	}
}

func (m *managedLoopbackListener) handleAcceptedConn(conn net.Conn, onConn func(io.ReadWriteCloser)) {
	if tc, ok := conn.(*net.TCPConn); ok {
		if ndErr := tc.SetNoDelay(true); ndErr != nil {
			closeCloserQuietly(conn)
			return
		}
	}
	if onConn == nil {
		closeCloserQuietly(conn)
		return
	}
	select {
	case <-m.stopCh:
		closeCloserQuietly(conn)
		return
	case m.sem <- struct{}{}:
	}
	go func() {
		defer func() { <-m.sem }()
		onConn(&deadlineStreamConn{conn: conn, writeTimeout: defaultStreamWriteDeadline})
	}()
}

type HostEnvironment struct{}

var _ nodecore.HostEnvironmentPort = (*HostEnvironment)(nil)

func NewHostEnvironment() *HostEnvironment {
	return &HostEnvironment{}
}

func (HostEnvironment) LANEndpoints(port int) []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if isVirtualNetworkInterface(iface.Name) {
			continue
		}
		out = appendInterfaceLANEndpoints(out, iface, port)
	}
	return out
}

func isVirtualNetworkInterface(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{
		"docker", "veth", "br-", "virbr", "cni", "flannel", "calico",
		"tun", "tap", "utun", "wg", "tailscale", "wsl", "vethernet", "lo",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func appendInterfaceLANEndpoints(out []string, iface net.Interface, port int) []string {
	addrs, err := iface.Addrs()
	if err != nil {
		return out
	}
	for _, addr := range addrs {
		ip4 := extractValidLANIPv4(addr)
		if ip4 == nil {
			continue
		}
		out = append(out, fmt.Sprintf("http://%s:%d", ip4.String(), port))
	}
	return out
}

func extractValidLANIPv4(addr net.Addr) net.IP {
	var ip net.IP
	switch v := addr.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	}
	if ip == nil {
		return nil
	}
	ip4 := ip.To4()
	if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsMulticast() || ip4.IsUnspecified() {
		return nil
	}
	return ip4
}

func (HostEnvironment) WorkerBinariesAvailable(distDir string) (bool, bool) {
	clean := strings.TrimSpace(distDir)
	if clean == "" {
		return false, false
	}
	winOK := regularFileExists(filepath.Join(clean, "gpu-worker-windows-amd64.exe"))
	linOK := regularFileExists(filepath.Join(clean, "gpu-worker-linux-amd64"))
	return winOK, linOK
}

func regularFileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

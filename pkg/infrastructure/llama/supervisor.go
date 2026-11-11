package llama

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
)

const MaxStderrTailBytes = 16 * 1024

type BoundedTailBuffer struct {
	mu   sync.Mutex
	buf  []byte
	head int
	full bool
}

func NewBoundedTailBuffer(capacity int) *BoundedTailBuffer {
	if capacity <= 0 {
		capacity = MaxStderrTailBytes
	}
	return &BoundedTailBuffer{buf: make([]byte, capacity)}
}

func (b *BoundedTailBuffer) Write(p []byte) (int, error) {
	total := len(p)
	if total == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	capLen := len(b.buf)
	if total >= capLen {
		copy(b.buf, p[total-capLen:])
		b.head = 0
		b.full = true
		return total, nil
	}

	first := min(total, capLen-b.head)
	copy(b.buf[b.head:], p[:first])
	rem := total - first
	if rem > 0 {
		copy(b.buf, p[first:])
		b.head = rem
		b.full = true
		return total, nil
	}
	b.head += first
	if b.head >= capLen {
		b.head = 0
		b.full = true
	}
	return total, nil
}

func (b *BoundedTailBuffer) appendString(s string) {
	if b == nil || s == "" {
		return
	}
	if _, err := b.Write([]byte(s)); err != nil {
		return
	}
}

func (b *BoundedTailBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.full {
		return string(b.buf[:b.head])
	}
	out := make([]byte, len(b.buf))
	tailLen := len(b.buf) - b.head
	copy(out, b.buf[b.head:])
	copy(out[tailLen:], b.buf[:b.head])
	return string(out)
}

type ProcessLaunchConfig struct {
	BinaryPath     string
	Args           []string
	WorkDir        string
	BaseEnv        []string
	ExtraEnv       []string
	LibraryPath    string
	BootBreaker    *PoolBootCircuitBreaker
	BootKey        string
	HealthCheckURL string
	StartupTimeout time.Duration
	Readiness      ReadinessMode
	BoundPort      int
}

type SupervisedProcess struct {
	stopOnce      sync.Once
	bootOnce      sync.Once
	stopped       atomic.Bool
	bootCompleted atomic.Bool
	externalReady atomic.Bool
	readiness     ReadinessMode
	boundPort     int
	cmd           *exec.Cmd
	done          chan struct{}
	stderr        *BoundedTailBuffer
	exitMu        sync.RWMutex
	exitErr       error
	bootDone      func(bool)
	bootAbort     func()
	jobAttached   bool
}

var (
	_ nodecore.RunningPoolProcess       = (*SupervisedProcess)(nil)
	_ nodecore.ReadinessMarkableProcess = (*SupervisedProcess)(nil)
	_ nodecore.BoundPortProcess         = (*SupervisedProcess)(nil)
)

func StartSupervisedProcess(ctx context.Context, cfg ProcessLaunchConfig) (*SupervisedProcess, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	readiness := cfg.Readiness
	if strings.TrimSpace(cfg.HealthCheckURL) != "" && readiness == ReadinessNone {
		readiness = ReadinessHTTPHealth
	}

	bootDone, bootAbort, err := acquireBootPermit(cfg.BootBreaker, cfg.BootKey)
	if err != nil {
		return nil, err
	}

	cmd, stderrBuf := buildSupervisedCommand(cfg)
	proc, err := startAndSuperviseSubprocess(cmd, stderrBuf, bootDone, bootAbort, readiness, cfg.BoundPort)
	if err != nil {
		bootDone(false)
		return nil, err
	}

	if strings.TrimSpace(cfg.HealthCheckURL) != "" {
		if err := waitForSupervisedHealth(ctx, proc, cfg.HealthCheckURL, cfg.StartupTimeout); err != nil {
			if IsAddressInUseOutput(proc.StderrTail()) {
				proc.abortBoot()
			} else {
				proc.finishBoot(false)
			}
			proc.Stop()
			return nil, err
		}
		proc.MarkReady()
		proc.finishBoot(true)
	}

	return proc, nil
}

type subprocessStartResult struct {
	proc *SupervisedProcess
	err  error
}

func startAndSuperviseSubprocess(
	cmd *exec.Cmd,
	stderrBuf *BoundedTailBuffer,
	bootDone func(bool),
	bootAbort func(),
	readiness ReadinessMode,
	boundPort int,
) (*SupervisedProcess, error) {
	startedCh := make(chan subprocessStartResult, 1)
	go func() {
		if runtime.GOOS == "linux" {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
		}
		if err := cmd.Start(); err != nil {
			startedCh <- subprocessStartResult{err: err}
			return
		}
		if attachErr := attachSubprocess(cmd); attachErr != nil {
			if waitErr := cmd.Wait(); waitErr != nil {
				stderrBuf.appendString("\n" + waitErr.Error())
			}
			releaseSubprocess(cmd)
			startedCh <- subprocessStartResult{err: attachErr}
			return
		}
		proc := &SupervisedProcess{
			readiness:   readiness,
			boundPort:   boundPort,
			cmd:         cmd,
			done:        make(chan struct{}),
			stderr:      stderrBuf,
			bootDone:    bootDone,
			bootAbort:   bootAbort,
			jobAttached: true,
		}
		startedCh <- subprocessStartResult{proc: proc}
		waitErr := cmd.Wait()
		releaseSubprocess(cmd)
		proc.recordExit(waitErr)
		close(proc.done)
	}()
	res := <-startedCh
	return res.proc, res.err
}

func acquireBootPermit(breaker *PoolBootCircuitBreaker, key string) (func(bool), func(), error) {
	if breaker == nil || strings.TrimSpace(key) == "" {
		return func(bool) {}, func() {}, nil
	}
	return breaker.AllowAbortable(key)
}

func buildSupervisedCommand(cfg ProcessLaunchConfig) (*exec.Cmd, *BoundedTailBuffer) {
	cmd := exec.Command(cfg.BinaryPath, cfg.Args...)
	workDir := strings.TrimSpace(cfg.WorkDir)
	if workDir == "" {
		workDir = filepath.Dir(cfg.BinaryPath)
	}
	cmd.Dir = workDir
	cmd.Env = mergeEnvOverrides(workerconfig.BuildSubprocessEnv(cfg.BaseEnv, filepath.Dir(cfg.BinaryPath), cfg.LibraryPath), cfg.ExtraEnv)
	prepareSupervisedSubprocess(cmd)
	stderrBuf := NewBoundedTailBuffer(MaxStderrTailBytes)
	cmd.Stderr = stderrBuf
	return cmd, stderrBuf
}

func waitForSupervisedHealth(ctx context.Context, proc *SupervisedProcess, healthURL string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()

	for {
		if probeHTTPHealthReady(waitCtx, client, healthURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waitCtx.Done():
			return fmt.Errorf("timed out waiting for supervised process health (%s)", proc.StderrTail())
		case <-proc.Done():
			return fmt.Errorf("supervised process exited prematurely: %s", proc.StderrTail())
		case <-ticker.C:
		}
	}
}

func probeHTTPHealthReady(ctx context.Context, client *http.Client, healthURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	if _, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); copyErr != nil {
		resp.Body.Close()
		return false
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		return false
	}
	return resp.StatusCode == http.StatusOK
}

func mergeEnvOverrides(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	out := append([]string(nil), base...)
	for _, entry := range extra {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}
		key := entry[:eq]
		replaced := false
		for i, existing := range out {
			if exEq := strings.IndexByte(existing, '='); exEq == len(key) && strings.EqualFold(existing[:exEq], key) {
				out[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, entry)
		}
	}
	return out
}

func (p *SupervisedProcess) finishBoot(success bool) {
	if p == nil || p.bootDone == nil {
		return
	}
	p.bootOnce.Do(func() {
		if success {
			p.bootCompleted.Store(true)
		}
		p.bootDone(success)
	})
}

func (p *SupervisedProcess) abortBoot() {
	if p == nil || p.bootAbort == nil {
		return
	}
	p.bootOnce.Do(func() {
		p.bootAbort()
	})
}

func (p *SupervisedProcess) MarkReady() {
	if p == nil {
		return
	}
	p.externalReady.Store(true)
}

func (p *SupervisedProcess) BoundPort() int {
	if p == nil {
		return 0
	}
	return p.boundPort
}

func (p *SupervisedProcess) MarkBootOutcome(success bool) {
	if success {
		p.MarkReady()
	}
	p.finishBoot(success)
}

func (p *SupervisedProcess) recordExit(waitErr error) {
	p.exitMu.Lock()
	p.exitErr = waitErr
	p.exitMu.Unlock()

	if waitErr != nil {
		p.stderr.appendString("\n" + waitErr.Error())
		if !p.stopped.Load() && !IsAddressInUseOutput(p.stderr.String()) {
			p.finishBoot(false)
			return
		}
		p.abortBoot()
		return
	}
	if !p.stopped.Load() {
		if IsAddressInUseOutput(p.stderr.String()) {
			p.abortBoot()
			return
		}
		if p.readiness == ReadinessNone && !p.externalReady.Load() {
			p.finishBoot(false)
			return
		}
		if !p.bootCompleted.Load() && !p.externalReady.Load() {
			p.finishBoot(false)
			return
		}
		p.finishBoot(true)
		return
	}
	p.abortBoot()
}

func (p *SupervisedProcess) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.stopped.Store(true)
		terminateProcessTree(p.cmd)
		if p.waitProcessReap(3 * time.Second) {
			return
		}
		killProcessDirect(p.cmd)
		if !p.waitProcessReap(2 * time.Second) {
			p.finishBoot(false)
		}
	})
}

func (p *SupervisedProcess) waitProcessReap(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		if !p.bootCompleted.Load() {
			p.abortBoot()
		}
		return true
	case <-timer.C:
		return false
	}
}

func (p *SupervisedProcess) Done() <-chan struct{} {
	if p == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return p.done
}

func (p *SupervisedProcess) StderrTail() string {
	if p == nil || p.stderr == nil {
		return ""
	}
	return p.stderr.String()
}

func (p *SupervisedProcess) ExitErr() error {
	if p == nil {
		return nil
	}
	p.exitMu.RLock()
	defer p.exitMu.RUnlock()
	return p.exitErr
}

func (p *SupervisedProcess) JobObjectAttached() bool {
	if p == nil {
		return false
	}
	return p.jobAttached
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/hardware"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/observability"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workergui"
	"github.com/mathmach/gpu-grid-worker/pkg/presentation/workerhttp"
)

func main() {
	attachParentConsoleIfNeeded(os.Args[1:])
	bootCfg, err := workerconfig.LoadWorkerConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid CLI flags: %v\n", err)
		os.Exit(2)
	}

	hwInspector := hardware.NewVulkanInspector()
	runtimeFS := cas.NewRuntimeFS()
	if bootCfg.ListDevices {
		os.Exit(runListDevicesMode(bootCfg, hwInspector, runtimeFS))
	}

	logger := observability.NewJSONLogger(os.Stderr)
	eng := buildWorkerEngine(bootCfg, hwInspector, runtimeFS, logger)
	ln := acquireSingletonControlListener(bootCfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	focusCh := make(chan struct{}, 2)
	srv := &http.Server{
		Handler:           workerhttp.NewHandler(eng, focusCh, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	shutdownDone := make(chan struct{})
	go shutdownOnCancel(ctx, eng, srv, logger, shutdownDone)
	printBanner(eng.MachineID(), bootCfg.ControlAddr)
	go runInitialScanAndAutoStart(ctx, eng, bootCfg.AutoStart, logger)

	if !bootCfg.Headless && !bootCfg.NoBrowser && bootCfg.HasDesktopDisplay {
		go serveControlHTTP(srv, ln)
		workergui.RunDesktopGUI(ctx, eng, focusCh)
		stop()
		<-shutdownDone
		return
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Worker singleton IPC server error: %v\n", err)
		stop()
		<-shutdownDone
		os.Exit(1)
	}
	stop()
	<-shutdownDone
}

func runListDevicesMode(bootCfg workerconfig.WorkerConfig, hw worker.HardwareInspector, runtimeFS worker.RuntimeFS) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runtimeDir := runtimeFS.FindExistingRuntime(bootCfg.DataDir, llamaBinaryName())
	if runtimeDir == "" {
		eng := buildWorkerEngine(bootCfg, hw, runtimeFS, observability.NewJSONLogger(os.Stderr))
		eng.RefreshNetworkBootstrap(ctx)
		var err error
		runtimeDir, err = eng.EnsureRuntime(ctx, eng.Snapshot().GatewayURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to prepare Vulkan runtime: %v\n", err)
			return 1
		}
	}
	devices, err := hw.ProbeGPUs(ctx, runtimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "HardwareProbeError: %v\n", err)
		return 1
	}
	for _, dev := range devices {
		fmt.Printf("%s: %s (%d MiB, %d MiB free)\n", dev.DeviceID, dev.Name, dev.VramTotalMiB, dev.VramFreeMiB)
	}
	return 0
}

func llamaBinaryName() string {
	if runtime.GOOS == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}

func acquireSingletonControlListener(cfg workerconfig.WorkerConfig) net.Listener {
	if running, pid := workerhttp.CheckAlreadyRunning(cfg.ControlAddr); running {
		fmt.Fprintf(os.Stderr, "ERROR: GPU Grid Worker is already running on this machine (PID %d). Only 1 Worker per physical host is allowed.\n", pid)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", cfg.ControlAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to bind Worker singleton lock on %s (%v). Only 1 Worker per physical host is allowed.\n", cfg.ControlAddr, err)
		os.Exit(1)
	}
	return ln
}

func shutdownOnCancel(ctx context.Context, eng *worker.Engine, srv *http.Server, logger *slog.Logger, done chan<- struct{}) {
	defer close(done)
	<-ctx.Done()
	eng.Stop("Shutting down Worker process...")
	if err := srv.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Warn("worker_ipc_close_failed", "error", err.Error())
	}
}

func printBanner(machineID, controlAddr string) {
	fmt.Printf("GPU Grid Worker (%s/%s) | MachineID: %s | IPC: http://%s/api/ping\n", runtime.GOOS, runtime.GOARCH, machineID, controlAddr)
}

func runInitialScanAndAutoStart(ctx context.Context, eng *worker.Engine, autoStart bool, logger *slog.Logger) {
	if ctx.Err() != nil {
		return
	}
	eng.RefreshNetworkBootstrap(ctx)
	snap := eng.Snapshot()
	if !autoStart && !snap.HasWorkerToken && eng.ResolveExistingRuntimeDir() == "" {
		return
	}
	if err := eng.ScanGPUs(ctx, snap.GatewayURL); err != nil {
		if ctx.Err() == nil {
			logger.Warn("worker_initial_gpu_scan_failed", "error", err.Error())
		}
		return
	}
	if ctx.Err() != nil || !autoStart {
		return
	}
	postSnap := eng.Snapshot()
	if !postSnap.Running && strings.TrimSpace(postSnap.WorkerToken) != "" {
		if err := eng.Start(worker.StartParams{}); err != nil {
			logger.Warn("worker_autostart_failed", "error", err.Error())
		}
	}
}

func serveControlHTTP(srv *http.Server, ln net.Listener) {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Worker singleton IPC server error: %v\n", err)
	}
}

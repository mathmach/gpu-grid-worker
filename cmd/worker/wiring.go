package main

import (
	"context"
	"log/slog"

	"github.com/mathmach/gpu-grid-worker/pkg/application/worker"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/cas"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/engines"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/idle"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/llama"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/tunnel"
	"github.com/mathmach/gpu-grid-worker/pkg/infrastructure/workerconfig"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type rpcSupervisorAdapter struct {
	inner *llama.RPCSupervisor
}

func (a rpcSupervisorAdapter) SpawnRPC(ctx context.Context, runtimeDir, cacheDir string, gpus []protocol.GpuInfo, port int) (worker.RunningRPCProcess, error) {
	return a.inner.SpawnRPCServer(ctx, runtimeDir, cacheDir, gpus, port)
}

type soloSupervisorAdapter struct {
	inner *engines.CompositeSoloSupervisor
}

func (a soloSupervisorAdapter) SupportedBackends(runtimeDir string) []string {
	return a.inner.SupportedBackends(runtimeDir)
}

func (a soloSupervisorAdapter) SetEnabledBackends(backends []string) {
	a.inner.SetEnabledBackends(backends)
}

func (a soloSupervisorAdapter) SpawnSolo(ctx context.Context, s worker.SoloEngineSpec) (worker.RunningRPCProcess, error) {
	var mmproj, vocoder string
	if len(s.ComponentPaths) > 0 {
		mmproj = s.ComponentPaths[string(nodecore.RoleMMProj)]
		vocoder = s.ComponentPaths[string(nodecore.RoleVocoder)]
	}
	return a.inner.SpawnSoloServer(ctx, llama.SoloLaunchSpec{
		ModelPath: s.ModelPath, MMProjPath: mmproj, VocoderPath: vocoder,
		Task: s.Task, TargetBackend: s.TargetBackend, RuntimeDir: s.RuntimeDir,
		ListenPort: s.ListenPort, ContextLength: s.ContextLength, ParallelSlots: s.ParallelSlots,
		PoolVRAMMiB: s.PoolVRAMMiB, TotalGPUs: s.TotalGPUs, RPCEndpoints: s.RPCEndpoints,
		TensorWeights: s.TensorWeights, GPUs: s.GPUs, ComponentPaths: s.ComponentPaths,
	})
}

type tunnelDialerAdapter struct {
	inner *tunnel.WSClientDialer
}

func (a tunnelDialerAdapter) Dial(ctx context.Context, wsURL string) (worker.TunnelSocket, error) {
	return a.inner.DialWorkerSocket(ctx, wsURL)
}

func buildWorkerEngine(bootCfg workerconfig.WorkerConfig, hw worker.HardwareInspector, runtimeFS worker.RuntimeFS, logger *slog.Logger) *worker.Engine {
	casAdapter := cas.NewWorkerCASAdapter(cas.NewRuntimeArchiveVerifier(), nil)
	gatewayCli := cas.NewGatewayClient()
	loopbackTransport := tunnel.NewLoopbackTransport()
	soloSup := engines.NewCompositeSoloSupervisor(engines.CompositeSoloConfig{
		DataDir: bootCfg.DataDir, RuntimeFS: runtimeFS, EnabledBackends: bootCfg.EnabledBackends,
		BaseEnv: workerconfig.BuildSubprocessEnv(nil, "", ""), BootTimeout: bootCfg.SoloEngineBootTimeout,
	})
	var bootBackends []string
	if bootCfg.EnabledBackendsExplicit {
		bootBackends = bootCfg.EnabledBackends
	}
	return worker.NewEngine(worker.Dependencies{
		Boot: worker.BootConfig{
			GatewayURL: bootCfg.GatewayURL, EmbeddedGatewayURL: bootCfg.EmbeddedGatewayURL,
			Token: bootCfg.Token, WorkerName: bootCfg.WorkerName,
			Devices: bootCfg.Devices, ModelMode: bootCfg.ModelMode, SelectedModels: bootCfg.SelectedModels,
			EnabledBackends: bootBackends, AllowInsecureWS: bootCfg.InsecureWS,
			CDNModelsBaseURL: bootCfg.CDNModelsBaseURL, ScheduleMode: bootCfg.ScheduleMode,
			IdleThresholdSec: bootCfg.IdleThresholdSec, DataDir: bootCfg.DataDir,
			DefaultLanguage: bootCfg.DefaultLanguage,
			ControlPort:     bootCfg.ControlPort, SoloEngineBootTimeout: bootCfg.SoloEngineBootTimeout,
		},
		Hardware: hw, Idle: idle.NewOSIdleDetector(),
		RPC: rpcSupervisorAdapter{inner: llama.NewRPCSupervisor()}, Solo: soloSupervisorAdapter{inner: soloSup},
		Tunnel: tunnelDialerAdapter{inner: tunnel.NewWSClientDialer()}, Archive: casAdapter,
		ModelCache: casAdapter, CacheInventory: casAdapter,
		ConfigStore:   workerconfig.NewWorkerConfigStore(bootCfg.DataDir, bootCfg.ConfigPath),
		GatewayClient: gatewayCli, Portal: gatewayCli, Browser: cas.NewOSBrowserLauncher(),
		RuntimeFS: runtimeFS, Loopback: loopbackTransport, LoopbackRelay: loopbackTransport, Logger: logger,
	})
}

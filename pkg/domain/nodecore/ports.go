package nodecore

import (
	"context"
	"io"
)

type PoolLaunchSpec struct {
	PoolID        string
	ModelPath     string
	MMProjPath    string
	VocoderPath   string
	Format        ModelFormat
	Task          ModelTask
	Backend       EngineBackend
	Mode          PoolMode
	ListenPort    int
	RPCEndpoints  []string
	Devices       []string
	TotalLayers   int
	TotalGPUs     int
	TensorWeights []int
	ParallelSlots int
	ContextLength int
	TargetVRAMMiB int
	Embeddings    bool
	Reranking     bool
	Components    []BundleComponent
	LDLibraryPath string
}

type RunningPoolProcess interface {
	Stop()
	Done() <-chan struct{}
	StderrTail() string
	MarkBootOutcome(success bool)
}

type ReadinessMarkableProcess interface {
	MarkReady()
}

type BoundPortProcess interface {
	BoundPort() int
}

type InferenceBackendFactory interface {
	SupportsFormat(format ModelFormat) bool
	IsReady() bool
	SpawnPool(ctx context.Context, spec PoolLaunchSpec) (RunningPoolProcess, error)
}

type InferenceEngineRegistry interface {
	RegisterBackend(backend EngineBackend, factory InferenceBackendFactory)
	FactoryForBackend(backend EngineBackend) (InferenceBackendFactory, bool)
	SupportsBackend(backend EngineBackend) bool
}

type LoopbackBridgeFactory interface {
	AllocatePort() (int, error)
	ListenLoopback(onConn func(io.ReadWriteCloser)) (io.Closer, int, error)
}

type StreamDemuxer interface {
	Register(streamID uint32, dst io.WriteCloser)
	DispatchFrame(rawFrame []byte) bool
	CloseStream(streamID uint32)
	CloseAll()
	HasStream(streamID uint32) bool
}

type OwnedStreamDemuxer interface {
	DispatchOwnedFrame(ownedFrame []byte) bool
}

type StreamAborter interface {
	AbortStream(streamID uint32)
}

type StreamDemuxerFactory func(onEvict func(streamID uint32, reason error)) StreamDemuxer

type HostEnvironmentPort interface {
	LANEndpoints(port int) []string
	WorkerBinariesAvailable(distDir string) (bool, bool)
}

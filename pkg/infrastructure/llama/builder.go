package llama

import (
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

type LlamaServerBuilder struct {
	modelPath      string
	mmprojPath     string
	vocoderPath    string
	mode           nodecore.PoolMode
	rpcEndpoints   []string
	rpcDevices     []string
	localDevices   []string
	tensorWeights  []int
	parallelSlots  int
	modelCtxLength int
	targetVRAMMiB  int
	host           string
	port           int
	contBatching   bool
	embeddings     bool
	reranking      bool
}

func NewLlamaServerBuilder() *LlamaServerBuilder {
	return &LlamaServerBuilder{
		host:          "127.0.0.1",
		parallelSlots: 1,
	}
}

func (b *LlamaServerBuilder) WithModel(path string) *LlamaServerBuilder {
	b.modelPath = strings.TrimSpace(path)
	return b
}

func (b *LlamaServerBuilder) WithMMProj(path string) *LlamaServerBuilder {
	b.mmprojPath = strings.TrimSpace(path)
	return b
}

func (b *LlamaServerBuilder) WithVocoder(path string) *LlamaServerBuilder {
	b.vocoderPath = strings.TrimSpace(path)
	return b
}

func (b *LlamaServerBuilder) WithEmbeddings(enabled bool) *LlamaServerBuilder {
	b.embeddings = enabled
	return b
}

func (b *LlamaServerBuilder) WithReranking(enabled bool) *LlamaServerBuilder {
	b.reranking = enabled
	return b
}

func (b *LlamaServerBuilder) WithMode(mode nodecore.PoolMode) *LlamaServerBuilder {
	b.mode = mode
	return b
}

func (b *LlamaServerBuilder) WithRPCEndpoints(endpoints []string) *LlamaServerBuilder {
	b.rpcEndpoints = cleanNonEmptyList(endpoints)
	return b
}

func (b *LlamaServerBuilder) WithRPCDevices(devices []string) *LlamaServerBuilder {
	b.rpcDevices = cleanNonEmptyList(devices)
	return b
}

func (b *LlamaServerBuilder) WithLocalDevices(devices []string) *LlamaServerBuilder {
	b.localDevices = cleanNonEmptyList(devices)
	return b
}

func (b *LlamaServerBuilder) WithTensorSplit(weights []int) *LlamaServerBuilder {
	b.tensorWeights = weights
	return b
}

func (b *LlamaServerBuilder) WithParallel(slots int) *LlamaServerBuilder {
	slots = max(1, slots)
	b.parallelSlots = slots
	b.contBatching = slots > 1
	return b
}

func (b *LlamaServerBuilder) WithContextWindow(modelCtxLength, targetVRAMMiB int) *LlamaServerBuilder {
	b.modelCtxLength = modelCtxLength
	b.targetVRAMMiB = targetVRAMMiB
	return b
}

func (b *LlamaServerBuilder) WithBind(host string, port int) *LlamaServerBuilder {
	if strings.TrimSpace(host) != "" {
		b.host = strings.TrimSpace(host)
	}
	b.port = port
	return b
}

func (b *LlamaServerBuilder) BuildArgs() ([]string, error) {
	if b.modelPath == "" {
		return nil, errors.New("empty model path")
	}
	if b.port <= 0 || b.port > 65535 {
		return nil, errors.New("invalid local port for llama-server")
	}

	if err := validateTensorWeights(b.tensorWeights); err != nil {
		return nil, err
	}
	deviceArgs, err := b.buildTransportAndDeviceArgs()
	if err != nil {
		return nil, err
	}

	slots := min(max(1, b.parallelSlots), 256)
	ctxSize := resolveContextWindowTokens(slots, b.modelCtxLength, b.targetVRAMMiB)

	args := make([]string, 0, 20)
	args = append(args, "-m", b.modelPath)
	args = append(args, deviceArgs...)
	args = append(args,
		"-ngl", "99",
		"--parallel", strconv.Itoa(slots),
		"-c", strconv.Itoa(ctxSize),
		"--host", b.host,
		"--port", strconv.Itoa(b.port),
	)
	if b.contBatching {
		args = append(args, "--cont-batching")
	}
	if splitCSV := formatTensorSplitCSV(b.tensorWeights); splitCSV != "" {
		args = append(args, "--tensor-split", splitCSV)
	}
	return b.appendMultimodalFeatureArgs(args), nil
}

func (b *LlamaServerBuilder) appendMultimodalFeatureArgs(args []string) []string {
	if b.mmprojPath != "" {
		args = append(args, "--mmproj", b.mmprojPath)
	}
	if b.vocoderPath != "" {
		args = append(args, "--model-vocoder", b.vocoderPath, "--tts-use-guide-tokens")
	}
	if b.embeddings {
		args = append(args, "--embedding")
	}
	if b.reranking {
		args = append(args, "--reranking")
	}
	return args
}

func (b *LlamaServerBuilder) buildTransportAndDeviceArgs() ([]string, error) {
	if len(b.rpcEndpoints) > 0 {
		if err := validateRPCEndpoints(b.rpcEndpoints); err != nil {
			return nil, err
		}
		if len(b.rpcDevices) == 0 {
			return nil, errors.New("missing RPC devices")
		}
		if len(b.tensorWeights) > 1 && len(b.tensorWeights) != len(b.rpcDevices) {
			return nil, errors.New("tensor-split weight count must match RPC device count")
		}
		return []string{
			"--rpc", strings.Join(b.rpcEndpoints, ","),
			"--device", strings.Join(b.rpcDevices, ","),
		}, nil
	}
	if b.mode == nodecore.PoolModeSolo || len(b.localDevices) > 0 {
		devs := b.localDevices
		if len(devs) == 0 {
			devs = b.rpcDevices
		}
		if len(devs) > 0 {
			return []string{"--device", strings.Join(devs, ",")}, nil
		}
		return nil, nil
	}
	return nil, errors.New("missing RPC endpoints or devices")
}

func validateRPCEndpoints(endpoints []string) error {
	for _, ep := range endpoints {
		host, portStr, err := net.SplitHostPort(strings.TrimSpace(ep))
		if err != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n,") {
			return errors.New("invalid RPC endpoint address")
		}
		port, convErr := strconv.Atoi(portStr)
		if convErr != nil || port <= 0 || port > 65535 {
			return errors.New("invalid RPC endpoint port")
		}
	}
	return nil
}

func validateTensorWeights(weights []int) error {
	for _, wt := range weights {
		if wt <= 0 {
			return errors.New("tensor-split weights must be positive")
		}
	}
	return nil
}

func resolveContextWindowTokens(parallelSlots, modelCtxLength, targetVRAMMiB int) int {
	slots := min(max(1, parallelSlots), 256)
	if modelCtxLength <= 0 {
		return min(2048*slots, 2_097_152)
	}
	perSlotCap := perSlotContextCeiling(targetVRAMMiB)
	perSlot := min(modelCtxLength, perSlotCap)
	if modelCtxLength >= 512 {
		perSlot = max(512, perSlot)
	} else {
		perSlot = max(256, perSlot)
	}
	return min(perSlot*slots, 2_097_152)
}

func perSlotContextCeiling(targetVRAMMiB int) int {
	switch {
	case targetVRAMMiB <= 0:
		return 8192
	case targetVRAMMiB < 4096:
		return 2048
	case targetVRAMMiB < 8192:
		return 4096
	case targetVRAMMiB < 16384:
		return 8192
	case targetVRAMMiB < 32768:
		return 16384
	default:
		return 32768
	}
}

func formatTensorSplitCSV(weights []int) string {
	if len(weights) <= 1 {
		return ""
	}
	parts := make([]string, len(weights))
	for i, wt := range weights {
		parts[i] = strconv.Itoa(wt)
	}
	return strings.Join(parts, ",")
}

func cleanNonEmptyList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

type rpcServerBuilder struct {
	host      string
	port      int
	deviceIDs []string
	enableCSC bool
}

func newRPCServerBuilder() *rpcServerBuilder {
	return &rpcServerBuilder{
		host:      "127.0.0.1",
		enableCSC: true,
	}
}

func (b *rpcServerBuilder) withPort(port int) *rpcServerBuilder {
	b.port = port
	return b
}

func (b *rpcServerBuilder) withGPUs(gpus []protocol.GpuInfo) *rpcServerBuilder {
	ids := make([]string, 0, len(gpus))
	for _, g := range gpus {
		if id := strings.TrimSpace(g.DeviceID); id != "" {
			ids = append(ids, id)
		}
	}
	b.deviceIDs = ids
	return b
}

func (b *rpcServerBuilder) buildArgs() ([]string, error) {
	if b.port <= 0 || b.port > 65535 {
		return nil, errors.New("invalid local port for ggml-rpc-server")
	}
	if len(b.deviceIDs) == 0 {
		return nil, derr.New(derr.CodeNoVulkanGPU, 422, "no Vulkan-compatible GPU selected", nil)
	}
	args := []string{"-H", b.host, "-p", strconv.Itoa(b.port)}
	if b.enableCSC {
		args = append(args, "-c")
	}
	args = append(args, "-d", strings.Join(b.deviceIDs, ","))
	return args, nil
}

package nodecore

import (
	"encoding/json"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

type ModelFormat string

const (
	FormatGGUF        ModelFormat = "gguf"
	FormatSafetensors ModelFormat = "safetensors"
	FormatONNX        ModelFormat = "onnx"
	FormatPyTorch     ModelFormat = "pytorch"
	FormatWeightsBin  ModelFormat = "bin"
	FormatDirectory   ModelFormat = "directory"
	FormatGeneric     ModelFormat = "model"
)

type PoolMode string

const (
	PoolModeSolo PoolMode = "SOLO"
	PoolModeMesh PoolMode = "MESH"
)

type EngineBackend string

const (
	BackendLlamaLocal       EngineBackend = "llama_local"
	BackendLlamaRPC         EngineBackend = "llama_rpc"
	BackendSafetensors      EngineBackend = "safetensors_sidecar"
	BackendONNXRuntime      EngineBackend = "onnx_genai"
	BackendDiffusionLocal   EngineBackend = "diffusion_local"
	BackendDiffusionSidecar EngineBackend = "diffusion_sidecar"
	BackendWhisperLocal     EngineBackend = "whisper_local"
	BackendAudioLocal       EngineBackend = "audio_local"
	BackendOmniLocal        EngineBackend = "omni_local"

	backendPyTorch  EngineBackend = "pytorch"
	backendExternal EngineBackend = "external"
)

var engineBackendAliases = map[string]EngineBackend{
	"llama_local":         BackendLlamaLocal,
	"llama-local":         BackendLlamaLocal,
	"llama_rpc":           BackendLlamaRPC,
	"llama-rpc":           BackendLlamaRPC,
	"safetensors_sidecar": BackendSafetensors,
	"safetensors-sidecar": BackendSafetensors,
	"safetensors":         BackendSafetensors,
	"onnx_genai":          BackendONNXRuntime,
	"onnx-genai":          BackendONNXRuntime,
	"onnx-runtime":        BackendONNXRuntime,
	"onnx_runtime":        BackendONNXRuntime,
	"onnx":                BackendONNXRuntime,
	"diffusion_local":     BackendDiffusionLocal,
	"diffusion-local":     BackendDiffusionLocal,
	"diffusion_cpp":       BackendDiffusionLocal,
	"diffusion-cpp":       BackendDiffusionLocal,
	"sd_local":            BackendDiffusionLocal,
	"sd-server":           BackendDiffusionLocal,
	"diffusion_sidecar":   BackendDiffusionSidecar,
	"diffusion-sidecar":   BackendDiffusionSidecar,
	"whisper_local":       BackendWhisperLocal,
	"whisper-local":       BackendWhisperLocal,
	"whisper_cpp":         BackendWhisperLocal,
	"whisper-cpp":         BackendWhisperLocal,
	"whisper":             BackendWhisperLocal,
	"audio_local":         BackendAudioLocal,
	"audio-local":         BackendAudioLocal,
	"omni_local":          BackendOmniLocal,
	"omni-local":          BackendOmniLocal,
}

func ParseEngineBackend(raw string) (EngineBackend, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if backend, ok := engineBackendAliases[key]; ok {
		return backend, nil
	}
	return "", derr.New(derr.CodeValidation, 400, "unsupported engine backend", nil)
}

func (b EngineBackend) String() string               { return string(b) }
func (b EngineBackend) IsZero() bool                 { return strings.TrimSpace(string(b)) == "" }
func (b EngineBackend) MarshalJSON() ([]byte, error) { return json.Marshal(string(b)) }

func (b *EngineBackend) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return derr.Wrap(derr.CodeValidation, 400, "invalid engineBackend JSON string", err)
	}
	parsed, err := ParseEngineBackend(raw)
	if err != nil {
		return err
	}
	*b = parsed
	return nil
}

func RequiredBackendForModel(format ModelFormat, mode PoolMode) EngineBackend {
	switch format {
	case FormatGGUF:
		if strings.EqualFold(strings.TrimSpace(string(mode)), string(PoolModeSolo)) {
			return BackendLlamaLocal
		}
		return BackendLlamaRPC
	case FormatSafetensors, FormatDirectory:
		return BackendSafetensors
	case FormatONNX:
		return BackendONNXRuntime
	case FormatPyTorch, FormatWeightsBin:
		return backendPyTorch
	default:
		return backendExternal
	}
}

func RequiredBackendForTask(task ModelTask, format ModelFormat, mode PoolMode) EngineBackend {
	parsed, err := ParseModelTask(string(task))
	if err != nil {
		return backendExternal
	}
	switch parsed {
	case TaskTextToImage, TaskImageToImage, TaskTextToVideo, TaskImageToVideo:
		if format == FormatPyTorch {
			return BackendDiffusionSidecar
		}
		return BackendDiffusionLocal
	case TaskSpeechToText:
		return BackendWhisperLocal
	case TaskTextToSpeech:
		if format == FormatONNX {
			return BackendONNXRuntime
		}
		return BackendAudioLocal
	case TaskRealtimeOmni:
		return BackendOmniLocal
	case TaskEmbeddings, TaskRerank:
		return requiredEmbeddingBackend(format)
	default:
		return RequiredBackendForModel(format, mode)
	}
}

func requiredEmbeddingBackend(format ModelFormat) EngineBackend {
	switch format {
	case FormatONNX:
		return BackendONNXRuntime
	case FormatSafetensors, FormatDirectory:
		return BackendSafetensors
	default:
		return BackendLlamaLocal
	}
}

func WorkerSupportsBackend(supported []EngineBackend, target EngineBackend) bool {
	normTarget, err := ParseEngineBackend(string(target))
	if err != nil || len(supported) == 0 {
		return false
	}
	for _, item := range supported {
		if normItem, itemErr := ParseEngineBackend(string(item)); itemErr == nil && normItem == normTarget {
			return true
		}
	}
	return false
}

const RemoteManifestSuffix = ".manifest.json"

var recognizedModelExtensions = []string{
	RemoteManifestSuffix,
	".safetensors",
	".gguf",
	".onnx",
	".pth",
	".bin",
	".pt",
}

var transientDownloadSuffixes = []string{
	".part",
	".tmp",
	".partial",
	".crdownload",
	".download",
}

func StripModelExtension(raw string) string {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	for _, ext := range recognizedModelExtensions {
		if strings.HasSuffix(lower, ext) && len(trimmed) > len(ext) {
			return trimmed[:len(trimmed)-len(ext)]
		}
	}
	return trimmed
}

func IsSupportedModelFileName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || strings.HasPrefix(trimmed, ".") {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, transient := range transientDownloadSuffixes {
		if strings.HasSuffix(lower, transient) {
			return false
		}
	}
	for _, ext := range recognizedModelExtensions {
		if strings.HasSuffix(lower, ext) && len(lower) > len(ext) {
			return true
		}
	}
	return false
}

func IsRemoteManifestFileName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || strings.HasPrefix(trimmed, ".") {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, transient := range transientDownloadSuffixes {
		if strings.HasSuffix(lower, transient) {
			return false
		}
	}
	return strings.HasSuffix(lower, RemoteManifestSuffix) && len(lower) > len(RemoteManifestSuffix)
}

func DetectModelFormat(name string, isDir bool) ModelFormat {
	if isDir {
		return FormatDirectory
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasSuffix(lower, ".gguf"):
		return FormatGGUF
	case strings.HasSuffix(lower, ".safetensors"):
		return FormatSafetensors
	case strings.HasSuffix(lower, ".onnx"):
		return FormatONNX
	case strings.HasSuffix(lower, ".pt") || strings.HasSuffix(lower, ".pth"):
		return FormatPyTorch
	case strings.HasSuffix(lower, ".bin"):
		return FormatWeightsBin
	default:
		return FormatGeneric
	}
}

func DefaultBackendForFormat(format ModelFormat) EngineBackend {
	return RequiredBackendForModel(format, PoolModeMesh)
}

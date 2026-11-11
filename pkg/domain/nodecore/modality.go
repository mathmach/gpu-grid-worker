package nodecore

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

type ModelTask string

const (
	TaskTextGeneration ModelTask = "text_generation"
	TaskVisionChat     ModelTask = "vision_chat"
	TaskTextToImage    ModelTask = "text_to_image"
	TaskImageToImage   ModelTask = "image_to_image"
	TaskTextToVideo    ModelTask = "text_to_video"
	TaskImageToVideo   ModelTask = "image_to_video"
	TaskSpeechToText   ModelTask = "speech_to_text"
	TaskTextToSpeech   ModelTask = "text_to_speech"
	TaskEmbeddings     ModelTask = "embeddings"
	TaskRerank         ModelTask = "rerank"
	TaskRealtimeOmni   ModelTask = "realtime_omni"
)

var modelTaskAliases = map[string]ModelTask{
	"":                TaskTextGeneration,
	"text":            TaskTextGeneration,
	"chat":            TaskTextGeneration,
	"text_generation": TaskTextGeneration,
	"text-generation": TaskTextGeneration,
	"vision":          TaskVisionChat,
	"vlm":             TaskVisionChat,
	"vision_chat":     TaskVisionChat,
	"vision-chat":     TaskVisionChat,
	"t2i":             TaskTextToImage,
	"image":           TaskTextToImage,
	"image_gen":       TaskTextToImage,
	"text_to_image":   TaskTextToImage,
	"text-to-image":   TaskTextToImage,
	"i2i":             TaskImageToImage,
	"image_edit":      TaskImageToImage,
	"image_to_image":  TaskImageToImage,
	"image-to-image":  TaskImageToImage,
	"t2v":             TaskTextToVideo,
	"video":           TaskTextToVideo,
	"video_gen":       TaskTextToVideo,
	"text_to_video":   TaskTextToVideo,
	"text-to-video":   TaskTextToVideo,
	"i2v":             TaskImageToVideo,
	"image_to_video":  TaskImageToVideo,
	"image-to-video":  TaskImageToVideo,
	"asr":             TaskSpeechToText,
	"stt":             TaskSpeechToText,
	"audio_asr":       TaskSpeechToText,
	"speech_to_text":  TaskSpeechToText,
	"speech-to-text":  TaskSpeechToText,
	"tts":             TaskTextToSpeech,
	"speech":          TaskTextToSpeech,
	"audio_tts":       TaskTextToSpeech,
	"text_to_speech":  TaskTextToSpeech,
	"text-to-speech":  TaskTextToSpeech,
	"embedding":       TaskEmbeddings,
	"embeddings":      TaskEmbeddings,
	"rerank":          TaskRerank,
	"reranker":        TaskRerank,
	"omni":            TaskRealtimeOmni,
	"realtime":        TaskRealtimeOmni,
	"omni_realtime":   TaskRealtimeOmni,
	"realtime_omni":   TaskRealtimeOmni,
	"realtime-omni":   TaskRealtimeOmni,
}

func ParseModelTask(raw string) (ModelTask, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if task, ok := modelTaskAliases[key]; ok {
		return task, nil
	}
	return "", derr.New(derr.CodeValidation, 400, "unsupported model task", map[string]any{"task": raw})
}

func AllowsMeshTopology(task ModelTask, format ModelFormat) bool {
	if format != "" && format != FormatGGUF {
		return false
	}
	return task == "" || task == TaskTextGeneration || task == TaskVisionChat
}

type ComponentRole string

const (
	RolePrimary        ComponentRole = "primary"
	RoleMMProj         ComponentRole = "mmproj"
	RoleDiffusionModel ComponentRole = "diffusion_model"
	RoleTextEncoder    ComponentRole = "text_encoder"
	RoleClipL          ComponentRole = "clip_l"
	RoleClipG          ComponentRole = "clip_g"
	RoleT5XXL          ComponentRole = "t5xxl"
	RoleVAE            ComponentRole = "vae"
	RoleClipVision     ComponentRole = "clip_vision"
	RoleVocoder        ComponentRole = "vocoder"
)

type BundleComponent struct {
	Role        ComponentRole `json:"role"`
	FileName    string        `json:"fileName"`
	FilePath    string        `json:"-"`
	SHA256      string        `json:"sha256,omitempty"`
	DownloadURL string        `json:"downloadUrl,omitempty"`
	SizeBytes   int64         `json:"sizeBytes,omitempty"`
}

type ModelBundleManifest struct {
	BundleID   string            `json:"bundleId"`
	Task       ModelTask         `json:"task"`
	Components []BundleComponent `json:"components"`
}

func EffectiveWeightMiB(fileSizeMiB int, components []BundleComponent) int {
	var sumBytes int64
	for _, c := range components {
		if c.SizeBytes > 0 {
			sumBytes += c.SizeBytes
		}
	}
	compMiB := 0
	if sumBytes > 0 {
		compMiB = int((sumBytes + (1 << 20) - 1) >> 20)
	}
	return max(fileSizeMiB, compMiB)
}

func (m ModelBundleManifest) ComputeManifestDigest() string {
	if len(m.Components) == 0 {
		return ""
	}
	parts := make([]string, len(m.Components))
	for i, c := range m.Components {
		parts[i] = strings.ToLower(strings.TrimSpace(string(c.Role))) + ":" +
			strings.ToLower(strings.TrimSpace(c.SHA256)) + ":" +
			strings.TrimSpace(c.FileName) + ":" +
			strconv.FormatInt(c.SizeBytes, 10)
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func InferComponentRoleFromName(fileName string) ComponentRole {
	lower := strings.ToLower(strings.TrimSpace(fileName))
	switch {
	case strings.Contains(lower, "mmproj"):
		return RoleMMProj
	case strings.Contains(lower, "clip_vision") || strings.Contains(lower, "clip-vision"):
		return RoleClipVision
	case strings.Contains(lower, "umt5") || strings.Contains(lower, "t5xxl") || strings.Contains(lower, "t5-xxl"):
		return RoleT5XXL
	case strings.Contains(lower, "clip_l") || strings.Contains(lower, "clip-l"):
		return RoleClipL
	case strings.Contains(lower, "clip_g") || strings.Contains(lower, "clip-g"):
		return RoleClipG
	case strings.Contains(lower, "vae"):
		return RoleVAE
	case strings.Contains(lower, "vocoder") || strings.Contains(lower, "wavtokenizer"):
		return RoleVocoder
	case isDiffusionWeightName(lower):
		return RoleDiffusionModel
	default:
		return RolePrimary
	}
}

func isDiffusionWeightName(lower string) bool {
	keywords := []string{"wan2.1", "wan2.2", "wan-", "wan_", "flux", "sdxl", "sd3", "ltx-video", "ltx_video", "qwen-image", "unet", "dit"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func InferModelTaskFromHints(nameOrID, architecture string, roles []ComponentRole) ModelTask {
	combined := strings.ToLower(strings.TrimSpace(nameOrID) + " " + strings.TrimSpace(architecture))
	if task, matched := matchMediaTaskKeywords(combined); matched {
		return task
	}
	if slices.Contains(roles, RoleDiffusionModel) || slices.Contains(roles, RoleVAE) {
		if strings.Contains(combined, "i2v") {
			return TaskImageToVideo
		}
		if strings.Contains(combined, "wan") || strings.Contains(combined, "video") || strings.Contains(combined, "t2v") || strings.Contains(combined, "ltx") {
			return TaskTextToVideo
		}
		return TaskTextToImage
	}
	if slices.Contains(roles, RoleVocoder) {
		return TaskTextToSpeech
	}
	if slices.Contains(roles, RoleMMProj) {
		return TaskVisionChat
	}
	return TaskTextGeneration
}

var mediaTaskKeywordRules = []struct {
	keyword string
	task    ModelTask
}{
	{"whisper", TaskSpeechToText},
	{"outetts", TaskTextToSpeech},
	{"orpheus", TaskTextToSpeech},
	{"kokoro", TaskTextToSpeech},
	{"-tts", TaskTextToSpeech},
	{"rerank", TaskRerank},
	{"embed", TaskEmbeddings},
	{"bge-", TaskEmbeddings},
	{"nomic-embed", TaskEmbeddings},
	{"omni", TaskRealtimeOmni},
	{"i2v", TaskImageToVideo},
	{"image-to-video", TaskImageToVideo},
	{"wan2", TaskTextToVideo},
	{"wan-", TaskTextToVideo},
	{"t2v", TaskTextToVideo},
	{"ltx-video", TaskTextToVideo},
	{"flux", TaskTextToImage},
	{"sdxl", TaskTextToImage},
	{"sd3", TaskTextToImage},
	{"qwen-image", TaskTextToImage},
	{"stable-diffusion", TaskTextToImage},
	{"-vl", TaskVisionChat},
	{"pixtral", TaskVisionChat},
	{"llava", TaskVisionChat},
	{"vision", TaskVisionChat},
}

func matchMediaTaskKeywords(combined string) (ModelTask, bool) {
	for _, rule := range mediaTaskKeywordRules {
		if strings.Contains(combined, rule.keyword) {
			return rule.task, true
		}
	}
	return "", false
}

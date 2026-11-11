package protocol

import (
	"context"
	"encoding/binary"
	"net/url"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/trace"
)

func EncodeStreamFrame(streamID uint32, payload []byte) []byte {
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], streamID)
	copy(frame[4:], payload)
	return frame
}

func DecodeStreamFrame(frame []byte) (uint32, []byte, bool) {
	if len(frame) < 4 {
		return 0, nil, false
	}
	return binary.BigEndian.Uint32(frame[:4]), frame[4:], true
}

func InjectTraceParent(ctx context.Context, msg *ControlMsg) {
	if msg == nil {
		return
	}
	if norm, ok := trace.NormalizeTraceParent(msg.TraceParent); ok {
		msg.TraceParent = norm
		return
	}
	if tp, ok := trace.LookupTraceParent(ctx); ok {
		msg.TraceParent = tp
		return
	}
	msg.TraceParent = trace.NewTraceParent()
}

func ExtractTraceParent(ctx context.Context, msg ControlMsg) context.Context {
	return trace.WithTraceParent(ctx, msg.TraceParent)
}

func BuildModelDownloadURL(baseURL, fileName string) string {
	base := normalizeDownloadBasePath(baseURL)
	escapedFile := escapeDownloadRelativePath(fileName)
	if base == "" || escapedFile == "" {
		return ""
	}
	return base + "/" + escapedFile
}

func BuildComponentDownloadURL(baseURL, modelID, fileName string) string {
	base := normalizeDownloadBasePath(baseURL)
	cleanModel := strings.TrimSpace(modelID)
	escapedFile := escapeDownloadRelativePath(fileName)
	if base == "" || cleanModel == "" || escapedFile == "" {
		return ""
	}
	return base + "/" + url.PathEscape(cleanModel) + "/" + escapedFile
}

func normalizeDownloadBasePath(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(base), "/models") {
		return base
	}
	return base + "/models"
}

func escapeDownloadRelativePath(rel string) string {
	clean := strings.TrimPrefix(strings.TrimSpace(rel), "/")
	if clean == "" {
		return ""
	}
	parts := strings.Split(clean, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

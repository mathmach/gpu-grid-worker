package protocol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/trace"
	"github.com/mathmach/gpu-grid-worker/pkg/protocol"
)

func TestControlConstantsAndRemovedControlMsgFields(t *testing.T) {
	controlTypes := []string{
		protocol.ControlHello,
		protocol.ControlRejected,
		protocol.ControlTunnelOpen,
		protocol.ControlTunnelClose,
		protocol.ControlState,
		protocol.ControlUpdateModels,
		protocol.ControlPauseState,
		protocol.ControlJobCredit,
		protocol.ControlConfigureSoloEngine,
		protocol.ControlSoloEngineReady,
		protocol.ControlSoloEngineError,
		protocol.ControlStopSoloEngine,
		protocol.ControlMeshRelayOpen,
		protocol.ControlEnsureMeshModel,
		protocol.ControlMeshPeerReady,
		protocol.ControlMeshPeerError,
		protocol.ControlStopMeshPeer,
		protocol.ControlCacheInventory,
	}
	if len(controlTypes) != 18 {
		t.Fatalf("expected 18 canonical ControlMsg types, got %d", len(controlTypes))
	}
	seen := make(map[string]bool, len(controlTypes))
	for _, ct := range controlTypes {
		if strings.TrimSpace(ct) == "" || seen[ct] {
			t.Fatalf("duplicate or empty ControlMsg constant %q", ct)
		}
		seen[ct] = true
	}

	msg := protocol.ControlMsg{
		Type:   protocol.ControlEnsureMeshModel,
		PoolID: "pool-unified-1",
		Reason: "ok",
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal ControlMsg: %v", err)
	}
	encoded := string(raw)
	for _, removedKey := range []string{"\"arch\"", "\"clusterState\"", "\"deltaPackets\"", "\"poolIdRef\""} {
		if strings.Contains(encoded, removedKey) {
			t.Fatalf("unexpected legacy JSON field %s in %s", removedKey, encoded)
		}
	}
	var roundTrip protocol.ControlMsg
	if err := json.Unmarshal(raw, &roundTrip); err != nil || roundTrip.PoolID != "pool-unified-1" || roundTrip.Type != protocol.ControlEnsureMeshModel {
		t.Fatalf("ControlMsg JSON round-trip mismatch: got %+v err=%v", roundTrip, err)
	}
}

func TestStreamFrameEncodeDecodeAndInitiatorBit(t *testing.T) {
	streamID := protocol.WorkerInitiatedStreamMask | 42
	payload := []byte("POST /v1/chat/completions HTTP/1.1\r\n\r\n")
	frame := protocol.EncodeStreamFrame(streamID, payload)
	if len(frame) != 4+len(payload) {
		t.Fatalf("unexpected encoded frame length %d", len(frame))
	}
	gotID, gotPayload, ok := protocol.DecodeStreamFrame(frame)
	if !ok || gotID != streamID || !bytes.Equal(gotPayload, payload) {
		t.Fatalf("DecodeStreamFrame mismatch: ok=%v id=%x payload=%q", ok, gotID, gotPayload)
	}
	if _, _, shortOK := protocol.DecodeStreamFrame([]byte{1, 2, 3}); shortOK {
		t.Fatal("expected frame shorter than 4 bytes to fail DecodeStreamFrame")
	}
}

func TestTraceParentInjectionAndExtraction(t *testing.T) {
	protocol.InjectTraceParent(context.Background(), nil)

	tp := trace.NewTraceParent()
	ctx := trace.WithTraceParent(context.Background(), tp)
	var msg protocol.ControlMsg
	protocol.InjectTraceParent(ctx, &msg)
	if msg.TraceParent != tp {
		t.Fatalf("expected InjectTraceParent to propagate context traceparent %q, got %q", tp, msg.TraceParent)
	}

	explicit := trace.NewTraceParent()
	msgWithExplicit := protocol.ControlMsg{TraceParent: strings.ToUpper(explicit)}
	protocol.InjectTraceParent(ctx, &msgWithExplicit)
	if msgWithExplicit.TraceParent != explicit {
		t.Fatalf("expected InjectTraceParent to normalize explicit message traceparent %q, got %q", explicit, msgWithExplicit.TraceParent)
	}

	extractedCtx := protocol.ExtractTraceParent(context.Background(), msgWithExplicit)
	if got := trace.TraceParentFromContext(extractedCtx); got != explicit {
		t.Fatalf("expected ExtractTraceParent=%q, got %q", explicit, got)
	}
}

func TestBuildModelAndComponentDownloadURLs(t *testing.T) {
	if got := protocol.BuildModelDownloadURL("https://grid.example.com/download/", "Qwen 2.5.gguf"); got != "https://grid.example.com/download/models/Qwen%202.5.gguf" {
		t.Fatalf("unexpected BuildModelDownloadURL: %q", got)
	}
	if got := protocol.BuildModelDownloadURL("https://grid.example.com/download/models", "/sub/wan.gguf"); got != "https://grid.example.com/download/models/sub/wan.gguf" {
		t.Fatalf("unexpected BuildModelDownloadURL with /models suffix: %q", got)
	}
	if got := protocol.BuildComponentDownloadURL("/download", "wan2.1-t2v", "vae/wan_vae.safetensors"); got != "/download/models/wan2.1-t2v/vae/wan_vae.safetensors" {
		t.Fatalf("unexpected BuildComponentDownloadURL: %q", got)
	}
	if got := protocol.BuildModelDownloadURL("", "a.gguf"); got != "" {
		t.Fatalf("expected empty baseURL to return empty URL, got %q", got)
	}
	if got := protocol.BuildComponentDownloadURL("/download", "", "a.gguf"); got != "" {
		t.Fatalf("expected empty modelID to return empty component URL, got %q", got)
	}
}

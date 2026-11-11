package nodecore_test

import (
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

func TestDisplayCurrencyAndIdentityValidation(t *testing.T) {
	brl, err := nodecore.ParseDisplayCurrency("brl")
	if err != nil || brl != nodecore.CurrencyBRL || brl.String() != "BRL" || brl.IsZero() {
		t.Fatalf("expected BRL display currency, got %q err=%v", brl, err)
	}
	usd, err := nodecore.ParseDisplayCurrency("USD")
	if err != nil || usd != nodecore.CurrencyUSD || usd.String() != "USD" {
		t.Fatalf("expected USD display currency, got %q err=%v", usd, err)
	}
	if _, badErr := nodecore.ParseDisplayCurrency("EUR"); badErr == nil {
		t.Fatal("expected EUR display currency to fail validation")
	}

	computed := nodecore.ComputeMachineID("host-alpha", "vulkan-0")
	mid, err := nodecore.ParseMachineID(computed.String())
	if err != nil || mid != computed || len(nodecore.ShortMachineKey(mid.String())) != 12 {
		t.Fatalf("expected valid MachineID, got %q err=%v", mid, err)
	}
	if _, badMachine := nodecore.ParseMachineID("bad-machine"); badMachine == nil {
		t.Fatal("expected invalid MachineID rejected")
	}

	tier, err := nodecore.ParseTrustTier("verified_tls")
	if err != nil || tier != nodecore.TrustTierVerifiedTLS {
		t.Fatalf("expected VERIFIED_TLS trust tier, got %q err=%v", tier, err)
	}
	if gotMin := nodecore.MinTrustTier(nodecore.DeriveTrustTier(true, true), nodecore.TrustTierCommunity); gotMin != nodecore.TrustTierCommunity {
		t.Fatalf("expected MinTrustTier=COMMUNITY, got %q", gotMin)
	}

	digest, err := nodecore.ParseSHA256Digest("sha256:" + strings.Repeat("A", 64))
	if err != nil || digest.IsZero() || digest.String() != strings.Repeat("a", 64) {
		t.Fatalf("expected normalized lowercase SHA256Digest, got %q err=%v", digest, err)
	}
}

func TestWorkerSessionFSMFormatsAndDownloadURLGuard(t *testing.T) {
	state := nodecore.WorkerConnectedUnauth
	idle, err := state.TransitionTo(nodecore.WorkerIdle)
	if err != nil || idle != nodecore.WorkerIdle {
		t.Fatalf("expected CONNECTED_UNAUTH -> IDLE, got %s err=%v", idle, err)
	}
	inPool, err := idle.TransitionTo(nodecore.WorkerInPool)
	if err != nil || inPool != nodecore.WorkerInPool {
		t.Fatalf("expected IDLE -> IN_POOL, got %s err=%v", inPool, err)
	}
	if _, badErr := nodecore.WorkerClosed.TransitionTo(nodecore.WorkerPauseDraining); badErr == nil {
		t.Fatal("expected CLOSED -> PAUSE_DRAINING transition to fail")
	}

	fmtType := nodecore.DetectModelFormat("qwen2.5-7b-q4_k_m.gguf", false)
	if fmtType != nodecore.FormatGGUF || !nodecore.AllowsMeshTopology(nodecore.TaskTextGeneration, fmtType) {
		t.Fatalf("expected GGUF format to support MESH topology, got %s", fmtType)
	}
	fmtST := nodecore.DetectModelFormat("flux1-dev.safetensors", false)
	if fmtST != nodecore.FormatSafetensors || nodecore.AllowsMeshTopology(nodecore.TaskTextToImage, fmtST) {
		t.Fatalf("expected safetensors format to disallow MESH topology, got %s", fmtST)
	}

	sameOrigin, err := nodecore.ValidateWorkerDownloadURL("/download/models/qwen.gguf", "https://grid.example.com", false)
	if err != nil || sameOrigin != "https://grid.example.com/download/models/qwen.gguf" {
		t.Fatalf("expected relative download URL resolved against gateway, got %q err=%v", sameOrigin, err)
	}
	if _, untrustedErr := nodecore.ValidateWorkerDownloadURL("http://169.254.169.254/latest/meta-data", "https://grid.example.com", false); untrustedErr == nil {
		t.Fatal("expected metadata SSRF target rejected by ValidateWorkerDownloadURL")
	}
}

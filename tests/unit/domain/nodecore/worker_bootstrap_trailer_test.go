package nodecore_test

import (
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

func TestWorkerBootstrapTrailerRoundTripAndIntegrityVerification(t *testing.T) {
	fakeBinaryPrefix := []byte("MZ\x90\x00FAKE_PE_OR_ELF_EXECUTABLE_PAYLOAD_BYTES_0123456789")
	trailer, err := nodecore.EncodeWorkerBootstrapTrailer("  https://grid.vps-example.com/ ")
	if err != nil {
		t.Fatalf("EncodeWorkerBootstrapTrailer failed: %v", err)
	}
	if len(trailer) == 0 || len(trailer) > nodecore.MaxWorkerBootstrapTailBytes {
		t.Fatalf("unexpected trailer length: %d", len(trailer))
	}

	fullExe := append(append([]byte(nil), fakeBinaryPrefix...), trailer...)
	gotGW, ok := nodecore.DecodeWorkerBootstrapTrailer(fullExe)
	if !ok || gotGW != "https://grid.vps-example.com" {
		t.Fatalf("expected https://grid.vps-example.com, got ok=%v gw=%q", ok, gotGW)
	}

	if _, plainOK := nodecore.DecodeWorkerBootstrapTrailer(fakeBinaryPrefix); plainOK {
		t.Fatal("expected plain binary without GGWKBOOT trailer to return false")
	}

	corrupted := append([]byte(nil), fullExe...)
	corrupted[len(fakeBinaryPrefix)+5] ^= 0xFF
	if _, badOK := nodecore.DecodeWorkerBootstrapTrailer(corrupted); badOK {
		t.Fatal("expected SHA-256 digest mismatch on corrupted trailer to return false")
	}

	if _, emptyErr := nodecore.EncodeWorkerBootstrapTrailer("   "); emptyErr == nil {
		t.Fatal("expected empty gateway URL to fail trailer encoding")
	}
	if _, subErr := nodecore.EncodeWorkerBootstrapTrailer("https://grid.vps-example.com/subpath"); subErr == nil {
		t.Fatal("expected subpath gateway URL to fail trailer encoding")
	}
}

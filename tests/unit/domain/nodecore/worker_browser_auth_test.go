package nodecore_test

import (
	"strings"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

func TestGenerateAndVerifyPKCES256RoundTrip(t *testing.T) {
	verifier, challenge, err := nodecore.GeneratePKCEPair()
	if err != nil {
		t.Fatalf("GeneratePKCEPair failed: %v", err)
	}
	if len(verifier) < 43 || len(challenge) < 43 {
		t.Fatalf("expected >=43 char RFC 7636 verifier and challenge, got %d / %d", len(verifier), len(challenge))
	}
	if verifier == challenge {
		t.Fatal("expected S256 challenge to differ from raw code verifier")
	}
	if !nodecore.VerifyPKCES256(verifier, challenge) {
		t.Fatal("expected VerifyPKCES256 to accept matching verifier and challenge")
	}
	if nodecore.VerifyPKCES256(verifier+"x", challenge) {
		t.Fatal("expected VerifyPKCES256 to reject wrong verifier")
	}
	if nodecore.VerifyPKCES256("short", challenge) {
		t.Fatal("expected VerifyPKCES256 to reject short verifier")
	}
	if nodecore.VerifyPKCES256(verifier, "short") {
		t.Fatal("expected VerifyPKCES256 to reject short challenge")
	}
}

func TestValidatePKCEAuthorizeParamsBoundsAndDefaults(t *testing.T) {
	validState := strings.Repeat("a", 32)
	validChallenge := nodecore.ComputePKCES256Challenge(strings.Repeat("v", 43))

	st, ch, port, err := nodecore.ValidatePKCEAuthorizeParams(validState, validChallenge, 0)
	if err != nil {
		t.Fatalf("expected default port 0 to normalize to 17771, got err: %v", err)
	}
	if st != validState || ch != validChallenge || port != nodecore.DefaultWorkerControlPort {
		t.Fatalf("unexpected normalized PKCE params: state=%q challenge=%q port=%d", st, ch, port)
	}

	if _, _, customPort, cErr := nodecore.ValidatePKCEAuthorizeParams(validState, validChallenge, 28881); cErr != nil || customPort != 28881 {
		t.Fatalf("expected custom unprivileged port 28881 accepted, got port=%d err=%v", customPort, cErr)
	}

	invalidCases := []struct {
		state     string
		challenge string
		port      int
	}{
		{state: "too_short", challenge: validChallenge, port: 17771},
		{state: validState + " space", challenge: validChallenge, port: 17771},
		{state: validState, challenge: "too_short_challenge", port: 17771},
		{state: validState, challenge: validChallenge + "a", port: 17771},
		{state: validState, challenge: validChallenge + "+/", port: 17771},
		{state: validState, challenge: validChallenge, port: 80},
		{state: validState, challenge: validChallenge, port: 70000},
	}
	for idx, tc := range invalidCases {
		if _, _, _, badErr := nodecore.ValidatePKCEAuthorizeParams(tc.state, tc.challenge, tc.port); badErr == nil {
			t.Fatalf("case %d: expected validation error for %+v", idx, tc)
		}
	}
}

func TestValidatePKCEPollParamsBounds(t *testing.T) {
	validState := strings.Repeat("b", 32)
	verifier, _, err := nodecore.GeneratePKCEPair()
	if err != nil {
		t.Fatalf("GeneratePKCEPair: %v", err)
	}
	st, vf, pErr := nodecore.ValidatePKCEPollParams(validState, verifier)
	if pErr != nil || st != validState || vf != verifier {
		t.Fatalf("expected valid PKCE poll params, got state=%q verifier=%q err=%v", st, vf, pErr)
	}
	if _, _, badErr := nodecore.ValidatePKCEPollParams("short", verifier); badErr == nil {
		t.Fatal("expected short state rejection")
	}
	if _, _, badErr := nodecore.ValidatePKCEPollParams(validState, "short_verifier"); badErr == nil {
		t.Fatal("expected short verifier rejection")
	}
}

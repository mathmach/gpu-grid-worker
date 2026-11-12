package nodecore_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

func deterministicBootstrapKeyPair(seedByte byte) (string, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seedByte}, ed25519.SeedSize))
	pub := priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub), priv
}

func TestNetworkBootstrapEd25519SignVerifyAndCanonicalInvariance(t *testing.T) {
	if _, normDefault, err := nodecore.NormalizeBootstrapPubKeyHex(nodecore.DefaultNetworkBootstrapPublicKeyHex); err != nil || len(normDefault) != 64 {
		t.Fatalf("expected valid embedded DefaultNetworkBootstrapPublicKeyHex, got len=%d err=%v", len(normDefault), err)
	}
	defaults := nodecore.DefaultNetworkBootstrapURLs()
	if len(defaults) < 2 {
		t.Fatalf("expected at least 2 default bootstrap mirrors, got %v", defaults)
	}

	pubHex, priv := deterministicBootstrapKeyPair(0x31)
	refNow := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	doc, rawJSON, err := nodecore.SignNetworkBootstrapManifest(priv, nodecore.NetworkBootstrapSignInput{
		Version:    3,
		GatewayURL: "  https://api.gpugrid-prod.example.com/ ",
		BootstrapURLs: []string{
			"https://mirror-a.example.com/network.json",
			"https://mirror-b.example.com",
		},
		IssuedAt:  refNow,
		ExpiresAt: refNow.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("SignNetworkBootstrapManifest failed: %v", err)
	}
	if doc.GatewayURL != "https://api.gpugrid-prod.example.com" {
		t.Fatalf("expected normalized gatewayUrl, got %q", doc.GatewayURL)
	}
	if len(doc.BootstrapURLs) != 2 || doc.BootstrapURLs[1] != "https://mirror-b.example.com"+nodecore.WellKnownNetworkBootstrapPath {
		t.Fatalf("unexpected normalized bootstrapUrls: %v", doc.BootstrapURLs)
	}

	verified, err := nodecore.VerifyNetworkBootstrapManifest(rawJSON, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		MinVersion:        3,
		Now:               refNow.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("VerifyNetworkBootstrapManifest failed: %v", err)
	}
	if verified.Version != 3 || verified.GatewayURL != "https://api.gpugrid-prod.example.com" || verified.PublicKey != pubHex {
		t.Fatalf("unexpected verified manifest: %+v", verified)
	}

	compactJSON, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal compact: %v", err)
	}
	if _, err := nodecore.VerifyNetworkBootstrapManifest(compactJSON, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		MinVersion:        1,
		Now:               refNow.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("expected JSON whitespace invariance across canonical Ed25519 verification, got %v", err)
	}
}

func TestNetworkBootstrapRejectsTamperingRollbackExpiryAndSSRF(t *testing.T) {
	pubHex, priv := deterministicBootstrapKeyPair(0x52)
	otherPubHex, otherPriv := deterministicBootstrapKeyPair(0x79)
	refNow := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)

	_, validRaw, err := nodecore.SignNetworkBootstrapManifest(priv, nodecore.NetworkBootstrapSignInput{
		Version:    5,
		GatewayURL: "https://gw-live.example.com",
		IssuedAt:   refNow,
		ExpiresAt:  refNow.Add(6 * time.Hour),
	})
	if err != nil {
		t.Fatalf("SignNetworkBootstrapManifest: %v", err)
	}

	tamperedGW := bytes.ReplaceAll(validRaw, []byte("https://gw-live.example.com"), []byte("https://evil-rce.example.com"))
	if _, err := nodecore.VerifyNetworkBootstrapManifest(tamperedGW, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		Now:               refNow,
	}); err == nil {
		t.Fatal("expected tampered gatewayUrl to fail Ed25519 signature verification")
	}

	tamperedVer := bytes.ReplaceAll(validRaw, []byte(`"version": 5`), []byte(`"version": 99`))
	if _, err := nodecore.VerifyNetworkBootstrapManifest(tamperedVer, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		Now:               refNow,
	}); err == nil {
		t.Fatal("expected tampered version to fail Ed25519 signature verification")
	}

	if _, err := nodecore.VerifyNetworkBootstrapManifest(validRaw, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{otherPubHex},
		Now:               refNow,
	}); err == nil {
		t.Fatal("expected untrusted public key to be rejected")
	}

	if _, err := nodecore.VerifyNetworkBootstrapManifest(validRaw, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{otherPubHex, strings.ToUpper(pubHex)},
		MinVersion:        5,
		Now:               refNow,
	}); err != nil {
		t.Fatalf("expected multi-key rotation ring to accept valid second key, got %v", err)
	}

	if _, err := nodecore.VerifyNetworkBootstrapManifest(validRaw, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		MinVersion:        6,
		Now:               refNow,
	}); err == nil {
		t.Fatal("expected anti-rollback version 5 < 6 to be rejected")
	}

	if _, err := nodecore.VerifyNetworkBootstrapManifest(validRaw, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		MinVersion:        1,
		Now:               refNow.Add(7 * time.Hour),
	}); err == nil {
		t.Fatal("expected expired manifest to be rejected")
	}

	if _, err := nodecore.VerifyNetworkBootstrapManifest(validRaw, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		MinVersion:        1,
		Now:               refNow.Add(-48 * time.Hour),
	}); err == nil {
		t.Fatal("expected far-future issuedAt manifest to be rejected")
	}

	for _, badTarget := range []string{
		"http://gw-live.example.com",
		"https://127.0.0.1:8000",
		"https://192.168.1.50:8000",
		"https://169.254.169.254",
		"https://gw-live.example.com:17771",
	} {
		if _, _, signErr := nodecore.SignNetworkBootstrapManifest(otherPriv, nodecore.NetworkBootstrapSignInput{
			Version:    1,
			GatewayURL: badTarget,
			IssuedAt:   refNow,
		}); signErr == nil {
			t.Fatalf("expected unsafe production target %q to be rejected", badTarget)
		}
	}

	if _, _, signErr := nodecore.SignNetworkBootstrapManifest(otherPriv, nodecore.NetworkBootstrapSignInput{
		Version:           1,
		GatewayURL:        "http://127.0.0.1:17771",
		IssuedAt:          refNow,
		AllowLocalTargets: true,
	}); signErr == nil {
		t.Fatal("expected worker loopback control port 17771 to remain blocked even with AllowLocalTargets=true")
	}

	withUnknownField := bytes.Replace(validRaw, []byte(`"version": 5,`), []byte(`"version": 5, "extra": true,`), 1)
	if _, err := nodecore.VerifyNetworkBootstrapManifest(withUnknownField, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{pubHex},
		Now:               refNow,
	}); err == nil {
		t.Fatal("expected unknown JSON field to be rejected")
	}
}

func TestWorkerBootstrapPayloadExtendedTrailerRoundTrip(t *testing.T) {
	pubHex, _ := deterministicBootstrapKeyPair(0x19)
	fakeBin := []byte("ELF_SYNTHETIC_EXECUTABLE_HEADER_BYTES_9876543210")

	trailer, err := nodecore.EncodeWorkerBootstrapPayload(nodecore.WorkerBootstrapPayload{
		GatewayURL:          "https://seed.example.com/",
		BootstrapURLs:       []string{"https://pages.example.com/network.json"},
		BootstrapPublicKeys: []string{strings.ToUpper(pubHex)},
	})
	if err != nil {
		t.Fatalf("EncodeWorkerBootstrapPayload failed: %v", err)
	}

	combined := append(append([]byte(nil), fakeBin...), trailer...)
	decoded, ok := nodecore.DecodeWorkerBootstrapPayload(combined)
	if !ok {
		t.Fatal("expected DecodeWorkerBootstrapPayload to succeed")
	}
	if decoded.GatewayURL != "https://seed.example.com" || len(decoded.BootstrapURLs) != 1 || len(decoded.BootstrapPublicKeys) != 1 || decoded.BootstrapPublicKeys[0] != pubHex {
		t.Fatalf("unexpected decoded payload: %+v", decoded)
	}

	legacyGW, legacyOK := nodecore.DecodeWorkerBootstrapTrailer(combined)
	if !legacyOK || legacyGW != "https://seed.example.com" {
		t.Fatalf("expected backward-compatible DecodeWorkerBootstrapTrailer to return https://seed.example.com, got ok=%v gw=%q", legacyOK, legacyGW)
	}

	discoveryOnlyTrailer, err := nodecore.EncodeWorkerBootstrapPayload(nodecore.WorkerBootstrapPayload{
		BootstrapURLs: []string{"https://pages.example.com/network.json"},
	})
	if err != nil {
		t.Fatalf("EncodeWorkerBootstrapPayload discovery-only failed: %v", err)
	}
	discDecoded, discOK := nodecore.DecodeWorkerBootstrapPayload(append(append([]byte(nil), fakeBin...), discoveryOnlyTrailer...))
	if !discOK || discDecoded.GatewayURL != "" || len(discDecoded.BootstrapURLs) != 1 {
		t.Fatalf("expected discovery-only payload to decode cleanly, got ok=%v payload=%+v", discOK, discDecoded)
	}
}

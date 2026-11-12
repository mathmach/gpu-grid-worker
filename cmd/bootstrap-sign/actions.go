package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"
)

type bootstrapKeyPairFile struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

func runKeygen(outPath string, force bool, stdout io.Writer) error {
	cleanOut := strings.TrimSpace(outPath)
	if cleanOut == "" {
		return errors.New("-key-out is required with -keygen")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate Ed25519 keypair: %w", err)
	}
	doc := bootstrapKeyPairFile{
		PublicKey:  hex.EncodeToString(pub),
		PrivateKey: hex.EncodeToString(priv),
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := writeKeyFile(cleanOut, encoded, force); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Generated Ed25519 keypair -> %s\nPublic Key Hex: %s\n", cleanOut, doc.PublicKey)
	return err
}

func writeKeyFile(path string, data []byte, force bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("open key file: %w", err)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("write key file: %w", writeErr)
	}
	return closeErr
}

func runSign(cfg cliFlags, stdout io.Writer) error {
	if strings.TrimSpace(cfg.keyPath) == "" {
		return errors.New("-key is required with -sign")
	}
	if strings.TrimSpace(cfg.gatewayURL) == "" {
		return errors.New("-gateway is required with -sign")
	}
	if cfg.version < 1 {
		return errors.New("-version (>= 1) is required with -sign")
	}
	if cfg.expiresHours < 0 {
		return errors.New("-expires-hours must be >= 0")
	}
	priv, err := loadSigningPrivateKey(cfg.keyPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	var exp time.Time
	if cfg.expiresHours > 0 {
		exp = now.Add(time.Duration(cfg.expiresHours) * time.Hour)
	}
	doc, rawJSON, err := nodecore.SignNetworkBootstrapManifest(priv, nodecore.NetworkBootstrapSignInput{
		Version:           cfg.version,
		GatewayURL:        cfg.gatewayURL,
		BootstrapURLs:     splitCSVTrimmed(cfg.bootstrapURLs),
		IssuedAt:          now,
		ExpiresAt:         exp,
		AllowLocalTargets: cfg.allowLocal,
	})
	if err != nil {
		return err
	}
	if _, err := nodecore.VerifyNetworkBootstrapManifest(rawJSON, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: []string{doc.PublicKey},
		MinVersion:        cfg.version,
		Now:               now,
		AllowLocalTargets: cfg.allowLocal,
	}); err != nil {
		return fmt.Errorf("self-verification failed: %w", err)
	}
	return writeSignedManifestOutput(cfg.outPath, rawJSON, doc, stdout)
}

func writeSignedManifestOutput(outPath string, rawJSON []byte, doc nodecore.NetworkBootstrapManifest, stdout io.Writer) error {
	cleanOut := strings.TrimSpace(outPath)
	if cleanOut == "" {
		_, err := stdout.Write(rawJSON)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cleanOut), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(cleanOut, rawJSON, 0o644); err != nil {
		return fmt.Errorf("write manifest %s: %w", cleanOut, err)
	}
	_, err := fmt.Fprintf(stdout, "Signed %s -> gateway=%s version=%d pubkey=%s\n", cleanOut, doc.GatewayURL, doc.Version, doc.PublicKey)
	return err
}

func loadSigningPrivateKey(keyPath string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(strings.TrimSpace(keyPath))
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	var doc bootstrapKeyPairFile
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse key file JSON: %w", err)
	}
	privHex := strings.ToLower(strings.TrimSpace(doc.PrivateKey))
	if len(privHex) != ed25519.PrivateKeySize*2 {
		return nil, errors.New("key file privateKey must be 128 hex characters")
	}
	privBytes, err := hex.DecodeString(privHex)
	if err != nil || len(privBytes) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 privateKey hex in key file")
	}
	priv := ed25519.PrivateKey(privBytes)
	if strings.TrimSpace(doc.PublicKey) != "" {
		_, declaredHex, err := nodecore.NormalizeBootstrapPubKeyHex(doc.PublicKey)
		if err != nil {
			return nil, err
		}
		derivedPub, ok := priv.Public().(ed25519.PublicKey)
		if !ok || hex.EncodeToString(derivedPub) != declaredHex {
			return nil, errors.New("key file publicKey does not match derived Ed25519 public key")
		}
	}
	return priv, nil
}

func runVerify(cfg cliFlags, stdout io.Writer) error {
	cleanIn := strings.TrimSpace(cfg.inPath)
	if cleanIn == "" {
		return errors.New("-in is required with -verify")
	}
	raw, err := os.ReadFile(cleanIn)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	doc, err := nodecore.VerifyNetworkBootstrapManifest(raw, nodecore.VerifyBootstrapOptions{
		TrustedPublicKeys: splitCSVTrimmed(cfg.pubKeys),
		MinVersion:        cfg.minVersion,
		Now:               time.Now().UTC(),
		AllowLocalTargets: cfg.allowLocal,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Verified manifest OK: gateway=%s version=%d issuedAt=%s pubkey=%s\n", doc.GatewayURL, doc.Version, doc.IssuedAt, doc.PublicKey)
	return err
}

func splitCSVTrimmed(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if clean := strings.TrimSpace(p); clean != "" {
			out = append(out, clean)
		}
	}
	return out
}

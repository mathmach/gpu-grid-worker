package nodecore

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

const (
	NetworkBootstrapSchemaV1            = "gpu-grid-network-bootstrap:v1"
	WellKnownNetworkBootstrapPath       = "/.well-known/gpu-grid-network.json"
	MaxNetworkBootstrapManifestBytes    = 64 << 10
	MaxNetworkBootstrapMirrors          = 8
	maxBootstrapFutureClockSkew         = 24 * time.Hour
	DefaultNetworkBootstrapPublicKeyHex = "848258800f36859e4e26d6294b47a503da9bb8340647d86745329f324278cfb9"
)

func DefaultNetworkBootstrapURLs() []string {
	return []string{
		"https://mathmach.github.io/gpu-grid-worker/network.json",
		"https://github.com/mathmach/gpu-grid-worker/releases/latest/download/network.json",
	}
}

type NetworkBootstrapManifest struct {
	Schema        string   `json:"schema"`
	Version       uint64   `json:"version"`
	GatewayURL    string   `json:"gatewayUrl"`
	BootstrapURLs []string `json:"bootstrapUrls,omitempty"`
	IssuedAt      string   `json:"issuedAt"`
	ExpiresAt     string   `json:"expiresAt,omitempty"`
	PublicKey     string   `json:"publicKey"`
	Signature     string   `json:"signature"`
}

type NetworkBootstrapSignInput struct {
	Version           uint64
	GatewayURL        string
	BootstrapURLs     []string
	IssuedAt          time.Time
	ExpiresAt         time.Time
	AllowLocalTargets bool
}

type VerifyBootstrapOptions struct {
	TrustedPublicKeys []string
	MinVersion        uint64
	Now               time.Time
	AllowLocalTargets bool
}

func NormalizeBootstrapPubKeyHex(raw string) ([]byte, string, error) {
	clean := strings.ToLower(strings.TrimSpace(raw))
	if len(clean) != ed25519.PublicKeySize*2 {
		return nil, "", derr.New(derr.CodeValidation, 400, "bootstrap Ed25519 public key must be 64 hex characters", nil)
	}
	decoded, err := hex.DecodeString(clean)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, "", derr.Wrap(derr.CodeValidation, 400, "invalid bootstrap Ed25519 public key hex", err)
	}
	return decoded, clean, nil
}

func NormalizeBootstrapPublicKeyList(rawList []string) ([]string, error) {
	if len(rawList) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(rawList))
	out := make([]string, 0, len(rawList))
	for _, item := range rawList {
		if strings.TrimSpace(item) == "" {
			continue
		}
		_, normHex, err := NormalizeBootstrapPubKeyHex(item)
		if err != nil {
			return nil, err
		}
		if !seen[normHex] {
			seen[normHex] = true
			out = append(out, normHex)
		}
	}
	return out, nil
}

func NormalizeBootstrapGatewayTarget(raw string, allowLocal bool) (string, error) {
	norm, err := NormalizeGatewayURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(norm)
	if err != nil {
		return "", derr.Validationf(derr.CodeValidation, "invalid bootstrap gateway URL %q", raw)
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if isAlwaysBlockedMetadataOrControlTarget(host, effectiveURLPort(u)) {
		return "", derr.New(derr.CodeValidation, 400, "bootstrap gateway URL targets restricted metadata or control port", nil)
	}
	if !allowLocal {
		if !strings.EqualFold(u.Scheme, "https") {
			return "", derr.New(derr.CodeValidation, 400, "bootstrap gateway URL must use https", nil)
		}
		if isBlockedDownloadHost(host) || IsLocalOrPrivateGatewayHost(host) {
			return "", derr.New(derr.CodeValidation, 400, "bootstrap gateway URL must not target local or private network hosts", nil)
		}
	}
	return norm, nil
}

func NormalizeBootstrapDiscoveryURL(raw string, allowLocal bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", derr.New(derr.CodeValidation, 400, "bootstrap discovery URL is required", nil)
	}
	if strings.ContainsAny(trimmed, " \t\r\n\x00?#\\") {
		return "", derr.Validationf(derr.CodeValidation, "invalid characters in bootstrap discovery URL %q", raw)
	}
	u, err := parseBootstrapDiscoveryURL(trimmed, raw)
	if err != nil {
		return "", err
	}
	if err := enforceBootstrapDiscoveryHostPolicy(u, allowLocal); err != nil {
		return "", err
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = WellKnownNetworkBootstrapPath
	}
	return u.String(), nil
}

func parseBootstrapDiscoveryURL(trimmed, raw string) (*url.URL, error) {
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, derr.Validationf(derr.CodeValidation, "invalid bootstrap discovery URL %q", raw)
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return nil, derr.Validationf(derr.CodeValidation, "unsupported scheme in bootstrap discovery URL %q", raw)
	}
	if strings.TrimSpace(u.Hostname()) == "" || hasDotTraversalSegment(u.Path) {
		return nil, derr.Validationf(derr.CodeValidation, "unsafe bootstrap discovery URL %q", raw)
	}
	if portStr := u.Port(); portStr != "" {
		portNum, pErr := strconv.Atoi(portStr)
		if pErr != nil || portNum < 1 || portNum > 65535 {
			return nil, derr.Validationf(derr.CodeValidation, "invalid port in bootstrap discovery URL %q", raw)
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u, nil
}

func enforceBootstrapDiscoveryHostPolicy(u *url.URL, allowLocal bool) error {
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if isAlwaysBlockedMetadataOrControlTarget(host, effectiveURLPort(u)) {
		return derr.New(derr.CodeValidation, 400, "bootstrap discovery URL targets restricted metadata or control port", nil)
	}
	isLocal := isBlockedDownloadHost(host) || IsLocalOrPrivateGatewayHost(host)
	if !allowLocal {
		if u.Scheme != "https" {
			return derr.New(derr.CodeValidation, 400, "bootstrap discovery URL must use https", nil)
		}
		if isLocal {
			return derr.New(derr.CodeValidation, 400, "bootstrap discovery URL must not target local or private network hosts", nil)
		}
		return nil
	}
	if u.Scheme == "http" && !isLocal {
		return derr.New(derr.CodeValidation, 400, "plaintext http bootstrap discovery URL is restricted to local hosts", nil)
	}
	return nil
}

func NormalizeBootstrapDiscoveryURLList(rawList []string, allowLocal bool) ([]string, error) {
	if len(rawList) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(rawList))
	out := make([]string, 0, len(rawList))
	for _, item := range rawList {
		if strings.TrimSpace(item) == "" {
			continue
		}
		norm, err := NormalizeBootstrapDiscoveryURL(item, allowLocal)
		if err != nil {
			return nil, err
		}
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	if len(out) > MaxNetworkBootstrapMirrors {
		return nil, derr.Validationf(derr.CodeValidation, "bootstrap mirror list exceeds maximum of %d URLs", MaxNetworkBootstrapMirrors)
	}
	return out, nil
}

func CanonicalNetworkBootstrapSigningPayload(version uint64, gwURL, issuedAt, expiresAt string, bootstrapURLs []string) []byte {
	var b strings.Builder
	b.Grow(192 + len(gwURL) + len(issuedAt) + len(expiresAt))
	b.WriteString(NetworkBootstrapSchemaV1)
	b.WriteString("\nversion=")
	b.WriteString(strconv.FormatUint(version, 10))
	b.WriteString("\ngateway_url=")
	b.WriteString(gwURL)
	b.WriteString("\nissued_at=")
	b.WriteString(issuedAt)
	b.WriteString("\nexpires_at=")
	b.WriteString(expiresAt)
	b.WriteString("\nbootstrap_urls=")
	b.WriteString(strings.Join(bootstrapURLs, ","))
	b.WriteByte('\n')
	return []byte(b.String())
}

func SignNetworkBootstrapManifest(priv ed25519.PrivateKey, in NetworkBootstrapSignInput) (NetworkBootstrapManifest, []byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return NetworkBootstrapManifest{}, nil, derr.New(derr.CodeValidation, 400, "invalid Ed25519 private key length", nil)
	}
	if in.Version < 1 {
		return NetworkBootstrapManifest{}, nil, derr.New(derr.CodeValidation, 400, "bootstrap manifest version must be >= 1", nil)
	}
	normGW, err := NormalizeBootstrapGatewayTarget(in.GatewayURL, in.AllowLocalTargets)
	if err != nil {
		return NetworkBootstrapManifest{}, nil, err
	}
	normURLs, err := NormalizeBootstrapDiscoveryURLList(in.BootstrapURLs, in.AllowLocalTargets)
	if err != nil {
		return NetworkBootstrapManifest{}, nil, err
	}
	issuedStr, expiresStr, err := formatSigningTimestamps(in.IssuedAt, in.ExpiresAt)
	if err != nil {
		return NetworkBootstrapManifest{}, nil, err
	}
	pubBytes, ok := priv.Public().(ed25519.PublicKey)
	if !ok || len(pubBytes) != ed25519.PublicKeySize {
		return NetworkBootstrapManifest{}, nil, derr.New(derr.CodeInternal, 500, "failed to derive Ed25519 public key", nil)
	}
	msg := CanonicalNetworkBootstrapSigningPayload(in.Version, normGW, issuedStr, expiresStr, normURLs)
	sig := ed25519.Sign(priv, msg)
	doc := NetworkBootstrapManifest{
		Schema:        NetworkBootstrapSchemaV1,
		Version:       in.Version,
		GatewayURL:    normGW,
		BootstrapURLs: normURLs,
		IssuedAt:      issuedStr,
		ExpiresAt:     expiresStr,
		PublicKey:     hex.EncodeToString(pubBytes),
		Signature:     hex.EncodeToString(sig),
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return NetworkBootstrapManifest{}, nil, err
	}
	return doc, append(encoded, '\n'), nil
}

func formatSigningTimestamps(issuedAt, expiresAt time.Time) (string, string, error) {
	iat := issuedAt.UTC()
	if issuedAt.IsZero() {
		iat = time.Now().UTC()
	}
	issuedStr := iat.Truncate(time.Second).Format(time.RFC3339)
	if expiresAt.IsZero() {
		return issuedStr, "", nil
	}
	exp := expiresAt.UTC().Truncate(time.Second)
	if !exp.After(iat.Truncate(time.Second)) {
		return "", "", derr.New(derr.CodeValidation, 400, "bootstrap expiresAt must be after issuedAt", nil)
	}
	return issuedStr, exp.Format(time.RFC3339), nil
}

func VerifyNetworkBootstrapManifest(raw []byte, opts VerifyBootstrapOptions) (NetworkBootstrapManifest, error) {
	doc, err := decodeStrictBootstrapManifest(raw)
	if err != nil {
		return NetworkBootstrapManifest{}, err
	}
	if err := validateBootstrapEnvelopeFields(&doc, opts.MinVersion, opts.AllowLocalTargets); err != nil {
		return NetworkBootstrapManifest{}, err
	}
	if err := validateBootstrapTimestamps(&doc, opts.Now); err != nil {
		return NetworkBootstrapManifest{}, err
	}
	if err := verifyBootstrapEd25519Signature(&doc, opts.TrustedPublicKeys); err != nil {
		return NetworkBootstrapManifest{}, err
	}
	return doc, nil
}

func decodeStrictBootstrapManifest(raw []byte) (NetworkBootstrapManifest, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(raw) > MaxNetworkBootstrapManifestBytes {
		return NetworkBootstrapManifest{}, derr.New(derr.CodeValidation, 400, "invalid network bootstrap manifest size", nil)
	}
	var doc NetworkBootstrapManifest
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return NetworkBootstrapManifest{}, derr.Wrap(derr.CodeValidation, 400, "malformed network bootstrap JSON", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return NetworkBootstrapManifest{}, derr.New(derr.CodeValidation, 400, "unexpected trailing data in network bootstrap JSON", nil)
	}
	return doc, nil
}

func validateBootstrapEnvelopeFields(doc *NetworkBootstrapManifest, minVersion uint64, allowLocal bool) error {
	if doc.Schema != NetworkBootstrapSchemaV1 {
		return derr.New(derr.CodeValidation, 400, "unsupported network bootstrap schema", nil)
	}
	if doc.Version < 1 {
		return derr.New(derr.CodeValidation, 400, "network bootstrap version must be >= 1", nil)
	}
	if doc.Version < minVersion {
		return derr.Validationf(derr.CodeValidation, "network bootstrap rollback rejected: version %d < %d", doc.Version, minVersion)
	}
	normGW, err := NormalizeBootstrapGatewayTarget(doc.GatewayURL, allowLocal)
	if err != nil {
		return err
	}
	normURLs, err := NormalizeBootstrapDiscoveryURLList(doc.BootstrapURLs, allowLocal)
	if err != nil {
		return err
	}
	doc.GatewayURL = normGW
	doc.BootstrapURLs = normURLs
	return nil
}

func validateBootstrapTimestamps(doc *NetworkBootstrapManifest, refNow time.Time) error {
	issuedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(doc.IssuedAt))
	if err != nil {
		return derr.Wrap(derr.CodeValidation, 400, "invalid network bootstrap issuedAt timestamp", err)
	}
	now := refNow.UTC()
	if refNow.IsZero() {
		now = time.Now().UTC()
	}
	if issuedAt.After(now.Add(maxBootstrapFutureClockSkew)) {
		return derr.New(derr.CodeValidation, 400, "network bootstrap issuedAt is too far in the future", nil)
	}
	doc.IssuedAt = issuedAt.UTC().Format(time.RFC3339)
	cleanExp := strings.TrimSpace(doc.ExpiresAt)
	if cleanExp == "" {
		doc.ExpiresAt = ""
		return nil
	}
	expiresAt, err := time.Parse(time.RFC3339, cleanExp)
	if err != nil {
		return derr.Wrap(derr.CodeValidation, 400, "invalid network bootstrap expiresAt timestamp", err)
	}
	if !expiresAt.After(issuedAt) || now.After(expiresAt) {
		return derr.New(derr.CodeValidation, 400, "network bootstrap manifest has expired", nil)
	}
	doc.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	return nil
}

func verifyBootstrapEd25519Signature(doc *NetworkBootstrapManifest, rawTrustedKeys []string) error {
	pubBytes, normPubHex, err := NormalizeBootstrapPubKeyHex(doc.PublicKey)
	if err != nil {
		return err
	}
	trusted := rawTrustedKeys
	if len(trusted) == 0 {
		trusted = []string{DefaultNetworkBootstrapPublicKeyHex}
	}
	trustedNorm, err := NormalizeBootstrapPublicKeyList(trusted)
	if err != nil {
		return err
	}
	if !matchesTrustedBootstrapKey(normPubHex, trustedNorm) {
		return derr.New(derr.CodeForbidden, 403, "network bootstrap public key is not trusted", nil)
	}
	sigHex := strings.ToLower(strings.TrimSpace(doc.Signature))
	if len(sigHex) != ed25519.SignatureSize*2 {
		return derr.New(derr.CodeForbidden, 403, "invalid network bootstrap signature length", nil)
	}
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return derr.Wrap(derr.CodeForbidden, 403, "invalid network bootstrap signature hex", err)
	}
	msg := CanonicalNetworkBootstrapSigningPayload(doc.Version, doc.GatewayURL, doc.IssuedAt, doc.ExpiresAt, doc.BootstrapURLs)
	if !ed25519.Verify(pubBytes, msg, sigBytes) {
		return derr.New(derr.CodeForbidden, 403, "network bootstrap Ed25519 signature verification failed", nil)
	}
	doc.PublicKey = normPubHex
	doc.Signature = sigHex
	return nil
}

func matchesTrustedBootstrapKey(candidateHex string, trustedHexList []string) bool {
	matched := 0
	candBytes := []byte(candidateHex)
	for _, trusted := range trustedHexList {
		if len(trusted) == len(candBytes) {
			matched |= subtle.ConstantTimeCompare(candBytes, []byte(trusted))
		}
	}
	return matched == 1
}

package nodecore

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

type MachineID string
type SHA256Digest string
type TrustTier string

const (
	TrustTierCommunity   TrustTier = "COMMUNITY"
	TrustTierVerifiedTLS TrustTier = "VERIFIED_TLS"

	DefaultWorkerControlPort = 17771
	MinLoopbackCallbackPort  = 1024
	MaxLoopbackCallbackPort  = 65535
	pkceS256ChallengeLen     = 43
)

func unmarshalNominal[T ~string](dst *T, data []byte, label string, parse func(string) (T, error)) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return derr.Wrap(derr.CodeValidation, 400, "invalid "+label+" JSON string", err)
	}
	parsed, err := parse(raw)
	if err != nil {
		return err
	}
	*dst = parsed
	return nil
}

func ComputeMachineID(parts ...string) MachineID {
	normalized := make([]string, len(parts))
	for i, p := range parts {
		normalized[i] = strings.TrimSpace(p)
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "|")))
	return MachineID("m-" + hex.EncodeToString(sum[:]))
}

func ParseMachineID(raw string) (MachineID, error) {
	clean := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(clean, "m-") || len(clean) != 66 {
		return "", derr.New(derr.CodeValidation, 400, "machineId must match m-<64hex>", nil)
	}
	hexPart := clean[2:]
	if !isLowerHex64(hexPart) || strings.Trim(hexPart, "0") == "" {
		return "", derr.New(derr.CodeValidation, 400, "machineId must contain 64 non-zero hexadecimal characters", nil)
	}
	return MachineID(clean), nil
}

func (id MachineID) String() string               { return string(id) }
func (id MachineID) IsZero() bool                 { return strings.TrimSpace(string(id)) == "" }
func (id MachineID) MarshalJSON() ([]byte, error) { return json.Marshal(string(id)) }
func (id *MachineID) UnmarshalJSON(b []byte) error {
	return unmarshalNominal(id, b, "machineId", ParseMachineID)
}

func ShortMachineKey(machineID string) string {
	m := strings.TrimSpace(machineID)
	if len(m) > 12 {
		return m[:12]
	}
	return m
}

func ParseSHA256Digest(raw string) (SHA256Digest, error) {
	clean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "sha256:")
	if !isLowerHex64(clean) {
		return "", derr.New(derr.CodeValidation, 400, "sha256 digest must contain exactly 64 hexadecimal characters", nil)
	}
	return SHA256Digest(clean), nil
}

func (d SHA256Digest) String() string               { return string(d) }
func (d SHA256Digest) IsZero() bool                 { return strings.TrimSpace(string(d)) == "" }
func (d SHA256Digest) MarshalJSON() ([]byte, error) { return json.Marshal(string(d)) }
func (d *SHA256Digest) UnmarshalJSON(b []byte) error {
	return unmarshalNominal(d, b, "sha256Digest", ParseSHA256Digest)
}

func ParseTrustTier(raw string) (TrustTier, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(TrustTierCommunity):
		return TrustTierCommunity, nil
	case string(TrustTierVerifiedTLS), "VERIFIED":
		return TrustTierVerifiedTLS, nil
	default:
		return "", derr.New(derr.CodeValidation, 400, "invalid trust tier (expected COMMUNITY or VERIFIED_TLS)", nil)
	}
}

func ParseTrustTierFilter(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "any", "community":
		return "community", nil
	case "verified_only", "verified_tls", "verified":
		return "verified_tls", nil
	default:
		return "", derr.New(derr.CodeValidation, 400, "invalid trust tier filter (expected COMMUNITY or VERIFIED_TLS)", nil)
	}
}

func DeriveTrustTier(verifiedProvider, activeTLS bool) TrustTier {
	if verifiedProvider && activeTLS {
		return TrustTierVerifiedTLS
	}
	return TrustTierCommunity
}

func MinTrustTier(a, b TrustTier) TrustTier {
	if a == TrustTierVerifiedTLS && b == TrustTierVerifiedTLS {
		return TrustTierVerifiedTLS
	}
	return TrustTierCommunity
}

func (t TrustTier) SatisfiesFilter(filter string) bool {
	if t != "" && t != TrustTierCommunity && t != TrustTierVerifiedTLS {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(filter)) {
	case "", "any", "community":
		return true
	case "verified_only", "verified_tls", "verified":
		return t == TrustTierVerifiedTLS
	default:
		return false
	}
}

func (t TrustTier) String() string               { return string(t) }
func (t TrustTier) IsZero() bool                 { return strings.TrimSpace(string(t)) == "" }
func (t TrustTier) MarshalJSON() ([]byte, error) { return json.Marshal(string(t)) }
func (t *TrustTier) UnmarshalJSON(b []byte) error {
	return unmarshalNominal(t, b, "trustTier", ParseTrustTier)
}

func GeneratePKCEPair() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	return verifier, ComputePKCES256Challenge(verifier), nil
}

func ComputePKCES256Challenge(codeVerifier string) string {
	sum := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func ValidatePKCEAuthorizeParams(rawState, rawChallenge string, rawPort int) (string, string, int, error) {
	state := strings.TrimSpace(rawState)
	if len(state) < 16 || len(state) > 128 || !isURLSafeToken(state) {
		return "", "", 0, derr.New(derr.CodeValidation, 400, "invalid worker OAuth state parameter", nil)
	}
	challenge := strings.TrimSpace(rawChallenge)
	if !isValidPKCES256Challenge(challenge) {
		return "", "", 0, derr.New(derr.CodeValidation, 400, "invalid PKCE S256 code_challenge parameter", nil)
	}
	port := rawPort
	if port == 0 {
		port = DefaultWorkerControlPort
	}
	if port < MinLoopbackCallbackPort || port > MaxLoopbackCallbackPort {
		return "", "", 0, derr.New(derr.CodeValidation, 400, "invalid worker loopback callback port", nil)
	}
	return state, challenge, port, nil
}

func ValidatePKCEPollParams(rawState, rawVerifier string) (string, string, error) {
	state := strings.TrimSpace(rawState)
	if len(state) < 16 || len(state) > 128 || !isURLSafeToken(state) {
		return "", "", derr.New(derr.CodeValidation, 400, "invalid worker OAuth state parameter", nil)
	}
	verifier := strings.TrimSpace(rawVerifier)
	if len(verifier) < 43 || len(verifier) > 128 || !isURLSafeToken(verifier) {
		return "", "", derr.New(derr.CodeValidation, 400, "invalid PKCE S256 code_verifier parameter", nil)
	}
	return state, verifier, nil
}

func VerifyPKCES256(codeVerifier, expectedChallenge string) bool {
	cleanVerifier := strings.TrimSpace(codeVerifier)
	cleanExpected := strings.TrimSpace(expectedChallenge)
	if len(cleanVerifier) < 43 || len(cleanVerifier) > 128 || !isURLSafeToken(cleanVerifier) {
		return false
	}
	if !isValidPKCES256Challenge(cleanExpected) {
		return false
	}
	computed := ComputePKCES256Challenge(cleanVerifier)
	return ConstantTimeTokenEqual(computed, cleanExpected)
}

func RandomHex(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func SHA256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func ConstantTimeTokenEqual(a, b string) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isValidPKCES256Challenge(challenge string) bool {
	if len(challenge) != pkceS256ChallengeLen || !isURLSafeToken(challenge) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(decoded) == sha256.Size
}

func isURLSafeToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isAlpha && !isDigit && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range 64 {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

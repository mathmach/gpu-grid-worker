package nodecore

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

func NormalizeGatewayURL(raw string) (string, error) {
	rawTrim := strings.TrimSpace(raw)
	if rawTrim == "" {
		return "", derr.New(derr.CodeValidation, 400, "gateway URL is required", nil)
	}
	if strings.ContainsAny(rawTrim, " \t\r\n\x00") {
		return "", derr.Validationf(derr.CodeValidation, "invalid whitespace or control characters in gateway URL %q", raw)
	}
	if strings.ContainsAny(rawTrim, "?#") {
		return "", derr.Validationf(derr.CodeValidation, "gateway URL must not contain query or fragment parameters %q", raw)
	}
	trimmed := strings.TrimRight(rawTrim, "/")
	if trimmed == "" {
		return "", derr.New(derr.CodeValidation, 400, "gateway URL is required", nil)
	}

	candidate, err := applyGatewayScheme(trimmed, raw)
	if err != nil {
		return "", err
	}
	return validateNormalizedGatewayURL(candidate, raw)
}

func applyGatewayScheme(trimmed, raw string) (string, error) {
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "http://"):
		return "http://" + trimmed[7:], nil
	case strings.HasPrefix(lower, "https://"):
		return "https://" + trimmed[8:], nil
	case strings.HasPrefix(lower, "ws://"):
		return "http://" + trimmed[5:], nil
	case strings.HasPrefix(lower, "wss://"):
		return "https://" + trimmed[6:], nil
	}
	if strings.Contains(trimmed, "://") {
		return "", derr.Validationf(derr.CodeValidation, "unsupported scheme in gateway URL %q", raw)
	}

	authority, pathSuffix := splitGatewayAuthorityAndPath(trimmed)
	if pathSuffix != "" {
		return "", derr.Validationf(derr.CodeValidation, "gateway URL must not contain a subpath %q", raw)
	}
	host, formattedAuthority, err := extractAndFormatGatewayAuthority(authority)
	if err != nil {
		return "", err
	}
	if IsLocalOrPrivateGatewayHost(host) {
		return "http://" + formattedAuthority, nil
	}
	return "https://" + formattedAuthority, nil
}

func validateNormalizedGatewayURL(candidate, raw string) (string, error) {
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", derr.Validationf(derr.CodeValidation, "invalid gateway URL %q", raw)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", derr.Validationf(derr.CodeValidation, "gateway URL must not contain a subpath %q", raw)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", derr.Validationf(derr.CodeValidation, "missing host in gateway URL %q", raw)
	}
	if portStr := parsed.Port(); portStr != "" {
		portNum, pErr := strconv.Atoi(portStr)
		if pErr != nil || portNum < 1 || portNum > 65535 {
			return "", derr.Validationf(derr.CodeValidation, "invalid port in gateway URL %q", raw)
		}
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func splitGatewayAuthorityAndPath(raw string) (string, string) {
	slash := strings.IndexByte(raw, '/')
	if slash < 0 {
		return raw, ""
	}
	return raw[:slash], raw[slash:]
}

func extractAndFormatGatewayAuthority(authority string) (string, string, error) {
	if strings.TrimSpace(authority) == "" {
		return "", "", derr.New(derr.CodeValidation, 400, "gateway host is required", nil)
	}
	if h, portStr, hasPort := splitAuthorityHostPort(authority); hasPort {
		portNum, pErr := strconv.Atoi(portStr)
		if pErr != nil || portNum < 1 || portNum > 65535 {
			return "", "", derr.Validationf(derr.CodeValidation, "invalid gateway port %q", portStr)
		}
		if h == "" {
			return "", "", derr.New(derr.CodeValidation, 400, "gateway host is required", nil)
		}
		return h, authority, nil
	}
	unbracketed := strings.Trim(authority, "[]")
	if addr, err := netip.ParseAddr(unbracketed); err == nil && addr.Is6() {
		return unbracketed, "[" + unbracketed + "]", nil
	}
	if strings.Contains(unbracketed, ":") {
		return "", "", derr.Validationf(derr.CodeValidation, "invalid gateway authority %q", authority)
	}
	return unbracketed, authority, nil
}

func splitAuthorityHostPort(authority string) (string, string, bool) {
	if strings.HasPrefix(authority, "[") {
		idx := strings.Index(authority, "]:")
		if idx < 0 {
			return "", "", false
		}
		return authority[1:idx], authority[idx+2:], true
	}
	if strings.Count(authority, ":") == 1 {
		host, portStr, _ := strings.Cut(authority, ":")
		return host, portStr, true
	}
	return "", "", false
}

func IsLocalOrPrivateGatewayHost(host string) bool {
	clean := strings.TrimSpace(host)
	if clean == "" || strings.EqualFold(clean, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(clean)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

func ValidateWorkerDownloadURL(rawURL, gatewayBaseURL string, allowPlainHTTP bool) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", derr.New(derr.CodeValidation, 400, "download URL is required", nil)
	}
	if strings.ContainsAny(trimmed, " \t\r\n\x00#\\") {
		return "", derr.Validationf(derr.CodeValidation, "invalid characters in download URL %q", rawURL)
	}

	gwParsed, gwSecure, err := parseOptionalGatewayOrigin(gatewayBaseURL)
	if err != nil {
		return "", err
	}
	resolved, err := resolveCandidateDownloadURL(trimmed, gwParsed)
	if err != nil {
		return "", err
	}
	u, err := parseDownloadTargetURL(resolved, rawURL)
	if err != nil {
		return "", err
	}
	if err := enforceDownloadSchemeAndSSRF(u, gwParsed, gwSecure, allowPlainHTTP); err != nil {
		return "", err
	}
	return u.String(), nil
}

func parseOptionalGatewayOrigin(gatewayBaseURL string) (*url.URL, bool, error) {
	cleanGW := strings.TrimSpace(gatewayBaseURL)
	if cleanGW == "" {
		return nil, false, nil
	}
	lowerRaw := strings.ToLower(cleanGW)
	rawSecure := strings.HasPrefix(lowerRaw, "https://") || strings.HasPrefix(lowerRaw, "wss://")
	normGW, err := NormalizeGatewayURL(cleanGW)
	if err != nil {
		return nil, false, err
	}
	parsed, pErr := url.Parse(normGW)
	if pErr != nil {
		return nil, false, derr.Validationf(derr.CodeValidation, "invalid gateway URL %q", gatewayBaseURL)
	}
	isSecure := rawSecure || strings.EqualFold(parsed.Scheme, "https")
	return parsed, isSecure, nil
}

func resolveCandidateDownloadURL(trimmed string, gwParsed *url.URL) (string, error) {
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return trimmed, nil
	}
	if strings.Contains(trimmed, "://") || strings.HasPrefix(trimmed, "//") {
		return "", derr.Validationf(derr.CodeValidation, "unsupported scheme in download URL %q", trimmed)
	}
	if gwParsed == nil {
		return "", derr.New(derr.CodeValidation, 400, "gateway base URL is required for relative download path", nil)
	}
	rel := strings.TrimLeft(trimmed, "/")
	if rel == "" || hasDotTraversalSegment(rel) {
		return "", derr.Validationf(derr.CodeValidation, "unsafe relative download path %q", trimmed)
	}
	return strings.TrimRight(gwParsed.String(), "/") + "/" + rel, nil
}

func parseDownloadTargetURL(resolved, rawURL string) (*url.URL, error) {
	u, err := url.Parse(resolved)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, derr.Validationf(derr.CodeValidation, "invalid download URL %q", rawURL)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return nil, derr.Validationf(derr.CodeValidation, "unsupported download scheme in %q", rawURL)
	}
	if strings.TrimSpace(u.Hostname()) == "" || hasDotTraversalSegment(u.Path) {
		return nil, derr.Validationf(derr.CodeValidation, "unsafe download URL %q", rawURL)
	}
	if portStr := u.Port(); portStr != "" {
		portNum, pErr := strconv.Atoi(portStr)
		if pErr != nil || portNum < 1 || portNum > 65535 {
			return nil, derr.Validationf(derr.CodeValidation, "invalid port in download URL %q", rawURL)
		}
	}
	return u, nil
}

func enforceDownloadSchemeAndSSRF(u, gwParsed *url.URL, gwSecure, allowPlainHTTP bool) error {
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	isHTTP := strings.EqualFold(u.Scheme, "http")
	if isHTTP && gwSecure {
		return derr.New(derr.CodeValidation, 400, "plaintext HTTP download downgrade is forbidden over TLS gateway", nil)
	}
	matchesGWOrigin := matchesExplicitGatewayOrigin(u, gwParsed)
	if isBlockedDownloadHost(host) {
		if !matchesGWOrigin || isAlwaysBlockedMetadataOrControlTarget(host, effectiveURLPort(u)) {
			return derr.New(derr.CodeValidation, 400, "download URL targets restricted loopback, link-local, or private network host", map[string]any{"host": host})
		}
	}
	if isHTTP && !allowPlainHTTP && !matchesGWOrigin {
		return derr.New(derr.CodeValidation, 400, "plaintext HTTP download URL is forbidden", nil)
	}
	return nil
}

func matchesExplicitGatewayOrigin(target, gw *url.URL) bool {
	if target == nil || gw == nil {
		return false
	}
	tHost := normalizeHostLiteral(target.Hostname())
	gHost := normalizeHostLiteral(gw.Hostname())
	if tHost == "" || gHost == "" || tHost != gHost {
		return false
	}
	return effectiveURLPort(target) == effectiveURLPort(gw)
}

func normalizeHostLiteral(raw string) string {
	clean := strings.ToLower(strings.TrimSpace(raw))
	if addr, err := netip.ParseAddr(clean); err == nil {
		return addr.Unmap().String()
	}
	return clean
}

func effectiveURLPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func isBlockedDownloadHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return true
	}
	return addr.Is4() && isSharedCGNATIPv4(addr.As4())
}

func isSharedCGNATIPv4(b [4]byte) bool {
	return b[0] == 100 && b[1] >= 64 && b[1] <= 127
}

func isAlwaysBlockedMetadataOrControlTarget(host, port string) bool {
	if port == "17771" {
		return true
	}
	if strings.EqualFold(host, "metadata.google.internal") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast()
}

func hasDotTraversalSegment(rawPath string) bool {
	pathPart, _, _ := strings.Cut(rawPath, "?")
	for _, seg := range strings.Split(pathPart, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

const (
	workerBootstrapMagic        = "GGWKBOOT"
	workerBootstrapFooterBytes  = 4 + 32 + 8
	maxBootstrapPayloadBytes    = 1024
	MaxWorkerBootstrapTailBytes = maxBootstrapPayloadBytes + workerBootstrapFooterBytes
)

type WorkerBootstrapPayload struct {
	GatewayURL          string   `json:"gatewayUrl,omitempty"`
	BootstrapURLs       []string `json:"bootstrapUrls,omitempty"`
	BootstrapPublicKeys []string `json:"bootstrapPublicKeys,omitempty"`
}

func EncodeWorkerBootstrapTrailer(rawGatewayURL string) ([]byte, error) {
	norm, err := NormalizeGatewayURL(rawGatewayURL)
	if err != nil {
		return nil, err
	}
	return EncodeWorkerBootstrapPayload(WorkerBootstrapPayload{GatewayURL: norm})
}

func EncodeWorkerBootstrapPayload(cfg WorkerBootstrapPayload) ([]byte, error) {
	var normalized WorkerBootstrapPayload
	if strings.TrimSpace(cfg.GatewayURL) != "" {
		normGW, err := NormalizeGatewayURL(cfg.GatewayURL)
		if err != nil {
			return nil, err
		}
		normalized.GatewayURL = normGW
	}
	normURLs, err := NormalizeBootstrapDiscoveryURLList(cfg.BootstrapURLs, true)
	if err != nil {
		return nil, err
	}
	normKeys, err := NormalizeBootstrapPublicKeyList(cfg.BootstrapPublicKeys)
	if err != nil {
		return nil, err
	}
	normalized.BootstrapURLs = normURLs
	normalized.BootstrapPublicKeys = normKeys
	if normalized.GatewayURL == "" && len(normalized.BootstrapURLs) == 0 {
		return nil, derr.New(derr.CodeValidation, 400, "worker bootstrap payload requires gatewayUrl or bootstrapUrls", nil)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > maxBootstrapPayloadBytes {
		return nil, derr.New(derr.CodeValidation, 400, "worker bootstrap payload exceeds maximum size", nil)
	}
	digest := sha256.Sum256(payload)
	out := make([]byte, len(payload)+workerBootstrapFooterBytes)
	copy(out, payload)
	metaStart := len(payload)
	binary.BigEndian.PutUint32(out[metaStart:metaStart+4], uint32(len(payload)))
	copy(out[metaStart+4:metaStart+36], digest[:])
	copy(out[metaStart+36:], workerBootstrapMagic)
	return out, nil
}

func DecodeWorkerBootstrapTrailer(tail []byte) (string, bool) {
	parsed, ok := DecodeWorkerBootstrapPayload(tail)
	if !ok || parsed.GatewayURL == "" {
		return "", false
	}
	return parsed.GatewayURL, true
}

func DecodeWorkerBootstrapPayload(tail []byte) (WorkerBootstrapPayload, bool) {
	payload, ok := extractVerifiedBootstrapPayloadBytes(tail)
	if !ok {
		return WorkerBootstrapPayload{}, false
	}
	var parsed WorkerBootstrapPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return WorkerBootstrapPayload{}, false
	}
	return normalizeDecodedBootstrapPayload(parsed)
}

func extractVerifiedBootstrapPayloadBytes(tail []byte) ([]byte, bool) {
	if len(tail) < workerBootstrapFooterBytes+2 {
		return nil, false
	}
	metaStart := len(tail) - workerBootstrapFooterBytes
	if string(tail[metaStart+36:]) != workerBootstrapMagic {
		return nil, false
	}
	payloadLen := int(binary.BigEndian.Uint32(tail[metaStart : metaStart+4]))
	if payloadLen <= 0 || payloadLen > maxBootstrapPayloadBytes || metaStart < payloadLen {
		return nil, false
	}
	payload := tail[metaStart-payloadLen : metaStart]
	expectedDigest := tail[metaStart+4 : metaStart+36]
	actualDigest := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(actualDigest[:], expectedDigest) != 1 {
		return nil, false
	}
	return payload, true
}

func normalizeDecodedBootstrapPayload(parsed WorkerBootstrapPayload) (WorkerBootstrapPayload, bool) {
	var out WorkerBootstrapPayload
	if strings.TrimSpace(parsed.GatewayURL) != "" {
		normGW, err := NormalizeGatewayURL(parsed.GatewayURL)
		if err != nil {
			return WorkerBootstrapPayload{}, false
		}
		out.GatewayURL = normGW
	}
	normURLs, err := NormalizeBootstrapDiscoveryURLList(parsed.BootstrapURLs, true)
	if err != nil {
		return WorkerBootstrapPayload{}, false
	}
	normKeys, err := NormalizeBootstrapPublicKeyList(parsed.BootstrapPublicKeys)
	if err != nil {
		return WorkerBootstrapPayload{}, false
	}
	out.BootstrapURLs = normURLs
	out.BootstrapPublicKeys = normKeys
	if out.GatewayURL == "" && len(out.BootstrapURLs) == 0 {
		return WorkerBootstrapPayload{}, false
	}
	return out, true
}

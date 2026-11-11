package cas

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

const maxHTTPRedirectHops = 10

type ssrfOriginCtxKey struct{}

var alwaysBlockedSSRFPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func withSSRFOriginLocal(ctx context.Context, originLocal bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ssrfOriginCtxKey{}, originLocal)
}

func ssrfOriginLocalFromContext(ctx context.Context) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	v, ok := ctx.Value(ssrfOriginCtxKey{}).(bool)
	return v, ok
}

type safeRoundTripper struct {
	base        http.RoundTripper
	guardCustom bool
}

func (rt *safeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, derr.New(derr.CodeValidation, 400, "invalid HTTP request", nil)
	}
	ctx := req.Context()
	if _, ok := ssrfOriginLocalFromContext(ctx); !ok {
		ctx = withSSRFOriginLocal(ctx, isLocalOrLANHost(req.URL.Hostname()))
		req = req.WithContext(ctx)
	}
	if rt.guardCustom {
		if _, err := resolveSafeDialAddrs(ctx, req.URL.Hostname()); err != nil {
			return nil, err
		}
	}
	return rt.base.RoundTrip(req)
}

func newSafeTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           makeSafeDialContext(dialer),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

func newSafeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     &safeRoundTripper{base: newSafeTransport()},
		CheckRedirect: safeCheckRedirect,
	}
}

func wrapSafeHTTPClient(client *http.Client, defaultTimeout time.Duration) *http.Client {
	if client == nil {
		return newSafeHTTPClient(defaultTimeout)
	}
	cloned := *client
	cloned.Transport = wrapSafeRoundTripper(client.Transport)
	prevRedirect := client.CheckRedirect
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := safeCheckRedirect(req, via); err != nil {
			return err
		}
		if prevRedirect != nil {
			return prevRedirect(req, via)
		}
		return nil
	}
	return &cloned
}

func wrapSafeRoundTripper(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		return &safeRoundTripper{base: newSafeTransport()}
	}
	if existing, ok := rt.(*safeRoundTripper); ok {
		return existing
	}
	if tr, ok := rt.(*http.Transport); ok {
		trClone := tr.Clone()
		trClone.DialContext = makeSafeDialContext(&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		})
		return &safeRoundTripper{base: trClone}
	}
	return &safeRoundTripper{base: rt, guardCustom: true}
}

func ValidateFetchURL(ctx context.Context, rawURL string) (context.Context, *url.URL, error) {
	parsed, err := validateInitialDownloadURL(rawURL)
	if err != nil {
		return ctx, nil, err
	}
	return withSSRFOriginLocal(ctx, isLocalOrLANHost(parsed.Hostname())), parsed, nil
}

func validateInitialDownloadURL(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, derr.New(derr.CodeValidation, 400, "download URL is required", nil)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.User != nil {
		return nil, derr.New(derr.CodeValidation, 400, "invalid download URL", nil)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, derr.New(derr.CodeValidation, 400, "download URL must use http or https", nil)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return nil, derr.New(derr.CodeValidation, 400, "download URL host is required", nil)
	}
	if ip, ipErr := parseNormalizedAddr(host); ipErr == nil {
		if isAlwaysBlockedAddr(ip) {
			return nil, derr.New(derr.CodeValidation, 400, "download URL points to a restricted network address", nil)
		}
	}
	return parsed, nil
}

func safeCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxHTTPRedirectHops {
		return errors.New("stopped after maximum allowed redirects")
	}
	if req == nil || req.URL == nil || req.URL.User != nil {
		return derr.New(derr.CodeValidation, 400, "invalid redirect URL", nil)
	}
	nextScheme := strings.ToLower(req.URL.Scheme)
	if nextScheme != "http" && nextScheme != "https" {
		return derr.New(derr.CodeValidation, 400, "unsupported redirect scheme", nil)
	}
	prev := via[len(via)-1]
	if strings.EqualFold(prev.URL.Scheme, "https") && nextScheme != "https" {
		return derr.New(derr.CodeValidation, 400, "redirect scheme downgrade from https to http is forbidden", nil)
	}
	if !sameURLOrigin(prev.URL, req.URL) {
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
	}
	originLocal := isLocalOrLANHost(via[0].URL.Hostname())
	*req = *req.WithContext(withSSRFOriginLocal(req.Context(), originLocal))
	return validateRedirectDestination(via[0].URL, req.URL)
}

func validateRedirectDestination(originURL, nextURL *url.URL) error {
	nextHost := strings.ToLower(strings.TrimSpace(nextURL.Hostname()))
	if nextHost == "" {
		return derr.New(derr.CodeValidation, 400, "redirect target host is empty", nil)
	}
	originLocal := isLocalOrLANHost(originURL.Hostname())
	if ip, err := parseNormalizedAddr(nextHost); err == nil {
		if isAlwaysBlockedAddr(ip) {
			return derr.New(derr.CodeValidation, 400, "redirect to restricted address is forbidden", nil)
		}
		if (ip.IsLoopback() || ip.IsPrivate()) && !originLocal {
			return derr.New(derr.CodeValidation, 400, "redirect from public host to private or loopback address is forbidden", nil)
		}
		return nil
	}
	if isLocalHostname(nextHost) && !originLocal {
		return derr.New(derr.CodeValidation, 400, "redirect from public host to localhost is forbidden", nil)
	}
	return nil
}

func makeSafeDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		portNum, err := strconv.Atoi(portStr)
		if err != nil || portNum < 1 || portNum > 65535 {
			return nil, fmt.Errorf("invalid port %q", portStr)
		}
		ips, err := resolveSafeDialAddrs(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			target := netip.AddrPortFrom(ip, uint16(portNum)).String()
			conn, dialErr := dialer.DialContext(ctx, network, target)
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("no valid IP addresses resolved for host")
	}
}

func resolveSafeDialAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	clean := strings.ToLower(strings.TrimSpace(host))
	originLocal, hasOrigin := ssrfOriginLocalFromContext(ctx)
	if ip, err := parseNormalizedAddr(clean); err == nil {
		allowPrivate := originLocal || (!hasOrigin && (ip.IsLoopback() || ip.IsPrivate()))
		if isBlockedRemoteIP(ip, allowPrivate) {
			return nil, derr.New(derr.CodeValidation, 400, "dial to restricted IP literal is forbidden", nil)
		}
		return []netip.Addr{ip}, nil
	}
	resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", clean)
	if err != nil {
		return nil, err
	}
	allowPrivate := originLocal || (!hasOrigin && isLocalHostname(clean))
	valid := make([]netip.Addr, 0, len(resolved))
	for _, raw := range resolved {
		ip := raw.Unmap()
		if err := validateResolvedDialIP(ip, allowPrivate); err != nil {
			return nil, err
		}
		valid = append(valid, ip)
	}
	if len(valid) == 0 {
		return nil, errors.New("host resolved to zero usable IP addresses")
	}
	return valid, nil
}

func validateResolvedDialIP(ip netip.Addr, allowPrivate bool) error {
	if isAlwaysBlockedAddr(ip) {
		return derr.New(derr.CodeValidation, 400, "DNS resolved to restricted network address", nil)
	}
	if ip.IsLoopback() && !allowPrivate {
		return derr.New(derr.CodeValidation, 400, "DNS rebinding to loopback address is forbidden", nil)
	}
	if ip.IsPrivate() && !allowPrivate {
		return derr.New(derr.CodeValidation, 400, "DNS rebinding to private network address is forbidden", nil)
	}
	return nil
}

func isBlockedRemoteIP(ip netip.Addr, allowPrivate bool) bool {
	if isAlwaysBlockedAddr(ip) {
		return true
	}
	if !allowPrivate && (ip.IsLoopback() || ip.IsPrivate()) {
		return true
	}
	return false
}

func parseNormalizedAddr(host string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}, err
	}
	return ip.Unmap(), nil
}

func isAlwaysBlockedAddr(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, pfx := range alwaysBlockedSSRFPrefixes {
		if pfx.Contains(ip) {
			return true
		}
	}
	return false
}

func isLocalHostname(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

func isLocalOrLANHost(host string) bool {
	if isLocalHostname(host) {
		return true
	}
	ip, err := parseNormalizedAddr(host)
	if err != nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func sameURLOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectiveURLPort(a) == effectiveURLPort(b)
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

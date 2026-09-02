package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxRedirects = 5

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF special-purpose addresses
	netip.MustParsePrefix("192.0.2.0/24"),  // documentation
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), // reserved
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 can embed a private IPv4 target
	netip.MustParsePrefix("fec0::/10"),
}

// newPublicClient makes WebFetch connect only to public HTTP(S) targets.
// Redirects are revalidated and DNS answers are dialed by IP to close the
// DNS-rebinding gap between validation and connection.
func newPublicClient(timeout time.Duration) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	// A proxy resolves and connects to the requested host itself, which bypasses
	// the public-address check below. Public egress must connect directly.
	base.Proxy = nil
	base.DialContext = dialPublicContext
	return &http.Client{
		Transport: publicTransport{base: base},
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("web request: too many redirects (maximum %d)", maxRedirects)
			}
			return validatePublicURL(req.URL)
		},
	}
}

// validatePublicURL rejects a model-controlled URL before WebFetch starts it.
// It permits ordinary query parameters but refuses credential-shaped ones.
func validatePublicURL(u *url.URL) error {
	if u == nil {
		return errors.New("web request: URL is required")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("web request: URL must use http or https")
	}
	if u.Hostname() == "" {
		return errors.New("web request: URL requires a host")
	}
	if u.User != nil {
		return errors.New("web request: URL must not include userinfo")
	}
	if port := u.Port(); port != "" && !standardPort(u.Scheme, port) {
		return errors.New("web request: URL must use the standard port for its scheme")
	}
	if name := sensitiveQueryParameter(u); name != "" {
		return fmt.Errorf("web request: URL query parameter %q looks like a credential", name)
	}

	host := u.Hostname()
	if isLocalHostname(host) {
		return errors.New("web request: local hostnames are not allowed")
	}
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return validatePublicAddr(addr)
	}
	return nil
}

type publicTransport struct{ base http.RoundTripper }

func (t publicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validatePublicURL(req.URL); err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func dialPublicContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("web request: invalid destination: %w", err)
	}
	addrs, err := resolvePublicHost(ctx, host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{}
	var lastErr error
	for _, addr := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("web request: connect to public host: %w", lastErr)
}

func resolvePublicHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if isLocalHostname(host) {
		return nil, errors.New("web request: local hostnames are not allowed")
	}
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if err := validatePublicAddr(addr); err != nil {
			return nil, err
		}
		return []netip.Addr{addr.Unmap()}, nil
	}

	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("web request: resolve host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("web request: host resolved to no addresses")
	}
	for _, addr := range addrs {
		if err := validatePublicAddr(addr); err != nil {
			return nil, err
		}
	}
	return addrs, nil
}

func validatePublicAddr(addr netip.Addr) error {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return fmt.Errorf("web request: address %s is not public", addr)
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("web request: address %s is not public", addr)
		}
	}
	return nil
}

func standardPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

func isLocalHostname(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func sensitiveQueryParameter(u *url.URL) string {
	for name := range u.Query() {
		switch strings.ToLower(strings.ReplaceAll(name, "-", "_")) {
		case "access_token", "api_key", "apikey", "authorization", "credential", "password", "secret", "signature", "sig", "token", "x_amz_signature", "x_goog_signature":
			return name
		}
	}
	return ""
}

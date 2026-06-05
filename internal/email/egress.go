package email

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const emailDialTimeout = 30 * time.Second

// Ranges that netip.IsPrivate does not cover but are still effectively internal:
// carrier-grade NAT (RFC 6598) and the NAT64 well-known prefix (RFC 6052).
var (
	cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
)

// ValidateAccountEgress rejects email server hosts that would make stellad
// connect to local or private infrastructure on behalf of an authenticated user.
func ValidateAccountEgress(acct EmailAccount) error {
	// Server-side egress reaches public hosts only (see validatePublicAddr), so
	// an unencrypted connection would put the account credentials on the wire in
	// cleartext. Reject TLS=none for the daemon path.
	if acct.IMAPTLS == "none" {
		return fmt.Errorf("imap_tls=none is not allowed for server-side email; use ssl or starttls")
	}
	if acct.SMTPTLS == "none" {
		return fmt.Errorf("smtp_tls=none is not allowed for server-side email; use ssl or starttls")
	}
	if _, err := ResolvePublicHost("imap", acct.IMAPHost); err != nil {
		return err
	}
	if _, err := ResolvePublicHost("smtp", acct.SMTPHost); err != nil {
		return err
	}
	return nil
}

// ResolvePublicHost resolves host and rejects any result that is not safe for
// server-side email egress. Callers must connect to the returned IPs, not resolve
// the original hostname again, or DNS rebinding reopens the SSRF hole.
func ResolvePublicHost(kind, host string) ([]netip.Addr, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("%s_host is required", kind)
	}
	if splitHost, _, err := net.SplitHostPort(host); err == nil && splitHost != "" {
		return nil, fmt.Errorf("%s_host must not include a port", kind)
	}

	literal := strings.Trim(host, "[]")
	if addr, err := netip.ParseAddr(literal); err == nil {
		addr = addr.Unmap()
		if err := validatePublicAddr(kind, host, addr); err != nil {
			return nil, err
		}
		return []netip.Addr{addr}, nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s_host %q: %w", kind, host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %s_host %q: no addresses", kind, host)
	}
	addrs := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return nil, fmt.Errorf("resolve %s_host %q: invalid address %q", kind, host, ip.String())
		}
		addr = addr.Unmap()
		if err := validatePublicAddr(kind, host, addr); err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func DialPublicTCP(ctx context.Context, kind, host string, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("%s_port must be between 1 and 65535", kind)
	}
	addrs, err := ResolvePublicHost(kind, host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: emailDialTimeout}
	portString := strconv.Itoa(port)
	var lastErr error
	for _, addr := range addrs {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), portString))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("dial %s_host %q: %w", kind, host, lastErr)
}

func validatePublicAddr(kind, host string, addr netip.Addr) error {
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() || cgnatPrefix.Contains(addr) || nat64Prefix.Contains(addr) {
		return fmt.Errorf("%s_host %q resolves to disallowed address %s", kind, host, addr)
	}
	return nil
}

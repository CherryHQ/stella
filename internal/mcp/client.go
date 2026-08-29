package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/diagnostic"
)

// clientImpl identifies Stella to MCP servers during the initialize handshake.
var clientImpl = &mcpsdk.Implementation{Name: "stella", Version: "0.1.0"}

// Client is a live connection to one external MCP server. It is safe to Close
// more than once.
type Client struct {
	session   *mcpsdk.ClientSession
	closeOnce sync.Once
}

// Connect opens an MCP session to the server described by reg, injecting the
// bearer token (may be empty) on every HTTP request. Only HTTP-based transports
// are built; an unsupported transport is rejected here rather than dialed.
func Connect(ctx context.Context, reg Registration, bearer string) (*Client, error) {
	transport, err := buildTransport(reg, bearer)
	if err != nil {
		return nil, connectionError(reg, err)
	}
	c := mcpsdk.NewClient(clientImpl, nil)
	session, err := c.Connect(ctx, transport, nil)
	if err != nil {
		return nil, connectionError(reg, err)
	}
	return &Client{session: session}, nil
}

// connectionFailure keeps the operational cause available to errors.Is/As,
// while Error deliberately excludes it: SDK and net/url errors can echo the
// full endpoint, including query credentials.
type connectionFailure struct {
	name     string
	endpoint string
	detail   string
	cause    error
}

func (e *connectionFailure) Error() string {
	if e.detail == "" {
		return fmt.Sprintf("mcp: connect %q (%s) failed", e.name, e.endpoint)
	}
	return fmt.Sprintf("mcp: connect %q (%s) failed: %s", e.name, e.endpoint, e.detail)
}

func (e *connectionFailure) Unwrap() error { return e.cause }

func connectionError(reg Registration, cause error) error {
	return &connectionFailure{
		name:     reg.Name,
		endpoint: diagnostic.Endpoint(reg.URL),
		detail:   safeValidationDetail(cause),
		cause:    cause,
	}
}

// safeValidationDetail retains only validation text known not to echo caller
// input. Transport, SDK, and new validation errors stay opaque until audited.
func safeValidationDetail(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, safe := range []string{
		"mcp: endpoint url must ",
		"mcp: endpoint url requires a host",
	} {
		if strings.HasPrefix(message, safe) {
			return message
		}
	}
	if strings.HasPrefix(message, "mcp: unsupported transport ") {
		return "mcp: unsupported transport; only streamable_http and sse are allowed"
	}
	return ""
}

// buildTransport returns the SDK transport for the registration. It is the
// single choke point that enforces "HTTP/SSE only": any transport other than
// streamable_http or sse is refused.
func buildTransport(reg Registration, bearer string) (mcpsdk.Transport, error) {
	if err := validateEndpointURL(reg.URL); err != nil {
		return nil, err
	}
	httpClient := safeHTTPClient(bearer)
	switch reg.Transport {
	case TransportStreamableHTTP:
		return &mcpsdk.StreamableClientTransport{Endpoint: reg.URL, HTTPClient: httpClient}, nil
	case TransportSSE:
		return &mcpsdk.SSEClientTransport{Endpoint: reg.URL, HTTPClient: httpClient}, nil
	default:
		return nil, fmt.Errorf("mcp: unsupported transport %q: only %q and %q are allowed (stdio is not supported)", reg.Transport, TransportStreamableHTTP, TransportSSE)
	}
}

// ListTools returns the tools the server currently advertises.
func (c *Client) ListTools(ctx context.Context) ([]*mcpsdk.Tool, error) {
	res, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: list tools: %w", err)
	}
	return res.Tools, nil
}

// CallTool proxies a tools/call for the remote tool name with the given args
// and flattens the result content to a single string for the model.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	res, err := c.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("mcp: call tool %q: %w", name, err)
	}
	text := flattenContent(res.Content)
	if res.IsError {
		return text, fmt.Errorf("mcp: tool %q returned an error: %s", name, text)
	}
	return text, nil
}

// Close ends the session. Idempotent so multiple tool wrappers can share one
// client and each safely Close it on registry teardown.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.session.Close() })
	return err
}

// flattenContent renders MCP content blocks as plain text. Text blocks are
// concatenated; non-text blocks are JSON-encoded so nothing is silently lost.
func flattenContent(content []mcpsdk.Content) string {
	var b strings.Builder
	for _, block := range content {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if tc, ok := block.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
			continue
		}
		if raw, err := json.Marshal(block); err == nil {
			b.Write(raw)
		}
	}
	return b.String()
}

// authRoundTripper injects a bearer token on every request. When the token is
// empty it is a transparent pass-through, so unauthenticated servers work too.
type authRoundTripper struct {
	base   http.RoundTripper
	bearer string
}

func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := a.base
	if base == nil {
		base = safeBaseTransport()
	}
	if err := validateEndpointURL(req.URL.String()); err != nil {
		return nil, err
	}
	if a.bearer != "" {
		// Clone before mutating: RoundTrippers must not modify the caller's request.
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	return base.RoundTrip(req)
}

func safeHTTPClient(bearer string) *http.Client {
	return &http.Client{
		Transport: &authRoundTripper{base: safeBaseTransport(), bearer: bearer},
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := validateEndpointURL(req.URL.String()); err != nil {
				return err
			}
			if len(via) == 0 || !sameOrigin(req.URL, via[0].URL) {
				return fmt.Errorf("mcp: redirect to a different origin is not allowed")
			}
			return nil
		},
	}
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func safeBaseTransport() http.RoundTripper {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = safeDialContext
	return base
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("mcp: invalid endpoint address %q: %w", address, err)
	}
	ips, err := resolveSafeHost(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	dialer := &net.Dialer{}
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func validateEndpointURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mcp: invalid endpoint url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("mcp: endpoint url must use http or https")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("mcp: endpoint url requires a host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("mcp: endpoint url must not include userinfo, query, or fragment")
	}
	if ip, err := parseIPLiteral(u.Hostname()); err == nil {
		if err := validatePublicIP(ip); err != nil {
			return err
		}
	}
	if isLocalHostname(u.Hostname()) {
		return fmt.Errorf("mcp: endpoint host %q is not allowed", u.Hostname())
	}
	return nil
}

func resolveSafeHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if isLocalHostname(host) {
		return nil, fmt.Errorf("mcp: endpoint host %q is not allowed", host)
	}
	if ip, err := parseIPLiteral(host); err == nil {
		if err := validatePublicIP(ip); err != nil {
			return nil, err
		}
		return []netip.Addr{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("mcp: resolve endpoint host %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("mcp: endpoint host %q resolved no addresses", host)
	}
	for _, ip := range addrs {
		if err := validatePublicIP(ip); err != nil {
			return nil, fmt.Errorf("mcp: endpoint host %q resolved to disallowed address %s: %w", host, ip, err)
		}
	}
	return addrs, nil
}

func parseIPLiteral(host string) (netip.Addr, error) {
	return netip.ParseAddr(strings.Trim(host, "[]"))
}

func validatePublicIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("mcp: endpoint address %s is not allowed", ip)
	}
	return nil
}

func isLocalHostname(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// toolInputSchema converts an MCP tool's input schema (any, typically
// map[string]any from the wire) into the map[string]any shape Stella's tool
// definitions use. A nil or unconvertible schema yields an empty object schema.
func toolInputSchema(schema any) map[string]any {
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	if schema == nil {
		return map[string]any{"type": "object"}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{"type": "object"}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return map[string]any{"type": "object"}
	}
	return m
}

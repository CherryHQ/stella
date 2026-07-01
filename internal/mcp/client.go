package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
		return nil, err
	}
	c := mcpsdk.NewClient(clientImpl, nil)
	session, err := c.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect %q (%s): %w", reg.Name, reg.URL, err)
	}
	return &Client{session: session}, nil
}

// buildTransport returns the SDK transport for the registration. It is the
// single choke point that enforces "HTTP/SSE only": any transport other than
// streamable_http or sse is refused.
func buildTransport(reg Registration, bearer string) (mcpsdk.Transport, error) {
	httpClient := &http.Client{Transport: &authRoundTripper{base: http.DefaultTransport, bearer: bearer}}
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
		base = http.DefaultTransport
	}
	if a.bearer != "" {
		// Clone before mutating: RoundTrippers must not modify the caller's request.
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	return base.RoundTrip(req)
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

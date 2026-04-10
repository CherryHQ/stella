package mcp

import (
	"context"
	"maps"
	"net/http"
	"os/exec"
	"sort"
	"time"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Session is the subset of MCP client session behavior used by Anna's runtime.
type Session interface {
	Close() error
	Wait() error
	ListTools(ctx context.Context, params *officialmcp.ListToolsParams) (*officialmcp.ListToolsResult, error)
	CallTool(ctx context.Context, params *officialmcp.CallToolParams) (*officialmcp.CallToolResult, error)
}

// DialFunc establishes one MCP client session for the provided server config.
type DialFunc func(ctx context.Context, server ServerConfig) (Session, error)

func defaultDial(ctx context.Context, server ServerConfig) (Session, error) {
	transport, err := newTransport(server)
	if err != nil {
		return nil, err
	}
	client := officialmcp.NewClient(&officialmcp.Implementation{Name: "anna", Version: "dev"}, nil)
	return client.Connect(ctx, transport, nil)
}

func newTransport(server ServerConfig) (officialmcp.Transport, error) {
	httpClient := newHTTPClient(server.TimeoutSeconds, server.Headers)
	switch server.Transport {
	case TransportStdio:
		cmd := exec.Command(server.Command, server.Args...)
		if len(server.Env) > 0 {
			cmd.Env = append(cmd.Environ(), flattenEnv(server.Env)...)
		}
		return &officialmcp.CommandTransport{Command: cmd}, nil
	case TransportSSE:
		return &officialmcp.SSEClientTransport{Endpoint: server.URL, HTTPClient: httpClient}, nil
	case TransportStreamableHTTP, TransportHTTP:
		return &officialmcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: httpClient}, nil
	default:
		return nil, ErrUnsupportedTransport{Transport: server.Transport}
	}
}

func newHTTPClient(timeoutSeconds int, headers map[string]string) *http.Client {
	client := &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}
	if len(headers) == 0 {
		return client
	}
	client.Transport = headerRoundTripper{base: http.DefaultTransport, headers: cloneHeaders(headers)}
	return client
}

func flattenEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(env))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	maps.Copy(cloned, headers)
	return cloned
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := req.Clone(req.Context())
	for key, value := range rt.headers {
		clone.Header.Set(key, value)
	}
	return base.RoundTrip(clone)
}

type ErrUnsupportedTransport struct {
	Transport string
}

func (e ErrUnsupportedTransport) Error() string {
	return "mcp: unsupported transport " + e.Transport
}

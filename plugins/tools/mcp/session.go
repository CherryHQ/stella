package mcp

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"sort"
	"sync"
	"time"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vaayne/anna/internal/sandbox"
)

// Session is the subset of MCP client session behavior used by Anna's runtime.
type Session interface {
	Close() error
	Wait() error
	ListTools(ctx context.Context, params *officialmcp.ListToolsParams) (*officialmcp.ListToolsResult, error)
	CallTool(ctx context.Context, params *officialmcp.CallToolParams) (*officialmcp.CallToolResult, error)
}

// DialFunc establishes one MCP client session for the provided server config.
type DialFunc func(ctx context.Context, server ServerConfig, host sandbox.Host) (Session, error)

func defaultDial(ctx context.Context, server ServerConfig, host sandbox.Host) (Session, error) {
	transport, err := newTransport(server, host)
	if err != nil {
		return nil, err
	}
	client := officialmcp.NewClient(&officialmcp.Implementation{Name: "anna", Version: "dev"}, nil)
	return client.Connect(ctx, transport, nil)
}

func newTransport(server ServerConfig, host sandbox.Host) (officialmcp.Transport, error) {
	httpClient := newHTTPClient(server.TimeoutSeconds, server.Headers)
	switch server.Transport {
	case TransportStdio:
		if host == nil {
			return nil, ErrSandboxHostRequired{Transport: server.Transport}
		}
		proc, err := host.StartProcess(context.Background(), sandbox.ProcessRequest{
			Path:    server.Command,
			Args:    append([]string(nil), server.Args...),
			Env:     cloneHeaders(server.Env),
			Timeout: time.Duration(server.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			return nil, err
		}
		closer := &sandboxProcessCloser{process: proc}
		return &officialmcp.IOTransport{
			Reader: &sandboxProcessReader{ReadCloser: proc.Stdout(), closer: closer},
			Writer: &sandboxProcessWriter{WriteCloser: proc.Stdin(), closer: closer},
		}, nil
	case TransportSSE:
		// EX-009: remote MCP dialing remains an explicit trust-boundary exception in Phase 6.
		sandbox.LogExceptionPath("EX-009", "plugins/tools/mcp", "network", "remote MCP "+server.Transport+" transport dials outside sandbox.Host mediation")
		return &officialmcp.SSEClientTransport{Endpoint: server.URL, HTTPClient: httpClient}, nil
	case TransportStreamableHTTP, TransportHTTP:
		// EX-009: remote MCP dialing remains an explicit trust-boundary exception in Phase 6.
		sandbox.LogExceptionPath("EX-009", "plugins/tools/mcp", "network", "remote MCP "+server.Transport+" transport dials outside sandbox.Host mediation")
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

type ErrSandboxHostRequired struct {
	Transport string
}

func (e ErrSandboxHostRequired) Error() string {
	return "mcp: transport " + e.Transport + " requires sandbox host mediation"
}

type sandboxProcessCloser struct {
	process sandbox.ProcessHandle
	once    sync.Once
	err     error
}

func (c *sandboxProcessCloser) Close() error {
	c.once.Do(func() {
		if c.process != nil {
			c.err = c.process.Close()
		}
	})
	return c.err
}

type sandboxProcessReader struct {
	io.ReadCloser
	closer *sandboxProcessCloser
}

func (r *sandboxProcessReader) Close() error {
	return errors.Join(r.ReadCloser.Close(), r.closer.Close())
}

type sandboxProcessWriter struct {
	io.WriteCloser
	closer *sandboxProcessCloser
}

func (w *sandboxProcessWriter) Close() error {
	return errors.Join(w.WriteCloser.Close(), w.closer.Close())
}

package mcp

import (
	"context"
	"net/http"
	"os/exec"
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
	httpClient := &http.Client{Timeout: time.Duration(server.TimeoutSeconds) * time.Second}
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

func flattenEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	return result
}

type ErrUnsupportedTransport struct {
	Transport string
}

func (e ErrUnsupportedTransport) Error() string {
	return "mcp: unsupported transport " + e.Transport
}

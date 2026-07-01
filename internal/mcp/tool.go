package mcp

import (
	"context"
	"sync"

	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// toolProxy adapts one remote MCP tool to Stella's Tool interface. The
// agent-facing name is namespaced by server (mcp__<server>__<tool>); calls are
// proxied to the tool's original remote name. A cached tool list may create the
// proxy without an open session, so Execute lazily connects on first use.
type toolProxy struct {
	mu sync.Mutex

	client  mcpClient
	connect connectFunc
	reg     Registration
	bearer  string

	def        pkgtools.Definition
	remoteName string
}

func (t *toolProxy) Definition() pkgtools.Definition { return t.def }

func (t *toolProxy) Execute(ctx context.Context, args map[string]any) (string, error) {
	client, err := t.ensureClient(ctx)
	if err != nil {
		return "", err
	}
	return client.CallTool(ctx, t.remoteName, args)
}

func (t *toolProxy) ensureClient(ctx context.Context) (mcpClient, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		return t.client, nil
	}
	client, err := t.connect(ctx, t.reg, t.bearer)
	if err != nil {
		return nil, err
	}
	t.client = client
	return client, nil
}

// Close ends the MCP session if this tool opened or inherited one. Client.Close
// is idempotent, so the registry may call it once per tool sharing a session.
func (t *toolProxy) Close() error {
	t.mu.Lock()
	client := t.client
	t.client = nil
	t.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Close()
}

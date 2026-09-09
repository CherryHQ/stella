package agent

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/tools"
)

// authorizedTool rechecks mutable policy at invocation time. Runner creation
// is only a visibility snapshot; a long-lived runner must not keep executing a
// tool after an administrator changes its override or native Agent admission.
// identity and nativeID are supplied by the trusted registry composition, so
// the guard never parses ownership from the exported model-facing name.
type authorizedTool struct {
	inner    tools.Tool
	identity ToolIdentity
	nativeID string
	native   *plugin.NativePolicy
	fetch    ToolOverrideFetcher
	userID   string
	agentID  string
}

func (t *authorizedTool) Definition() tools.Definition { return t.inner.Definition() }

func (t *authorizedTool) authorize(ctx context.Context) error {
	if t.nativeID != "" {
		if t.native == nil {
			return plugin.ErrNativePolicyUnavailable
		}
		allowed, err := t.native.Allows(ctx, t.nativeID, t.agentID)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("native plugin %s unavailable: %w", t.nativeID, authz.ErrForbidden)
		}
	}
	if t.fetch == nil {
		return nil
	}
	rows, err := t.fetch(ctx, t.userID, t.agentID)
	if err != nil {
		return fmt.Errorf("load tool override for %q: %w", t.identity, err)
	}
	if !FilterToolEnabled(true, t.identity, rows) {
		return fmt.Errorf("tool %q is disabled by override: %w", t.Definition().Name, authz.ErrForbidden)
	}
	return nil
}

func (t *authorizedTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if err := t.authorize(ctx); err != nil {
		return "", err
	}
	return t.inner.Execute(ctx, args)
}

// ExecuteContent deliberately remains an optional-interface-preserving
// forwarding method. The registry can therefore keep image/content results,
// while text-only tools retain the standard string-to-content conversion.
func (t *authorizedTool) ExecuteContent(ctx context.Context, args map[string]any) ([]ai.ContentBlock, error) {
	if err := t.authorize(ctx); err != nil {
		return nil, err
	}
	return tools.ExecuteToolContent(ctx, t.inner, args)
}

func (t *authorizedTool) Close() error {
	if closer, ok := t.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func wrapAuthorizedTool(tool tools.Tool, identity ToolIdentity, nativeID string, native *plugin.NativePolicy, fetch ToolOverrideFetcher, userID, agentID string) tools.Tool {
	if tool == nil || (nativeID == "" && fetch == nil) {
		return tool
	}
	return &authorizedTool{
		inner: tool, identity: identity, nativeID: nativeID, native: native,
		fetch: fetch, userID: userID, agentID: agentID,
	}
}

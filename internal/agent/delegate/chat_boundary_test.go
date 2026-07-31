package delegate

import (
	"context"
	"slices"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/pkg/tools"
)

// capturingRunner records what a delegate run was actually handed.
type capturingRunner struct {
	ctx      context.Context //nolint:containedctx // the captured value is the assertion
	excluded []string
}

func (r *capturingRunner) RunDelegateSession(ctx context.Context, req SessionRunRequest) (SessionRunResult, error) {
	r.ctx = ctx
	r.excluded = req.ExcludedTools
	return SessionRunResult{SessionID: req.SessionID, Output: "done", Complete: true}, nil
}

type stubTool struct{ name string }

func (t stubTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (t stubTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

func registryWith(names ...string) *tools.Registry {
	r := tools.NewRegistry()
	for _, name := range names {
		r.Register(stubTool{name: name})
	}
	return r
}

// TestDelegateRunDropsParentChatBinding covers the chat -> delegate boundary. A
// delegate inherits the parent turn's context, and when the parent is a Telegram
// or group turn that context proves a durable chat binding — the authority that
// addresses that chat's live session. A delegate's task text can come from a
// tool result, so inheriting it would let untrusted content act on the
// conversation that spawned the run.
func TestDelegateRunDropsParentChatBinding(t *testing.T) {
	runner := &capturingRunner{}
	tool := NewDelegateTool(DelegateConfig{SessionRunner: runner, Registry: registryWith("read_file")})

	parent := agentctx.WithChatBinding(context.Background(), agentctx.ChatBinding{
		Channel:    "group:g1",
		SessionKey: "agent:group:g1",
	})
	if _, ok := agentctx.ChatBindingFromContext(parent); !ok {
		t.Fatal("parent context must carry the binding this test is about")
	}

	if res := tool.runDelegate(parent, delegateTaskConfig{ID: "t1", Task: "summarize"}); res.Error != "" {
		t.Fatalf("runDelegate: %s", res.Error)
	}
	if runner.ctx == nil {
		t.Fatal("delegate never ran")
	}
	if binding, ok := agentctx.ChatBindingFromContext(runner.ctx); ok {
		t.Fatalf("delegate inherited the parent chat binding %+v", binding)
	}
}

// TestDelegateAlwaysExcludesDelegate pins the recursion guard: a delegate can
// never spawn another delegate. A preset whitelist only hides more tools, so
// naming "delegate" in one must not re-admit it.
func TestDelegateAlwaysExcludesDelegate(t *testing.T) {
	reg := registryWith("read_file", delegateToolName)
	tool := NewDelegateTool(DelegateConfig{SessionRunner: &capturingRunner{}, Registry: reg})

	for _, tc := range []struct {
		name         string
		whitelist    []string
		hasWhitelist bool
	}{
		{name: "no whitelist"},
		{name: "whitelist of other tools", whitelist: []string{"read_file"}, hasWhitelist: true},
		{name: "whitelist naming the blocked tool", whitelist: []string{delegateToolName}, hasWhitelist: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			excluded := tool.excludedTools(tc.whitelist, tc.hasWhitelist)
			if !slices.Contains(excluded, delegateToolName) {
				t.Fatalf("excluded = %v, want it to contain %q", excluded, delegateToolName)
			}
		})
	}
}

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/toolmeta"
	"github.com/CherryHQ/stella/pkg/tools"
)

type authorizedContentTool struct {
	closed       int
	textCalls    int
	contentCalls int
}

func (t *authorizedContentTool) Definition() tools.Definition {
	return tools.Definition{Name: "content"}
}

func (t *authorizedContentTool) Execute(context.Context, map[string]any) (string, error) {
	t.textCalls++
	return "text", nil
}

func (t *authorizedContentTool) ExecuteContent(context.Context, map[string]any) ([]ai.ContentBlock, error) {
	t.contentCalls++
	return []ai.ContentBlock{ai.ImageContent{Data: "bytes", MimeType: "image/png"}}, nil
}

func (t *authorizedContentTool) Close() error {
	t.closed++
	return nil
}

func TestAuthorizedToolPreservesContentAndClose(t *testing.T) {
	inner := &authorizedContentTool{}
	identity := ToolIdentity{CoreToolName: "content"}
	var rows []ToolOverride
	reads := 0
	guarded := wrapAuthorizedTool(inner, identity, "", nil, func(_ context.Context, userID, agentID string) ([]ToolOverride, error) {
		if userID != "user" || agentID != "agent" {
			t.Fatalf("policy owner = %q/%q, want user/agent", userID, agentID)
		}
		reads++
		return rows, nil
	}, "user", "agent")
	if _, ok := guarded.(*authorizedTool); !ok {
		t.Fatal("test must exercise the authorization wrapper")
	}
	blocks, err := tools.ExecuteToolContent(t.Context(), guarded, nil)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("content = %#v, err=%v", blocks, err)
	}
	if image, ok := blocks[0].(ai.ImageContent); !ok || image.Data != "bytes" || image.MimeType != "image/png" {
		t.Fatalf("content block = %#v", blocks[0])
	}
	if result, err := guarded.Execute(t.Context(), nil); err != nil || result != "text" {
		t.Fatalf("text = %q, err=%v", result, err)
	}
	rows = []ToolOverride{{Identity: identity, Scope: ToolOverrideScopeSystem, Enabled: false}}
	if _, err := tools.ExecuteToolContent(t.Context(), guarded, nil); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("content after deny = %v, want forbidden", err)
	}
	if _, err := guarded.Execute(t.Context(), nil); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("text after deny = %v, want forbidden", err)
	}
	if reads != 4 || inner.contentCalls != 1 || inner.textCalls != 1 {
		t.Fatalf("reads/content/text calls = %d/%d/%d, want 4/1/1", reads, inner.contentCalls, inner.textCalls)
	}
	closer, ok := guarded.(interface{ Close() error })
	if !ok {
		t.Fatal("authorized tool lost Close")
	}
	if err := closer.Close(); err != nil || inner.closed != 1 {
		t.Fatalf("close err=%v closed=%d", err, inner.closed)
	}
}

type invocationNativeStore struct {
	plugin.NativeStore // Every unused persistence method must remain uncalled.
	nativeID           string
	enabled            bool
	denied             map[string]bool
	err                error
}

func (s *invocationNativeStore) GetNativeAdmission(_ context.Context, nativeID, agentID string) (bool, bool, bool, error) {
	wantID := s.nativeID
	if wantID == "" {
		wantID = "system/scheduler"
	}
	if nativeID != wantID {
		return false, false, false, errors.New("unexpected native identity")
	}
	return s.enabled, true, s.denied[agentID], s.err
}

func TestHostToolMetadataReachesInvocationAdmission(t *testing.T) {
	const (
		toolName = "host__owned"
		nativeID = "tool/host"
	)
	store := &invocationNativeStore{nativeID: nativeID, enabled: true, denied: make(map[string]bool)}
	policy := plugin.NewNativePolicy(store, plugin.NativeRegistryMap{nativeID: true})
	rows := toolmeta.NewRegistry(toolmeta.ActionTool{Name: toolName, PluginID: nativeID, LocalName: toolName})
	calls := 0
	cfg := failClosedConfig(t)
	cfg.NativePolicy = policy
	cfg.ToolMetaRegistry = rows
	cfg.PluginTools = func(context.Context, pkgplugins.ToolBuildContext) ([]tools.Tool, error) {
		return []tools.Tool{countingTool{name: toolName, calls: &calls}}, nil
	}
	registry, _, _, err := buildToolRegistry(t.Context(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatalf("build host tool: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if _, err := registry.Execute(t.Context(), toolName, nil); err != nil {
		t.Fatalf("initial host tool call: %v", err)
	}
	store.denied[cfg.BuiltinParams.AgentID] = true
	if _, err := registry.Execute(t.Context(), toolName, nil); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("host tool after Agent deny = %v, want forbidden", err)
	}
	store.denied[cfg.BuiltinParams.AgentID] = false
	store.enabled = false
	if _, err := registry.Execute(t.Context(), toolName, nil); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("host tool after global deny = %v, want forbidden", err)
	}
	if calls != 1 {
		t.Fatalf("host tool calls = %d, want 1", calls)
	}
}

func TestBuiltNativeToolsRecheckGlobalAndAgentAdmission(t *testing.T) {
	const toolName = "scheduler__job_list"
	store := &invocationNativeStore{enabled: true, denied: make(map[string]bool)}
	policy := plugin.NewNativePolicy(store, plugin.NativeRegistryMap{"system/scheduler": true})
	registries := make(map[string]*tools.Registry)
	callCounts := make(map[string]*int)
	for _, agentID := range []string{"agent-a", "agent-b"} {
		cfg := failClosedConfig(t)
		cfg.BuiltinParams.AgentID = agentID
		cfg.NativePolicy = policy
		cfg.ToolMetaRegistry = toolmeta.NewRegistry(toolmeta.ActionTool{
			Name: toolName, PluginID: "system/scheduler", LocalName: "job_list",
		})
		calls := new(int)
		callCounts[agentID] = calls
		cfg.BuiltinTools = []BuiltinTool{{
			Tool: countingTool{name: toolName, calls: calls},
			Available: func(ctx context.Context, params RunnerParams) (bool, error) {
				return policy.Allows(ctx, "system/scheduler", params.AgentID)
			},
		}}
		reg, _, _, err := buildToolRegistry(t.Context(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
		if err != nil {
			t.Fatalf("build %s: %v", agentID, err)
		}
		t.Cleanup(func() {
			if err := reg.Close(); err != nil {
				t.Error(err)
			}
		})
		registries[agentID] = reg
		if _, err := reg.Execute(t.Context(), toolName, nil); err != nil {
			t.Fatalf("initial call for %s: %v", agentID, err)
		}
	}

	store.denied["agent-a"] = true
	if _, err := registries["agent-a"].Execute(t.Context(), toolName, nil); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Agent A after deny = %v, want forbidden", err)
	}
	if _, err := registries["agent-b"].Execute(t.Context(), toolName, nil); err != nil {
		t.Fatalf("Agent A deny affected Agent B: %v", err)
	}
	if *callCounts["agent-a"] != 1 || *callCounts["agent-b"] != 2 {
		t.Fatalf("calls after Agent deny = %d/%d, want 1/2", *callCounts["agent-a"], *callCounts["agent-b"])
	}

	store.enabled = false
	for agentID, reg := range registries {
		if _, err := reg.Execute(t.Context(), toolName, nil); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("%s after global deny = %v, want forbidden", agentID, err)
		}
	}
	store.enabled = true
	delete(store.denied, "agent-a")
	for agentID, reg := range registries {
		if _, err := reg.Execute(t.Context(), toolName, nil); err != nil {
			t.Fatalf("%s after restoring admission: %v", agentID, err)
		}
	}
	store.err = errors.New("native policy read failed")
	for agentID, reg := range registries {
		if _, err := reg.Execute(t.Context(), toolName, nil); !errors.Is(err, store.err) {
			t.Fatalf("%s after read failure = %v, want storage error", agentID, err)
		}
	}
	if *callCounts["agent-a"] != 2 || *callCounts["agent-b"] != 3 {
		t.Fatalf("final calls = %d/%d, want 2/3", *callCounts["agent-a"], *callCounts["agent-b"])
	}
}

func TestAuthorizedToolFailsClosedOnOverrideReadFailure(t *testing.T) {
	inner := &authorizedContentTool{}
	readErr := errors.New("override read failed")
	guarded := wrapAuthorizedTool(inner, ToolIdentity{CoreToolName: "content"}, "", nil,
		func(context.Context, string, string) ([]ToolOverride, error) { return nil, readErr }, "user", "agent")
	if _, err := guarded.Execute(t.Context(), nil); !errors.Is(err, readErr) {
		t.Fatalf("text error = %v, want read failure", err)
	}
	if _, err := tools.ExecuteToolContent(t.Context(), guarded, nil); !errors.Is(err, readErr) {
		t.Fatalf("content error = %v, want read failure", err)
	}
	if inner.textCalls != 0 || inner.contentCalls != 0 {
		t.Fatal("inner tool ran after override read failure")
	}
}

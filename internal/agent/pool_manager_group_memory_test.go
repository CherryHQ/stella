package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestStructuredGroupBeforeRunKeepsGroupOutOfPluginUserIdentity(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	var pluginBuild pkgplugins.BeforeRunContext
	pm := &PoolManager{
		structuredGroupMemory: true,
		groupFactPromptLoader: func(_ context.Context, gotGroupID, system string) (string, error) {
			if gotGroupID != groupID {
				t.Fatalf("Group Fact loader group_id = %q, want %q", gotGroupID, groupID)
			}
			return system + "\nGroup Facts", nil
		},
		beforeRunBuilder: func(_ context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error) {
			pluginBuild = build
			return pkgplugins.BeforeRunResult{SystemPrompt: "plugin replacement"}, nil
		},
	}
	beforeRun := pm.runtimeBeforeRunFunc(&config.Snapshot{})

	got, err := beforeRun(context.Background(), session.Info{
		ID:      "agent-1:group:" + groupID,
		UserID:  groupID,
		AgentID: "agent-1",
		GroupID: groupID,
	}, "model", "hello", "base", []ai.Message{})
	if err != nil {
		t.Fatalf("before run: %v", err)
	}
	if pluginBuild.UserID != "" {
		t.Fatalf("plugin UserID = %q, want empty for group session", pluginBuild.UserID)
	}
	if pluginBuild.GroupID != groupID {
		t.Fatalf("plugin GroupID = %q, want %q", pluginBuild.GroupID, groupID)
	}
	if pluginBuild.SystemPrompt != "base" {
		t.Fatalf("plugin system prompt = %q, want unmodified base prompt", pluginBuild.SystemPrompt)
	}
	if got != "plugin replacement\nGroup Facts" {
		t.Fatalf("final system prompt = %q, want plugin result followed by Group Facts", got)
	}
}

func TestDirectMessageBeforeRunKeepsPluginUserIdentity(t *testing.T) {
	var pluginBuild pkgplugins.BeforeRunContext
	pm := &PoolManager{
		beforeRunBuilder: func(_ context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error) {
			pluginBuild = build
			return pkgplugins.BeforeRunResult{SystemPrompt: build.SystemPrompt}, nil
		},
	}
	beforeRun := pm.runtimeBeforeRunFunc(&config.Snapshot{})

	_, err := beforeRun(context.Background(), session.Info{
		ID:      "dm-1",
		UserID:  "user-1",
		AgentID: "agent-1",
	}, "model", "hello", "base", nil)
	if err != nil {
		t.Fatalf("before run: %v", err)
	}
	if pluginBuild.UserID != "user-1" || pluginBuild.GroupID != "" {
		t.Fatalf("plugin identity = user %q group %q, want user-1 and empty group", pluginBuild.UserID, pluginBuild.GroupID)
	}
}

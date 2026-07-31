package access

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type promptTestAgents struct{}

func (promptTestAgents) GetPromptAgent(context.Context, string) (PromptAgent, error) {
	return PromptAgent{SystemPrompt: "test", Workspace: "/agent-definition/a1"}, nil
}

type promptTestProjects struct{}

func (promptTestProjects) ProjectRoot(context.Context, string, string) (string, error) {
	return "", nil
}

type promptTestPlugins struct {
	build *pkgplugins.SystemPromptContext
}

func (p promptTestPlugins) SessionPluginView(context.Context) (pkgplugins.SessionPluginView, error) {
	return pkgplugins.SessionPluginView{}, nil
}

func (p promptTestPlugins) SystemPromptSections(_ context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
	*p.build = build
	return nil, nil
}
func (promptTestPlugins) ManifestPluginPrompts() []pkgplugins.SystemPromptSection { return nil }

func TestAuthorizedPromptUsesPrincipalWorkspace(t *testing.T) {
	stellaHome := t.TempDir()
	for _, tt := range []struct {
		name string
		info session.Info
		want string
	}{
		{name: "personal", info: session.Info{UserID: "u1", AgentID: "a1"}, want: filepath.Join(stellaHome, "users", "u1", "agents", "a1")},
		{name: "group", info: session.Info{UserID: "g1", GroupID: "g1", AgentID: "a1"}, want: filepath.Join(stellaHome, "users", "group-g1", "agents", "a1")},
		{name: "user-less", info: session.Info{AgentID: "a1"}, want: "/agent-definition/a1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var build pkgplugins.SystemPromptContext
			builder, err := NewSystemPromptBuilder(SystemPromptDeps{
				StellaHome: stellaHome,
				Memory:     memorytest.New(),
				Agents:     promptTestAgents{},
				Projects:   promptTestProjects{},
				Workspace:  AgentPromptWorkspace{},
				Plugins:    promptTestPlugins{build: &build},
				SkillStore: agentSkillStore{},
				Skills: func(context.Context, pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
					return pkgplugins.SystemPromptSection{}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := builder.BuildSessionSystemPrompt(context.Background(), SystemPromptBuildInput{Info: tt.info}); err != nil {
				t.Fatal(err)
			}
			if build.WorkspaceRoot != tt.want {
				t.Errorf("WorkspaceRoot = %q, want %q", build.WorkspaceRoot, tt.want)
			}
			if tt.info.GroupID != "" {
				if build.UserRoot != agent.GroupHomeDir(stellaHome, tt.info.GroupID) {
					t.Errorf("UserRoot = %q, want group root", build.UserRoot)
				}
			}
		})
	}
}

// agentSkillStore is inert: this test exercises prompt workspace resolution,
// and the injected Skills builder prevents any store access.
type agentSkillStore struct{}

func (agentSkillStore) List(context.Context, pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	return nil, nil
}

func (agentSkillStore) Resolve(context.Context, string, pkgplugins.SkillViewContext) (*pkgplugins.Skill, error) {
	return nil, nil
}

func (agentSkillStore) ListByScope(context.Context, string, string, string) ([]pkgplugins.Skill, error) {
	return nil, nil
}
func (agentSkillStore) LoadFile(context.Context, string, string) (string, error) { return "", nil }
func (agentSkillStore) ListFiles(context.Context, string) ([]string, error)      { return nil, nil }
func (agentSkillStore) Create(context.Context, pkgplugins.Skill, map[string]string) (string, error) {
	return "", nil
}
func (agentSkillStore) Update(context.Context, string, pkgplugins.SkillUpdatePatch) error { return nil }
func (agentSkillStore) UpsertFile(context.Context, string, string, string) error          { return nil }
func (agentSkillStore) DeleteFile(context.Context, string, string) error                  { return nil }
func (agentSkillStore) Delete(context.Context, string) error                              { return nil }

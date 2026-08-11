package access

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type promptTestAgents struct{}

func (promptTestAgents) GetPromptAgent(context.Context, string) (PromptAgent, error) {
	return PromptAgent{SystemPrompt: "test", Workspace: "/agent-definition/a1"}, nil
}

type promptTestProjects struct{ descriptor agent.ProjectDescriptor }

type promptTestWorkspace struct{ root string }

func (w promptTestWorkspace) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	if req.GroupID != "" {
		principal := filepath.Join(w.root, "users", "group-"+req.GroupID)
		return home.WorkspaceView{PrincipalRoot: principal, AgentRoot: filepath.Join(principal, "agents", req.AgentID)}, nil
	}
	if req.UserID != "" {
		principal := filepath.Join(w.root, "users", req.UserID)
		return home.WorkspaceView{PrincipalRoot: principal, AgentRoot: filepath.Join(principal, "agents", req.AgentID)}, nil
	}
	return home.WorkspaceView{}, nil
}

func (w promptTestWorkspace) OpenRoot(ctx context.Context, req home.WorkspaceRequest, scope home.RootScope, _ home.RootAccess) (home.RootOperations, error) {
	view, err := w.WorkspaceView(ctx, req)
	if err != nil {
		return nil, err
	}
	dir := view.AgentRoot
	if scope == home.RootPrincipalData {
		dir = view.DataRoot
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return testRootOperations{Root: root}, nil
}

func (p promptTestProjects) ResolveProject(context.Context, string, string, string) (agent.ProjectDescriptor, error) {
	if p.descriptor.ID == "" {
		return agent.ProjectDescriptor{}, agent.ErrProjectNotFound
	}
	return p.descriptor, nil
}

func TestPromptPreviewUsesAuthorizedRootToLeafProjectContextWithoutHostPath(t *testing.T) {
	stellaHome := t.TempDir()
	root := filepath.Join(stellaHome, "users", "u1", "agents", "a1")
	project := filepath.Join(root, "projects", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		filepath.Join(root, "AGENTS.md"):    "preview root instructions",
		filepath.Join(project, "AGENTS.md"): "preview project instructions",
	} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var build pkgplugins.SystemPromptContext
	builder, err := NewSystemPromptBuilder(SystemPromptDeps{
		StellaHome: stellaHome,
		Memory:     memorytest.New(),
		Agents:     promptTestAgents{},
		Projects:   promptTestProjects{descriptor: agent.ProjectDescriptor{ID: "p1", UserID: "u1", AgentID: "a1", Path: "projects/app"}}.ResolveProject,
		Workspace:  promptTestWorkspace{root: stellaHome},
		Plugins:    promptTestPlugins{build: &build},
		SkillStore: agentSkillStore{},
		Skills: func(context.Context, pkgplugins.SystemPromptContext) (pkgplugins.SystemPromptSection, error) {
			return pkgplugins.SystemPromptSection{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := builder.BuildSessionSystemPrompt(context.Background(), SystemPromptBuildInput{Info: session.Info{UserID: "u1", AgentID: "a1", ProjectID: "p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "preview root instructions") || !strings.Contains(got, "preview project instructions") || strings.Contains(got, stellaHome) {
		t.Fatalf("preview prompt lacks logical root-to-leaf context or leaks host path:\n%s", got)
	}
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
				Projects:   promptTestProjects{}.ResolveProject,
				Workspace:  promptTestWorkspace{root: stellaHome},
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

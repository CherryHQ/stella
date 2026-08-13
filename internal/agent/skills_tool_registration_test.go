package agent

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/ai"
)

type oneSkillRuntime struct {
	emptySkillRuntime
	skill skillstool.Skill
}

func (r oneSkillRuntime) ListIdentityVisible(context.Context, skillstool.ViewContext) ([]skillstool.Skill, error) {
	return []skillstool.Skill{r.skill}, nil
}

func (r oneSkillRuntime) LoadCurrentRevision(context.Context, skillstool.Skill) (skillstool.ManagedRevision, error) {
	return skillstool.ManagedRevision{
		Skill: r.skill,
		Files: map[string][]byte{skillstool.MainFile: []byte("# Private")},
		Modes: map[string]fs.FileMode{skillstool.MainFile: 0o444},
	}, nil
}

type recordingSkillReadAuthorizer struct{ calls int }

func (a *recordingSkillReadAuthorizer) BeginRead(context.Context) (skillstool.SkillReadDecision, error) {
	return a, nil
}

func (a *recordingSkillReadAuthorizer) AllowRead(context.Context, string, string, string, string) (bool, error) {
	a.calls++
	return false, nil
}

func TestBuildToolRegistryRegistersAuthorizedReadOnlySkillsTool(t *testing.T) {
	home := t.TempDir()
	identity := skillstool.Skill{
		ID:            "private",
		Scope:         "user",
		UserID:        "user-1",
		Name:          "private-runbook",
		Description:   "private incident response",
		Status:        skillstool.SkillStatusActive,
		ContentDigest: strings.Repeat("a", 64),
	}
	authorizer := &recordingSkillReadAuthorizer{}
	reg, _, _, err := buildToolRegistry(t.Context(), runnerConfig{
		Sandbox: sandbox.Config{Paths: sandbox.Paths{
			StellaHome: home,
			AgentRoot:  filepath.Join(home, "agents", "agent-1"),
			UserRoot:   filepath.Join(home, "users", "user-1"),
		}},
		BuiltinParams:       RunnerParams{UserID: identity.UserID, AgentID: "agent-1"},
		SkillRevisionReader: oneSkillRuntime{skill: identity},
		SkillReadAuthorizer: authorizer,
	}, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	if !reg.Has("skills") {
		t.Fatal("skills tool is not registered")
	}
	out, err := reg.Execute(t.Context(), "skills", map[string]any{"action": "search_installed", "query": "incident response"})
	if err != nil {
		t.Fatal(err)
	}
	if authorizer.calls != 1 || out != "No installed skills found." {
		t.Fatalf("denied Skill search = %q with %d authorization calls", out, authorizer.calls)
	}
	if _, err := reg.Execute(t.Context(), "skills", map[string]any{"action": "install"}); err == nil {
		t.Fatal("agent-facing skills tool accepted a management action")
	}
}

package agent

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	skillstool "github.com/CherryHQ/stella/internal/skill"
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
	for _, spec := range skillstool.RuntimeActionTools() {
		name := spec.Name
		if !reg.Has(name) {
			t.Fatalf("%s tool is not registered", name)
		}
	}
	out, err := reg.Execute(t.Context(), "skill_installed_search", map[string]any{"q": "incident response"})
	if err != nil {
		t.Fatal(err)
	}
	if authorizer.calls != 1 || out != "No installed skills found." {
		t.Fatalf("denied Skill search = %q with %d authorization calls", out, authorizer.calls)
	}
	// The read authorizer gates both split names, not just the search: a denied
	// identity Skill must not come back through skill_load either.
	if out, err := reg.Execute(t.Context(), "skill_load", map[string]any{"name": identity.Name}); err == nil || strings.Contains(out, "Private") {
		t.Fatalf("denied Skill load out=%q err=%v, want a refusal with no content", out, err)
	}
	if authorizer.calls != 2 {
		t.Fatalf("authorization calls = %d, want one per split tool", authorizer.calls)
	}
	// Skill management stays on the HTTP API: no management action is projected
	// to the model, and the retired union name is gone from the registry.
	for _, absent := range []string{"skills", "skill_install", "settings_skill_update", "settings_skill_delete"} {
		if reg.Has(absent) {
			t.Fatalf("agent-facing registry exposes %q", absent)
		}
	}
}

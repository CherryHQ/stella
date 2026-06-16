package skills

import (
	"path/filepath"
	"strings"
	"testing"
)

// isolatingView mirrors what runner_builder builds for the bwrap backend.
func isolatingView() SkillDirView {
	return SkillDirView{
		Isolated:           true,
		SystemSkillsHost:   "/srv/.stella/.agents/skills",
		SystemSkillsView:   "/opt/stella/.agents/skills",
		AgentSkillsHost:    "/srv/.stella/agents/a1/.agents/skills",
		AgentSkillsView:    "/opt/stella/agent-skills",
		SystemDBSkillsHost: "/srv/.stella/.agents/db-skills",
		SystemDBSkillsView: "/opt/stella/db-skills",
		UserDataHost:       "/srv/.stella/users/u1/data",
		UserDataView:       "/user",
		WorkspaceHost:      "/srv/.stella/users/u1/agents/a1",
		WorkspaceView:      "/workspace",
	}
}

func TestSkillDirView_remapsReachableTiers(t *testing.T) {
	v := isolatingView()
	cases := []struct {
		name string
		host string
		want string
	}{
		{"system", "/srv/.stella/.agents/skills/foo", "/opt/stella/.agents/skills/foo"},
		{"user", "/srv/.stella/users/u1/data/.agents/skills/bar", "/user/.agents/skills/bar"},
		{"project", "/srv/.stella/users/u1/agents/a1/projects/p1/.agents/skills/baz", "/workspace/projects/p1/.agents/skills/baz"},
		{"workspace-root", "/srv/.stella/users/u1/agents/a1/.agents/skills/qux", "/workspace/.agents/skills/qux"},
		{"agent", "/srv/.stella/agents/a1/.agents/skills/agentlevel", "/opt/stella/agent-skills/agentlevel"},
		{"system-db", "/srv/.stella/.agents/db-skills/installed", "/opt/stella/db-skills/installed"},
	}
	for _, c := range cases {
		if got := v.apply(c.host); got != c.want {
			t.Errorf("%s: apply(%q) = %q, want %q", c.name, c.host, got, c.want)
		}
	}
}

// An isolating backend must never emit a host path: a skill dir under no mounted
// root is dropped, not leaked.
func TestSkillDirView_isolatedOmitsUnmappedHostPath(t *testing.T) {
	v := isolatingView()
	got := v.apply("/srv/.stella/secrets/.agents/skills/leaky")
	if got != "" {
		t.Fatalf("unmapped dir on isolating backend = %q, want \"\" (omitted)", got)
	}
}

// A sibling-escaping path must not be mistaken for a child of a root.
func TestSkillDirView_rejectsSiblingPrefix(t *testing.T) {
	v := isolatingView()
	// /user data root is "/srv/.stella/users/u1/data"; a sibling "data-evil" shares
	// the string prefix but is not under the root.
	got := v.apply("/srv/.stella/users/u1/data-evil/.agents/skills/x")
	if strings.HasPrefix(got, "/user") {
		t.Fatalf("sibling path mismapped under /user: %q", got)
	}
	if got != "" {
		t.Fatalf("sibling path on isolating backend = %q, want \"\" (omitted)", got)
	}
}

// The zero value (host-execution / non-sandbox callers) passes host paths through.
func TestSkillDirView_identityPassthrough(t *testing.T) {
	var v SkillDirView // zero value: Isolated=false, no roots
	host := filepath.Join("/home", "x", ".agents", "skills", "foo")
	if got := v.apply(host); got != host {
		t.Errorf("identity apply(%q) = %q, want unchanged", host, got)
	}
}

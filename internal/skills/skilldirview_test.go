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
		BuiltinSkillsHost:  "/srv/.stella/bundles/revision",
		BuiltinSkillsView:  "/opt/stella/skills/builtin",
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
		{"builtin", "/srv/.stella/bundles/revision/system/foo", "/opt/stella/skills/builtin/system/foo"},
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

func TestSkillDirView_homeDirectoriesUseRelativeCatalogCoordinates(t *testing.T) {
	cases := []struct {
		scope, relative, want string
	}{
		{"system", ".stella-revisions/system/a", "/opt/stella/db-skills/.stella-revisions/system/a"},
		{"system_agent", "agent", "/opt/stella/agent-skills/agent"},
		{"user", "user", "/user/.agents/skills/user"},
		{"user_agent", "agent", "/workspace/.agents/skills/agent"},
	}
	for _, tc := range cases {
		if got := isolatingView().homeDirectory(tc.scope, tc.relative); got != tc.want {
			t.Errorf("isolated %s = %q, want %q", tc.scope, got, tc.want)
		}
	}
	nonIsolated := isolatingView()
	nonIsolated.Isolated = false
	nonIsolated.SystemDBSkillsView = `C:\stella\skills`
	if got := nonIsolated.homeDirectory("system", "a/b"); got != filepath.Join(`C:\stella\skills`, "a", "b") {
		t.Fatalf("non-isolated directory = %q", got)
	}
	nonIsolated.UserDataView = `C:\stella\user`
	if got := nonIsolated.homeDirectory("user", "a/b"); got != filepath.Join(`C:\stella\user`, ".agents", "skills", "a", "b") {
		t.Fatalf("non-isolated user directory = %q", got)
	}
	for _, invalid := range []string{"", "/host/private", "../escape", "a/../../escape", `C:\host\private`, "C:/host/private", `a\escape`} {
		if got := isolatingView().homeDirectory("user", invalid); got != "" {
			t.Errorf("invalid relative %q mapped to %q", invalid, got)
		}
	}
	if got := isolatingView().homeDirectory("unknown", "a"); got != "" {
		t.Fatalf("unknown scope mapped to %q", got)
	}
}

func TestSkillDirView_homeDirectoriesFailClosedForEmptyUserMappings(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		for _, scope := range []string{"user", "user_agent"} {
			view := isolatingView()
			view.Isolated = isolated
			if scope == "user" {
				view.UserDataView = ""
			} else {
				view.WorkspaceView = ""
			}
			if got := view.homeDirectory(scope, "revision"); got != "" {
				t.Errorf("isolated=%t %s empty mapping = %q, want omitted", isolated, scope, got)
			}
		}
	}
}

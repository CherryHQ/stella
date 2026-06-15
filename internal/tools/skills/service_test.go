package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// writeFSSystemSkill drops a filesystem system skill under stellaHome.
func writeFSSystemSkill(t *testing.T, stellaHome, name, description string) {
	t.Helper()
	dir := filepath.Join(stellaHome, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, pkgplugins.SkillMainFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func mustCreateDBSkill(t *testing.T, store *testSkillStore, sk pkgplugins.Skill) {
	t.Helper()
	files := map[string]string{pkgplugins.SkillMainFile: "---\nname: " + sk.Name + "\ndescription: x\n---\n"}
	if _, err := store.Create(context.Background(), sk, files); err != nil {
		t.Fatalf("create db skill %s/%s: %v", sk.Scope, sk.Name, err)
	}
}

// TestResolveScopedExactScope guards CR-002: ResolveScoped must return the row
// for the exact scope asked for, even when a higher-precedence scope holds the
// same name and would shadow it in the effective (store.Resolve) query.
func TestResolveScopedExactScope(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	svc := NewService(store, t.TempDir())
	ctx := context.Background()
	vc := pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}

	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "user", Name: "dup", UserID: userID})
	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "user_agent", Name: "dup", UserID: userID, AgentID: agentID})
	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "system_agent", Name: "dup", AgentID: agentID})

	for _, scope := range []string{"user", "user_agent", "system_agent"} {
		rs, err := svc.ResolveScoped(ctx, "dup", scope, vc, "")
		if err != nil {
			t.Fatalf("ResolveScoped(%s): %v", scope, err)
		}
		if rs == nil {
			t.Fatalf("ResolveScoped(%s) = nil, want the %s row", scope, scope)
		}
		if rs.Scope != scope {
			t.Fatalf("ResolveScoped(%s).Scope = %s, want %s", scope, rs.Scope, scope)
		}
	}
}

// TestResolvePrecedenceDBOverFSSystem guards CR-003: a DB user/agent skill must
// shadow a filesystem system skill of the same name during model resolution.
func TestResolvePrecedenceDBOverFSSystem(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	stellaHome := t.TempDir()
	writeFSSystemSkill(t, stellaHome, "shared", "from filesystem system")
	svc := NewService(store, stellaHome)
	ctx := context.Background()
	vc := pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}

	// With only the FS system skill, Resolve returns it.
	rs, err := svc.Resolve(ctx, "shared", vc, "")
	if err != nil {
		t.Fatalf("Resolve (fs only): %v", err)
	}
	if rs == nil || rs.Scope != "system" {
		t.Fatalf("Resolve (fs only) = %#v, want system scope", rs)
	}

	// Once a DB user skill of the same name exists, it must win.
	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "user", Name: "shared", UserID: userID})
	rs, err = svc.Resolve(ctx, "shared", vc, "")
	if err != nil {
		t.Fatalf("Resolve (db+fs): %v", err)
	}
	if rs == nil || rs.Scope != "user" {
		t.Fatalf("Resolve (db+fs) = %#v, want user scope to shadow fs system", rs)
	}
}

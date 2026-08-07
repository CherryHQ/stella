package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/resources"
)

type countingResolveStore struct {
	*testSkillStore
	resolveCalls int
}

type homeSkillFileStore struct {
	*testSkillStore
	loaded   *pkgplugins.HomeSkillFile
	loadErr  error
	touched  []string
	touchErr error
}

func (s *homeSkillFileStore) LoadHomeSkillFile(context.Context, string, string, pkgplugins.SkillViewContext) (*pkgplugins.HomeSkillFile, error) {
	return s.loaded, s.loadErr
}

func (s *homeSkillFileStore) TouchReflectSkillRuntimeUse(_ context.Context, _ string, _ string, _ string, digest string) error {
	s.touched = append(s.touched, digest)
	return s.touchErr
}

func (s *countingResolveStore) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext) (*pkgplugins.Skill, error) {
	s.resolveCalls++
	return s.testSkillStore.Resolve(ctx, name, vc)
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

// TestResolveScopedFindsDisabledAndDBSystem guards CR-002: scoped management
// lookup must find rows the runtime-visible List filters out — disabled
// (knowledge) entries and DB-backed system skills.
func TestResolveScopedFindsDisabledAndDBSystem(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	svc := NewService(store, t.TempDir())
	ctx := context.Background()
	vc := pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}

	// A disabled (knowledge) user skill is excluded from List but must resolve.
	mustCreateDBSkill(t, store, pkgplugins.Skill{
		Scope: "user", Name: "kn", UserID: userID, DisableModelInvocation: true,
	})
	rs, err := svc.ResolveScoped(ctx, "kn", "user", vc, "")
	if err != nil {
		t.Fatalf("ResolveScoped(kn): %v", err)
	}
	if rs == nil || rs.Scope != "user" {
		t.Fatalf("ResolveScoped(kn) = %#v, want disabled user skill", rs)
	}

	// A DB-backed system skill must resolve via the DB, not only the filesystem.
	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "system", Name: "dbsys"})
	rs, err = svc.ResolveScoped(ctx, "dbsys", "system", vc, "")
	if err != nil {
		t.Fatalf("ResolveScoped(dbsys): %v", err)
	}
	if rs == nil || rs.Scope != "system" || rs.ID == "" {
		t.Fatalf("ResolveScoped(dbsys) = %#v, want DB system skill", rs)
	}
}

// TestResolvePrecedenceDBOverFSSystem guards CR-003: a DB user/agent skill must
// shadow a filesystem system skill of the same name during model resolution.
func TestResolvePrecedenceDBOverBuiltinSystem(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	stellaHome := t.TempDir()
	svc := NewService(store, stellaHome)
	ctx := context.Background()
	vc := pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}

	// With only the release builtin, Resolve returns the Registry descriptor.
	rs, err := svc.Resolve(ctx, "stella", vc, "")
	if err != nil {
		t.Fatalf("Resolve (builtin only): %v", err)
	}
	if rs == nil || rs.Scope != "system" {
		t.Fatalf("Resolve (builtin only) = %#v, want system scope", rs)
	}

	// Once a DB user skill of the same name exists, it must win.
	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "user", Name: "stella", UserID: userID})
	rs, err = svc.Resolve(ctx, "stella", vc, "")
	if err != nil {
		t.Fatalf("Resolve (db+fs): %v", err)
	}
	if rs == nil || rs.Scope != "user" {
		t.Fatalf("Resolve (db+fs) = %#v, want user scope to shadow fs system", rs)
	}
}

func TestBuiltinRegistryReadsDoNotNeedAStoreOrMirror(t *testing.T) {
	home := t.TempDir()
	registry, err := resources.Default()
	if err != nil {
		t.Fatal(err)
	}
	descriptor := registry.BuiltinSkills()[0]
	svc := NewService(nil, home, registry)
	merged, err := svc.ListMerged(context.Background(), pkgplugins.SkillViewContext{}, "")
	if err != nil || len(merged) == 0 {
		t.Fatalf("ListMerged() = %d, %v", len(merged), err)
	}
	content, dir, resolved, err := svc.LoadFile(context.Background(), descriptor.APIID, pkgplugins.SkillMainFile, pkgplugins.SkillViewContext{}, "")
	if err != nil || resolved == nil || content == "" {
		t.Fatalf("LoadFile builtin = %q, %#v, %v", content, resolved, err)
	}
	wantDir, err := registry.BundlePath(home)
	if err != nil || dir != filepath.Join(wantDir, filepath.FromSlash(descriptor.Root)) {
		t.Fatalf("builtin dir = %q, want %q (%v)", dir, filepath.Join(wantDir, filepath.FromSlash(descriptor.Root)), err)
	}
	if _, err := os.Stat(filepath.Join(home, "bundles")); !os.IsNotExist(err) {
		t.Fatalf("registry read created a runtime mirror: %v", err)
	}
}

func TestBuiltinStableIDChecksPGShadowOnce(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	counting := &countingResolveStore{testSkillStore: store}
	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "user", Name: "stella", UserID: userID})
	registry, err := resources.Default()
	if err != nil {
		t.Fatal(err)
	}
	ref := registry.BuiltinSkills()[0]
	if ref.Name != "stella" {
		for _, descriptor := range registry.BuiltinSkills() {
			if descriptor.Name == "stella" {
				ref = descriptor
				break
			}
		}
	}
	svc := NewService(counting, t.TempDir(), registry)
	rs, err := svc.Resolve(context.Background(), ref.APIID, pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}, "")
	if err != nil || rs == nil || rs.Scope != "user" {
		t.Fatalf("Resolve(%q) = %#v, %v; want PG user shadow", ref.APIID, rs, err)
	}
	if counting.resolveCalls != 1 {
		t.Fatalf("PG Resolve calls = %d, want 1", counting.resolveCalls)
	}
}

func TestAgentSkillPolicyFiltersOnlyTheResolvedWinner(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	svc := NewService(store, t.TempDir())
	ctx := context.Background()
	vc := pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}

	// A DB system Skill shadows the builtin. Disabling that winner must produce
	// no lower-precedence fallback, regardless of whether it is named by API ID
	// or ordinary name.
	mustCreateDBSkill(t, store, pkgplugins.Skill{Scope: "system", Name: "stella"})
	vc.DisabledSkillRefs = []string{"system:stella"}
	for _, name := range []string{"stella", "builtin-stella", "builtin:stella"} {
		rs, err := svc.Resolve(ctx, name, vc, "")
		if err != nil || rs != nil {
			t.Fatalf("Resolve(%q) = %#v, %v; disabled winner must not fall back", name, rs, err)
		}
	}

	// Activation is independent from disable_model_invocation: an active policy
	// leaves a model-disabled skill to the existing invocation filter, rather
	// than treating its content state as an activation disablement.
	vc.DisabledSkillRefs = nil
	rs, err := svc.Resolve(ctx, "stella", vc, "")
	if err != nil || rs == nil || rs.Scope != "system" {
		t.Fatalf("Resolve active policy = %#v, %v; want system winner", rs, err)
	}
}

func TestLoadFileSuppressedHomeWinnerShadowsBuiltin(t *testing.T) {
	base, userID, agentID := newTestSkillStore(t)
	store := &homeSkillFileStore{testSkillStore: base, loaded: &pkgplugins.HomeSkillFile{Suppressed: true}}
	_, _, resolved, err := NewService(store, t.TempDir()).LoadFile(context.Background(), "stella", pkgplugins.SkillMainFile, pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}, "")
	if err == nil || resolved != nil {
		t.Fatalf("suppressed Home winner = resolved %#v, err %v; must not fall through to builtin", resolved, err)
	}
}

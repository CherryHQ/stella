package skills

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func newHomeStoreFixture(t *testing.T) (*HomeStore, *HomeSkillManager, *HomeCatalog, context.Context, *skillMigrationService, string, string, *time.Time) {
	t.Helper()
	t.Setenv("STELLA_HOME", t.TempDir())
	manager, _, ctx, migration, userID, agentID, now := newHomeSkillManagerFixture(t)
	catalog, err := NewHomeCatalog(migration.homes, catalogInventory{
		{Scope: "system"},
		{Scope: "system_agent", AgentID: agentID},
		{Scope: "user", UserID: userID},
		{Scope: "user_agent", UserID: userID, AgentID: agentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.catalog = catalog
	store, err := NewHomeStore(catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	return store, manager, catalog, ctx, migration, userID, agentID, now
}

func TestNewHomeStoreRejectsSplitCatalogAuthority(t *testing.T) {
	manager, managerCatalog, _, migration, _, _, _ := newHomeSkillManagerFixture(t)
	otherCatalog, err := NewHomeCatalog(migration.homes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewHomeStore(otherCatalog, manager); err == nil {
		t.Fatal("HomeStore accepted a catalog different from its manager")
	}
	if _, err := NewHomeStore(managerCatalog, manager); err != nil {
		t.Fatalf("HomeStore rejected its manager catalog: %v", err)
	}
}

func createHomeStoreSkill(t *testing.T, store *HomeStore, sk Skill, files map[string]string) SkillSnapshot {
	t.Helper()
	snapshot, err := store.CreateManagedSkill(context.Background(), sk, files)
	if err != nil {
		t.Fatalf("create %s/%s: %v", sk.Scope, sk.Name, err)
	}
	return snapshot
}

func TestHomeCatalogGetReadsExactCanonicalRoot(t *testing.T) {
	store, _, catalog, ctx, migration, userID, _, _ := newHomeStoreFixture(t)
	created := createHomeStoreSkill(t, store, Skill{Scope: "user", UserID: userID, Name: "exact", Description: "exact"}, map[string]string{MainFile: catalogBody("exact")})

	got, err := catalog.Get(ctx, created.Skill.ID)
	if err != nil || got.Skill.ID != created.Skill.ID || got.Digest != created.Skill.ContentDigest || !got.Managed {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := catalog.Get(ctx, created.Skill.ID+"x"); err == nil {
		t.Fatal("non-canonical ID accepted")
	}
	missingID, err := encodeFilesystemSkillID("user", userID, "", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Get(ctx, missingID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing = %v", err)
	}

	root, err := home.UserSkillCatalog(userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.homes.UseSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		if err := filesystem.Mkdir(ctx, sandbox.PathWorkspace+"/ordinary", 0o755); err != nil {
			return err
		}
		return filesystem.Write(ctx, sandbox.PathWorkspace+"/ordinary/"+MainFile, strings.NewReader(catalogBody("ordinary")), sandbox.WriteOptions{})
	}); err != nil {
		t.Fatal(err)
	}
	ordinaryID, err := encodeFilesystemSkillID("user", userID, "", "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := store.Get(ctx, ordinaryID)
	if err != nil || ordinary.ContentDigest != "" {
		t.Fatalf("ordinary HomeStore Get = %+v, %v", ordinary, err)
	}
}

func TestHomeStoreListsReadyHomesWithoutSkillCatalogAsEmpty(t *testing.T) {
	db, ctx, base, migration := newSkillMigrationFixture(t)
	userID, agentID := seedFixtures(t, db)
	principal, err := migration.homes.Ensure(ctx, home.Principal(home.UserPrincipal, userID))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := migration.homes.Ensure(ctx, home.Agent(home.UserPrincipal, userID, agentID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migration.homes.Ensure(ctx, home.SystemAgentSkills(agentID)); err != nil {
		t.Fatal(err)
	}
	inventory, err := NewStorageHomeCatalogInventory(sqlc.New(db))
	if err != nil {
		t.Fatal(err)
	}
	roots, err := inventory.ListRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []HomeCatalogRoot{{Scope: "system_agent", AgentID: agentID}, {Scope: "user", UserID: userID}, {Scope: "user_agent", UserID: userID, AgentID: agentID}}; !reflect.DeepEqual(roots, want) {
		t.Fatalf("inventory roots = %#v, want %#v", roots, want)
	}
	callbacks := 0
	catalog, err := NewHomeCatalog(callbackWrappingHomeCatalog{inner: migration.homes, wrap: func(filesystem sandbox.Filesystem) sandbox.Filesystem {
		callbacks++
		return filesystem
	}}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewHomeSkillPublisher(migration.homes)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewHomeSkillManager(catalog, publisher, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewHomeStore(catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range []struct {
		name string
		call func() ([]Skill, error)
	}{
		{"all", func() ([]Skill, error) { return store.ListAll(ctx) }},
		{"admin", func() ([]Skill, error) { return store.ListForAdmin(ctx, userID) }},
		{"agent context", func() ([]Skill, error) { return store.ListForAgentContext(ctx, userID, agentID) }},
	} {
		t.Run(list.name, func(t *testing.T) {
			rows, err := list.call()
			if err != nil || len(rows) != 0 {
				t.Fatalf("rows = %+v, %v", rows, err)
			}
		})
	}
	// Only the system-agent Home is itself the catalog root. The owner Homes
	// above lack their optional nested catalog, so list reads must not invoke
	// their callback to create it.
	if callbacks != 3 {
		t.Fatalf("catalog callbacks = %d, want only one system-agent callback per list", callbacks)
	}
	for _, missing := range []string{
		filepath.Join(base, filepath.FromSlash(path.Join(principal.Locator, "data", ".agents", "skills"))),
		filepath.Join(base, filepath.FromSlash(path.Join(agent.Locator, ".agents", "skills"))),
	} {
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatalf("list read created catalog bytes at %q: %v", missing, err)
		}
	}
	for _, root := range []HomeCatalogRoot{
		{Scope: "user", UserID: userID},
		{Scope: "user_agent", UserID: userID, AgentID: agentID},
		{Scope: "system_agent", AgentID: agentID},
	} {
		id, err := encodeFilesystemSkillID(root.Scope, root.UserID, root.AgentID, "absent")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, id); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Get %s = %v, want fs.ErrNotExist", root.Scope, err)
		}
	}
}

func TestHomeStoreCurrentStateShapesAndSafeMutations(t *testing.T) {
	store, manager, _, ctx, _, userID, agentID, now := newHomeStoreFixture(t)
	common := func(scope, user, agent, name string, disabled bool) SkillSnapshot {
		return createHomeStoreSkill(t, store, Skill{
			ID: "legacy-id", Scope: scope, UserID: user, AgentID: agent, Name: name, Description: name,
			DisableModelInvocation: disabled, Metadata: []byte(`{"created_by":"reflect","source":"caller"}`),
		}, map[string]string{MainFile: string(managedSkillMarkdown(name, name, "body"))})
	}
	system := common("system", "", "", "same", false)
	_ = system
	common("system_agent", "", agentID, "same", false)
	common("user", userID, "", "same", false)
	userAgent := common("user_agent", userID, agentID, "same", true)
	common("system", "", "", "old", false)

	deprecatedRoot, err := home.UserAgentSkillCatalog(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	deprecatedDigest, err := manager.publisher.Publish(ctx, HomeSkillPublishRequest{
		Root: deprecatedRoot, Name: "old",
		Metadata: HomeSkillMetadata{Status: SkillStatusDeprecated, Metadata: map[string]any{"created_by": ManualSkillCreatedBy}, CreatedAt: *now, UpdatedAt: *now, LegacyLifecycleVersion: 1},
		Files:    []HomeSkillFile{{Path: MainFile, Content: managedSkillMarkdown("old", "old", "body"), Mode: 0o644}},
	})
	if err != nil || deprecatedDigest == "" {
		t.Fatalf("publish deprecated = %q, %v", deprecatedDigest, err)
	}

	visible, err := store.List(ctx, ViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	for _, sk := range visible {
		if sk.Name == "same" {
			t.Fatal("disabled user-agent skill did not shadow lower scopes")
		}
	}
	resolved, err := store.Resolve(ctx, "old", ViewContext{UserID: userID, AgentID: agentID})
	if err != nil || resolved == nil || resolved.Scope != "system" {
		t.Fatalf("deprecated fallback = %+v, %v", resolved, err)
	}

	contextRows, err := store.ListForAgentContext(ctx, userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	var sameScopes []string
	for _, sk := range contextRows {
		if sk.Name == "same" {
			sameScopes = append(sameScopes, sk.Scope)
		}
	}
	if want := []string{"user_agent", "user", "system_agent", "system"}; !reflect.DeepEqual(sameScopes, want) {
		t.Fatalf("agent context scopes = %v, want %v", sameScopes, want)
	}
	byScope, err := store.ListByScope(ctx, "user_agent", userID, agentID)
	if err != nil || len(byScope) != 2 || byScope[0].Name != "old" || byScope[1].Name != "same" {
		t.Fatalf("scope rows = %+v, %v", byScope, err)
	}
	all, err := store.ListAll(ctx)
	if err != nil || len(all) != 6 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool {
		if all[i].Scope != all[j].Scope {
			return all[i].Scope < all[j].Scope
		}
		return skillCreatedIDLess(all[i], all[j])
	}) {
		t.Fatalf("ListAll ordering is unstable: %+v", all)
	}
	forUser, err := store.ListForUser(ctx, userID, []string{agentID, agentID})
	if err != nil || len(forUser) != 5 {
		t.Fatalf("user rows = %+v, %v", forUser, err)
	}
	forAdmin, err := store.ListForAdmin(ctx, userID)
	if err != nil || len(forAdmin) != 6 {
		t.Fatalf("admin rows = %+v, %v", forAdmin, err)
	}

	reflectOwned := createHomeStoreSkill(t, store, Skill{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "reflect", Description: "reflect", DisableModelInvocation: true}, map[string]string{MainFile: catalogBody("reflect")})
	// Generic creation always overwrites caller ownership to manual; publish an
	// explicit Reflect revision to exercise the current-state filter.
	reflectRoot, err := home.UserAgentSkillCatalog(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	reflectDigest, err := manager.publisher.Publish(ctx, HomeSkillPublishRequest{
		Root: reflectRoot, Name: "reflect-owned", Metadata: HomeSkillMetadata{Status: SkillStatusActive, DisableModelInvocation: true, Metadata: map[string]any{"created_by": ReflectSkillCreatedBy}, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second), LegacyLifecycleVersion: 1},
		Files: []HomeSkillFile{{Path: MainFile, Content: managedSkillMarkdown("reflect-owned", "reflect-owned", "body"), Mode: 0o644}},
	})
	if err != nil || reflectDigest == "" {
		t.Fatalf("publish reflect = %q, %v", reflectDigest, err)
	}
	reflectRows, err := store.ListActiveReflectOwnedUserAgentSkills(ctx, userID, agentID)
	if err != nil || len(reflectRows) != 1 || reflectRows[0].Name != "reflect-owned" || !reflectRows[0].DisableModelInvocation {
		t.Fatalf("reflect rows = %+v, %v", reflectRows, err)
	}
	if got, err := store.Get(ctx, userAgent.Skill.ID); err != nil || got.ContentDigest != userAgent.Skill.ContentDigest || CreatedBy(*got) != ManualSkillCreatedBy {
		t.Fatalf("canonical/manual Get = %+v, %v", got, err)
	}
	if reflectOwned.Skill.ID == "legacy-id" || !strings.HasPrefix(reflectOwned.Skill.ID, filesystemSkillIDPrefix) {
		t.Fatalf("create did not return canonical ID: %q", reflectOwned.Skill.ID)
	}

	binary := string([]byte{0, 0xff, 1})
	mutable := createHomeStoreSkill(t, store, Skill{Scope: "user", UserID: userID, Name: "mutable", Description: "before"}, map[string]string{
		MainFile: string(managedSkillMarkdown("mutable", "before", "body")), "bin/raw": binary,
	})
	wrongOwner := ManagedSkillUpdate{ID: mutable.Skill.ID, Scope: "user", UserID: "other", ExpectedDigest: mutable.Skill.ContentDigest}
	if _, err := store.UpdateManagedSkill(ctx, wrongOwner); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("wrong owner update = %v", err)
	}
	if _, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: mutable.Skill.ID, Scope: "user", UserID: userID, ExpectedDigest: ""}); err == nil {
		t.Fatal("missing digest accepted")
	}
	description := "after"
	updated, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: mutable.Skill.ID, Scope: "user", UserID: userID, ExpectedDigest: mutable.Skill.ContentDigest,
		Patch: UpdatePatch{Description: &description}, Files: map[string]string{"note": "updated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.catalog.LoadManagedSnapshot(ctx, updated.Skill.ID)
	if err != nil || string(loaded.Files[1].Content) != binary || loaded.Files[1].Mode != 0o644 || len(loaded.Files) != 3 {
		t.Fatalf("complete binary update = %+v, %v", loaded, err)
	}
	if _, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: mutable.Skill.ID, Scope: "user", UserID: userID, ExpectedDigest: mutable.Skill.ContentDigest}); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("stale update = %v", err)
	}
	forgedDelete := ManagedSkillDelete{ID: updated.Skill.ID, Scope: updated.Skill.Scope, UserID: "forged", AgentID: updated.Skill.AgentID, ExpectedDigest: updated.Skill.ContentDigest}
	if err := store.DeleteManagedSkill(ctx, forgedDelete); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("forged Home delete = %v", err)
	}
	if _, err := store.DeleteManagedSkillFile(ctx, ManagedSkillFileDelete{ManagedSkillDelete: forgedDelete, Path: "note"}); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("forged Home file delete = %v", err)
	}
	afterFileDelete, err := store.DeleteManagedSkillFile(ctx, ManagedSkillFileDelete{ManagedSkillDelete: ManagedSkillDelete{ID: updated.Skill.ID, Scope: updated.Skill.Scope, UserID: updated.Skill.UserID, AgentID: updated.Skill.AgentID, ExpectedDigest: updated.Skill.ContentDigest}, Path: "note"})
	if err != nil || len(afterFileDelete.Files) != 2 {
		t.Fatalf("safe file delete = %+v, %v", afterFileDelete, err)
	}
	if err := store.DeleteManagedSkill(ctx, ManagedSkillDelete{ID: afterFileDelete.Skill.ID, Scope: afterFileDelete.Skill.Scope, UserID: afterFileDelete.Skill.UserID, AgentID: afterFileDelete.Skill.AgentID, ExpectedDigest: updated.Skill.ContentDigest}); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("stale delete = %v", err)
	}
	if err := store.DeleteManagedSkill(ctx, ManagedSkillDelete{ID: afterFileDelete.Skill.ID, Scope: afterFileDelete.Skill.Scope, UserID: afterFileDelete.Skill.UserID, AgentID: afterFileDelete.Skill.AgentID, ExpectedDigest: afterFileDelete.Skill.ContentDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, afterFileDelete.Skill.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted Get = %v", err)
	}
}

func TestHomeStoreRejectsInvalidRequestsBeforeHomeCallback(t *testing.T) {
	_, manager, _, ctx, migration, userID, _, _ := newHomeStoreFixture(t)
	calls := 0
	catalog, err := NewHomeCatalog(callbackWrappingHomeCatalog{inner: migration.homes, wrap: func(filesystem sandbox.Filesystem) sandbox.Filesystem {
		calls++
		return filesystem
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.catalog = catalog
	store, err := NewHomeStore(catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManagedSkill(ctx, Skill{Scope: "user", UserID: userID, Name: "bad", Description: "bad", Status: SkillStatusDeprecated}, map[string]string{MainFile: catalogBody("bad")}); err == nil {
		t.Fatal("deprecated create accepted")
	}
	id, err := encodeFilesystemSkillID("user", userID, "", "bad")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: id, Scope: "user", UserID: "wrong", ExpectedDigest: strings.Repeat("a", 64)}); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("wrong owner = %v", err)
	}
	status := SkillStatusDeprecated
	if _, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: id, Scope: "user", UserID: userID, ExpectedDigest: strings.Repeat("a", 64), Patch: UpdatePatch{Status: &status}}); err == nil {
		t.Fatal("deprecated update accepted")
	}
	if calls != 0 {
		t.Fatalf("invalid requests opened Home %d times", calls)
	}
	system, err := store.CreateManagedSkill(ctx, Skill{Scope: "system", Name: "system", Description: "system"}, map[string]string{MainFile: catalogBody("system")})
	if err != nil {
		t.Fatal(err)
	}
	calls = 0
	request := ManagedSkillDelete{ID: system.Skill.ID, Scope: system.Skill.Scope, ExpectedDigest: system.Skill.ContentDigest}
	if _, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: request.ID, Scope: request.Scope, ExpectedDigest: request.ExpectedDigest}); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("system update = %v", err)
	}
	if err := store.DeleteManagedSkill(ctx, request); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("system delete = %v", err)
	}
	if _, err := store.DeleteManagedSkillFile(ctx, ManagedSkillFileDelete{ManagedSkillDelete: request, Path: "note"}); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("system file delete = %v", err)
	}
	for _, update := range []ManagedSkillUpdate{
		{ID: id, Scope: "user", UserID: userID, ExpectedDigest: strings.Repeat("a", 64), DeleteFiles: []string{"note", "note"}},
		{ID: id, Scope: "user", UserID: userID, ExpectedDigest: strings.Repeat("a", 64), Files: map[string]string{"note": "new"}, DeleteFiles: []string{"note"}},
	} {
		if _, err := store.UpdateManagedSkill(ctx, update); !errors.Is(err, ErrInvalidManagedSkillFileMutation) {
			t.Fatalf("invalid shared file mutation = %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid system/file requests opened Home %d times", calls)
	}
}

func TestStorageHomeCatalogInventoryFiltersAndFailsClosed(t *testing.T) {
	_, db, ctx := newTestStore(t)
	insert := func(kind string, principalKind, principalID, agentID any, state string) {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(ctx, `INSERT INTO storage_home (id, home_kind, principal_kind, principal_id, agent_id, store_id, locator, state) VALUES ($1, $2, $3, $4, $5, 'inventory', $6, $7)`, id, kind, principalKind, principalID, agentID, "inventory/"+id, state); err != nil {
			t.Fatal(err)
		}
	}
	insert("system_skill", nil, nil, nil, "ready")
	insert("system_agent_skill", nil, nil, "agent-1", "ready")
	insert("principal", "user", "user-1", nil, "ready")
	insert("agent", "user", "user-1", "agent-1", "ready")
	insert("principal", "group", "group-1", nil, "ready")
	insert("agent", "group", "group-1", "agent-1", "ready")
	insert("principal", "user", "not-ready", nil, "provisioning")
	insert("agent", "user", "gone", "agent-1", "tombstoned")
	insert("principal", "user", "purged", nil, "purged")

	inventory, err := NewStorageHomeCatalogInventory(sqlc.New(db))
	if err != nil {
		t.Fatal(err)
	}
	roots, err := inventory.ListRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []HomeCatalogRoot{{Scope: "system"}, {Scope: "system_agent", AgentID: "agent-1"}, {Scope: "user", UserID: "user-1"}, {Scope: "user_agent", UserID: "user-1", AgentID: "agent-1"}}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots = %#v, want %#v", roots, want)
	}
	insert("principal", "robot", "broken", nil, "ready")
	if _, err := inventory.ListRoots(ctx); err == nil {
		t.Fatal("malformed ready registry row was accepted")
	}
	if _, _, err := homeCatalogRootFromStorageHome(homeCatalogInventoryRecord{homeKind: "agent", principalKind: "user", principalID: "../bad", agentID: "agent", hasPrincipalKind: true, hasPrincipalID: true, hasAgentID: true}); err == nil {
		t.Fatal("malformed identity bypassed typed root validation")
	}
}

func TestHomeStoreAndInventoryStayWithinAuthorityBoundary(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	homeStoreSource, err := os.ReadFile(filepath.Join(repo, "internal/skills/home_store.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sqlc", "pgx", "PGStore", "filepath", "os."} {
		if strings.Contains(string(homeStoreSource), forbidden) {
			t.Errorf("HomeStore crosses authority boundary through %q", forbidden)
		}
	}
	querySource, err := os.ReadFile(filepath.Join(repo, "internal/db/queries/storage_home.sql"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(querySource), "-- name: ListReadyStorageHomeCatalogRoots")
	if start < 0 {
		t.Fatal("inventory query missing")
	}
	query := string(querySource[start:])
	for _, forbidden := range []string{"skill_file", "skill_changelog", "skill_usage", "FROM skill", "JOIN skill"} {
		if strings.Contains(query, forbidden) {
			t.Errorf("inventory query reads mutable Skill table %q", forbidden)
		}
	}
	storeSource, err := os.ReadFile(filepath.Join(repo, "internal/skills/store.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\n\tUpdate(ctx", "\n\tUpsertFile(ctx", "\n\tDeleteFile(ctx", "\n\tDelete(ctx", "ListSkillChangelogBySkill"} {
		if strings.Contains(string(storeSource), forbidden) {
			t.Errorf("shared Skill store retains shallow port %q", forbidden)
		}
	}
	adapterSource, err := os.ReadFile(filepath.Join(repo, "internal/pluginhost/platform.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"viewContextForSkill", "a.s.ListAll"} {
		if strings.Contains(string(adapterSource), forbidden) {
			t.Errorf("plugin Skill adapter recovers mutation facts unsafely through %q", forbidden)
		}
	}
}

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/home"
	internalplugin "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

type filePolicyAgentStore struct{}

func (filePolicyAgentStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	return config.Agent{ID: id, Scope: config.AgentScopeRestricted, CreatorID: "file-policy-user", Enabled: true}, nil
}

func (filePolicyAgentStore) ListAgents(context.Context) ([]config.Agent, error) { return nil, nil }

type filePolicyAgentAssignments struct{}

func (filePolicyAgentAssignments) ListUserAgentIDs(context.Context, string) ([]string, error) {
	return []string{"file-policy-agent"}, nil
}

func TestToolOverrideStoreProjectsRealFilePolicyAndCAS(t *testing.T) {
	ctx := t.Context()
	db := dbtest.New(t)
	userID := "00000000-0000-4000-8000-000000000781"
	otherUserID := "00000000-0000-4000-8000-000000000782"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user(id,email) VALUES($1,'file-policy@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO auth_user(id,email) VALUES($1,'file-policy-other@example.invalid')`, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent(id,name,workspace) VALUES('file-policy-agent','File Policy Agent','')`); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pep := agentaccess.NewService(filePolicyAgentStore{}, filePolicyAgentAssignments{})
	files := internalplugin.NewFileService(internalplugin.NewResourceStore(manager), pep)
	admin, err := authz.NewUserAuthority(authz.UserID(userID), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx = authz.WithAuthority(ctx, admin)
	access, err := files.Begin(admin)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.CreatePlugin(ctx, internalplugin.ScopeSystem, "", "policy", map[string]internalplugin.ResourceFile{
		"plugin.json": {Data: []byte(`{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"policy"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	resource, err = access.SetDisabledTools(ctx, resource.Key.ID(), resource.SettingsDigest, []string{"main/read"})
	if err != nil {
		t.Fatal(err)
	}
	userKey := internalplugin.ResourceKey{Scope: internalplugin.ScopeUser, UserID: userID, Kind: internalplugin.ResourcePlugin, Name: "policy"}
	if _, err := access.SetDisabledTools(ctx, userKey.ID(), "", []string{"main/read"}); err != nil {
		t.Fatal(err)
	}
	userAgentKey := internalplugin.ResourceKey{Scope: internalplugin.ScopeUserAgent, UserID: userID, AgentID: "file-policy-agent", Kind: internalplugin.ResourcePlugin, Name: "policy"}
	if _, err := access.SetDisabledTools(ctx, userAgentKey.ID(), "", []string{"main/read", "main/write"}); err != nil {
		t.Fatal(err)
	}
	identity := ToolIdentity{PluginID: resource.Key.ID(), ServerKey: "main", LocalToolName: "read"}
	store := NewToolOverrideStore(db, files)
	fetched, err := store.Fetch(ctx, userID, "file-policy-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 4 {
		t.Fatalf("file policy fetch = %#v", fetched)
	}
	writeIdentity := ToolIdentity{PluginID: resource.Key.ID(), ServerKey: "main", LocalToolName: "write"}
	readScopes := map[string]bool{}
	writeSeen := false
	for _, row := range fetched {
		if row.Identity == writeIdentity {
			if row.Scope != ToolOverrideScopeUserAgent || row.Enabled {
				t.Fatalf("user-agent write policy row = %#v", row)
			}
			writeSeen = true
			continue
		}
		if row.Identity != identity || row.Enabled || (row.Scope != ToolOverrideScopeSystem && row.Scope != ToolOverrideScopeUser && row.Scope != ToolOverrideScopeUserAgent) {
			t.Fatalf("file policy fetch row = %#v", row)
		}
		readScopes[row.Scope] = true
	}
	if !writeSeen || len(readScopes) != 3 {
		t.Fatalf("file policy additive scopes = %#v", fetched)
	}
	resources, err := files.ReadSnapshot(ctx, admin, "file-policy-agent")
	if err != nil || len(resources) != 1 || resources[0].Content == nil || len(resources[0].DisabledTools) != 2 {
		t.Fatalf("disabled file catalog resource = %#v, err=%v", resources, err)
	}
	// The user-agent settings also contain main/write. The system and user
	// layers deliberately keep their independent main/read denies above it.

	key := ToolOverrideKey{Identity: identity, Scope: ToolOverrideScopeSystem}
	current, err := store.Get(ctx, key)
	if err != nil || !current.Present || current.Enabled || current.Version == "" {
		t.Fatalf("file policy get = %#v, err=%v", current, err)
	}
	userAgentIdentityKey := ToolOverrideKey{Identity: identity, Scope: ToolOverrideScopeUserAgent, UserID: userID, AgentID: "file-policy-agent"}
	userAgentCurrent, err := store.Get(ctx, userAgentIdentityKey)
	if err != nil || !userAgentCurrent.Present || userAgentCurrent.Enabled {
		t.Fatalf("user-agent file policy get = %#v, err=%v", userAgentCurrent, err)
	}
	updated, err := store.SetIfVersion(ctx, ToolOverrideWrite{Identity: identity, Scope: userAgentIdentityKey.Scope, UserID: userID, AgentID: "file-policy-agent", Enabled: true}, userAgentCurrent.Version)
	if err != nil || !updated.Enabled || updated.Present || updated.Version == current.Version {
		t.Fatalf("user-agent file policy re-enable = %#v, err=%v", updated, err)
	}
	fetched, err = store.Fetch(ctx, userID, "file-policy-agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range fetched {
		if row.Identity == identity && row.Scope == ToolOverrideScopeSystem && row.Enabled {
			t.Fatalf("lower-scope enable bypassed system deny: %#v", fetched)
		}
	}
	if _, err := store.SetIfVersion(ctx, ToolOverrideWrite{Identity: identity, Scope: userAgentIdentityKey.Scope, UserID: userID, AgentID: "file-policy-agent", Enabled: false}, userAgentCurrent.Version); !errors.Is(err, config.ErrAgentVersionConflict) {
		t.Fatalf("stale user-agent file policy write = %v, want conflict", err)
	}
	other, err := authz.NewUserAuthority(authz.UserID(otherUserID), false)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.Get(authz.WithAuthority(context.Background(), other), userAgentIdentityKey)
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("cross-user file policy read = %#v, err=%v, want forbidden", foreign, err)
	}
}

func TestToolOverrideStoreProjectsGroupCaptureAndRejectsGuest(t *testing.T) {
	ctx := t.Context()
	db := dbtest.New(t)
	groupID := "00000000-0000-4000-8000-000000000783"
	adminID := "00000000-0000-4000-8000-000000000784"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user(id,email) VALUES($1,'file-policy-group-admin@example.invalid')`, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent(id,name,workspace) VALUES('file-group-agent','File Group Agent','')`); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pep := agentaccess.NewService(filePolicyAgentStore{}, filePolicyAgentAssignments{})
	files := internalplugin.NewFileService(internalplugin.NewResourceStore(manager), pep)
	admin, err := authz.NewUserAuthority(authz.UserID(adminID), true)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := authz.WithAuthority(ctx, admin)
	access, err := files.Begin(admin)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.CreatePlugin(adminCtx, internalplugin.ScopeSystem, "", "group-policy", map[string]internalplugin.ResourceFile{
		"plugin.json": {Data: []byte(`{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"group-policy"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A system-agent deny must remain effective for a group turn even though
	// that turn has no user identity and cannot read user or user-agent roots.
	agentKey := internalplugin.ResourceKey{Scope: internalplugin.ScopeSystemAgent, AgentID: "file-group-agent", Kind: internalplugin.ResourcePlugin, Name: "group-policy"}
	if _, err := access.SetDisabledTools(adminCtx, agentKey.ID(), "", []string{"main/read"}); err != nil {
		t.Fatal(err)
	}
	userKey := internalplugin.ResourceKey{Scope: internalplugin.ScopeUser, UserID: adminID, Kind: internalplugin.ResourcePlugin, Name: "group-policy"}
	if _, err := access.SetDisabledTools(adminCtx, userKey.ID(), "", []string{"main/write"}); err != nil {
		t.Fatal(err)
	}
	group, err := authz.NewGroupAgentAuthority(authz.GroupID(groupID), authz.AgentID("file-group-agent"))
	if err != nil {
		t.Fatal(err)
	}
	groupCtx := authz.WithAuthority(ctx, group)
	store := NewToolOverrideStore(db, files)
	rows, err := store.Fetch(groupCtx, groupID, "file-group-agent")
	if err != nil {
		t.Fatal(err)
	}
	identity := ToolIdentity{PluginID: resource.Key.ID(), ServerKey: "main", LocalToolName: "read"}
	if len(rows) != 1 || rows[0].Identity != identity || rows[0].Scope != ToolOverrideScopeSystem || rows[0].Enabled || FilterToolEnabled(true, identity, rows) {
		t.Fatalf("group file policy rows = %#v", rows)
	}
	resources, err := files.Capture(groupCtx, group, "file-group-agent")
	if err != nil || len(resources) != 1 || len(resources[0].DisabledTools) != 1 {
		t.Fatalf("group capture = %#v, err=%v", resources, err)
	}
	guest, err := authz.NewGuestAuthority(authz.GuestID("guest"), "channel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(authz.WithAuthority(ctx, guest), groupID, "file-group-agent"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("guest file policy fetch = %v, want forbidden", err)
	}
}

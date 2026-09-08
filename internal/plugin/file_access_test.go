package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func fileAccessService(t *testing.T) (*FileService, authz.Authority, authz.Authority) {
	t.Helper()
	db := dbtest.New(t)
	adminID := "00000000-0000-4000-8000-000000000777"
	userID := "00000000-0000-4000-8000-000000000778"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'file-admin@example.invalid'),($2,'file-user@example.invalid')`, adminID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES('file-agent','File Agent','')`); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	admin, err := authz.NewUserAuthority(authz.UserID(adminID), true)
	if err != nil {
		t.Fatal(err)
	}
	user, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	return NewFileService(NewResourceStore(manager), nil), admin, user
}

func TestFileAccessRejectsCrossOwnerAndNonUserRoots(t *testing.T) {
	service, admin, user := fileAccessService(t)
	adminAccess, err := service.Begin(admin)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"demo"}`)
	created, err := adminAccess.CreatePlugin(t.Context(), ScopeUser, "", "demo", map[string]ResourceFile{"plugin.json": {Data: manifest}})
	if err != nil {
		t.Fatal(err)
	}
	userAccess, err := service.Begin(user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userAccess.Get(t.Context(), created.Key.ID()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner read = %v, want forbidden", err)
	}
	guest, err := authz.NewGuestAuthority("guest", "channel")
	if err != nil {
		t.Fatal(err)
	}
	guestAccess, err := service.Begin(guest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guestAccess.List(t.Context(), ResourcePlugin, nil, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest list = %v, want forbidden", err)
	}
	if got, err := service.Capture(t.Context(), guest, ""); err != nil || len(got) != 0 {
		t.Fatalf("guest capture = %#v, %v", got, err)
	}
}

func TestFileAccessImportCopyAndNegativeToggle(t *testing.T) {
	service, admin, _ := fileAccessService(t)
	access, err := service.Begin(admin)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"$schema":"`+agentpackage.PluginSchemaV1+`","name":"source","version":"1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin"), []byte("run"), 0o755); err != nil {
		t.Fatal(err)
	}
	imported, err := access.ImportPlugin(t.Context(), source, ScopeSystem, "", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if imported.Package == nil || imported.Package.Manifest.Name != "renamed" {
		t.Fatalf("imported package = %#v", imported.Package)
	}
	file, _, err := access.ReadFile(t.Context(), imported.Key.ID(), "bin")
	if err != nil || file.Mode.Perm()&0o111 == 0 {
		t.Fatalf("imported executable = %#v, %v", file, err)
	}
	copied, err := access.CopyPlugin(t.Context(), imported.Key.ID(), ScopeUser, "", "copy")
	if err != nil || copied.Package == nil || copied.Package.Manifest.Name != "copy" {
		t.Fatalf("copy = %#v, %v", copied, err)
	}
	missingKey := ResourceKey{Scope: ScopeSystem, Kind: ResourcePlugin, Name: "negative"}
	if _, err := access.SetEnabled(t.Context(), missingKey.ID(), "", false); err != nil {
		t.Fatal(err)
	}
	negative, err := access.Get(t.Context(), missingKey.ID())
	if err != nil || !negative.Disabled {
		t.Fatalf("negative resource = %#v, %v", negative, err)
	}
	if _, err := access.SetEnabled(t.Context(), missingKey.ID(), negative.SettingsDigest, true); err != nil {
		t.Fatalf("negative enable = %v", err)
	}
}

type disabledFileAgentStore struct{}

func (disabledFileAgentStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	return config.Agent{ID: id, Scope: config.AgentScopeRestricted, CreatorID: "file-user", Enabled: false}, nil
}

func (disabledFileAgentStore) ListAgents(context.Context) ([]config.Agent, error) { return nil, nil }

type disabledFileAgentLinks struct{}

func (disabledFileAgentLinks) ListUserAgentIDs(context.Context, string) ([]string, error) {
	return []string{"file-agent"}, nil
}

func TestFileServiceReadSnapshotAllowsDisabledAgent(t *testing.T) {
	db := dbtest.New(t)
	userID := "00000000-0000-4000-8000-000000000779"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'file-disabled@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES('file-agent','Disabled File Agent','')`); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pep := agentaccess.NewService(disabledFileAgentStore{}, disabledFileAgentLinks{})
	service := NewFileService(NewResourceStore(manager), pep)
	admin, err := authz.NewUserAuthority(authz.UserID(userID), true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.CreatePlugin(t.Context(), ScopeSystem, "", "disabled-visible", map[string]ResourceFile{
		"plugin.json": {Data: []byte(`{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"disabled-visible"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := service.ReadSnapshot(t.Context(), reader, "file-agent")
	if err != nil {
		t.Fatalf("read snapshot for disabled agent = %v", err)
	}
	if len(resources) != 1 || resources[0].Key.Name != "disabled-visible" {
		t.Fatalf("read snapshot = %#v", resources)
	}
	if _, err := service.Capture(t.Context(), reader, "file-agent"); !errors.Is(err, agentaccess.ErrForbidden) {
		t.Fatalf("execute capture for disabled agent = %v, want forbidden", err)
	}
}

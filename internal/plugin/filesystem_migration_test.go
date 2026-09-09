package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestPreparedFileDigestIsIndependentOfMapOrderAndZipMetadata(t *testing.T) {
	first := map[string]ResourceFile{
		"plugin.json": {Data: []byte(`{"name":"demo"}`), Mode: 0o644},
		"bin/run":     {Data: []byte("run"), Mode: 0o755},
	}
	second := map[string]ResourceFile{
		"bin/run":     {Data: []byte("run"), Mode: 0o755},
		"plugin.json": {Data: []byte(`{"name":"demo"}`), Mode: 0o644},
	}
	if PreparedFileDigest(first) != PreparedFileDigest(second) {
		t.Fatal("digest changed with map insertion order")
	}
	second["bin/run"] = ResourceFile{Data: []byte("run"), Mode: 0o644}
	if PreparedFileDigest(first) == PreparedFileDigest(second) {
		t.Fatal("digest ignored executable mode")
	}
}

func TestLegacyFileExportDigestIncludesSourceAndMCPBinding(t *testing.T) {
	key := ResourceKey{Scope: ScopeUser, UserID: "user", Kind: ResourceMCP, Name: "demo"}
	base := LegacyFileExport{Entries: []LegacyFileExportEntry{{Key: key, Files: map[string]ResourceFile{
		"mcp/demo.json": {Data: []byte(`{"url":"https://example.invalid","transport":"sse"}`), Mode: 0o644},
	}, SourceDigest: "sha256:source"}}}
	changed := base
	changed.Entries = append([]LegacyFileExportEntry(nil), base.Entries...)
	changed.Entries[0].SourceDigest = "sha256:changed"
	if base.Digest() == changed.Digest() {
		t.Fatal("digest ignored source binding")
	}
	changed = base
	changed.MCPMappings = []MCPMigrationEvidence{{OldConfigID: "config", OldServerKey: "main", NewResource: key, OldCredential: "vault-ref"}}
	if base.Digest() == changed.Digest() {
		t.Fatal("digest ignored MCP mapping")
	}
}

func TestPrepareLegacyFileExportRemoteDisabledUsesMCPNegative(t *testing.T) {
	db := dbtest.New(t)
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	resources := NewResourceStore(manager)
	definitionID := "disabled-remote"
	spec, err := PublishDefinitionSpec(json.RawMessage(`{"origin":"remote_mcp","mcp_servers":{"main":{"url":"https://mcp.example","transport":"sse","auth_type":"none"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO plugin_definition(id,display_name,source,spec,default_enabled,revision) VALUES($1,'Disabled Remote','custom',$2::jsonb,false,1)`, definitionID, spec); err != nil {
		t.Fatal(err)
	}
	configID := "00000000-0000-4000-8000-000000000901"
	childID := "00000000-0000-4000-8000-000000000902"
	if _, err := db.Exec(t.Context(), `INSERT INTO plugin_config(id,plugin_id,scope,enabled,config,credential_refs,revision) VALUES($1,$2,'system',false,NULL,'{}',1)`, configID, definitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO plugin_config_mcp_server(id,config_id,server_key) VALUES($1,$2,'main')`, childID, configID); err != nil {
		t.Fatal(err)
	}
	service := NewLegacyService(db, nil, nil, nil)
	export, err := service.PrepareLegacyFileExport(t.Context(), resources)
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Entries) != 1 || export.Entries[0].Key.Kind != ResourceMCP || export.Entries[0].Key.Name != definitionID {
		t.Fatalf("entries=%+v", export.Entries)
	}
	settings := export.Settings[0].Settings
	if !slices.Contains(settings.Forbidden, "mcp:"+definitionID) {
		t.Fatalf("forbidden=%v, want mcp target", settings.Forbidden)
	}
	if slices.Contains(settings.Forbidden, "plugin:"+definitionID) {
		t.Fatalf("forbidden contains phantom package negative: %v", settings.Forbidden)
	}
}

type filesystemMigrationFixture struct {
	db      *pgxpool.Pool
	manager *home.WorkspaceManager
	store   *ResourceStore
	service *LegacyService
	userID  string
	agentID string
}

func newFilesystemMigrationFixture(t *testing.T) *filesystemMigrationFixture {
	t.Helper()
	db := dbtest.New(t)
	userID := "00000000-0000-4000-8000-000000000451"
	agentID := "migration-agent"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'migration@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'Migration Agent','')`, agentID); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store := NewResourceStore(manager)
	return &filesystemMigrationFixture{
		db: db, manager: manager, store: store,
		service: NewLegacyService(db, nil, nil, nil),
		userID:  userID, agentID: agentID,
	}
}

func migrationDefinitionSpec(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	spec, err := PublishDefinitionSpec(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func insertMigrationDefinition(t *testing.T, fixture *filesystemMigrationFixture, id string, source Source, spec json.RawMessage, defaultEnabled bool) {
	t.Helper()
	if _, err := fixture.db.Exec(t.Context(), `
		INSERT INTO plugin_definition(id,display_name,source,spec,default_enabled,revision)
		VALUES($1,$1,$2,$3::jsonb,$4,1)`, id, source, spec, defaultEnabled); err != nil {
		t.Fatal(err)
	}
}

func insertMigrationConfig(t *testing.T, fixture *filesystemMigrationFixture, id, pluginID string, scope Scope, userID, agentID string, enabled bool, payload json.RawMessage) {
	t.Helper()
	if _, err := fixture.db.Exec(t.Context(), `
		INSERT INTO plugin_config(id,plugin_id,scope,user_id,agent_id,enabled,config,credential_refs,revision)
		VALUES($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,''),$6,$7::jsonb,'{}',1)`, id, pluginID, scope, userID, agentID, enabled, payload); err != nil {
		t.Fatal(err)
	}
}

func migrationEntry(t *testing.T, export LegacyFileExport, key ResourceKey) LegacyFileExportEntry {
	t.Helper()
	for _, entry := range export.Entries {
		if entry.Key.ID() == key.ID() {
			return entry
		}
	}
	t.Fatalf("missing export entry %s", key.ID())
	return LegacyFileExportEntry{}
}

func TestPrepareLegacyFileExportMaterializesBuiltinDefault(t *testing.T) {
	fixture := newFilesystemMigrationFixture(t)
	definitionID := "builtin-default"
	insertMigrationDefinition(t, fixture, definitionID, SourceBuiltin, migrationDefinitionSpec(t, `{"version":"1.0.0"}`), true)

	export, err := fixture.service.PrepareLegacyFileExport(t.Context(), fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	key := ResourceKey{Scope: ScopeSystem, Kind: ResourcePlugin, Name: definitionID}
	entry := migrationEntry(t, export, key)
	if _, ok := entry.Files["plugin.json"]; !ok {
		t.Fatalf("builtin entry files = %v", entry.Files)
	}
	if err := fixture.service.PublishLegacyFileExport(t.Context(), fixture.store, export); err != nil {
		t.Fatal(err)
	}
	resource, err := fixture.store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if resource.Package == nil || resource.Package.Manifest.Name != definitionID {
		t.Fatalf("published builtin = %#v", resource)
	}
}

func TestPrepareLegacyFileExportPreservesCustomCASExecutable(t *testing.T) {
	fixture := newFilesystemMigrationFixture(t)
	contentStore, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := t.TempDir()
	manifest := `{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"custom-cas"}`
	if err := os.WriteFile(filepath.Join(packageRoot, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(packageRoot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(packageRoot, "bin", "run")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\necho migrated\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	published, err := agentpackage.PublishDirectory(packageRoot, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := migrationDefinitionSpec(t, `{"origin":"package","content":{"digest":"`+published.Digest+`"}}`)
	insertMigrationDefinition(t, fixture, "custom-cas", SourceCustom, spec, false)
	insertMigrationConfig(t, fixture, "00000000-0000-4000-8000-000000000461", "custom-cas", ScopeSystem, "", "", true, json.RawMessage(`{}`))
	fixture.service = NewLegacyService(fixture.db, nil, contentStore, nil)
	// PublishDirectory above wrote to a separate destination. Copy the exact
	// source into the configured CAS so the migration verifies its digest.
	if _, err := agentpackage.PublishDirectory(packageRoot, contentStore.root); err != nil {
		t.Fatal(err)
	}

	export, err := fixture.service.PrepareLegacyFileExport(t.Context(), fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	key := ResourceKey{Scope: ScopeSystem, Kind: ResourcePlugin, Name: "custom-cas"}
	entry := migrationEntry(t, export, key)
	if got := entry.Files["bin/run"].Mode.Perm() & 0o111; got != 0o111 {
		t.Fatalf("binary mode = %#o, want executable", got)
	}
	if err := fixture.service.PublishLegacyFileExport(t.Context(), fixture.store, export); err != nil {
		t.Fatal(err)
	}
	file, _, err := fixture.store.ReadFile(t.Context(), key, "bin/run")
	if err != nil {
		t.Fatal(err)
	}
	if file.Mode.Perm()&0o111 != 0o111 || !bytes.Contains(file.Data, []byte("migrated")) {
		t.Fatalf("published binary = mode %#o data %q", file.Mode.Perm(), file.Data)
	}
}

func TestLegacyFileExportCopiesAllScopesAndPreservesOverride(t *testing.T) {
	fixture := newFilesystemMigrationFixture(t)
	definitionID := "scope-copies"
	insertMigrationDefinition(t, fixture, definitionID, SourceBuiltin, migrationDefinitionSpec(t, `{"binaries":[{"name":"runner","tool":"runner","version":"release"}]}`), true)
	scopes := []struct {
		name                     string
		scope                    Scope
		userID, agentID, version string
	}{
		{"system", ScopeSystem, "", "", "system"},
		{"system-agent", ScopeSystemAgent, "", fixture.agentID, "system-agent"},
		{"user", ScopeUser, fixture.userID, "", "user"},
		{"user-agent", ScopeUserAgent, fixture.userID, fixture.agentID, "user-agent"},
	}
	for i, item := range scopes {
		payload := json.RawMessage(`{"binaries":{"runner":{"version":"` + item.version + `"}}}`)
		insertMigrationConfig(t, fixture, fmt.Sprintf("00000000-0000-4000-8000-%012d", 470+i), definitionID, item.scope, item.userID, item.agentID, true, payload)
	}
	export, err := fixture.service.PrepareLegacyFileExport(t.Context(), fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Entries) != len(scopes) {
		t.Fatalf("entries = %d, want %d", len(export.Entries), len(scopes))
	}
	if err := fixture.service.PublishLegacyFileExport(t.Context(), fixture.store, export); err != nil {
		t.Fatal(err)
	}
	roots := []struct {
		key  ResourceKey
		want string
	}{
		{ResourceKey{Scope: ScopeSystem, Kind: ResourcePlugin, Name: definitionID}, "system"},
		{ResourceKey{Scope: ScopeSystemAgent, AgentID: fixture.agentID, Kind: ResourcePlugin, Name: definitionID}, "system-agent"},
		{ResourceKey{Scope: ScopeUser, UserID: fixture.userID, Kind: ResourcePlugin, Name: definitionID}, "user"},
		{ResourceKey{Scope: ScopeUserAgent, UserID: fixture.userID, AgentID: fixture.agentID, Kind: ResourcePlugin, Name: definitionID}, "user-agent"},
	}
	for _, item := range roots {
		resource, err := fixture.store.Get(t.Context(), item.key)
		if err != nil {
			t.Fatal(err)
		}
		data, err := fs.ReadFile(resource.Content.FS(), "plugin.json")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"version":"`+item.want+`"`)) {
			t.Fatalf("%s manifest = %s", item.key.ID(), data)
		}
	}
}

func TestPublishLegacyFileExportDoesNotOverwriteDirectoryCollision(t *testing.T) {
	fixture := newFilesystemMigrationFixture(t)
	definitionID := "collision"
	insertMigrationDefinition(t, fixture, definitionID, SourceBuiltin, migrationDefinitionSpec(t, `{}`), true)
	export, err := fixture.service.PrepareLegacyFileExport(t.Context(), fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	key := ResourceKey{Scope: ScopeSystem, Kind: ResourcePlugin, Name: definitionID}
	if _, err := fixture.store.CreatePlugin(t.Context(), key, map[string]ResourceFile{
		"plugin.json": {Data: []byte(`{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"` + definitionID + `"}`), Mode: 0o644},
		"README.md":   {Data: []byte("preexisting"), Mode: 0o644},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.PublishLegacyFileExport(t.Context(), fixture.store, export); !errors.Is(err, ErrLegacyFileExportConflict) {
		t.Fatalf("collision error = %v, want conflict", err)
	}
	file, _, err := fixture.store.ReadFile(t.Context(), key, "README.md")
	if err != nil || string(file.Data) != "preexisting" {
		t.Fatalf("collision changed existing file = %q, %v", file.Data, err)
	}
}

func TestPrepareLegacyFileExportProjectsPluginToolOverrides(t *testing.T) {
	fixture := newFilesystemMigrationFixture(t)
	definitionID := "tool-policy"
	insertMigrationDefinition(t, fixture, definitionID, SourceBuiltin, migrationDefinitionSpec(t, `{"binaries":[{"name":"runner","tool":"runner"}]}`), true)
	insertMigrationConfig(t, fixture, "00000000-0000-4000-8000-000000000481", definitionID, ScopeSystem, "", "", true, json.RawMessage(`{}`))
	insertMigrationConfig(t, fixture, "00000000-0000-4000-8000-000000000482", definitionID, ScopeUser, fixture.userID, "", true, json.RawMessage(`{}`))
	if _, err := fixture.db.Exec(t.Context(), `
		INSERT INTO tool_override(plugin_id,local_tool_name,scope,enabled)
		VALUES($1,'runner','system',false)`, definitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(t.Context(), `
		INSERT INTO tool_override(plugin_id,local_tool_name,scope,user_id,enabled)
		VALUES($1,'runner','user',$2,false)`, definitionID, fixture.userID); err != nil {
		t.Fatal(err)
	}
	export, err := fixture.service.PrepareLegacyFileExport(t.Context(), fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	settings := make(map[string]ResourceSettings, len(export.Settings))
	for _, item := range export.Settings {
		settings[legacyOwnerID(item.Scope, item.UserID, item.AgentID)] = item.Settings
	}
	for _, owner := range []struct {
		scope Scope
		user  string
		want  string
	}{
		{ScopeSystem, "", "system"},
		{ScopeUser, fixture.userID, "user"},
	} {
		got, ok := settings[legacyOwnerID(owner.scope, owner.user, "")]
		if !ok {
			t.Fatalf("missing settings for %s", owner.scope)
		}
		if !slices.Equal(got.DisabledTools["plugin:"+definitionID], []string{"runner"}) {
			t.Fatalf("%s disabled tools = %#v", owner.scope, got.DisabledTools)
		}
	}
}

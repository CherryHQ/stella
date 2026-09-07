package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestAccessUpdateConfigUsesExpectedRevision(t *testing.T) {
	db := dbtest.New(t)
	catalog := NewCatalog()
	definition := Definition{
		ID: "cas", DisplayName: "CAS",
		Source: SourceBuiltin, Spec: publishedSpec(t, `{}`), DefaultEnabled: true, Revision: 1,
	}
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, catalog, BackendPolicy{Transition: inlineBackendPolicyTransition}, func(_ context.Context, fn func() error) error { return fn() })
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("sync defaults: %v", err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := access.ListConfigs(t.Context(), definition.ID, ScopeSystem, "")
	if err != nil {
		t.Fatalf("list system configs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("system config count = %d, want 1", len(configs))
	}
	initialRevision := configs[0].Revision
	disabled := false
	updated, err := access.UpdateConfig(t.Context(), definition.ID, configs[0].ID, initialRevision, ConfigPatch{
		EnabledSet: true,
		Enabled:    &disabled,
	})
	if err != nil {
		t.Fatalf("disable config: %v", err)
	}
	if updated.Revision <= initialRevision || updated.Enabled == nil || *updated.Enabled {
		t.Fatalf("updated config = %+v, want disabled revision", updated)
	}
	if _, err := access.UpdateConfig(t.Context(), definition.ID, configs[0].ID, initialRevision, ConfigPatch{
		EnabledSet: true,
		Enabled:    &disabled,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}
}

func TestSyncBuiltinDefaultsReconcilesMCPChildrenFromDefinition(t *testing.T) {
	db := dbtest.New(t)
	catalog := NewCatalog()
	definition := Definition{
		ID: "mcp-bundle", DisplayName: "MCP bundle", Source: SourceBuiltin,
		Spec:           publishedSpec(t, `{"mcp_servers":{"alpha":{"url":"https://alpha.example","transport":"sse"},"beta":{"url":"https://beta.example","transport":"sse"}}}`),
		DefaultEnabled: true, Revision: 1,
	}
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, catalog, BackendPolicy{Transition: inlineBackendPolicyTransition}, inlineBackendPolicyFence)
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := access.ListConfigs(t.Context(), definition.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 {
		t.Fatalf("system configs = %d/%v, want one", len(configs), err)
	}
	if len(configs[0].MCPServers) != 2 {
		t.Fatalf("first sync MCP children = %#v, want two", configs[0].MCPServers)
	}
	ids := map[string]string{}
	for _, child := range configs[0].MCPServers {
		ids[child.ServerKey] = child.ID
	}
	// The returned projection is caller-owned. A forged child must not become
	// durable state or alter the next read.
	configs[0].MCPServers = append(configs[0].MCPServers, MCPServerChild{ID: "forged", ServerKey: "forged"})
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	configs, err = access.ListConfigs(t.Context(), definition.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 {
		t.Fatalf("second system configs = %d/%v, want one", len(configs), err)
	}
	if len(configs[0].MCPServers) != 2 {
		t.Fatalf("second sync MCP children = %#v, want two", configs[0].MCPServers)
	}
	for _, child := range configs[0].MCPServers {
		if child.ID != ids[child.ServerKey] {
			t.Fatalf("child %q ID changed from %q to %q", child.ServerKey, ids[child.ServerKey], child.ID)
		}
	}
}

func TestAccessCreateCustomMCPResourceContentBoundary(t *testing.T) {
	db := dbtest.New(t)
	userID := seedMoveUser(t, db)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, NewCatalog(), BackendPolicy{
		Validate:   func(context.Context, Definition, Config, []string) error { return nil },
		Transition: inlineBackendPolicyTransition,
	}, func(_ context.Context, fn func() error) error { return fn() })
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	base := Definition{ID: "remote-boundary", DisplayName: "Remote", Spec: publishedSpec(t, `{"mcp_servers":{"main":{"url":"https://declared.example","transport":"sse","auth_type":"none"}}}`)}
	for _, payload := range []string{`{"binaries":[]}`, `{"session_env":[]}`} {
		_, _, err := access.CreateCustom(t.Context(), base, Config{Scope: ScopeUser, Enabled: boolPtr(true), Payload: json.RawMessage(payload)})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("payload %s error = %v, want ErrForbidden", payload, err)
		}
	}
	createdDef, createdConfig, err := access.CreateCustom(t.Context(), base, Config{Scope: ScopeUser, Enabled: boolPtr(true), Payload: json.RawMessage(`{"mcp_servers":{"main":{"url":"https://example.test","transport":"sse","auth_type":"none","credential_mode":"shared"}}}`)})
	if err != nil {
		t.Fatalf("valid remote MCP create: %v", err)
	}
	if createdDef.ID != base.ID || createdConfig.PluginID != base.ID {
		t.Fatalf("created identities = %q/%q", createdDef.ID, createdConfig.PluginID)
	}
}

func TestAccessCreateCustomFromDirectoryRejectsNonAdmin(t *testing.T) {
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", false)
	if err != nil {
		t.Fatal(err)
	}
	access := &Access{service: &Service{}, authority: authority}
	if _, _, err := access.CreateCustomFromDirectory(t.Context(), t.TempDir(), Config{Scope: ScopeUser}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin package create = %v, want ErrForbidden", err)
	}
}

func TestAccessCreateCustomRejectsRawPackageDeclaration(t *testing.T) {
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access := &Access{service: &Service{txBound: true}, authority: authority}
	_, _, err = access.CreateCustom(t.Context(), Definition{
		ID: "claimed-package", DisplayName: "Claimed package",
		Spec: json.RawMessage(`{"origin":"package","content":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"skills":[{"name":"claimed"}]}`),
	}, Config{Scope: ScopeSystem, Enabled: boolPtr(false)})
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("raw package create = %v, want ErrInvalidDefinition", err)
	}
}

func TestAccessDirectoryPackageRejectsNestedMutation(t *testing.T) {
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access := &Access{service: &Service{txBound: true}, authority: authority}
	if _, _, err := access.CreateCustomFromDirectory(t.Context(), t.TempDir(), Config{Scope: ScopeSystem}); !errors.Is(err, ErrNestedMutation) {
		t.Fatalf("nested package create = %v, want ErrNestedMutation", err)
	}
}

func TestAccessDirectoryPackageIdentityCASAndConfigIdentity(t *testing.T) {
	db := dbtest.New(t)
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, NewCatalog(), BackendPolicy{
		Validate:   func(context.Context, Definition, Config, []string) error { return nil },
		Transition: inlineBackendPolicyTransition,
	}, inlineBackendPolicyFence, WithContentStore(store))
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := access.CreateCustomFromDirectory(t.Context(), testPackageDirectory(t, "cas.package", "one"), Config{Scope: ScopeSystem})
	if err != nil {
		t.Fatalf("first package create: %v", err)
	}
	configs, err := access.ListConfigs(t.Context(), first.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 {
		t.Fatalf("created configs = %d/%v, want one", len(configs), err)
	}
	configID := configs[0].ID

	if _, err := access.UpdateDefinitionFromDirectory(t.Context(), first.ID, first.Revision, testPackageDirectory(t, "other.package", "wrong id")); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("different package identity update = %v, want ErrInvalidDefinition", err)
	}
	if _, err := access.UpdateDefinitionFromDirectory(t.Context(), first.ID, first.Revision+1, testPackageDirectory(t, "cas.package", "stale")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale package update = %v, want ErrConflict", err)
	}
	updated, err := access.UpdateDefinitionFromDirectory(t.Context(), first.ID, first.Revision, testPackageDirectory(t, "cas.package", "two"))
	if err != nil {
		t.Fatalf("same identity package update: %v", err)
	}
	if updated.Revision != first.Revision+1 {
		t.Fatalf("updated revision = %d, want %d", updated.Revision, first.Revision+1)
	}
	configs, err = access.ListConfigs(t.Context(), first.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 || configs[0].ID != configID {
		t.Fatalf("config identity after package update = %#v/%v, want %q", configs, err, configID)
	}
}

func testPackageDirectory(t *testing.T, name, description string) string {
	t.Helper()
	root := t.TempDir()
	manifest := `{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"` + name + `","description":"` + description + `"}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAccessMoveConfigPreservesIDAndDerivesTargetUser(t *testing.T) {
	db := dbtest.New(t)
	userID := seedMoveUser(t, db)
	seedMoveAgent(t, db, "agent")
	service, definition := newMoveService(t, db, agentaccess.NewService(assignedAgentStore{}, assignedAgentLinks{}), nil)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	created, err := access.CreateConfig(t.Context(), Config{PluginID: definition.ID, Scope: ScopeUser, Enabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	moved, err := access.MoveConfig(t.Context(), definition.ID, created.ID, created.Revision, ScopeUserAgent, "agent", ConfigPatch{})
	if err != nil {
		t.Fatalf("MoveConfig: %v", err)
	}
	if moved.ID != created.ID || moved.Revision != created.Revision+1 {
		t.Fatalf("moved identity = %s/revision %d, want %s/revision %d", moved.ID, moved.Revision, created.ID, created.Revision+1)
	}
	if moved.Scope != ScopeUserAgent || moved.UserID != userID || moved.AgentID != "agent" {
		t.Fatalf("moved owner = %q/%q/%q, want user_agent/%s/agent", moved.Scope, moved.UserID, moved.AgentID, userID)
	}
}

func TestAccessMoveConfigRejectsUnauthorizedAgentSource(t *testing.T) {
	db := dbtest.New(t)
	userID := seedMoveUser(t, db)
	seedMoveAgent(t, db, "agent")
	allowedService, definition := newMoveService(t, db, agentaccess.NewService(assignedAgentStore{}, assignedAgentLinks{}), nil)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := allowedService.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	created, err := allowed.CreateConfig(t.Context(), Config{PluginID: definition.ID, Scope: ScopeUserAgent, AgentID: "agent", Enabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	deniedService := NewService(db, agentaccess.NewService(denyingMoveAgentStore{}, denyingMoveAgentLinks{}), allowedService.catalog, BackendPolicy{Transition: inlineBackendPolicyTransition}, func(_ context.Context, fn func() error) error { return fn() })
	denied, err := deniedService.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denied.MoveConfig(t.Context(), definition.ID, created.ID, created.Revision, ScopeUser, "", ConfigPatch{}); err == nil {
		t.Fatal("MoveConfig accepted an unauthorized source agent")
	}
}

func TestAccessMoveConfigRejectsBuiltinSystemSource(t *testing.T) {
	db := dbtest.New(t)
	service, definition := newMoveService(t, db, nil, nil)
	authority, err := authz.NewUserAuthority(authz.UserID("10000000-0000-0000-0000-000000000001"), true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := access.ListConfigs(t.Context(), definition.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 {
		t.Fatalf("ListConfigs = %d/%v, want one system config", len(configs), err)
	}
	if _, err := access.MoveConfig(t.Context(), definition.ID, configs[0].ID, configs[0].Revision, ScopeUser, "", ConfigPatch{}); !errors.Is(err, ErrBuiltinConfig) {
		t.Fatalf("MoveConfig error = %v, want ErrBuiltinConfig", err)
	}
}

func TestAccessMoveConfigRejectsStaleCASAndTargetCollision(t *testing.T) {
	db := dbtest.New(t)
	userID := seedMoveUser(t, db)
	seedMoveAgent(t, db, "agent")
	service, definition := newMoveService(t, db, agentaccess.NewService(assignedAgentStore{}, assignedAgentLinks{}), nil)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	first, err := access.CreateConfig(t.Context(), Config{PluginID: definition.ID, Scope: ScopeUser, Enabled: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := access.CreateConfig(t.Context(), Config{PluginID: definition.ID, Scope: ScopeUserAgent, AgentID: "agent", Enabled: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.MoveConfig(t.Context(), definition.ID, first.ID, first.Revision, ScopeUserAgent, "agent", ConfigPatch{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("target collision error = %v, want ErrConflict", err)
	}
	unchanged, err := access.GetConfig(t.Context(), definition.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Scope != ScopeUser || unchanged.Revision != first.Revision {
		t.Fatalf("collision mutated source = %+v", unchanged)
	}
	moved, err := access.MoveConfig(t.Context(), definition.ID, first.ID, first.Revision, ScopeUser, "", ConfigPatch{})
	if err != nil {
		t.Fatalf("same-tuple move: %v", err)
	}
	if _, err := access.MoveConfig(t.Context(), definition.ID, first.ID, first.Revision, ScopeUser, "", ConfigPatch{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error = %v, want ErrConflict", err)
	}
	if moved.ID != first.ID || moved.Revision != first.Revision+1 {
		t.Fatalf("same-tuple move = %+v", moved)
	}
	_ = second
}

func TestAccessMoveConfigValidatesBeforeMutation(t *testing.T) {
	db := dbtest.New(t)
	userID := seedMoveUser(t, db)
	service, definition := newMoveService(t, db, nil, func(context.Context, Definition, Config, []string) error { return nil })
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	created, err := access.CreateConfig(t.Context(), Config{PluginID: definition.ID, Scope: ScopeUser, Enabled: boolPtr(false), Payload: []byte(`{"binaries":{"tool":{"version":"1"}}}`)})
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	service.policy.Validate = func(context.Context, Definition, Config, []string) error { return ErrInvalidConfig }
	if _, err := access.MoveConfig(t.Context(), definition.ID, created.ID, created.Revision, ScopeUser, "", ConfigPatch{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("validation error = %v, want ErrInvalidConfig", err)
	}
	unchanged, err := access.GetConfig(t.Context(), definition.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Scope != ScopeUser || unchanged.Revision != created.Revision {
		t.Fatalf("failed move mutated config = %+v", unchanged)
	}
}

func newMoveService(t *testing.T, db *pgxpool.Pool, agents *agentaccess.Service, validate PayloadValidator) (*Service, Definition) {
	t.Helper()
	catalog := NewCatalog()
	definition := Definition{ID: "move", DisplayName: "Move", Source: SourceBuiltin, Spec: publishedSpec(t, `{"binaries":[{"name":"tool","tool":"uv","version":"latest"}]}`), DefaultEnabled: false, Revision: 1}
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, agents, catalog, BackendPolicy{Validate: validate, Transition: inlineBackendPolicyTransition}, func(_ context.Context, fn func() error) error { return fn() })
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("SyncBuiltinDefaults: %v", err)
	}
	return service, definition
}

func seedMoveUser(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, id, id+"@move.test"); err != nil {
		t.Fatalf("insert move user: %v", err)
	}
	return id
}

func seedMoveAgent(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `INSERT INTO agent (id, name, workspace, scope, creator_id) VALUES ($1, $2, '/tmp', 'restricted', '')`, id, id); err != nil {
		t.Fatalf("insert move agent: %v", err)
	}
}

type denyingMoveAgentStore struct{}

func (denyingMoveAgentStore) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{ID: "agent", Scope: config.AgentScopeRestricted, Enabled: true}, nil
}

func (denyingMoveAgentStore) ListAgents(context.Context) ([]config.Agent, error) { return nil, nil }

type denyingMoveAgentLinks struct{}

func (denyingMoveAgentLinks) ListUserAgentIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func TestBackendPolicyCASConflictSkipsTransition(t *testing.T) {
	db := dbtest.New(t)
	catalog := NewCatalog()
	definition := backendPolicyDefinition("policy-cas", false)
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	var transitions int
	service := NewService(db, nil, catalog, BackendPolicy{
		Transition: func(context.Context, pgx.Tx, authz.Authority, MutationKind, Definition, *Config, *Config) error {
			transitions++
			return nil
		},
	}, inlineBackendPolicyFence)
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("sync defaults: %v", err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := access.ListConfigs(t.Context(), definition.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 {
		t.Fatalf("list system config = %d/%v, want one", len(configs), err)
	}
	initial := configs[0]
	disabled := false
	if _, err := access.UpdateConfig(t.Context(), definition.ID, initial.ID, initial.Revision, ConfigPatch{EnabledSet: true, Enabled: &disabled}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if transitions != 1 {
		t.Fatalf("transition count after first update = %d, want 1", transitions)
	}
	if _, err := access.UpdateConfig(t.Context(), definition.ID, initial.ID, initial.Revision, ConfigPatch{EnabledSet: true, Enabled: &disabled}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	if transitions != 1 {
		t.Fatalf("transition count after stale update = %d, want unchanged", transitions)
	}
}

func TestBackendPolicyTransitionErrorRollsBackConfig(t *testing.T) {
	db := dbtest.New(t)
	catalog := NewCatalog()
	definition := backendPolicyDefinition("policy-rollback", true)
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("backend transition rejected")
	var hookSawCommittedRow bool
	service := NewService(db, nil, catalog, BackendPolicy{
		Transition: func(ctx context.Context, tx pgx.Tx, _ authz.Authority, _ MutationKind, _ Definition, _ *Config, after *Config) error {
			var revision int64
			if err := tx.QueryRow(ctx, `SELECT revision FROM plugin_config WHERE id = $1`, after.ID).Scan(&revision); err != nil {
				return err
			}
			hookSawCommittedRow = revision == after.Revision
			return wantErr
		},
	}, inlineBackendPolicyFence)
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("sync defaults: %v", err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := access.ListConfigs(t.Context(), definition.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 {
		t.Fatalf("list system config = %d/%v, want one", len(configs), err)
	}
	before := configs[0]
	disabled := false
	if _, err := access.UpdateConfig(t.Context(), definition.ID, before.ID, before.Revision, ConfigPatch{EnabledSet: true, Enabled: &disabled}); !errors.Is(err, wantErr) {
		t.Fatalf("transition error = %v, want %v", err, wantErr)
	}
	if !hookSawCommittedRow {
		t.Fatal("transition did not observe the post-CAS row in the same transaction")
	}
	after, err := access.GetConfig(t.Context(), definition.ID, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || !sameBackendPolicyBool(after.Enabled, before.Enabled) {
		t.Fatalf("config after transition rollback = %+v, want revision/enabled %+v", after, before)
	}
}

func TestBackendPolicyValidateOnlyFailsClosedAndRollsBack(t *testing.T) {
	db := dbtest.New(t)
	catalog := NewCatalog()
	definition := backendPolicyDefinition("policy-no-transition", false)
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, catalog, BackendPolicy{
		Validate: func(context.Context, Definition, Config, []string) error { return nil },
	}, inlineBackendPolicyFence)
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("sync defaults: %v", err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := access.ListConfigs(t.Context(), definition.ID, ScopeSystem, "")
	if err != nil || len(configs) != 1 {
		t.Fatalf("list system config = %d/%v, want one", len(configs), err)
	}
	before := configs[0]
	disabled := false
	if _, err := access.UpdateConfig(t.Context(), definition.ID, before.ID, before.Revision, ConfigPatch{EnabledSet: true, Enabled: &disabled}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("validate-only update = %v, want ErrInvalidConfig", err)
	}
	after, err := access.GetConfig(t.Context(), definition.ID, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || !sameBackendPolicyBool(after.Enabled, before.Enabled) {
		t.Fatalf("config after fail-closed rollback = %+v, want unchanged %+v", after, before)
	}
}

func sameBackendPolicyBool(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func backendPolicyDefinition(id string, defaultEnabled bool) Definition {
	return Definition{
		ID: id, DisplayName: id, Source: SourceBuiltin,
		Spec: publishedSpecOrPanic(`{}`), DefaultEnabled: defaultEnabled, Revision: 1,
	}
}

func inlineBackendPolicyFence(_ context.Context, fn func() error) error { return fn() }

func inlineBackendPolicyTransition(context.Context, pgx.Tx, authz.Authority, MutationKind, Definition, *Config, *Config) error {
	return nil
}

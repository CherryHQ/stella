package vault_test

import (
	"context"
	"encoding/json"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// vaultTestDB combines OIDCStore (for auth_user) with sqlc.Queries (for vault_entry).
type vaultTestDB struct {
	oidc *appdb.OIDCStore
	q    *sqlc.Queries
}

func (d *vaultTestDB) GetVaultUser(ctx context.Context, id string) (sqlc.VaultUser, error) {
	u, err := d.oidc.GetUser(ctx, id)
	if err != nil {
		return sqlc.VaultUser{}, err
	}
	return sqlc.VaultUser{AgePublicKey: u.AgePublicKey, AgePrivateKey: u.AgePrivateKey}, nil
}

func (d *vaultTestDB) GetVaultEntryByScope(ctx context.Context, arg sqlc.GetVaultEntryByScopeParams) (sqlc.VaultEntry, error) {
	return d.q.GetVaultEntryByScope(ctx, arg)
}

func (d *vaultTestDB) ListVaultEntriesByScope(ctx context.Context, arg sqlc.ListVaultEntriesByScopeParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesByScope(ctx, arg)
}

func (d *vaultTestDB) ListVaultEntriesForRuntime(ctx context.Context, arg sqlc.ListVaultEntriesForRuntimeParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesForRuntime(ctx, arg)
}

func (d *vaultTestDB) ListVaultEntriesForRuntimeFull(ctx context.Context, arg sqlc.ListVaultEntriesForRuntimeFullParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesForRuntimeFull(ctx, arg)
}

func (d *vaultTestDB) ListVaultEntryAgentBindings(ctx context.Context, vaultEntryID string) ([]string, error) {
	return d.q.ListVaultEntryAgentBindings(ctx, vaultEntryID)
}

func (d *vaultTestDB) ListVaultEntryProjectBindings(ctx context.Context, vaultEntryID string) ([]string, error) {
	return d.q.ListVaultEntryProjectBindings(ctx, vaultEntryID)
}

func (d *vaultTestDB) ReplaceVaultEntryAgentBindings(ctx context.Context, arg sqlc.ReplaceVaultEntryAgentBindingsParams) error {
	return d.q.ReplaceVaultEntryAgentBindings(ctx, arg)
}

func (d *vaultTestDB) ReplaceVaultEntryProjectBindings(ctx context.Context, arg sqlc.ReplaceVaultEntryProjectBindingsParams) error {
	return d.q.ReplaceVaultEntryProjectBindings(ctx, arg)
}

func (d *vaultTestDB) UpsertVaultEntryByScope(ctx context.Context, arg sqlc.UpsertVaultEntryByScopeParams) (sqlc.VaultEntry, error) {
	return d.q.UpsertVaultEntryByScope(ctx, arg)
}

func (d *vaultTestDB) DeleteVaultEntryByScope(ctx context.Context, arg sqlc.DeleteVaultEntryByScopeParams) error {
	return d.q.DeleteVaultEntryByScope(ctx, arg)
}

// testService sets up a vault Service backed by a real SQLite database. It
// creates a user with age keys provisioned and returns the service, oidcStore,
// and the created user ID.
func testService(t *testing.T) (*vault.Service, *appdb.OIDCStore, string) {
	t.Helper()
	svc, oidc, userID, _ := testServiceWithQueries(t)
	return svc, oidc, userID
}

func testServiceWithQueries(t *testing.T) (*vault.Service, *appdb.OIDCStore, string, *sqlc.Queries) {
	t.Helper()

	db := dbtest.New(t)

	oidc := appdb.NewOIDCStore(db)
	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity (master): %v", err)
	}

	testDB := &vaultTestDB{oidc: oidc, q: q}
	svc, err := vault.NewService(testDB, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	user, err := oidc.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "testuser@vault.test",
		Name:  "Test User",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := oidc.UpdateUserAgeKeys(ctx, user.ID, pubKey, encPrivKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}

	return svc, oidc, user.ID, q
}

func TestSetAndList(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, userID, "GITHUB_TOKEN", "ghp_secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	entries, err := svc.List(ctx, userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List: got %d entries, want 1", len(entries))
	}
	if entries[0].Name != "GITHUB_TOKEN" {
		t.Errorf("Name = %q, want %q", entries[0].Name, "GITHUB_TOKEN")
	}
	if entries[0].CreatedAt == "" {
		t.Error("CreatedAt is empty")
	}
	if entries[0].UpdatedAt == "" {
		t.Error("UpdatedAt is empty")
	}
}

func TestSetScopedRejectsSystemScope(t *testing.T) {
	t.Parallel()
	svc, _, _ := testService(t)
	ctx := context.Background()

	if err := svc.SetScoped(ctx, vault.ScopeSystem, "", "", "GLOBAL_TOKEN", "value"); err == nil {
		t.Fatal("SetScoped should reject system scope")
	}
	if err := svc.SetSystemScoped(ctx, vault.ScopeSystem, "", "GLOBAL_TOKEN", "value"); err != nil {
		t.Fatalf("SetSystemScoped: %v", err)
	}
	entries, err := svc.ListSystemScoped(ctx, vault.ScopeSystem, "")
	if err != nil {
		t.Fatalf("ListSystemScoped: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "GLOBAL_TOKEN" {
		t.Fatalf("entries = %+v, want GLOBAL_TOKEN", entries)
	}
}

func TestScopedWriteRejectsManagedToken(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	// Public scoped writes must never set the managed STELLA_TOKEN: a user_agent
	// entry would shadow it and force a token rotation on every sandbox start.
	if err := svc.SetScoped(ctx, vault.ScopeUserAgent, userID, "agent-1", vault.StellaTokenName, "value"); err == nil {
		t.Fatal("SetScoped should reject STELLA_TOKEN")
	}

	// The internal reserved path still owns the token.
	if err := svc.SetReserved(ctx, userID, vault.StellaTokenName, "managed"); err != nil {
		t.Fatalf("SetReserved(STELLA_TOKEN): %v", err)
	}
}

func TestSetValidation(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	invalid := []string{
		"",
		"lowercase",
		"123START",
		"HAS SPACE",
		"STELLA_SECRET",
		"STELLA_TOKEN",
		"PATH",
		"HOME",
		"LC_ALL",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := svc.Set(ctx, userID, name, "value"); err == nil {
				t.Errorf("Set(%q) = nil, want error", name)
			}
		})
	}
}

func TestLoadEnvDefaultNotInjected(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, userID, "API_KEY", "sk_test_123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	env, err := svc.LoadEnvForAgentProject(ctx, userID, "", "")
	if err != nil {
		t.Fatalf("LoadEnvForAgentProject: %v", err)
	}
	if _, ok := env["API_KEY"]; ok {
		t.Fatal("new vault entry was injected without a binding")
	}
	// LoadEnv is the unfiltered host-side view (OAuth bundle resolution).
	full, err := svc.LoadEnv(ctx, userID)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if got := full["API_KEY"]; got != "sk_test_123" {
		t.Fatalf("full env API_KEY = %q, want sk_test_123", got)
	}
}

func TestLoadEnvInjectAlways(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	injectAlways := true
	if err := svc.SetScopedWithOptions(ctx, vault.ScopeUser, userID, "", "API_KEY", "sk_test_123", vault.SetOptions{InjectAlways: &injectAlways}); err != nil {
		t.Fatalf("SetScopedWithOptions: %v", err)
	}
	env, err := svc.LoadEnv(ctx, userID)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if got := env["API_KEY"]; got != "sk_test_123" {
		t.Fatalf("API_KEY = %q, want sk_test_123", got)
	}
}

func TestLoadEnvBoundEntryMatchesAgentOrProject(t *testing.T) {
	t.Parallel()
	svc, _, userID, q := testServiceWithQueries(t)
	ctx := context.Background()

	createVaultTestAgent(t, q, "agent-bound")
	createVaultTestAgent(t, q, "agent-other")
	projectID := uuid.NewString()
	if _, err := q.CreateProject(ctx, sqlc.CreateProjectParams{ID: projectID, AgentID: "agent-other", UserID: userID, Name: "proj", BaseDir: "/tmp/proj"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := svc.SetScopedWithOptions(ctx, vault.ScopeUser, userID, "", "AGENT_TOKEN", "agent-value", vault.SetOptions{InjectAgentIDs: []string{"agent-bound"}, ReplaceAgents: true}); err != nil {
		t.Fatalf("SetScopedWithOptions(agent): %v", err)
	}
	if err := svc.SetScopedWithOptions(ctx, vault.ScopeUser, userID, "", "PROJECT_TOKEN", "project-value", vault.SetOptions{InjectProjectIDs: []string{projectID}, ReplaceProjects: true}); err != nil {
		t.Fatalf("SetScopedWithOptions(project): %v", err)
	}

	env, err := svc.LoadEnvForAgentProject(ctx, userID, "agent-bound", "")
	if err != nil {
		t.Fatalf("LoadEnvForAgentProject(agent-bound): %v", err)
	}
	if got := env["AGENT_TOKEN"]; got != "agent-value" {
		t.Fatalf("AGENT_TOKEN for bound agent = %q, want agent-value", got)
	}
	if _, ok := env["PROJECT_TOKEN"]; ok {
		t.Fatal("PROJECT_TOKEN injected without matching project")
	}

	env, err = svc.LoadEnvForAgentProject(ctx, userID, "agent-other", projectID)
	if err != nil {
		t.Fatalf("LoadEnvForAgentProject(project): %v", err)
	}
	if _, ok := env["AGENT_TOKEN"]; ok {
		t.Fatal("AGENT_TOKEN injected for unbound agent")
	}
	if got := env["PROJECT_TOKEN"]; got != "project-value" {
		t.Fatalf("PROJECT_TOKEN for bound project = %q, want project-value", got)
	}
}

func TestLoadEnvMigratedLegacyEntryInjected(t *testing.T) {
	t.Parallel()
	svc, oidc, userID, q := testServiceWithQueries(t)
	ctx := context.Background()

	user, err := oidc.GetUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	ciphertext, err := vault.Encrypt(user.AgePublicKey, "legacy-value")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := q.UpsertVaultEntryByScope(ctx, sqlc.UpsertVaultEntryByScopeParams{ID: uuid.NewString(), Scope: vault.ScopeUser, UserID: sqlcNullString(userID), Name: "LEGACY_TOKEN", Ciphertext: ciphertext, InjectAlways: true}); err != nil {
		t.Fatalf("insert legacy entry: %v", err)
	}
	env, err := svc.LoadEnv(ctx, userID)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if got := env["LEGACY_TOKEN"]; got != "legacy-value" {
		t.Fatalf("LEGACY_TOKEN = %q, want legacy-value", got)
	}
}

func TestLoadEnvForAgentMergesScopedPrecedence(t *testing.T) {
	t.Parallel()
	svc, _, userID, q := testServiceWithQueries(t)
	ctx := context.Background()

	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-a", Name: "Agent A", Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-b", Name: "Agent B", Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	sets := []struct {
		scope   string
		agentID string
		value   string
	}{
		{scope: vault.ScopeSystem, value: "system"},
		{scope: vault.ScopeSystemAgent, agentID: "agent-a", value: "system-agent"},
		{scope: vault.ScopeUser, value: "user"},
		{scope: vault.ScopeUserAgent, agentID: "agent-a", value: "user-agent"},
	}
	injectAlways := true
	for _, set := range sets {
		var err error
		opts := vault.SetOptions{InjectAlways: &injectAlways}
		if set.scope == vault.ScopeSystem || set.scope == vault.ScopeSystemAgent {
			err = svc.SetSystemScopedWithOptions(ctx, set.scope, set.agentID, "TOKEN", set.value, opts)
		} else {
			err = svc.SetScopedWithOptions(ctx, set.scope, userIDForScope(set.scope, userID), set.agentID, "TOKEN", set.value, opts)
		}
		if err != nil {
			t.Fatalf("set %s: %v", set.scope, err)
		}
	}

	env, err := svc.LoadEnvForAgent(ctx, userID, "agent-a")
	if err != nil {
		t.Fatalf("LoadEnvForAgent(agent-a): %v", err)
	}
	if got := env["TOKEN"]; got != "user-agent" {
		t.Fatalf("TOKEN for agent-a = %q, want user-agent", got)
	}

	env, err = svc.LoadEnvForAgent(ctx, userID, "agent-b")
	if err != nil {
		t.Fatalf("LoadEnvForAgent(agent-b): %v", err)
	}
	if got := env["TOKEN"]; got != "user" {
		t.Fatalf("TOKEN for agent-b = %q, want user", got)
	}
}

func createVaultTestAgent(t *testing.T, q *sqlc.Queries, id string) {
	t.Helper()
	if _, err := q.CreateAgent(context.Background(), sqlc.CreateAgentParams{
		ID: id, Name: id, Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent(%s): %v", id, err)
	}
}

func userIDForScope(scope string, userID string) string {
	if scope == vault.ScopeUser || scope == vault.ScopeUserAgent {
		return userID
	}
	return ""
}

func TestNewServiceInvalidKey(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)

	oidc := appdb.NewOIDCStore(db)
	testDB := &vaultTestDB{oidc: oidc, q: sqlc.New(db)}
	_, err := vault.NewService(testDB, "not-a-valid-age-key")
	if err == nil {
		t.Fatal("NewService with invalid key should fail")
	}
}

func TestSetNoAgeKeys(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)

	oidc := appdb.NewOIDCStore(db)
	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	testDB := &vaultTestDB{oidc: oidc, q: q}
	svc, err := vault.NewService(testDB, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	user, err := oidc.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "nokeys@vault.test",
		Name:  "No Keys",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := svc.Set(ctx, user.ID, "MY_KEY", "value"); err == nil {
		t.Fatal("Set should fail for user without age keys")
	}
}

func TestLoadEnvDoesNotAutoCreateStellaToken(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	env, err := svc.LoadEnv(ctx, userID)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if _, ok := env[vault.StellaTokenName]; ok {
		t.Fatalf("LoadEnv included %q; token service should create it", vault.StellaTokenName)
	}
}

func TestLoadEnvForAgentKeepsSystemSecretsWhenUserEntryFails(t *testing.T) {
	t.Parallel()
	svc, _, userID, q := testServiceWithQueries(t)
	ctx := context.Background()

	injectAlways := true
	if err := svc.SetSystemScopedWithOptions(ctx, vault.ScopeSystem, "", "GLOBAL_TOKEN", "system-value", vault.SetOptions{InjectAlways: &injectAlways}); err != nil {
		t.Fatalf("SetSystemScopedWithOptions: %v", err)
	}
	if _, err := q.UpsertVaultEntryByScope(ctx, sqlc.UpsertVaultEntryByScopeParams{
		ID: uuid.NewString(), Scope: vault.ScopeUser, UserID: sqlcNullString(userID), Name: "BROKEN_TOKEN", Ciphertext: "not-age", InjectAlways: true,
	}); err != nil {
		t.Fatalf("insert broken user entry: %v", err)
	}

	env, err := svc.LoadEnvForAgent(ctx, userID, "agent-a")
	if err != nil {
		t.Fatalf("LoadEnvForAgent: %v", err)
	}
	if got := env["GLOBAL_TOKEN"]; got != "system-value" {
		t.Fatalf("GLOBAL_TOKEN = %q, want system-value", got)
	}
	if _, ok := env["BROKEN_TOKEN"]; ok {
		t.Fatal("BROKEN_TOKEN should be skipped")
	}
}

func sqlcNullString(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func TestLoadEnvNoAgeKeys(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)

	oidc := appdb.NewOIDCStore(db)
	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	testDB := &vaultTestDB{oidc: oidc, q: q}
	svc, err := vault.NewService(testDB, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	user, err := oidc.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "nokeys2@vault.test",
		Name:  "No Keys 2",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	env, err := svc.LoadEnv(ctx, user.ID)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("LoadEnv got %d entries, want 0", len(env))
	}
}

func TestDeleteEntry(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, userID, "MY_SECRET", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := svc.Delete(ctx, userID, "MY_SECRET"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	entries, err := svc.List(ctx, userID)
	if err != nil {
		t.Fatalf("List after Delete: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List after Delete: got %d entries, want 0", len(entries))
	}
}

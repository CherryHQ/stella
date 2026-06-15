package vault_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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

func (d *vaultTestDB) UpsertVaultEntryByScope(ctx context.Context, arg sqlc.UpsertVaultEntryByScopeParams) error {
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

	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "vault_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

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

func TestLoadEnv(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	secrets := map[string]string{
		"GITHUB_TOKEN": "ghp_abc",
		"API_KEY":      "sk_test_123",
		"MY_SECRET":    "super_secret_value",
	}
	for name, val := range secrets {
		if err := svc.Set(ctx, userID, name, val); err != nil {
			t.Fatalf("Set(%q): %v", name, err)
		}
	}

	env, err := svc.LoadEnv(ctx, userID)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if len(env) != len(secrets) {
		t.Fatalf("LoadEnv: got %d entries, want %d", len(env), len(secrets))
	}
	for name, want := range secrets {
		got, ok := env[name]
		if !ok {
			t.Errorf("LoadEnv: missing key %q", name)
			continue
		}
		if got != want {
			t.Errorf("LoadEnv[%q] = %q, want %q", name, got, want)
		}
	}
}

func TestLoadEnvForAgentMergesScopedPrecedence(t *testing.T) {
	t.Parallel()
	svc, _, userID, q := testServiceWithQueries(t)
	ctx := context.Background()

	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-a", Name: "Agent A", Model: "test/model", Workspace: "workspace", Sandbox: "{}", EnabledBuiltinSkills: "[]", Scope: "system", Enabled: 1,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-b", Name: "Agent B", Model: "test/model", Workspace: "workspace", Sandbox: "{}", EnabledBuiltinSkills: "[]", Scope: "system", Enabled: 1,
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
	for _, set := range sets {
		var err error
		if set.scope == vault.ScopeSystem || set.scope == vault.ScopeSystemAgent {
			err = svc.SetSystemScoped(ctx, set.scope, set.agentID, "TOKEN", set.value)
		} else {
			err = svc.SetScoped(ctx, set.scope, userIDForScope(set.scope, userID), set.agentID, "TOKEN", set.value)
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

func userIDForScope(scope string, userID string) string {
	if scope == vault.ScopeUser || scope == vault.ScopeUserAgent {
		return userID
	}
	return ""
}

func TestNewServiceInvalidKey(t *testing.T) {
	t.Parallel()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "invalid_key_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	oidc := appdb.NewOIDCStore(db)
	testDB := &vaultTestDB{oidc: oidc, q: sqlc.New(db)}
	_, err = vault.NewService(testDB, "not-a-valid-age-key")
	if err == nil {
		t.Fatal("NewService with invalid key should fail")
	}
}

func TestSetNoAgeKeys(t *testing.T) {
	t.Parallel()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "no_keys_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

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

	if err := svc.SetSystemScoped(ctx, vault.ScopeSystem, "", "GLOBAL_TOKEN", "system-value"); err != nil {
		t.Fatalf("SetSystemScoped: %v", err)
	}
	if err := q.UpsertVaultEntryByScope(ctx, sqlc.UpsertVaultEntryByScopeParams{
		ID: "broken-user-entry", Scope: vault.ScopeUser, UserID: sqlcNullString(userID), Name: "BROKEN_TOKEN", Ciphertext: "not-age",
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

func sqlcNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func TestLoadEnvNoAgeKeys(t *testing.T) {
	t.Parallel()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "loadenv_nokeys_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

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

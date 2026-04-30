package vault_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/vault"
	"github.com/vaayne/anna/pkg/db/sqlc"
)

// testService sets up a vault Service backed by a real SQLite database. It
// creates a user with age keys provisioned and returns the service, queries
// object, and the created user ID.
func testService(t *testing.T) (*vault.Service, *sqlc.Queries, int64) {
	t.Helper()

	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "vault_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := sqlc.New(db)
	ctx := context.Background()

	// Generate master key.
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity (master): %v", err)
	}

	svc, err := vault.NewService(q, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Create a user.
	user, err := q.CreateAuthUser(ctx, sqlc.CreateAuthUserParams{
		Username:     "testuser",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("CreateAuthUser: %v", err)
	}

	// Provision age keys for that user.
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := q.UpdateUserAgeKeys(ctx, sqlc.UpdateUserAgeKeysParams{
		AgePublicKey:  pubKey,
		AgePrivateKey: encPrivKey,
		ID:            user.ID,
	}); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}

	return svc, q, user.ID
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

func TestSetValidation(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	invalid := []string{
		"",
		"lowercase",
		"123START",
		"HAS SPACE",
		"ANNA_SECRET",
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
	if len(env) != len(secrets)+1 {
		t.Fatalf("LoadEnv: got %d entries, want %d", len(env), len(secrets)+1)
	}
	if got := env[vault.AnnaTokenName]; !strings.HasPrefix(got, "anna_") {
		t.Fatalf("LoadEnv[%q] = %q, want anna_ prefix", vault.AnnaTokenName, got)
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

func TestNewServiceInvalidKey(t *testing.T) {
	t.Parallel()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "invalid_key_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = vault.NewService(sqlc.New(db), "not-a-valid-age-key")
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

	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	svc, err := vault.NewService(q, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Create a user without age keys.
	user, err := q.CreateAuthUser(ctx, sqlc.CreateAuthUserParams{
		Username:     "nokeys",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("CreateAuthUser: %v", err)
	}

	// Set should fail because user has no age public key.
	if err := svc.Set(ctx, user.ID, "MY_KEY", "value"); err == nil {
		t.Fatal("Set should fail for user without age keys")
	}
}

func TestLoadEnvAutoCreatesAnnaToken(t *testing.T) {
	t.Parallel()
	svc, _, userID := testService(t)
	ctx := context.Background()

	env, err := svc.LoadEnv(ctx, userID)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	token := env[vault.AnnaTokenName]
	if !strings.HasPrefix(token, "anna_") {
		t.Fatalf("LoadEnv[%q] = %q, want anna_ prefix", vault.AnnaTokenName, token)
	}

	again, err := svc.LoadEnv(ctx, userID)
	if err != nil {
		t.Fatalf("LoadEnv again: %v", err)
	}
	if again[vault.AnnaTokenName] != token {
		t.Fatal("LoadEnv regenerated ANNA_TOKEN; want stable token")
	}
}

func TestLoadEnvNoAgeKeys(t *testing.T) {
	t.Parallel()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "loadenv_nokeys_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	svc, err := vault.NewService(q, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	user, err := q.CreateAuthUser(ctx, sqlc.CreateAuthUserParams{
		Username:     "nokeys",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("CreateAuthUser: %v", err)
	}

	// LoadEnv should fail because user has no age private key.
	if _, err := svc.LoadEnv(ctx, user.ID); err == nil {
		t.Fatal("LoadEnv should fail for user without age keys")
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

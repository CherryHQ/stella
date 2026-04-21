package channel

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

func testVaultService(t *testing.T) (*vault.Service, int64) {
	t.Helper()

	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "config_cmd_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	svc, err := vault.NewService(q, masterID.String())
	if err != nil {
		t.Fatal(err)
	}

	user, err := q.CreateAuthUser(ctx, sqlc.CreateAuthUserParams{
		Username:     "testuser",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatal(err)
	}

	pub, encPriv, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateUserAgeKeys(ctx, sqlc.UpdateUserAgeKeysParams{
		AgePublicKey:  pub,
		AgePrivateKey: encPriv,
		ID:            user.ID,
	}); err != nil {
		t.Fatal(err)
	}

	return svc, user.ID
}

func TestHandleConfigNilVault(t *testing.T) {
	resp := handleConfig(context.Background(), nil, 1, "list")
	if !strings.Contains(resp, "not configured") {
		t.Errorf("expected not configured message, got %q", resp)
	}
}

func TestHandleConfigNotLoggedIn(t *testing.T) {
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "nologin_test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	masterID, _ := age.GenerateX25519Identity()
	svc, _ := vault.NewService(sqlc.New(db), masterID.String())

	resp := handleConfig(context.Background(), svc, 0, "list")
	if !strings.Contains(resp, "logged in") {
		t.Errorf("expected logged in message, got %q", resp)
	}
}

func TestHandleConfigListEmpty(t *testing.T) {
	svc, userID := testVaultService(t)
	resp := handleConfig(context.Background(), svc, userID, "list")
	if !strings.Contains(resp, "No secrets") {
		t.Errorf("expected empty list message, got %q", resp)
	}
}

func TestHandleConfigListDefault(t *testing.T) {
	svc, userID := testVaultService(t)
	resp := handleConfig(context.Background(), svc, userID, "")
	if !strings.Contains(resp, "No secrets") {
		t.Errorf("expected empty list for default subcommand, got %q", resp)
	}
}

func TestHandleConfigAddAndList(t *testing.T) {
	svc, userID := testVaultService(t)
	ctx := context.Background()

	resp := handleConfig(ctx, svc, userID, "add MY_KEY secret_value")
	if !strings.Contains(resp, "saved") {
		t.Fatalf("expected saved message, got %q", resp)
	}

	resp = handleConfig(ctx, svc, userID, "list")
	if !strings.Contains(resp, "MY_KEY") {
		t.Errorf("expected MY_KEY in list, got %q", resp)
	}
}

func TestHandleConfigAddMissingArgs(t *testing.T) {
	svc, userID := testVaultService(t)
	resp := handleConfig(context.Background(), svc, userID, "add MY_KEY")
	if !strings.Contains(resp, "Usage") {
		t.Errorf("expected usage message, got %q", resp)
	}
}

func TestHandleConfigRemove(t *testing.T) {
	svc, userID := testVaultService(t)
	ctx := context.Background()

	handleConfig(ctx, svc, userID, "add MY_KEY value")
	resp := handleConfig(ctx, svc, userID, "remove MY_KEY")
	if !strings.Contains(resp, "removed") {
		t.Fatalf("expected removed message, got %q", resp)
	}

	resp = handleConfig(ctx, svc, userID, "list")
	if strings.Contains(resp, "MY_KEY") {
		t.Errorf("expected MY_KEY gone, got %q", resp)
	}
}

func TestHandleConfigRemoveMissingArgs(t *testing.T) {
	svc, userID := testVaultService(t)
	resp := handleConfig(context.Background(), svc, userID, "remove")
	if !strings.Contains(resp, "Usage") {
		t.Errorf("expected usage message, got %q", resp)
	}
}

func TestHandleConfigUnknownSubcommand(t *testing.T) {
	svc, userID := testVaultService(t)
	resp := handleConfig(context.Background(), svc, userID, "unknown")
	if !strings.Contains(resp, "Usage") {
		t.Errorf("expected usage message, got %q", resp)
	}
}

func TestHandleConfigSetAlias(t *testing.T) {
	svc, userID := testVaultService(t)
	resp := handleConfig(context.Background(), svc, userID, "set MY_KEY val")
	if !strings.Contains(resp, "saved") {
		t.Errorf("expected saved message, got %q", resp)
	}
}

func TestHandleConfigDeleteAlias(t *testing.T) {
	svc, userID := testVaultService(t)
	ctx := context.Background()
	handleConfig(ctx, svc, userID, "add MY_KEY val")
	resp := handleConfig(ctx, svc, userID, "delete MY_KEY")
	if !strings.Contains(resp, "removed") {
		t.Errorf("expected removed message, got %q", resp)
	}
}

func TestHandleConfigAddInvalidKey(t *testing.T) {
	svc, userID := testVaultService(t)
	resp := handleConfig(context.Background(), svc, userID, "add ANNA_SECRET value")
	if !strings.Contains(resp, "Error") {
		t.Errorf("expected error for reserved key, got %q", resp)
	}
}

func TestHandleConfigDeleteCancelledCtx(t *testing.T) {
	svc, userID := testVaultService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp := handleConfig(ctx, svc, userID, "remove MY_KEY")
	if !strings.Contains(resp, "Error") {
		t.Errorf("expected error for cancelled context, got %q", resp)
	}
}

func TestHandleConfigListCancelledCtx(t *testing.T) {
	svc, userID := testVaultService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp := handleConfig(ctx, svc, userID, "list")
	if !strings.Contains(resp, "Error") {
		t.Errorf("expected error for cancelled context, got %q", resp)
	}
}

func TestHandleConfigAddValueWithSpaces(t *testing.T) {
	svc, userID := testVaultService(t)
	ctx := context.Background()

	resp := handleConfig(ctx, svc, userID, "add MY_KEY hello world foo")
	if !strings.Contains(resp, "saved") {
		t.Fatalf("expected saved message, got %q", resp)
	}

	resp = handleConfig(ctx, svc, userID, "list")
	if !strings.Contains(resp, "MY_KEY") {
		t.Errorf("expected MY_KEY in list, got %q", resp)
	}
}

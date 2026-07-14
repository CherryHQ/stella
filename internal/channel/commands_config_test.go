package channel

import (
	"context"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func testVaultService(t *testing.T) (*vault.Service, string) {
	t.Helper()

	db := dbtest.New(t)

	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	svc, err := vault.NewService(q, masterID.String(), nil)
	if err != nil {
		t.Fatal(err)
	}

	oidcStore := appdb.NewOIDCStore(db)
	user, err := oidcStore.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "testuser@test.local",
		Name:  "testuser",
	})
	if err != nil {
		t.Fatal(err)
	}

	pub, encPriv, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := oidcStore.UpdateUserAgeKeys(ctx, user.ID, pub, encPriv); err != nil {
		t.Fatal(err)
	}

	return svc, user.ID
}

func TestHandleConfigNilVault(t *testing.T) {
	resp, ok := handleConfig(context.Background(), nil, "1", "MY_KEY value")
	if ok {
		t.Error("expected ok=false for nil vault")
	}
	if !strings.Contains(resp, "not configured") {
		t.Errorf("expected not configured message, got %q", resp)
	}
}

func TestHandleConfigNotLoggedIn(t *testing.T) {
	db := dbtest.New(t)

	masterID, _ := age.GenerateX25519Identity()
	svc, _ := vault.NewService(sqlc.New(db), masterID.String(), nil)

	resp, ok := handleConfig(context.Background(), svc, "", "MY_KEY value")
	if ok {
		t.Error("expected ok=false for empty userID")
	}
	if !strings.Contains(resp, "logged in") {
		t.Errorf("expected logged in message, got %q", resp)
	}
}

func TestHandleConfigSet(t *testing.T) {
	svc, userID := testVaultService(t)
	resp, ok := handleConfig(context.Background(), svc, userID, "MY_KEY secret_value")
	if !ok {
		t.Errorf("expected ok=true, got message %q", resp)
	}
	if !strings.Contains(resp, "saved") {
		t.Errorf("expected saved message, got %q", resp)
	}
}

func TestHandleConfigMissingValue(t *testing.T) {
	svc, userID := testVaultService(t)
	resp, ok := handleConfig(context.Background(), svc, userID, "MY_KEY")
	if ok {
		t.Error("expected ok=false for missing value")
	}
	if !strings.Contains(resp, "Usage") {
		t.Errorf("expected usage message, got %q", resp)
	}
}

func TestHandleConfigNoArgs(t *testing.T) {
	svc, userID := testVaultService(t)
	resp, ok := handleConfig(context.Background(), svc, userID, "")
	if ok {
		t.Error("expected ok=false for empty args")
	}
	if !strings.Contains(resp, "Usage") {
		t.Errorf("expected usage message for empty args, got %q", resp)
	}
}

func TestHandleConfigInvalidKey(t *testing.T) {
	svc, userID := testVaultService(t)
	resp, ok := handleConfig(context.Background(), svc, userID, "STELLA_SECRET value")
	if ok {
		t.Error("expected ok=false for reserved key")
	}
	if !strings.Contains(resp, "Error") {
		t.Errorf("expected error for reserved key, got %q", resp)
	}
}

func TestHandleConfigValueWithSpaces(t *testing.T) {
	svc, userID := testVaultService(t)
	resp, ok := handleConfig(context.Background(), svc, userID, "MY_KEY hello world foo")
	if !ok {
		t.Errorf("expected ok=true, got message %q", resp)
	}
	if !strings.Contains(resp, "saved") {
		t.Errorf("expected saved message for value with spaces, got %q", resp)
	}
}

func TestHandleConfigCancelledCtx(t *testing.T) {
	svc, userID := testVaultService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, ok := handleConfig(ctx, svc, userID, "MY_KEY value")
	if ok {
		t.Error("expected ok=false for cancelled context")
	}
	if !strings.Contains(resp, "Error") {
		t.Errorf("expected error for cancelled context, got %q", resp)
	}
}

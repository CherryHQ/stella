package email_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestServiceNoConfigFriendlyError(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID := seedEmailUser(t, db, "no-config")
	vaultSvc := newEmailVaultService(t, db, userID)
	svc := email.NewService(emailConfigReader(vaultSvc), sqlc.New(db))

	acc, err := svc.Access(userAuthority(t, userID))
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	_, err = acc.Accounts(ctx)
	if err == nil || !strings.Contains(err.Error(), "no email account configured") {
		t.Fatalf("Accounts err=%v, want friendly no-config error", err)
	}
}

func TestServiceSendSuppressesDuplicate(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID := seedEmailUser(t, db, "send")
	vaultSvc := newEmailVaultService(t, db, userID)
	cfg := email.Config{Default: "work", Accounts: map[string]email.EmailAccount{"work": {
		IMAPHost: "8.8.8.8", SMTPHost: "1.1.1.1", Username: "user@example.com", Password: "secret", From: "user@example.com",
	}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := vaultSvc.SetScoped(ctx, vault.ScopeUser, userID, "", "EMAIL_CONFIG", string(b)); err != nil {
		t.Fatalf("set EMAIL_CONFIG: %v", err)
	}

	svc := email.NewService(emailConfigReader(vaultSvc), sqlc.New(db))
	sends := 0
	svc.SetSendFunc(func(email.EmailAccount, email.SendOptions) error {
		sends++
		return nil
	})
	opts := email.SendOptions{To: []string{"to@example.com"}, Subject: "hello", Body: "world"}
	acc1, err := svc.Access(userAuthority(t, userID))
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	first, err := acc1.Send(ctx, "", opts, "k1")
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	acc2, err := svc.Access(userAuthority(t, userID))
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	second, err := acc2.Send(ctx, "", opts, "k1")
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if sends != 1 || first.Duplicate || !second.Duplicate {
		t.Fatalf("sends=%d first=%+v second=%+v, want one send and duplicate second", sends, first, second)
	}
}

func seedEmailUser(t *testing.T, db *pgxpool.Pool, suffix string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, id, suffix+"-"+id+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func newEmailVaultService(t *testing.T, db *pgxpool.Pool, userID string) *vault.Service {
	t.Helper()
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	svc, err := vault.NewService(sqlc.New(db), masterID.String(), nil)
	if err != nil {
		t.Fatalf("vault.NewService: %v", err)
	}
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := appdb.NewOIDCStore(db).UpdateUserAgeKeys(context.Background(), userID, pubKey, encPrivKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}
	return svc
}

func emailConfigReader(vaultSvc *vault.Service) email.ConfigReader {
	if vaultSvc == nil {
		return nil
	}
	return func(ctx context.Context, userID string) (string, error) {
		return vaultSvc.Get(ctx, userID, email.ConfigName)
	}
}

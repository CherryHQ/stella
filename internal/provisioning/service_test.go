package provisioning

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/account"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/credential"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

type accountLifecycleStub struct{}

func (accountLifecycleStub) DeactivateUserIfUserRole(context.Context, authz.Authority, string) (account.AccountView, error) {
	return account.AccountView{}, nil
}

func TestCreateRotateProvisionedUserPreservesUnrelatedPAT(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	issuer := appdb.NewOIDCStore(db)
	issuerUser, err := issuer.CreateUser(ctx, auth.User{ID: uuid.Must(uuid.NewV7()).String(), Email: "issuer@example.test", Name: "Issuer", Role: auth.RoleAdmin})
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	q := sqlc.New(db)
	issuerToken, err := q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{
		PublicID: "issuer-public", UserID: issuerUser.ID, Name: "issuer", TokenHash: "issuer-hash", Last4: "hash", Scopes: []string{}, TokenUse: "provisioning",
	})
	if err != nil {
		t.Fatalf("create issuer token: %v", err)
	}
	recipientIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate vault identity: %v", err)
	}
	var logs bytes.Buffer
	svc := New(db, accountLifecycleStub{}, recipientIdentity.Recipient(), slog.New(slog.NewJSONHandler(&logs, nil)))
	input := CreateInput{ExternalID: "directory-42", Email: "ada@example.test", Name: "Ada", TokenName: DefaultTokenName, ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	created, err := svc.Create(ctx, Issuer{UserID: issuerUser.ID, TokenID: issuerToken.ID}, input)
	if err != nil {
		t.Fatalf("create provisioned user: %v", err)
	}
	if created.Token == "" || created.User.Role != auth.RoleUser || !created.User.IsActive || created.User.ActiveToken == nil {
		t.Fatalf("created provisioned user = %#v, want active role=user with one token", created.User)
	}
	user, err := q.GetAuthUser(ctx, created.User.UserID)
	if err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if user.AgePublicKey == "" || user.AgePrivateKey == "" {
		t.Fatal("configured vault recipient must create age keys before persistence")
	}
	var credentialCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM auth_credential WHERE user_id = $1`, created.User.UserID).Scan(&credentialCount); err != nil {
		t.Fatalf("count passwords: %v", err)
	}
	if credentialCount != 0 {
		t.Fatalf("provisioned password credentials = %d, want 0", credentialCount)
	}

	minted, err := credential.MintOpaque(credential.KindPAT)
	if err != nil {
		t.Fatalf("mint unrelated PAT: %v", err)
	}
	if _, err := q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{
		PublicID: minted.PublicID, UserID: created.User.UserID, Name: "unrelated", TokenHash: minted.TokenHash, Last4: minted.Last4, Scopes: []string{}, TokenUse: "personal",
	}); err != nil {
		t.Fatalf("create unrelated PAT: %v", err)
	}
	rotated, err := svc.Rotate(ctx, Issuer{UserID: issuerUser.ID, TokenID: issuerToken.ID}, created.User.ID, "replacement", time.Now().UTC().Add(48*time.Hour))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Token == "" || rotated.Token == created.Token || rotated.User.ActiveToken == nil || rotated.User.ActiveToken.Name != "replacement" {
		t.Fatalf("rotation result = %#v", rotated)
	}
	logOutput := logs.String()
	for _, want := range []string{"provisioned user created", "provisioned user token rotated", issuerUser.ID, issuerToken.ID, created.User.ID} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("structured issuer log missing %q: %s", want, logOutput)
		}
	}
	if strings.Contains(logOutput, created.Token) || strings.Contains(logOutput, rotated.Token) {
		t.Fatalf("structured issuer logs leaked plaintext token: %s", logOutput)
	}
	var activeProvisioned, activeUnrelated int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id = $1 AND issued_by_provisioning AND revoked_at IS NULL`, created.User.UserID).Scan(&activeProvisioned); err != nil {
		t.Fatalf("count active provisioned PATs: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id = $1 AND NOT issued_by_provisioning AND revoked_at IS NULL`, created.User.UserID).Scan(&activeUnrelated); err != nil {
		t.Fatalf("count active unrelated PATs: %v", err)
	}
	if activeProvisioned != 1 || activeUnrelated != 1 {
		t.Fatalf("active PATs = provisioned:%d unrelated:%d, want 1/1", activeProvisioned, activeUnrelated)
	}

	if _, err := svc.Create(ctx, Issuer{UserID: issuerUser.ID, TokenID: issuerToken.ID}, input); !errors.Is(err, ErrExternalIDDup) {
		t.Fatalf("duplicate external id = %v, want ErrExternalIDDup", err)
	}
	if err := q.UpdateUserRole(ctx, sqlc.UpdateUserRoleParams{Role: auth.RoleAdmin, ID: created.User.UserID}); err != nil {
		t.Fatalf("promote provisioned user: %v", err)
	}
	if _, err := svc.Rotate(ctx, Issuer{UserID: issuerUser.ID, TokenID: issuerToken.ID}, created.User.ID, "blocked", time.Now().UTC().Add(time.Hour)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rotate promoted user = %v, want ErrForbidden", err)
	}
}

func TestProvisioningMarkerSurvivesIssuerDeletion(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	issuerStore := appdb.NewOIDCStore(db)
	issuer, err := issuerStore.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "issuer-marker@example.test", Name: "Issuer", Role: auth.RoleAdmin, IsActive: true})
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	q := sqlc.New(db)
	issuerToken, err := q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{PublicID: "marker-issuer", UserID: issuer.ID, Name: "issuer", TokenHash: "issuer-hash", Last4: "hash", Scopes: []string{}, TokenUse: "provisioning"})
	if err != nil {
		t.Fatalf("create issuer token: %v", err)
	}
	svc := New(db, accountLifecycleStub{}, nil, nil)
	created, err := svc.Create(ctx, Issuer{UserID: issuer.ID, TokenID: issuerToken.ID}, CreateInput{ExternalID: "marker-user", Email: "marker-user@example.test", Name: "Marker", TokenName: "marker", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("create provisioned user: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM personal_access_token WHERE id=$1`, issuerToken.ID); err != nil {
		t.Fatalf("delete issuer token: %v", err)
	}
	var marker bool
	var issuerID *string
	if err := db.QueryRow(ctx, `SELECT issued_by_provisioning, issued_by_token_id FROM personal_access_token WHERE user_id=$1`, created.User.UserID).Scan(&marker, &issuerID); err != nil {
		t.Fatalf("load marked PAT after issuer deletion: %v", err)
	}
	if !marker || issuerID != nil {
		t.Fatalf("issuer SET NULL must retain provisioning marker: marker=%v issuer=%v", marker, issuerID)
	}
	minted, err := credential.MintOpaque(credential.KindPAT)
	if err != nil {
		t.Fatalf("mint second PAT: %v", err)
	}
	if _, err := q.CreateProvisionedPersonalAccessToken(ctx, provisionedPATParams(created.User.UserID, "", "second", time.Now().UTC().Add(time.Hour), minted)); err == nil {
		t.Fatal("active marked PAT must retain the one-token uniqueness backstop after issuer deletion")
	}
	if _, err := q.RevokeProvisionedPersonalAccessTokenByUser(ctx, created.User.UserID); err != nil {
		t.Fatalf("revoke marked PAT after issuer deletion: %v", err)
	}
	if _, err := q.CreateProvisionedPersonalAccessToken(ctx, sqlc.CreateProvisionedPersonalAccessTokenParams{
		PublicID: minted.PublicID, UserID: created.User.UserID, Name: "replacement", TokenHash: minted.TokenHash, Last4: minted.Last4, Scopes: []string{},
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Hour), Valid: true}, IssuedByTokenID: pgtype.Text{},
	}); err != nil {
		t.Fatalf("create replacement marked PAT after revoke: %v", err)
	}
}

func TestListAfterCursorSurvivesInsertAndDelete(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	issuerStore := appdb.NewOIDCStore(db)
	issuer, err := issuerStore.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "issuer-cursor@example.test", Name: "Issuer", Role: auth.RoleAdmin, IsActive: true})
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	q := sqlc.New(db)
	issuerToken, err := q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{PublicID: "cursor-issuer", UserID: issuer.ID, Name: "issuer", TokenHash: "issuer-hash", Last4: "hash", Scopes: []string{}, TokenUse: "provisioning"})
	if err != nil {
		t.Fatalf("create issuer token: %v", err)
	}
	svc := New(db, accountLifecycleStub{}, nil, nil)
	create := func(externalID string) User {
		t.Helper()
		created, err := svc.Create(ctx, Issuer{UserID: issuer.ID, TokenID: issuerToken.ID}, CreateInput{ExternalID: externalID, Email: externalID + "@example.test", Name: externalID, TokenName: "token", ExpiresAt: time.Now().UTC().Add(time.Hour)})
		if err != nil {
			t.Fatalf("create %s: %v", externalID, err)
		}
		return created.User
	}
	first, second, third := create("cursor-first"), create("cursor-second"), create("cursor-third")
	page1, err := svc.ListAfter(ctx, 2, nil)
	if err != nil || len(page1) != 2 {
		t.Fatalf("first page = %#v, %v", page1, err)
	}
	cursor := &Cursor{CreatedAt: page1[1].CreatedAt, ID: page1[1].ID}
	if _, err := db.Exec(ctx, `DELETE FROM auth_user WHERE id=$1`, first.UserID); err != nil {
		t.Fatalf("delete first page row: %v", err)
	}
	fourth := create("cursor-fourth")
	page2, err := svc.ListAfter(ctx, 2, cursor)
	if err != nil || len(page2) != 2 {
		t.Fatalf("second page = %#v, %v", page2, err)
	}
	seen := map[string]bool{}
	for _, user := range append(page1[1:], page2...) {
		if seen[user.ID] {
			t.Fatalf("duplicate surviving user across pages: %s", user.ID)
		}
		seen[user.ID] = true
	}
	for _, want := range []string{second.ID, third.ID, fourth.ID} {
		if !seen[want] {
			t.Fatalf("surviving user %s omitted after page mutation: %#v", want, seen)
		}
	}
}

// TestDeactivateSerializesAfterPromotion holds a real PostgreSQL role update
// open while provisioning reaches its conditional deactivation write. Once the
// promotion commits, the conditional UPDATE must see role=admin and refuse;
// the old Get-then-SetActive sequence could deactivate this now-admin target.
func TestDeactivateSerializesAfterPromotion(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := appdb.NewOIDCStore(db)
	issuer, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "issuer-race@example.test", Name: "Issuer", Role: auth.RoleAdmin, IsActive: true})
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	q := sqlc.New(db)
	issuerToken, err := q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{PublicID: "race-issuer", UserID: issuer.ID, Name: "issuer", TokenHash: "issuer-hash", Last4: "hash", Scopes: []string{}, TokenUse: "provisioning"})
	if err != nil {
		t.Fatalf("create issuer token: %v", err)
	}
	accountSvc := account.NewService(store, store, store, store, store, nil, nil, nil)
	svc := New(db, accountSvc, nil, nil)
	created, err := svc.Create(ctx, Issuer{UserID: issuer.ID, TokenID: issuerToken.ID}, CreateInput{ExternalID: "race-target", Email: "race-target@example.test", Name: "Target", TokenName: "token", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(issuer.ID), true)
	if err != nil {
		t.Fatalf("issuer authority: %v", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin promotion: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE auth_user SET role='admin', updated_at=now() WHERE id=$1`, created.User.UserID); err != nil {
		t.Fatalf("stage promotion: %v", err)
	}
	result := make(chan error, 1)
	var started sync.WaitGroup
	started.Add(1)
	go func() {
		started.Done()
		_, err := svc.Deactivate(ctx, Issuer{UserID: issuer.ID, TokenID: issuerToken.ID}, authority, created.User.ID)
		result <- err
	}()
	started.Wait()
	// The target's initial read may observe the old role, but the conditional
	// UPDATE is serialized behind this transaction and must re-check the role.
	time.Sleep(20 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit promotion: %v", err)
	}
	if err := <-result; !errors.Is(err, ErrForbidden) {
		t.Fatalf("deactivation after committed promotion = %v, want ErrForbidden", err)
	}
	var role string
	var active bool
	if err := db.QueryRow(ctx, `SELECT role, is_active FROM auth_user WHERE id=$1`, created.User.UserID).Scan(&role, &active); err != nil {
		t.Fatalf("load race target: %v", err)
	}
	if role != auth.RoleAdmin || !active {
		t.Fatalf("promotion/deactivation race left role=%q active=%v, want admin/true", role, active)
	}
}

// TestCreateChannelIdentitySerializesAfterPromotion proves the role check and
// identity insert share one target-row lock. A promotion that wins the lock
// must make the provisioning mutation fail without leaving an identity behind.
func TestCreateChannelIdentitySerializesAfterPromotion(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := appdb.NewOIDCStore(db)
	issuer, err := store.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "identity-issuer@example.test", Name: "Issuer", Role: auth.RoleAdmin, IsActive: true})
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	q := sqlc.New(db)
	issuerToken, err := q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{
		PublicID: "identity-race-issuer", UserID: issuer.ID, Name: "issuer", TokenHash: "identity-race-hash", Last4: "hash", Scopes: []string{}, TokenUse: "provisioning",
	})
	if err != nil {
		t.Fatalf("create issuer token: %v", err)
	}
	svc := New(db, accountLifecycleStub{}, nil, nil)
	created, err := svc.Create(ctx, Issuer{UserID: issuer.ID, TokenID: issuerToken.ID}, CreateInput{
		ExternalID: "identity-race-target", Email: "identity-race-target@example.test", Name: "Target", TokenName: "token", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin promotion: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE auth_user SET role='admin', updated_at=now() WHERE id=$1`, created.User.UserID); err != nil {
		t.Fatalf("stage promotion: %v", err)
	}
	result := make(chan error, 1)
	var started sync.WaitGroup
	started.Add(1)
	go func() {
		started.Done()
		_, err := svc.CreateChannelIdentity(ctx, Issuer{UserID: issuer.ID, TokenID: issuerToken.ID}, created.User.ID, ChannelIdentityInput{
			Platform: "feishu", ExternalID: "on_race", Name: "Target",
		})
		result <- err
	}()
	started.Wait()
	time.Sleep(20 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit promotion: %v", err)
	}
	if err := <-result; !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity create after committed promotion = %v, want ErrForbidden", err)
	}
	var identities int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_identity WHERE user_id=$1`, created.User.UserID).Scan(&identities); err != nil {
		t.Fatalf("count channel identities: %v", err)
	}
	if identities != 0 {
		t.Fatalf("channel identities after promotion race = %d, want 0", identities)
	}
}

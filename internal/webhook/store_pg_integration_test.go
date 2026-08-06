package webhook

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type dbUsers struct{}

func (dbUsers) IsActive(context.Context, string) (bool, error) { return true, nil }

type dbAccess struct{}

func (dbAccess) CanUseUser(context.Context, string, string) (bool, error) { return true, nil }

func newDBService(t *testing.T, db *pgxpool.Pool) *Service {
	t.Helper()
	svc, err := NewService(Config{Store: NewPostgresStore(db), Users: dbUsers{}, Access: dbAccess{}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func seedWebhookResource(t *testing.T, db *pgxpool.Pool, agentName string) (string, string) {
	t.Helper()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV7()).String()
	agentID := agentName + "-" + uuid.Must(uuid.NewV7()).String()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, $2, $3, true)", agentID, agentName, "/tmp"); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return userID, agentID
}

func createDBWebhook(t *testing.T, svc *Service, userID, agentID string) IssueResult {
	t.Helper()
	issued, err := svc.Create(context.Background(), CreateRequest{UserID: userID, Name: "deploy", AgentID: agentID, Provider: ProviderGeneric, IsEnabled: true, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return issued
}

func TestPostgresStoreCreateDisclosesOnceAndPersistsVerifierOnly(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	userID, agentID := seedWebhookResource(t, db, "agent")
	issued := createDBWebhook(t, newDBService(t, db), userID, agentID)
	if issued.Capability == "" || issued.Webhook.ID == "" {
		t.Fatal("Create did not mint resource and capability")
	}
	var provider, owner, hash, last4 string
	if err := db.QueryRow(ctx, "SELECT provider, user_id, token_hash, token_last4 FROM webhook WHERE id = $1", issued.Webhook.ID).Scan(&provider, &owner, &hash, &last4); err != nil {
		t.Fatal(err)
	}
	if provider != string(ProviderGeneric) || owner != userID {
		t.Fatalf("persisted provider/owner = %q/%q", provider, owner)
	}
	if hash == "" || hash == issued.Capability {
		t.Fatalf("token_hash must be a verifier, got %q", hash)
	}
	if last4 != issued.Webhook.TokenLast4 {
		t.Fatalf("token last4=%q want %q", last4, issued.Webhook.TokenLast4)
	}
	stable, err := newDBService(t, db).Get(ctx, userID, issued.Webhook.ID)
	if err != nil || stable.TokenPublicID == "" || stable.TokenLast4 == "" {
		t.Fatalf("stable get: %+v %v", stable, err)
	}
	if _, err := newDBService(t, db).ResolveCandidate(ctx, issued.Capability); err != nil {
		t.Fatalf("issued capability does not resolve: %v", err)
	}
}

func TestPostgresStoreRotateCASDeleteAndOwnerIsolation(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	svc := newDBService(t, db)
	userA, agentA := seedWebhookResource(t, db, "agent-a")
	userB, _ := seedWebhookResource(t, db, "agent-b")
	issued := createDBWebhook(t, svc, userA, agentA)
	if _, err := svc.Get(ctx, userB, issued.Webhook.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign get=%v", err)
	}
	if _, err := svc.Rotate(ctx, userB, issued.Webhook.ID, issued.Webhook.ETag()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign rotate=%v", err)
	}
	rotated, err := svc.Rotate(ctx, userA, issued.Webhook.ID, issued.Webhook.ETag())
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Capability == issued.Capability || rotated.Webhook.ETag() == issued.Webhook.ETag() {
		t.Fatal("rotation did not replace credential")
	}
	if _, err := svc.ResolveCandidate(ctx, issued.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old capability=%v", err)
	}
	if _, err := svc.Rotate(ctx, userA, issued.Webhook.ID, issued.Webhook.ETag()); !errors.Is(err, ErrStaleETag) {
		t.Fatalf("stale rotate=%v", err)
	}
	if ok, err := svc.Delete(ctx, userA, issued.Webhook.ID); err != nil || !ok {
		t.Fatalf("delete=%v,%v", ok, err)
	}
	if _, err := svc.ResolveCandidate(ctx, rotated.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted capability=%v", err)
	}
}

func TestPostgresStoreConcurrentRotateHasOneWinner(t *testing.T) {
	db := dbtest.New(t)
	svc := newDBService(t, db)
	userID, agentID := seedWebhookResource(t, db, "agent")
	issued := createDBWebhook(t, svc, userID, agentID)
	var wg sync.WaitGroup
	results := make(chan IssueResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			result, err := svc.Rotate(context.Background(), userID, issued.Webhook.ID, issued.Webhook.ETag())
			if err != nil {
				errs <- err
			} else {
				results <- result
			}
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(results) != 1 || len(errs) != 1 {
		t.Fatalf("results=%d errs=%d", len(results), len(errs))
	}
	if err := <-errs; !errors.Is(err, ErrStaleETag) {
		t.Fatalf("loser=%v", err)
	}
}

func TestPostgresStoreUpdateIsOwnerScopedAtomicPatchAndKeepsProvider(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	svc := newDBService(t, db)
	userID, agentID := seedWebhookResource(t, db, "agent")
	other, _ := seedWebhookResource(t, db, "other")
	issued := createDBWebhook(t, svc, userID, agentID)
	name, wait := "renamed", int32(120)
	if _, err := svc.Update(ctx, UpdateRequest{ID: issued.Webhook.ID, UserID: other, Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign update=%v", err)
	}
	var wg sync.WaitGroup
	for _, req := range []UpdateRequest{{ID: issued.Webhook.ID, UserID: userID, Name: &name}, {ID: issued.Webhook.ID, UserID: userID, WaitTimeoutSeconds: &wait}} {
		wg.Go(func() {
			if _, err := svc.Update(ctx, req); err != nil {
				t.Errorf("Update: %v", err)
			}
		})
	}
	wg.Wait()
	got, err := svc.Get(ctx, userID, issued.Webhook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.WaitTimeoutSeconds != wait || got.Provider != ProviderGeneric {
		t.Fatalf("atomic patch result=%+v", got)
	}
}

func TestPostgresAdmissionRequiresActiveUserAndAgent(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	userID, agentID := seedWebhookResource(t, db, "agent")
	svc := newDBService(t, db)
	issued := createDBWebhook(t, svc, userID, agentID)
	cand, err := svc.ResolveCandidate(ctx, issued.Capability)
	if err != nil {
		t.Fatal(err)
	}
	assertRejected := func(label string) {
		t.Helper()
		called := false
		err := svc.Admit(ctx, cand, func(context.Context, AdmittedInvocation) error { called = true; return nil })
		if !errors.Is(err, ErrNotFound) || called {
			t.Fatalf("%s admission = %v, called=%v; want opaque refusal", label, err, called)
		}
	}
	if _, err := db.Exec(ctx, "UPDATE auth_user SET is_active = false WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	assertRejected("inactive user")
	if _, err := db.Exec(ctx, "UPDATE auth_user SET is_active = true WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE agent SET enabled = false WHERE id = $1", agentID); err != nil {
		t.Fatal(err)
	}
	assertRejected("disabled agent")
}

func TestAdmitHoldsNoConnectionAcrossCallback(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	userID, agentID := seedWebhookResource(t, db, "agent")
	cfg := db.Config().Copy()
	cfg.MaxConns = 1
	one, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	svc := newDBService(t, one)
	issued := createDBWebhook(t, svc, userID, agentID)
	cand, err := svc.ResolveCandidate(ctx, issued.Capability)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- svc.Admit(ctx, cand, func(context.Context, AdmittedInvocation) error { close(entered); <-release; return nil })
	}()
	<-entered
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var value int
	if err := one.QueryRow(qctx, "SELECT 1").Scan(&value); err != nil {
		t.Fatalf("admission held DB connection: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWebhookSchemaDeletionPolicies(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	userID, agentID := seedWebhookResource(t, db, "agent")
	issued := createDBWebhook(t, newDBService(t, db), userID, agentID)
	if _, err := db.Exec(ctx, "DELETE FROM agent WHERE id = $1", agentID); err == nil {
		t.Fatal("schema allowed deleting an Agent still bound to a webhook")
	}
	if _, err := db.Exec(ctx, "DELETE FROM auth_user WHERE id = $1", userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM webhook WHERE id = $1", issued.Webhook.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("user delete did not cascade webhook: count=%d err=%v", count, err)
	}
}

func TestWebhookSchemaConstraints(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	userID, agentID := seedWebhookResource(t, db, "agent")
	_, err := db.Exec(ctx, "INSERT INTO webhook (id, user_id, agent_id, name, provider, wait_timeout_seconds, max_run_timeout_seconds, token_public_id, token_hash, token_last4) VALUES ($1,$2,$3,'x','generic',0,300,'public','hash','1234')", uuid.NewString(), userID, agentID)
	if err == nil {
		t.Fatal("schema accepted zero wait timeout")
	}
	_, err = db.Exec(ctx, "INSERT INTO webhook (id, user_id, agent_id, name, provider, wait_timeout_seconds, max_run_timeout_seconds, token_public_id, token_hash, token_last4) VALUES ($1,$2,$3,'x','generic',60,300,'public2','hash','12')", uuid.NewString(), userID, agentID)
	if err == nil {
		t.Fatal("schema accepted non-four-character token suffix")
	}
}

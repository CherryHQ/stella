package vault_test

import (
	"errors"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func poolVaultService(t *testing.T) (*vault.Service, *pgxpool.Pool, string) {
	t.Helper()
	db := dbtest.New(t)
	t.Cleanup(db.Close)

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate master identity: %v", err)
	}
	svc, err := vault.NewServiceForPool(db, masterID.String(), nil)
	if err != nil {
		t.Fatalf("new pool-backed vault service: %v", err)
	}
	oidc := appdb.NewOIDCStore(db)
	user, err := oidc.CreateUser(t.Context(), auth.User{
		ID:    uuid.NewString(),
		Email: "run-fence@vault.test",
		Name:  "Run Fence",
	})
	if err != nil {
		t.Fatalf("create vault user: %v", err)
	}
	publicKey, privateKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("generate user keys: %v", err)
	}
	if err := oidc.UpdateUserAgeKeys(t.Context(), user.ID, publicKey, privateKey); err != nil {
		t.Fatalf("provision user keys: %v", err)
	}
	return svc, db, user.ID
}

func terminalVaultRunGuard(t *testing.T, db *pgxpool.Pool, sessionID string) agentrun.Guard {
	t.Helper()
	q := sqlc.New(db)
	bootID, runID := uuid.NewString(), uuid.NewString()
	if _, err := q.CreateExecutorBoot(t.Context(), bootID); err != nil {
		t.Fatalf("create executor boot: %v", err)
	}
	if _, err := q.CreateAgentRun(t.Context(), sqlc.CreateAgentRunParams{
		ID:             runID,
		SessionID:      sessionID,
		ExecutorBootID: bootID,
		Source:         "run-fence-test",
		LeaseSeconds:   60,
	}); err != nil {
		t.Fatalf("create agent run: %v", err)
	}
	completedSession, err := q.CompleteAgentRunWithActivity(t.Context(), sqlc.CompleteAgentRunWithActivityParams{
		RunID:          runID,
		ExecutorBootID: bootID,
		Status:         agentrun.StatusCompleted,
		Reason:         "run-fence-test",
		SessionID:      sessionID,
	})
	if err != nil {
		t.Fatalf("complete agent run: %v", err)
	}
	if completedSession.SessionID != sessionID {
		t.Fatalf("completed session = %q, want %q", completedSession.SessionID, sessionID)
	}
	return agentrun.Guard{RunID: runID, SessionID: sessionID, ExecutorBootID: bootID}
}

func TestRunFenceCoversVaultWrites(t *testing.T) {
	service, db, userID := poolVaultService(t)
	ctx := t.Context()
	sessionID := "vault-run-fence-" + uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create run session: %v", err)
	}

	if err := service.Set(ctx, userID, "FENCED_SECRET", "before"); err != nil {
		t.Fatalf("unguarded seed secret: %v", err)
	}
	terminalGuard := terminalVaultRunGuard(t, db, sessionID)
	missingGuard := agentrun.Guard{
		RunID:          uuid.NewString(),
		SessionID:      sessionID,
		ExecutorBootID: uuid.NewString(),
	}
	malformedGuard := agentrun.Guard{SessionID: sessionID, ExecutorBootID: uuid.NewString()}

	if err := service.Set(agentrun.WithGuard(ctx, terminalGuard), userID, "FENCED_SECRET", "stale"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("terminal guarded Set = %v, want ErrLeaseLost", err)
	}
	if got, err := service.Get(ctx, userID, "FENCED_SECRET"); err != nil || got != "before" {
		t.Fatalf("terminal guarded Set value = %q, %v, want before", got, err)
	}

	if err := service.Set(agentrun.WithGuard(ctx, missingGuard), userID, "MISSING_SECRET", "blocked"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("missing guarded Set = %v, want ErrLeaseLost", err)
	}
	if _, err := service.Get(ctx, userID, "MISSING_SECRET"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing guarded Set lookup = %v, want pgx.ErrNoRows", err)
	}

	if err := service.Set(agentrun.WithGuard(ctx, malformedGuard), userID, "MALFORMED_SECRET", "blocked"); !errors.Is(err, agentrun.ErrInvalidGuard) {
		t.Fatalf("malformed guarded Set = %v, want ErrInvalidGuard", err)
	}
	if _, err := service.Get(ctx, userID, "MALFORMED_SECRET"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("malformed guarded Set lookup = %v, want pgx.ErrNoRows", err)
	}

	if err := service.Set(ctx, userID, "UNGUARDED_SECRET", "allowed"); err != nil {
		t.Fatalf("unguarded user Set: %v", err)
	}
	if got, err := service.Get(ctx, userID, "UNGUARDED_SECRET"); err != nil || got != "allowed" {
		t.Fatalf("unguarded user Set value = %q, %v, want allowed", got, err)
	}
}

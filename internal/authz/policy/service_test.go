package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestCreatePolicyBumpsRevisionAtomically(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	if got := currentRevision(t, pool); got != 0 {
		t.Fatalf("initial revision = %d, want 0", got)
	}
	id, rev, err := svc.CreatePolicy(ctx, denySystemAgentRead())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rev != 1 {
		t.Fatalf("returned revision = %d, want 1", rev)
	}
	if got := currentRevision(t, pool); got != 1 {
		t.Fatalf("committed revision = %d, want 1", got)
	}
	row, err := sqlc.New(pool).GetAuthzPolicy(ctx, id)
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if row.Status != statusActive {
		t.Fatalf("new policy status = %q, want active", row.Status)
	}
	if row.CatalogVersion != int64(authz.CatalogVersion) {
		t.Fatalf("catalog version = %d, want %d", row.CatalogVersion, authz.CatalogVersion)
	}
}

// A failed write inside the mutation transaction rolls back the revision bump:
// bump and policy write are one atomic unit.
func TestMutationRollsBackRevisionOnError(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := New(pool).store // white-box: exercise the shared private store directly

	boom := errors.New("boom")
	_, err := store.mutate(ctx, nil, func(_ *sqlc.Queries, _ int64) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("mutate error = %v, want boom", err)
	}
	if got := currentRevision(t, pool); got != 0 {
		t.Fatalf("revision after rolled-back mutation = %d, want 0", got)
	}
}

func TestActivationRejectsInactiveResourceWrite(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	// Vault is not cut over: writing a custom policy for it must fail closed,
	// with no transaction and no revision bump.
	_, _, err := svc.CreatePolicy(ctx, PolicyInput{
		Resource: authz.ResourceVault,
		Action:   authz.ActionRead,
		Effect:   EffectAllow,
	})
	if !errors.Is(err, ErrResourceInactive) {
		t.Fatalf("vault write = %v, want ErrResourceInactive", err)
	}
	if got := currentRevision(t, pool); got != 0 {
		t.Fatalf("rejected write bumped revision to %d, want 0", got)
	}

	// Agent is shadow-enabled and accepted.
	if _, _, err := svc.CreatePolicy(ctx, denySystemAgentRead()); err != nil {
		t.Fatalf("agent write should be accepted: %v", err)
	}
}

// Two mutations that attempt to commit in reverse order still serialize on the
// single counter row: the second to acquire the lock cannot commit until the
// first does, and its revision is strictly greater. This is the commit-ordered
// guarantee without a sequence.
func TestConcurrentMutationsSerializeUnderCounterLock(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := New(pool).store // white-box: exercise the shared private store directly

	aInCommit := make(chan struct{})
	releaseA := make(chan struct{})
	aRevCh := make(chan int64, 1)
	bRevCh := make(chan int64, 1)

	// A bumps (locks the counter), writes, then parks in beforeCommit holding
	// the row lock and its open transaction.
	go func() {
		rev, err := store.mutate(ctx, func() { close(aInCommit); <-releaseA }, func(qtx *sqlc.Queries, _ int64) error {
			return insertActiveAgentDeny(ctx, qtx, "a-policy")
		})
		if err != nil {
			aRevCh <- -1
			return
		}
		aRevCh <- rev
	}()

	<-aInCommit // A holds the lock; its transaction is uncommitted.

	// B starts and blocks on BumpAuthzPolicyRevision (the counter row is locked).
	go func() {
		rev, err := store.mutate(ctx, nil, func(qtx *sqlc.Queries, _ int64) error {
			return insertActiveAgentDeny(ctx, qtx, "b-policy")
		})
		if err != nil {
			bRevCh <- -1
			return
		}
		bRevCh <- rev
	}()

	// Wait until B is actually blocked waiting on a lock, and confirm nothing has
	// committed yet (revision still 0) while A parks pre-commit.
	waitForLockWaiter(t, pool)
	if got := currentRevision(t, pool); got != 0 {
		t.Fatalf("revision = %d while both mutations uncommitted, want 0", got)
	}
	select {
	case <-bRevCh:
		t.Fatal("B committed before A released the counter lock")
	default:
	}

	close(releaseA) // A commits revision 1; B may now proceed to revision 2.
	aRev := <-aRevCh
	bRev := <-bRevCh
	if aRev != 1 {
		t.Fatalf("A revision = %d, want 1", aRev)
	}
	if bRev != 2 {
		t.Fatalf("B revision = %d, want 2 (strictly after A)", bRev)
	}
	if got := currentRevision(t, pool); got != 2 {
		t.Fatalf("final revision = %d, want 2", got)
	}
}

// A reload that runs while a mutation is bumped+written but not yet committed
// sees a consistent snapshot: the old revision AND the old (empty) active set,
// never a torn (new revision, old rows) pair. After commit the next reload sees
// both the new revision and the new row.
func TestRepeatableReadReloadIsConsistentAcrossCommit(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := New(pool)
	svc := NewService(az)

	inCommit := make(chan struct{})
	release := make(chan struct{})
	svc.beforeCommit = func() { close(inCommit); <-release }

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := svc.CreatePolicy(ctx, denySystemAgentRead()); err != nil {
			t.Errorf("create: %v", err)
		}
	}()

	<-inCommit // mutation has bumped + written but not committed.
	snapMid, err := az.reload(ctx)
	if err != nil {
		t.Fatalf("reload during uncommitted mutation: %v", err)
	}
	if snapMid.revision != 0 {
		t.Fatalf("mid reload revision = %d, want 0 (mutation uncommitted)", snapMid.revision)
	}
	if len(snapMid.policies) != len(builtinPolicies()) {
		t.Fatalf("mid reload saw %d policies, want only the %d built-ins", len(snapMid.policies), len(builtinPolicies()))
	}

	close(release)
	<-done

	snapAfter, err := az.reload(ctx)
	if err != nil {
		t.Fatalf("reload after commit: %v", err)
	}
	if snapAfter.revision != 1 {
		t.Fatalf("post-commit reload revision = %d, want 1", snapAfter.revision)
	}
	if len(snapAfter.policies) != len(builtinPolicies())+1 {
		t.Fatalf("post-commit reload saw %d policies, want built-ins + 1 custom", len(snapAfter.policies))
	}
}

// insertActiveAgentDeny writes a minimal valid active agent-deny row on a tx.
func insertActiveAgentDeny(ctx context.Context, qtx *sqlc.Queries, id string) error {
	_, err := qtx.CreateAuthzPolicy(ctx, sqlc.CreateAuthzPolicyParams{
		ID:             id,
		Name:           id,
		ResourceType:   authz.ResourceAgent.String(),
		Action:         authz.ActionRead.String(),
		Effect:         string(EffectDeny),
		Subjects:       []byte(`{"any":true}`),
		Attributes:     []byte(`{}`),
		CatalogVersion: int64(authz.CatalogVersion),
		Status:         statusActive,
		Priority:       0,
	})
	return err
}

// waitForLockWaiter blocks until at least one backend is waiting on a lock,
// polling a deterministic condition (not sleeping for a fixed duration).
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'`).Scan(&n)
		if err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if n >= 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a blocked backend on the counter lock")
}

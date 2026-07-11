package policy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func currentRevision(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	rev, err := sqlc.New(pool).GetAuthzPolicyRevision(context.Background())
	if err != nil {
		t.Fatalf("read revision: %v", err)
	}
	return rev
}

// denySystemAgentRead is the custom policy used across tests: it flips the
// built-in "user reads a system agent" allow to a deny.
func denySystemAgentRead() PolicyInput {
	return PolicyInput{
		Name:       "deny system agent read",
		Resource:   authz.ResourceAgent,
		Action:     authz.ActionRead,
		Effect:     EffectDeny,
		Subjects:   NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []Predicate{Eq("scope", "system")},
	}
}

func TestBeginOneRevisionReadAndDecideNoDB(t *testing.T) {
	ctx := context.Background()
	az := New(dbtest.New(t))
	user := userAuthority(t, "u1", false)

	eval, err := az.Begin(ctx, user)
	if err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if got := az.revisionReads.Load(); got != 1 {
		t.Fatalf("revision reads after first begin = %d, want 1", got)
	}
	if got := az.reloads.Load(); got != 1 {
		t.Fatalf("reloads after first begin = %d, want 1 (cold cache)", got)
	}

	for i := range 3 {
		if _, err := eval.Decide(mustAgentRead(t, "a1", "", "system", false)); err != nil {
			t.Fatalf("decide %d: %v", i, err)
		}
	}
	if got := az.revisionReads.Load(); got != 1 {
		t.Fatalf("Decide performed a revision read: reads = %d, want 1", got)
	}
	if got := az.reloads.Load(); got != 1 {
		t.Fatalf("Decide triggered a reload: reloads = %d, want 1", got)
	}

	// Second Begin at the same revision is a cache hit: one more revision read,
	// no reload.
	if _, err := az.Begin(ctx, user); err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if got := az.revisionReads.Load(); got != 2 {
		t.Fatalf("revision reads after second begin = %d, want 2", got)
	}
	if got := az.reloads.Load(); got != 1 {
		t.Fatalf("second begin reloaded despite matching revision: reloads = %d, want 1", got)
	}
}

func TestTwoInstancesSeeMutationOnNextBeginWithoutNotify(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az1, az2 := New(pool), New(pool)
	svc := NewService(az1)
	user := userAuthority(t, "u1", false)
	req := mustAgentRead(t, "a1", "", "system", false)

	// Both instances begin at revision 0 and allow the system-agent read.
	for _, az := range []*Authorizer{az1, az2} {
		eval, err := az.Begin(ctx, user)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if dec, _ := eval.Decide(req); !dec.Allowed() {
			t.Fatal("system-agent read should start allowed")
		}
	}

	if _, rev, err := svc.CreatePolicy(ctx, denySystemAgentRead()); err != nil {
		t.Fatalf("create policy: %v", err)
	} else if rev != 1 {
		t.Fatalf("first mutation revision = %d, want 1", rev)
	}

	// Neither instance was notified; the next Begin reads the new revision and
	// reloads, so both now deny — with no LISTEN/NOTIFY involved.
	for i, az := range []*Authorizer{az1, az2} {
		eval, err := az.Begin(ctx, user)
		if err != nil {
			t.Fatalf("begin after mutation (instance %d): %v", i, err)
		}
		if eval.Revision() != 1 {
			t.Fatalf("instance %d evaluation revision = %d, want 1", i, eval.Revision())
		}
		if dec, _ := eval.Decide(req); dec.Allowed() {
			t.Fatalf("instance %d should deny after the deny policy committed", i)
		}
	}
}

func TestMutationAfterBeginKeepsOldSnapshot(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := New(pool)
	svc := NewService(az)
	user := userAuthority(t, "u1", false)
	req := mustAgentRead(t, "a1", "", "system", false)

	eval1, err := az.Begin(ctx, user)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if dec, _ := eval1.Decide(req); !dec.Allowed() {
		t.Fatal("eval1 should allow before mutation")
	}

	if _, _, err := svc.CreatePolicy(ctx, denySystemAgentRead()); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	// The already-running use case keeps its starting revision and snapshot.
	if eval1.Revision() != 0 {
		t.Fatalf("eval1 revision = %d, want 0 (bound at begin)", eval1.Revision())
	}
	if dec, _ := eval1.Decide(req); !dec.Allowed() {
		t.Fatal("eval1 must still allow using its old snapshot")
	}

	// A new use case sees the new revision and denies.
	eval2, err := az.Begin(ctx, user)
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if eval2.Revision() != 1 {
		t.Fatalf("eval2 revision = %d, want 1", eval2.Revision())
	}
	if dec, _ := eval2.Decide(req); dec.Allowed() {
		t.Fatal("eval2 must deny after mutation")
	}
}

func TestBeginFailsClosedOnRevisionLookupFailure(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := New(pool)
	pool.Close() // revision read will now fail

	_, err := az.Begin(ctx, userAuthority(t, "u1", false))
	if !errors.Is(err, authz.ErrAuthorizerUnavailable) {
		t.Fatalf("begin after pool close = %v, want ErrAuthorizerUnavailable", err)
	}
}

func TestBeginRejectsInvalidAuthority(t *testing.T) {
	ctx := context.Background()
	az := New(dbtest.New(t))
	_, err := az.Begin(ctx, authz.Authority{}) // zero authority is invalid
	if !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("begin with zero authority = %v, want ErrInvalidAuthority", err)
	}
}

func TestPublishNeverRegresses(t *testing.T) {
	az := New(dbtest.New(t))
	az.publish(&snapshot{revision: 5})
	az.publish(&snapshot{revision: 3}) // out-of-order, older
	if got := az.cachedRevision(); got != 5 {
		t.Fatalf("cache regressed to %d, want 5", got)
	}
	az.publish(&snapshot{revision: 8})
	if got := az.cachedRevision(); got != 8 {
		t.Fatalf("cache did not advance to 8, got %d", got)
	}
}

// TestOutOfOrderReloadDoesNotRegressCache drives two real reloads that finish in
// the opposite order to their revisions: reload A reads the older revision, is
// paused just before publishing, reload B publishes the newer revision, then A
// resumes. The cache must not move backward.
func TestOutOfOrderReloadDoesNotRegressCache(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := New(pool)
	svc := NewService(az)

	// Advance to revision 1 so reload A captures a concrete older revision.
	if _, _, err := svc.CreatePolicy(ctx, denySystemAgentRead()); err != nil {
		t.Fatalf("seed rev 1: %v", err)
	}

	var armed atomic.Bool
	reachedA := make(chan struct{})
	releaseA := make(chan struct{})
	az.beforePublish = func() {
		if armed.CompareAndSwap(true, false) {
			close(reachedA)
			<-releaseA
		}
	}

	armed.Store(true)
	aDone := make(chan int64, 1)
	go func() {
		snap, err := az.reload(ctx)
		if err != nil {
			aDone <- -1
			return
		}
		aDone <- snap.revision
	}()

	<-reachedA // A has read+compiled revision 1 and is paused pre-publish.

	// Commit a newer revision and let reload B publish it while A is paused.
	if _, _, err := svc.CreatePolicy(ctx, denySystemAgentRead()); err != nil {
		t.Fatalf("advance rev 2: %v", err)
	}
	snapB, err := az.reload(ctx) // not armed anymore, runs to completion
	if err != nil {
		t.Fatalf("reload B: %v", err)
	}
	if snapB.revision != 2 {
		t.Fatalf("reload B revision = %d, want 2", snapB.revision)
	}
	if got := az.cachedRevision(); got != 2 {
		t.Fatalf("cache after B = %d, want 2", got)
	}

	close(releaseA) // A publishes revision 1 last; must not regress the cache.
	if aRev := <-aDone; aRev != 1 {
		t.Fatalf("reload A returned revision %d, want its own consistent 1", aRev)
	}
	if got := az.cachedRevision(); got != 2 {
		t.Fatalf("cache regressed to %d after late reload A, want 2", got)
	}
}

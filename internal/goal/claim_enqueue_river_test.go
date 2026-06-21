package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// claim_enqueue_river_test.go drives a REAL *river.Client[pgx.Tx].InsertTx on the
// claim's pgx.Tx (TestClaimEnqueueAtomic uses a fake hook to prove the service
// rollback; this proves the cross-library contract). It locks that River's job
// insert participates in the claim transaction: visible only after commit, and
// rolled back together with the attempt when the claim aborts. If a River upgrade
// ever broke InsertTx's tx semantics, this fails instead of silently orphaning
// claims in production.
func TestClaimEnqueueAtomicRiver(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Insert-only client (no queues/workers): enough to exercise InsertTx.
	client, err := river.NewClient(riverpgxv5.New(h.db), &river.Config{})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	insert := func(ctx context.Context, tx pgx.Tx, gid, aid string) error {
		_, err := client.InsertTx(ctx, tx, goalAttemptArgs{GoalID: gid, AttemptID: aid}, goalInsertOpts())
		return err
	}
	countJobs := func() int {
		var n int
		if err := h.db.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1`, goalAttemptArgs{}.Kind()).Scan(&n); err != nil {
			t.Fatalf("count river_job: %v", err)
		}
		return n
	}

	t.Run("success commits the job with the claim", func(t *testing.T) {
		d := h.createRoot(KindLeaf, AcceptanceContract{})
		h.activate(d.ID)
		before := countJobs()

		if _, err := h.svc.Claim(ctx, d.ID, "w-1", AttemptEnqueuer(insert)); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if got := countJobs(); got != before+1 {
			t.Fatalf("river_job count = %d, want %d (job committed with claim)", got, before+1)
		}
	})

	t.Run("error after insert rolls the job back with the claim", func(t *testing.T) {
		d := h.createRoot(KindLeaf, AcceptanceContract{})
		h.activate(d.ID)
		before := countJobs()

		boom := errors.New("boom after insert")
		enqueue := AttemptEnqueuer(func(ctx context.Context, tx pgx.Tx, gid, aid string) error {
			// Insert the job, THEN fail — proves the River insert is undone by the
			// claim-tx rollback, not merely never attempted.
			if err := insert(ctx, tx, gid, aid); err != nil {
				return err
			}
			return boom
		})
		if _, err := h.svc.Claim(ctx, d.ID, "w-1", enqueue); !errors.Is(err, boom) {
			t.Fatalf("Claim err = %v, want %v", err, boom)
		}
		if got := countJobs(); got != before {
			t.Fatalf("river_job count = %d, want %d (insert rolled back)", got, before)
		}
		if atts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: d.ID}); err != nil {
			t.Fatalf("list attempts: %v", err)
		} else if len(atts) != 0 {
			t.Fatalf("attempts = %d, want 0 (claim rolled back)", len(atts))
		}
	})
}

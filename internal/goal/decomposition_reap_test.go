package goal

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TestDecompositionAttemptNotReaped pins CR-003: a decomposition attempt is minted
// with an instantly-stale lease and is never heartbeated (it runs in the planning
// session, not as a leased River worker), so the lease-based reaper must skip
// purpose=decomposition. Otherwise the next tick reaps it and bounces the composite
// active->draft mid-planning.
func TestDecompositionAttemptNotReaped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	root := h.createRoot(KindComposite, AcceptanceContract{})
	att, err := h.svc.BeginDecomposition(ctx, root.ID)
	if err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleActive {
		t.Fatalf("after BeginDecomposition lifecycle=%q want active", got)
	}

	// The reaper queries with a far-future 'now'; the decomposition attempt's lease
	// is already stale, so only the purpose filter keeps it out of the result.
	stale, err := h.q.ListStaleAttempts(ctx, sqlc.ListStaleAttemptsParams{
		Now:   pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Hour), Valid: true},
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListStaleAttempts: %v", err)
	}
	for _, a := range stale {
		if a.ID == att.ID {
			t.Fatalf("decomposition attempt %s returned by reaper; purpose=decomposition must be excluded", att.ID)
		}
	}

	// And the composite is unchanged (still active, not bounced to draft).
	if got := h.get(root.ID).Lifecycle; got != LifecycleActive {
		t.Fatalf("composite lifecycle=%q want active (decomposition not reaped)", got)
	}
}

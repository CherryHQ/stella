package goal

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// forceBlockState writes a raw lifecycle/block cause, bypassing the service —
// the point is enumerating every representable state, including ones the
// transition table would never produce.
func (h *harness) forceBlockState(id, lifecycle, blockReason, blockedBy string) {
	h.t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		UPDATE agent_goal SET lifecycle = $2, block_reason = $3, blocked_by = $4
		WHERE id = $1`, id, lifecycle, blockReason, blockedBy); err != nil {
		h.t.Fatalf("force block state: %v", err)
	}
}

// TestListInboxGoalsMatchesNeedsAttention pins the one remaining SQL copy of
// the needs-you predicate (ListInboxGoals' WHERE must run in the DB) to the
// canonical NeedsAttention: for every reachable lifecycle x block_reason x
// blocked_by combination on a root goal, inbox membership under the
// needs-attention branch must equal NeedsAttention. If either side changes
// without the other, this fails.
//
// Scoping conditions the Go predicate deliberately omits are neutralized:
// `since` is set in the future to disable the recently-closed branch, and all
// seeds are roots so the root-not-terminal condition collapses onto the goal
// itself.
func TestListInboxGoalsMatchesNeedsAttention(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	type seeded struct {
		id, lifecycle, blockReason, blockedBy string
	}
	var all []seeded
	seed := func(lifecycle, blockReason, blockedBy string) {
		g := h.createRoot(KindLeaf, AcceptanceContract{})
		h.forceBlockState(g.ID, lifecycle, blockReason, blockedBy)
		all = append(all, seeded{g.ID, lifecycle, blockReason, blockedBy})
	}

	blockReasons := []string{
		BlockDep, BlockNeedsVerdict, BlockNeedsPlanApproval, BlockBudgetExhausted,
		BlockPlanningInvalid, BlockEnvUnavailable, BlockContractConflict,
	}
	// blocked_by is constrained to these by ValidBlockedBy.
	blockedBys := []string{"", BlockEnvUnavailable, BlockContractConflict}
	for _, br := range blockReasons {
		for _, by := range blockedBys {
			seed(LifecycleBlocked, br, by)
		}
	}
	// accepted is omitted: agent_goal_check4 demands a real accepted_output,
	// and NeedsAttention only keys on lifecycle != blocked anyway.
	for _, lc := range []string{
		LifecycleDraft, LifecycleReady, LifecycleActive,
		LifecycleRejectedFinal, LifecycleCancelled,
	} {
		seed(lc, "", "")
	}
	// Residual block cause on a terminal row must not resurface in the inbox.
	seed(LifecycleCancelled, BlockNeedsVerdict, "")

	rows, err := h.q.ListInboxGoals(ctx, sqlc.ListInboxGoalsParams{
		UserID:     h.userID,
		Since:      time.Now().UTC().Add(24 * time.Hour),
		LimitCount: int32(len(all) + 10),
	})
	if err != nil {
		t.Fatalf("ListInboxGoals: %v", err)
	}
	inbox := map[string]bool{}
	for _, r := range rows {
		inbox[r.ID] = true
	}

	for _, s := range all {
		want := NeedsAttention(s.lifecycle, s.blockReason, s.blockedBy)
		if inbox[s.id] != want {
			t.Errorf("(%s, %q, %q): inbox=%v, NeedsAttention=%v — SQL and Go predicate drifted",
				s.lifecycle, s.blockReason, s.blockedBy, inbox[s.id], want)
		}
	}
}

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
func (h *harness) forceBlockState(id, lifecycle, blockReason string) {
	h.t.Helper()
	doneReason := ""
	if lifecycle == LifecycleDone {
		doneReason = DoneReasonCancelled
	}
	if _, err := h.db.Exec(context.Background(), `
		UPDATE agent_goal SET lifecycle = $2, block_reason = $3, done_reason = $4
		WHERE id = $1`, id, lifecycle, blockReason, doneReason); err != nil {
		h.t.Fatalf("force block state: %v", err)
	}
}

// TestListInboxGoalsMatchesNeedsAttention pins the one remaining SQL copy of
// the needs-you predicate (ListInboxGoals' WHERE must run in the DB) to the
// canonical NeedsAttention: for every reachable lifecycle x block_reason
// combination on a root goal, inbox membership under the
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
		id, lifecycle, blockReason string
	}
	var all []seeded
	seed := func(lifecycle, blockReason string) {
		g := h.createRoot(KindLeaf, AcceptanceContract{})
		h.forceBlockState(g.ID, lifecycle, blockReason)
		all = append(all, seeded{g.ID, lifecycle, blockReason})
	}

	blockReasons := []string{
		BlockNeedsVerdict, BlockNeedsPlanApproval, BlockBudgetExhausted,
		BlockPlanningInvalid, BlockEnvUnavailable, BlockContractConflict,
	}
	for _, br := range blockReasons {
		seed(LifecycleBlocked, br)
	}
	// accepted is omitted: agent_goal_check4 demands a real accepted_output,
	// and NeedsAttention only keys on lifecycle != blocked anyway.
	for _, lc := range []string{
		LifecycleDraft, LifecyclePending, LifecycleActive, LifecycleDone,
	} {
		seed(lc, "")
	}
	// Residual block cause on a terminal row must not resurface in the inbox.
	seed(LifecycleDone, BlockNeedsVerdict)

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
		want := NeedsAttention(s.lifecycle, s.blockReason)
		if inbox[s.id] != want {
			t.Errorf("(%s, %q): inbox=%v, NeedsAttention=%v -- SQL and Go predicate drifted",
				s.lifecycle, s.blockReason, inbox[s.id], want)
		}
	}
}

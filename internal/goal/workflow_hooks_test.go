package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// A frozen workflow replay can materialize a composite between a decomposition
// attempt's pre-tx read and its submit (the attempt was minted while the child
// was still draft/unplanned). The planned_at re-check under the lock must fail
// the late submit closed instead of overwriting the frozen plan and creating a
// second, content-keyed children set.
func TestSubmitDecompositionFailsClosedAfterFrozenMaterialize(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	att, err := h.svc.BeginDecomposition(ctx, root.ID)
	if err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("PromoteAttempt: %v", err)
	}
	frozen := DecompositionContent{Children: []ProposedChild{cmp_child("frozen-a", true)}}
	if err := h.svc.MaterializeFrozenLayer(ctx, root.ID, frozen, FrozenStamp{}); err != nil {
		t.Fatalf("MaterializeFrozenLayer: %v", err)
	}
	llm := DecompositionContent{Children: []ProposedChild{cmp_child("llm-a", true)}}
	if err := h.svc.SubmitDecomposition(ctx, att.ID, AttemptEvidence{}, llm); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("late decomposition submit = %v, want ErrInvalidTransition", err)
	}
	children := cmp_children(t, h, root.ID)
	if len(children) != 1 || children[0].Title != "child-frozen-a" {
		t.Fatalf("frozen children clobbered: %d children, first %q", len(children), children[0].Title)
	}
}

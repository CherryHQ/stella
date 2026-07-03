package workflow

import (
	"testing"

	"github.com/CherryHQ/stella/internal/goal"
)

func TestFrozenPlanValidateFullyFrozenHash(t *testing.T) {
	plan := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "a", Title: "A", Intent: "do A", Kind: goal.KindLeaf, Required: true}}}}
	if err := plan.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !plan.FullyFrozen() {
		t.Fatalf("leaf-only plan should be fully frozen")
	}
	h1, h2 := plan.Hash(), plan.Hash()
	if h1 != h2 {
		t.Fatalf("hash should be stable")
	}

	bad := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "a", Kind: goal.KindLeaf, Required: true}, Plan: &FrozenPlan{}}}}
	if err := bad.Validate(); err == nil {
		t.Fatalf("leaf sub-plan should fail")
	}

	partial := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "c", Title: "C", Intent: "do C", Kind: goal.KindComposite, Required: true}}}}
	if err := partial.Validate(); err != nil {
		t.Fatalf("partial validate: %v", err)
	}
	if partial.FullyFrozen() {
		t.Fatalf("nil composite plan should not be fully frozen")
	}
}

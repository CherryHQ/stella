package goal

import (
	"errors"
	"testing"
)

// detItemContract is a single required deterministic acceptance item.
func detItemContract() AcceptanceContract {
	return AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items:  []AcceptanceItem{{ID: "build", Kind: ItemDeterministic, Required: true, Command: "true"}},
	}
}

// TestValidateDecomposition_CompositeDeterministicRejected pins CR-001 at the
// proposal boundary: a composite child cannot carry a deterministic acceptance
// item (it produces no output, so the check would stall pending forever). A leaf
// child with the same contract is fine — leaves run their checks.
func TestValidateDecomposition_CompositeDeterministicRejected(t *testing.T) {
	composite := DecompositionContent{Children: []ProposedChild{{
		Key: "a", Title: "a", Intent: "a", Kind: KindComposite, Required: true,
		AcceptanceContract: detItemContract(),
	}}}
	if err := ValidateDecomposition(composite, 0, defaultMaxDepth); !errors.Is(err, ErrCompositeDeterministicContract) {
		t.Fatalf("composite child with deterministic contract err=%v want ErrCompositeDeterministicContract", err)
	}

	leaf := DecompositionContent{Children: []ProposedChild{{
		Key: "a", Title: "a", Intent: "a", Kind: KindLeaf, Required: true,
		AcceptanceContract: detItemContract(),
	}}}
	if err := ValidateDecomposition(leaf, 0, defaultMaxDepth); err != nil {
		t.Fatalf("leaf child with deterministic contract err=%v want nil", err)
	}
}

// TestValidateDecomposition_CompositeDepthGuard pins CR-003: a composite child
// must leave a level of depth for its own children (parentDepth+2 <= maxDepth).
// With maxDepth=1 a composite child is rejected (it could never decompose) while
// a leaf child at the same depth is allowed.
func TestValidateDecomposition_CompositeDepthGuard(t *testing.T) {
	composite := DecompositionContent{Children: []ProposedChild{{
		Key: "a", Title: "a", Intent: "a", Kind: KindComposite, Required: true,
	}}}
	if err := ValidateDecomposition(composite, 0, 1); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("composite child at maxDepth=1 err=%v want ErrDepthExceeded", err)
	}

	leaf := DecompositionContent{Children: []ProposedChild{{
		Key: "a", Title: "a", Intent: "a", Kind: KindLeaf, Required: true,
	}}}
	if err := ValidateDecomposition(leaf, 0, 1); err != nil {
		t.Fatalf("leaf child at maxDepth=1 err=%v want nil", err)
	}
}

// TestValidateDecomposition_BreadthCap pins CR-004: a plan exceeding the breadth
// cap is rejected before it fans out unbounded inserts.
func TestValidateDecomposition_BreadthCap(t *testing.T) {
	kids := make([]ProposedChild, maxDecompositionBreadth+1)
	for i := range kids {
		kids[i] = cmp_child(string(rune('a'+i%26))+string(rune('0'+i/26)), true)
	}
	if err := ValidateDecomposition(DecompositionContent{Children: kids}, 0, defaultMaxDepth); !errors.Is(err, ErrInvalidDecomposition) {
		t.Fatalf("over-breadth plan (%d children) err=%v want ErrInvalidDecomposition", len(kids), err)
	}
}

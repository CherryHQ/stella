package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/goal"
)

const PayloadFormatFrozenV0 = "frozen/v0"

type FrozenPlan struct {
	Children []FrozenNode        `json:"children"`
	Edges    []goal.ProposedEdge `json:"edges"`
}

type FrozenNode struct {
	Child goal.ProposedChild `json:"child"`
	Plan  *FrozenPlan        `json:"plan,omitempty"`
}

func DecodeFrozenPlan(b []byte) (FrozenPlan, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p FrozenPlan
	if err := dec.Decode(&p); err != nil {
		return FrozenPlan{}, fmt.Errorf("decode frozen plan: %w", err)
	}
	if dec.More() {
		return FrozenPlan{}, fmt.Errorf("decode frozen plan: trailing JSON")
	}
	return p, nil
}

func (p FrozenPlan) Validate() error {
	return p.ValidateMaxDepth(goal.ConvergencePolicy{}.Normalized().MaxDepth)
}

func (p FrozenPlan) ValidateMaxDepth(maxDepth int) error {
	return p.validateAt(0, maxDepth)
}

func (p FrozenPlan) validateAt(parentDepth, maxDepth int) error {
	content := goal.DecompositionContent{Edges: p.Edges}
	for _, n := range p.Children {
		if n.Child.Kind == goal.KindLeaf && n.Plan != nil {
			return fmt.Errorf("%w: leaf %q carries frozen sub-plan", goal.ErrInvalidDecomposition, n.Child.Key)
		}
		content.Children = append(content.Children, n.Child)
	}
	if err := goal.ValidateDecomposition(content, parentDepth, maxDepth); err != nil {
		return err
	}
	for _, n := range p.Children {
		if n.Child.Kind == goal.KindComposite && n.Plan != nil {
			if err := n.Plan.validateAt(parentDepth+1, maxDepth); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p FrozenPlan) FullyFrozen() bool {
	for _, n := range p.Children {
		if n.Child.Kind != goal.KindComposite {
			continue
		}
		if n.Plan == nil || !n.Plan.FullyFrozen() {
			return false
		}
	}
	return true
}

func (p FrozenPlan) Hash() string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (p FrozenPlan) decomposition() goal.DecompositionContent {
	out := goal.DecompositionContent{Edges: p.Edges}
	for _, n := range p.Children {
		out.Children = append(out.Children, n.Child)
	}
	return out
}

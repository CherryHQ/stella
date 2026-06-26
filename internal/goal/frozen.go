package goal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// frozen.go is the workflow seam (issue #594): a workflow is a goal whose
// decomposition plan was frozen from a once-accepted composite, so a later run
// SKIPS the planner and materializes a deterministic subtree. This file owns the
// recursive DTO, the snapshot (live tree -> FrozenPlan), and the recursive
// instantiation (FrozenPlan -> live tree). It lives in the goal package, not the
// workflow package, because the recursion drives the unexported materializer /
// converge internals directly; FrozenPlan is a distinct type from the planner's
// DecompositionContent, so the planner product stays unpolluted.

// FrozenPlan is a composite's frozen decomposition: the children + edges of ONE
// level. It mirrors DecompositionContent but nests — a composite child carries
// its own FrozenPlan so the whole tree freezes in one document. The light at the
// spectrum is per-node: a composite child with a non-nil Plan instantiates
// deterministically; a nil Plan is left to the planner to replan (the
// semi-frozen case).
type FrozenPlan struct {
	Children []FrozenNode   `json:"children"`
	Edges    []ProposedEdge `json:"edges,omitempty"`
}

// FrozenNode is one frozen child: its proposed spec plus, for a composite, the
// frozen sub-plan. Plan is nil for a leaf, and nil for a composite whose subtree
// is intentionally left to the planner (replan on instantiate).
type FrozenNode struct {
	Child ProposedChild `json:"child"`
	Plan  *FrozenPlan   `json:"plan,omitempty"`
}

// decomposition projects one frozen level onto the planner's DecompositionContent
// (children specs + edges) so Materialize / ValidateDecomposition consume it
// unchanged. The nested sub-plans are handled by the recursion, not here.
func (p FrozenPlan) decomposition() DecompositionContent {
	out := DecompositionContent{Edges: p.Edges}
	for _, n := range p.Children {
		out.Children = append(out.Children, n.Child)
	}
	return out
}

// Hash returns a stable content hash of the frozen plan (sha256 of its canonical
// JSON). Stamped into the instantiated root's context so a run is traceable back
// to the exact spec it materialized, independent of the workflow row's version.
func (p FrozenPlan) Hash() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ValidateFrozenPlan checks a frozen plan recursively: every level satisfies the
// same structural invariants as a planner decomposition (ValidateDecomposition),
// and a composite child with a frozen sub-plan is validated one level deeper.
// parentDepth is the depth of the composite that owns this plan (the root sits at
// depth 0). Returns the same sentinels as ValidateDecomposition.
func ValidateFrozenPlan(p FrozenPlan, parentDepth, maxDepth int) error {
	if err := ValidateDecomposition(p.decomposition(), parentDepth, maxDepth); err != nil {
		return err
	}
	for _, n := range p.Children {
		if n.Plan == nil {
			continue // leaf, or a composite left to the planner (semi-frozen)
		}
		if n.Child.Kind != KindComposite {
			return ErrInvalidDecomposition // only a composite carries a sub-plan
		}
		if err := ValidateFrozenPlan(*n.Plan, parentDepth+1, maxDepth); err != nil {
			return err
		}
	}
	return nil
}

// SnapshotFrozenPlan freezes a live composite's subtree into a FrozenPlan by
// reading the stored decomposition plan at each level (agent_goal.plan) and
// recursing into composite children. It captures only the reproducible SPEC
// (child proposals + edges verbatim); run-specific state (session, accepted
// output, attempts) is never read. A composite child whose own plan is empty
// (never decomposed in the source run) becomes a nil sub-plan — left to the
// planner on instantiate rather than frozen to nothing.
func (s *GoalService) SnapshotFrozenPlan(ctx context.Context, goalID string) (FrozenPlan, error) {
	g, err := getGoal(ctx, s.q, goalID)
	if err != nil {
		return FrozenPlan{}, err
	}
	var content DecompositionContent
	if err := unmarshalJSON(g.Plan, &content); err != nil {
		return FrozenPlan{}, fmt.Errorf("%w: goal %s plan: %w", ErrInvalidDecomposition, goalID, err)
	}
	fp := FrozenPlan{Edges: content.Edges}
	for _, pc := range content.Children {
		node := FrozenNode{Child: pc}
		if pc.Kind == KindComposite {
			sub, err := s.SnapshotFrozenPlan(ctx, childID(g.ID, pc.Key))
			if err != nil {
				return FrozenPlan{}, err
			}
			// Only freeze a sub-plan that actually decomposed; an empty one is a
			// composite to replan, not a frozen dead end.
			if len(sub.Children) > 0 {
				node.Plan = &sub
			}
		}
		fp.Children = append(fp.Children, node)
	}
	return fp, nil
}

// FrozenRootSpec is the root metadata for a workflow instantiation — everything
// the root composite goal needs that is NOT carried by the frozen plan itself.
// Context is stamped onto the root (workflow_id, version, plan_hash) for trace.
type FrozenRootSpec struct {
	UserID      string
	AgentID     string
	ProjectID   string
	Title       string
	Intent      string
	Contract    AcceptanceContract
	Convergence ConvergencePolicy
	Context     json.RawMessage // {"workflow_id","version","plan_hash"}; empty => "{}"
}

// InstantiateFrozen materializes a whole frozen tree into a live goal subtree in
// ONE transaction, never running the planner. It mirrors ApprovePlan but
// recursive: create the root composite, then at every level lock + SetGoalPlan +
// Materialize, move the composite active and its leaf children ready, and recurse
// into each composite child that carries a frozen sub-plan.
//
// Single-tx is deliberate. A per-level commit would leave a frozen composite
// child committed in 'draft', and the dispatcher's decomposition scan would race
// in and plan it before the recursion reached it. Holding the whole tree in one
// tx keeps every frozen composite locked->materialized->active atomically, never
// visible as draft-unplanned. A composite child with a nil sub-plan is left
// 'draft' on purpose — that one IS the planner's job (semi-frozen).
//
// Every child session is pre-minted OUTSIDE the tx (slow session minting must not
// run on the held connection — same discipline as Materialize). Child ids are
// deterministic (childID), so concurrent instantiations of the same workflow get
// disjoint trees off their distinct root ids.
func (s *GoalService) InstantiateFrozen(ctx context.Context, spec FrozenRootSpec, plan FrozenPlan) (sqlc.AgentGoal, error) {
	if s.newSession == nil {
		return sqlc.AgentGoal{}, fmt.Errorf("goal: no session minter configured")
	}
	// A workflow root is a composite; a deterministic check on it has no output
	// source and would stall the fold forever (mirrors CreateGoal's guard).
	if spec.Contract.HasDeterministicItem() {
		return sqlc.AgentGoal{}, ErrCompositeDeterministicContract
	}
	convergence := spec.Convergence.Normalized()
	if err := ValidateFrozenPlan(plan, 0, convergence.MaxDepth); err != nil {
		return sqlc.AgentGoal{}, err
	}

	rootID := newID()
	rootSession, err := s.newSession(ctx, spec.UserID, spec.AgentID, spec.ProjectID)
	if err != nil {
		return sqlc.AgentGoal{}, fmt.Errorf("mint root session: %w", err)
	}
	// Pre-mint a session for every node, keyed by its deterministic child id.
	sessions := make(map[string]string)
	var mint func(parentID string, p FrozenPlan) error
	mint = func(parentID string, p FrozenPlan) error {
		for _, n := range p.Children {
			cid := childID(parentID, n.Child.Key)
			sid, err := s.newSession(ctx, spec.UserID, spec.AgentID, spec.ProjectID)
			if err != nil {
				return fmt.Errorf("mint session for %q: %w", n.Child.Key, err)
			}
			sessions[cid] = sid
			if n.Plan != nil {
				if err := mint(cid, *n.Plan); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := mint(rootID, plan); err != nil {
		return sqlc.AgentGoal{}, err
	}

	context := spec.Context
	if len(context) == 0 {
		context = emptyJSON
	}

	var out sqlc.AgentGoal
	err = s.withTx(ctx, func(q *sqlc.Queries) error {
		root, err := q.CreateGoal(ctx, sqlc.CreateGoalParams{
			ID:                 rootID,
			UserID:             spec.UserID,
			AgentID:            spec.AgentID,
			ProjectID:          pgnull.Text(spec.ProjectID),
			ParentID:           pgtype.Text{},
			RootID:             rootID,
			Depth:              0,
			Position:           0,
			SessionID:          rootSession,
			Title:              spec.Title,
			Intent:             spec.Intent,
			Kind:               KindComposite,
			Priority:           PriorityRoutine,
			Required:           true,
			AcceptanceContract: marshalJSON(spec.Contract),
			ConvergencePolicy:  marshalJSON(convergence),
			ReviewPolicy:       ReviewNone,
			Lifecycle:          LifecycleDraft,
			Context:            context,
			DispatchHint:       emptyJSON,
		})
		if err != nil {
			return fmt.Errorf("create workflow root: %w", err)
		}
		if err := s.materializeFrozenTree(ctx, q, root, plan, sessions); err != nil {
			return err
		}
		out, err = getGoal(ctx, q, rootID)
		return err
	})
	return out, err
}

// materializeFrozenTree instantiates one composite level inside the caller's tx,
// then recurses into composite children with frozen sub-plans. The parent arrives
// 'draft' (the root from CreateGoal, a composite child from its parent's
// Materialize) and leaves 'active'. See InstantiateFrozen for the single-tx
// rationale.
func (s *GoalService) materializeFrozenTree(ctx context.Context, q *sqlc.Queries, parent sqlc.AgentGoal, plan FrozenPlan, sessions map[string]string) error {
	content := plan.decomposition()
	childSessions := make(map[string]string, len(plan.Children))
	for _, n := range plan.Children {
		childSessions[n.Child.Key] = sessions[childID(parent.ID, n.Child.Key)]
	}

	if err := q.LockGoalForWrite(ctx, parent.ID); err != nil {
		return fmt.Errorf("lock workflow goal for materialize: %w", err)
	}
	if err := q.SetGoalPlan(ctx, sqlc.SetGoalPlanParams{Plan: marshalJSON(content), ID: parent.ID}); err != nil {
		return fmt.Errorf("set workflow plan: %w", err)
	}
	if err := s.Materialize(ctx, q, parent, content, childSessions); err != nil {
		return err
	}
	rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
		ToLifecycle:   LifecycleActive,
		BlockReason:   "",
		ID:            parent.ID,
		FromLifecycle: LifecycleDraft,
	})
	if err != nil {
		return fmt.Errorf("activate workflow composite: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}

	// Release children: leaves run now (ready); a composite child with a frozen
	// sub-plan recurses (its own materialize moves it active in this same tx); a
	// composite child with a nil sub-plan stays draft for the planner.
	nodeByID := make(map[string]FrozenNode, len(plan.Children))
	for _, n := range plan.Children {
		nodeByID[childID(parent.ID, n.Child.Key)] = n
	}
	children, err := q.ListGoalChildren(ctx, pgnull.Text(parent.ID))
	if err != nil {
		return fmt.Errorf("list workflow children: %w", err)
	}
	for _, c := range children {
		if c.Lifecycle != LifecycleDraft {
			continue // already released by a retried materialize
		}
		if c.Kind != KindComposite {
			if _, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
				ToLifecycle:   LifecycleReady,
				BlockReason:   "",
				ID:            c.ID,
				FromLifecycle: LifecycleDraft,
			}); err != nil {
				return fmt.Errorf("release workflow leaf: %w", err)
			}
			continue
		}
		node := nodeByID[c.ID]
		if node.Plan == nil {
			continue // semi-frozen composite: the planner decomposes it
		}
		if err := s.materializeFrozenTree(ctx, q, c, *node.Plan, sessions); err != nil {
			return err
		}
	}
	return nil
}

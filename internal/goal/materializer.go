package goal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// childID is the deterministic child goal id derived from the composite goal id +
// the proposal's stable Key (contract §6 idempotency anchor): id =
// hex(sha256(goal_id + 0x00 + child.Key)) truncated to a 32-hex-char id.
// Re-running Materialize Get-then-skips existing children by primary key, so a
// retried materialize is a structural no-op, not application luck. The goal id is
// a stable anchor because a composite is decomposed exactly once (no replan): a
// plan rejected before materialize never created children, so re-decomposing the
// same keys cannot collide with live rows.
func childID(goalID, key string) string {
	h := sha256.Sum256(append(append([]byte(goalID), 0), []byte(key)...))
	return hex.EncodeToString(h[:16])
}

// Materialize creates a composite's children + edges from its proposed plan, in
// ONE caller-supplied tx (contract §6). It operates on the passed tx-bound
// querier — no import cycle, no own tx, no session minting (sessions are
// pre-minted outside the tx and resolved per child by the caller through the
// childSessions map). It is idempotent: the planned_at fence (MarkGoalPlanned
// CAS) short-circuits a second call, and each child's deterministic id makes
// re-insert a no-op.
//
// Steps:
//   - depth guard: parent.depth+1 ≤ max_depth (root's convergence_policy), else
//     ErrDepthExceeded.
//   - validate content (≥1 required child, edge keys resolve, no cycle), else
//     ErrInvalidDecomposition / ErrCycle.
//   - for each ProposedChild: insert (skip-if-exists) a child goal
//     (parent_id=composite, root_id=composite.root_id, depth=parent.depth+1,
//     position=index, contract/policy from the proposal, lifecycle draft).
//   - for each ProposedEdge: insert agent_goal_edge (resolve keys→ids); PK
//     collision = no-op.
//   - CAS planned_at.
//
// Replan reconcile (cancelling/detaching children dropped by a later plan) is
// intentionally out of scope: a composite is decomposed exactly once (planned_at
// gates re-decomposition). Re-enabling replan is a tracked follow-up (contract §10).
//
// The caller MUST hold LockGoalForWrite on parent so the planned_at CAS and child
// inserts cannot race a concurrent materialize.
func (s *GoalService) Materialize(ctx context.Context, qtx *sqlc.Queries, parent sqlc.AgentGoal, content DecompositionContent, childSessions map[string]string) error {
	_ = childSessions
	// Idempotency fence: a second call short-circuits once planned_at is set.
	if parent.PlannedAt.Valid {
		return nil
	}

	// Resolve the recursion ceiling from the ROOT's convergence policy (§6).
	maxDepth := defaultMaxDepth
	root, err := qtx.GetGoal(ctx, parent.RootID)
	if err == nil {
		var rp ConvergencePolicy
		if err := unmarshalJSON(root.ConvergencePolicy, &rp); err == nil {
			maxDepth = rp.Normalized().MaxDepth
		}
	}
	if err := ValidateDecomposition(content, int(parent.Depth), maxDepth); err != nil {
		return err
	}

	childDepth := parent.Depth + 1
	for i, ch := range content.Children {
		cid := childID(parent.ID, ch.Key)
		// Get-then-skip on the deterministic id makes a retried materialize a
		// structural no-op rather than a duplicate insert.
		if _, err := qtx.GetGoal(ctx, cid); err == nil {
			continue
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("probe child %q: %w", ch.Key, err)
		}

		kind := ch.Kind
		if kind == "" {
			kind = KindLeaf
		}
		reviewPolicy := ch.ReviewPolicy
		if reviewPolicy == "" {
			reviewPolicy = ReviewNone
		}
		if _, err := qtx.CreateGoal(ctx, sqlc.CreateGoalParams{
			ID:                 cid,
			UserID:             parent.UserID,
			AgentID:            parent.AgentID,
			ProjectID:          parent.ProjectID,
			ParentID:           pgtype.Text{String: parent.ID, Valid: true},
			RootID:             parent.RootID,
			Depth:              childDepth,
			Position:           int64(i),
			Title:              ch.Title,
			Intent:             ch.Intent,
			Kind:               kind,
			Priority:           PriorityRoutine,
			Required:           ch.Required,
			AcceptanceContract: marshalJSON(ch.AcceptanceContract),
			ConvergencePolicy:  marshalJSON(ch.ConvergencePolicy),
			ReviewPolicy:       reviewPolicy,
			Lifecycle:          LifecycleDraft,
			Context:            emptyJSON,
			DispatchHint:       emptyJSON,
			IdempotencyKey:     pgtype.Text{},
		}); err != nil {
			return fmt.Errorf("create child %q: %w", ch.Key, err)
		}
	}

	// Edges: resolve keys → deterministic child ids; a PK collision (a re-run
	// edge) is tolerated as a no-op.
	for _, e := range content.Edges {
		kind := e.Kind
		if kind == "" {
			kind = EdgeHard
		}
		onFailure := e.OnFailure
		if onFailure == "" {
			onFailure = OnFailureBlock
		}
		down := childID(parent.ID, e.DownstreamKey)
		up := childID(parent.ID, e.UpstreamKey)
		if _, err := qtx.GetEdge(ctx, sqlc.GetEdgeParams{GoalID: down, UpstreamID: up}); err == nil {
			continue // already materialized
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("probe edge %s→%s: %w", e.UpstreamKey, e.DownstreamKey, err)
		}
		if _, err := qtx.CreateEdge(ctx, sqlc.CreateEdgeParams{
			GoalID:     down,
			UpstreamID: up,
			EdgeKind:   kind,
			OnFailure:  onFailure,
		}); err != nil {
			return fmt.Errorf("create edge %s→%s: %w", e.UpstreamKey, e.DownstreamKey, err)
		}
	}

	rows, err := qtx.MarkGoalPlanned(ctx, parent.ID)
	if err != nil {
		return fmt.Errorf("mark goal planned: %w", err)
	}
	if rows == 0 {
		return nil
	}
	return nil
}

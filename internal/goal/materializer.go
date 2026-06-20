package goal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// childID is the deterministic child goal id derived from a revision +
// the proposal's stable Key (contract §6 idempotency anchor): id =
// hex(sha256(revision_id + 0x00 + child.Key)) truncated to a 32-hex-char id.
// Re-running Materialize Get-then-skips existing children by primary key, so a
// retried materialize is a structural no-op, not application luck.
func childID(revisionID, key string) string {
	h := sha256.Sum256(append(append([]byte(revisionID), 0), []byte(key)...))
	return hex.EncodeToString(h[:16])
}

// Materialize creates a composite's children + edges from an accepted revision,
// in ONE caller-supplied tx (contract §6). It operates on the passed tx-bound
// querier — no import cycle, no own tx, no session minting (sessions are
// pre-minted outside the tx and resolved per child by the caller through the
// childSessions map). It is idempotent: the materialized_at fence short-circuits
// a second call, and each child's deterministic id makes re-insert a no-op.
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
//   - set parent accepted_revision_id + required_total (count of required
//     children); stamp revision.materialized_at; supersede prior open revisions.
//
// Replan reconcile (cancelling/detaching children dropped by a later revision) is
// intentionally out of scope: BeginDecomposition gates a second decomposition out
// (draft-only), so a composite is decomposed exactly once. Re-enabling replan is a
// tracked follow-up (contract §10).
//
// childSessions maps a child Key → a pre-minted session_id. A missing entry is a
// programming error the caller (decompose.go) prevents by minting all sessions
// before opening the tx.
func (s *GoalService) Materialize(ctx context.Context, qtx *sqlc.Queries, rev sqlc.AgentGoalRevision, parent sqlc.AgentGoal, childSessions map[string]string) error {
	// Idempotency fence: a second call short-circuits once materialized_at is set.
	if rev.MaterializedAt.Valid {
		return nil
	}

	var content DecompositionContent
	if err := unmarshalJSON(rev.Content, &content); err != nil {
		return fmt.Errorf("%w: revision content: %w", ErrInvalidDecomposition, err)
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
	requiredTotal := int64(0)
	for i, ch := range content.Children {
		cid := childID(rev.ID, ch.Key)
		if ch.Required {
			requiredTotal++
		}
		// Get-then-skip on the deterministic id makes a retried materialize a
		// structural no-op rather than a duplicate insert.
		if _, err := qtx.GetGoal(ctx, cid); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("probe child %q: %w", ch.Key, err)
		}

		sid, ok := childSessions[ch.Key]
		if !ok {
			return fmt.Errorf("%w: no pre-minted session for child %q", ErrInvalidDecomposition, ch.Key)
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
			ParentID:           sql.NullString{String: parent.ID, Valid: true},
			RootID:             parent.RootID,
			Depth:              childDepth,
			Position:           int64(i),
			SessionID:          sid,
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
		down := childID(rev.ID, e.DownstreamKey)
		up := childID(rev.ID, e.UpstreamKey)
		if _, err := qtx.GetEdge(ctx, sqlc.GetEdgeParams{GoalID: down, UpstreamID: up}); err == nil {
			continue // already materialized
		} else if !errors.Is(err, sql.ErrNoRows) {
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

	// Point the parent at this revision, set its required_total, supersede any
	// prior open revision, and stamp the materialized fence.
	if err := qtx.SetGoalAcceptedRevision(ctx, sqlc.SetGoalAcceptedRevisionParams{
		AcceptedRevisionID: sql.NullString{String: rev.ID, Valid: true},
		ID:                 parent.ID,
	}); err != nil {
		return fmt.Errorf("set accepted revision: %w", err)
	}
	if err := qtx.SetGoalRequiredTotal(ctx, sqlc.SetGoalRequiredTotalParams{
		RequiredTotal: requiredTotal,
		ID:            parent.ID,
	}); err != nil {
		return fmt.Errorf("set required total: %w", err)
	}
	if err := qtx.SupersedeOpenRevisions(ctx, parent.ID); err != nil {
		return fmt.Errorf("supersede open revisions: %w", err)
	}
	rows, err := qtx.MaterializeRevision(ctx, rev.ID)
	if err != nil {
		return fmt.Errorf("materialize revision: %w", err)
	}
	if rows == 0 {
		// The fence rejected it: not accepted, or already materialized. A
		// concurrent materialize already did the work — treat as a no-op.
		return ErrInvalidTransition
	}
	return nil
}

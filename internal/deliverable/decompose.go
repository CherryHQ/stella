package deliverable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DecompositionContent is a revision's proposed children + edges (contract
// §3.7). Marshaled to the revision.content TEXT column. The materializer keys
// idempotency on (revision_id, child.Key).
type DecompositionContent struct {
	Children []ProposedChild `json:"children"`
	Edges    []ProposedEdge  `json:"edges"`
}

// ProposedChild is one child deliverable a revision proposes. Key is the stable
// id within the revision and the materialize idempotency key (replaces the old
// plan_item_id).
type ProposedChild struct {
	Key                string             `json:"key"`
	Title              string             `json:"title"`
	Intent             string             `json:"intent"`
	Kind               string             `json:"kind"` // leaf | composite
	Required           bool               `json:"required"`
	AcceptanceContract AcceptanceContract `json:"acceptance_contract"`
	ConvergencePolicy  ConvergencePolicy  `json:"convergence_policy"`
	ReviewPolicy       string             `json:"review_policy,omitempty"` // a composite child carries it
}

// ProposedEdge is one sibling dependency a revision proposes, by child Key.
type ProposedEdge struct {
	DownstreamKey string `json:"downstream_key"`
	UpstreamKey   string `json:"upstream_key"`
	Kind          string `json:"kind"`       // hard | soft
	OnFailure     string `json:"on_failure"` // block | fail | ignore
}

// ValidateDecomposition checks a DecompositionContent's structural invariants
// (contract §6): ≥1 required child, every child Key unique and non-empty, every
// edge key resolves to a child Key, no edge cycle (DFS over keys), known
// enum values, and a depth budget (each composite child must leave room under
// maxDepth — the per-child depth guard is enforced at materialize, but a
// proposal that needs more than maxDepth levels is rejected early here when the
// parent depth is known). Returns ErrInvalidDecomposition or ErrCycle.
//
// parentDepth is the composite's own depth; a child sits at parentDepth+1.
func ValidateDecomposition(c DecompositionContent, parentDepth, maxDepth int) error {
	keys := make(map[string]ProposedChild, len(c.Children))
	requiredCount := 0
	for _, ch := range c.Children {
		if ch.Key == "" {
			return ErrInvalidDecomposition
		}
		if _, dup := keys[ch.Key]; dup {
			return ErrInvalidDecomposition
		}
		if ch.Kind != "" && !ValidKind(ch.Kind) {
			return ErrInvalidDecomposition
		}
		if !ch.AcceptanceContract.Valid() || !ch.ConvergencePolicy.Valid() {
			return ErrInvalidContract
		}
		if ch.ReviewPolicy != "" && !ValidReviewPolicy(ch.ReviewPolicy) {
			return ErrInvalidDecomposition
		}
		if ch.Required {
			requiredCount++
		}
		keys[ch.Key] = ch
	}
	if requiredCount < 1 {
		return ErrInvalidDecomposition // a decomposition must commit to ≥1 required child
	}
	if parentDepth+1 > maxDepth {
		return ErrDepthExceeded
	}

	// Edges: keys resolve, kinds/policies known.
	adj := make(map[string][]string, len(c.Children))
	for _, e := range c.Edges {
		if _, ok := keys[e.DownstreamKey]; !ok {
			return ErrInvalidDecomposition
		}
		if _, ok := keys[e.UpstreamKey]; !ok {
			return ErrInvalidDecomposition
		}
		if e.DownstreamKey == e.UpstreamKey {
			return ErrCycle
		}
		if e.Kind != "" && !ValidEdgeKind(e.Kind) {
			return ErrInvalidDecomposition
		}
		if e.OnFailure != "" && !ValidOnFailure(e.OnFailure) {
			return ErrInvalidDecomposition
		}
		// Edge downstream depends on upstream: upstream → downstream in the DAG.
		adj[e.UpstreamKey] = append(adj[e.UpstreamKey], e.DownstreamKey)
	}
	if hasCycleDFS(keys, adj) {
		return ErrCycle
	}
	return nil
}

// hasCycleDFS reports whether the key dependency graph contains a cycle, via a
// three-color DFS over child keys.
func hasCycleDFS(keys map[string]ProposedChild, adj map[string][]string) bool {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(keys))
	var visit func(k string) bool
	visit = func(k string) bool {
		color[k] = gray
		for _, nxt := range adj[k] {
			switch color[nxt] {
			case gray:
				return true // back edge ⇒ cycle
			case white:
				if visit(nxt) {
					return true
				}
			}
		}
		color[k] = black
		return false
	}
	for k := range keys {
		if color[k] == white {
			if visit(k) {
				return true
			}
		}
	}
	return false
}

// ── Revision FSM (contract §2.3). All writes via withTx. ─────────────────────

// BeginDecomposition starts a composite's decomposition: draft→active, minting a
// purpose=decomposition attempt (contract §2.1). Guards kind=composite and no
// open/accepted revision. The planning session is pre-minted outside the tx.
func (s *DeliverableService) BeginDecomposition(ctx context.Context, id string) (sqlc.AgentDlvAttempt, error) {
	// Load the composite first to guard kind + revision state and to carry the
	// owner identity into the OUTSIDE-tx planning-session mint.
	d, err := getDeliverable(ctx, s.q, id)
	if err != nil {
		return sqlc.AgentDlvAttempt{}, err
	}
	if d.Kind != KindComposite || d.Lifecycle != LifecycleDraft {
		return sqlc.AgentDlvAttempt{}, ErrInvalidTransition
	}
	// A composite already carrying an open/accepted revision is not re-decomposed.
	if _, err := s.q.GetOpenRevision(ctx, id); err == nil {
		return sqlc.AgentDlvAttempt{}, ErrInvalidTransition
	} else if !errors.Is(err, sql.ErrNoRows) {
		return sqlc.AgentDlvAttempt{}, fmt.Errorf("probe open revision: %w", err)
	}
	if d.AcceptedRevisionID.Valid && d.AcceptedRevisionID.String != "" {
		return sqlc.AgentDlvAttempt{}, ErrInvalidTransition
	}

	// Mint the planning session OUTSIDE the tx (SQLite single-writer self-deadlock).
	if s.newPlanningSession == nil {
		return sqlc.AgentDlvAttempt{}, fmt.Errorf("deliverable: no planning session minter configured")
	}
	sessionID, err := s.newPlanningSession(ctx, d.UserID, d.AgentID, d.ProjectID.String)
	if err != nil {
		return sqlc.AgentDlvAttempt{}, fmt.Errorf("mint planning session: %w", err)
	}

	var out sqlc.AgentDlvAttempt
	err = s.withTx(ctx, func(q *sqlc.Queries) error {
		attemptNo := int(d.AttemptCount) + 1
		input := buildInputContext(d, nil, nil, "", attemptNo)
		att, err := q.CreateAttempt(ctx, sqlc.CreateAttemptParams{
			ID:              newID(),
			DeliverableID:   d.ID,
			UserID:          d.UserID,
			AgentID:         nullStr(d.AgentID),
			ExecutorAgentID: nullStr(d.AgentID),
			SessionID:       sessionID,
			Purpose:         PurposeDecomposition,
			AttemptNo:       int64(attemptNo),
			Status:          AttemptQueued,
			InputContext:    marshalJSON(input),
			LeaseExpiresAt:  nullStr(s.now()),
		})
		if err != nil {
			return fmt.Errorf("create decomposition attempt: %w", err)
		}
		rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
			ToLifecycle:   LifecycleActive,
			BlockReason:   "",
			ID:            d.ID,
			FromLifecycle: LifecycleDraft,
		})
		if err != nil {
			return fmt.Errorf("begin decomposition: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		out = att
		return nil
	})
	return out, err
}

// CreateRevision inserts a new revision in 'draft' with revision_no = max+1
// (contract §2.3 (none)→draft). content must validate before insert.
func (s *DeliverableService) CreateRevision(ctx context.Context, deliverableID string, content DecompositionContent, sourceAttemptID string) (sqlc.AgentDlvRevision, error) {
	d, err := getDeliverable(ctx, s.q, deliverableID)
	if err != nil {
		return sqlc.AgentDlvRevision{}, err
	}
	if err := s.validateContent(ctx, d, content); err != nil {
		return sqlc.AgentDlvRevision{}, err
	}

	var out sqlc.AgentDlvRevision
	err = s.withTx(ctx, func(q *sqlc.Queries) error {
		maxNo, err := q.GetMaxRevisionNo(ctx, deliverableID)
		if err != nil {
			return fmt.Errorf("max revision no: %w", err)
		}
		rev, err := q.CreateRevision(ctx, sqlc.CreateRevisionParams{
			ID:              newID(),
			DeliverableID:   deliverableID,
			RevisionNo:      maxNo + 1,
			Status:          RevisionDraft,
			ReviewPolicy:    d.ReviewPolicy,
			Content:         marshalJSON(content),
			SourceAttemptID: nullStr(sourceAttemptID),
		})
		if err != nil {
			return fmt.Errorf("create revision: %w", err)
		}
		out = rev
		return nil
	})
	return out, err
}

// validateContent runs the structural decomposition guards against a deliverable's
// own depth and the root's max_depth ceiling (contract §6). Shared by
// CreateRevision and the accept/approve paths so a malformed plan is rejected at
// the write boundary, not at materialize.
func (s *DeliverableService) validateContent(ctx context.Context, d sqlc.AgentDlvDeliverable, content DecompositionContent) error {
	maxDepth := defaultMaxDepth
	if root, err := s.q.GetDeliverable(ctx, d.RootID); err == nil {
		var rp ConvergencePolicy
		if err := unmarshalJSON(root.ConvergencePolicy, &rp); err == nil {
			maxDepth = rp.Normalized().MaxDepth
		}
	}
	return ValidateDecomposition(content, int(d.Depth), maxDepth)
}

// SubmitForReview moves a draft→in_review when review_policy=human and no other
// open revision exists (contract §2.3).
func (s *DeliverableService) SubmitForReview(ctx context.Context, revisionID string) (sqlc.AgentDlvRevision, error) {
	var out sqlc.AgentDlvRevision
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		rev, err := getRevision(ctx, q, revisionID)
		if err != nil {
			return err
		}
		// Only a draft moves into review, and only when no other revision is open.
		if open, err := q.GetOpenRevision(ctx, rev.DeliverableID); err == nil && open.ID != revisionID {
			return ErrInvalidTransition
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("probe open revision: %w", err)
		}
		rows, err := q.UpdateRevisionStatus(ctx, sqlc.UpdateRevisionStatusParams{
			ToStatus:   RevisionInReview,
			ID:         revisionID,
			FromStatus: RevisionDraft,
		})
		if err != nil {
			return fmt.Errorf("submit revision for review: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		out, err = getRevision(ctx, q, revisionID)
		return err
	})
	return out, err
}

// getRevision loads a revision, mapping sql.ErrNoRows to ErrNotFound.
func getRevision(ctx context.Context, q *sqlc.Queries, id string) (sqlc.AgentDlvRevision, error) {
	rev, err := q.GetRevision(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.AgentDlvRevision{}, ErrNotFound
	}
	return rev, err
}

// Accept auto-accepts a draft revision (review_policy=none) after validation:
// draft→accepted, then Materialize (contract §2.3).
func (s *DeliverableService) Accept(ctx context.Context, revisionID string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.acceptAndMaterialize(ctx, revisionID, RevisionDraft)
}

// acceptAndMaterialize is the shared accept→materialize flow for both the
// review_policy=none (from draft) and human-approval (from in_review) paths
// (contract §2.3, §6). It validates the content, pre-mints every child session
// OUTSIDE the tx (SQLite single-writer self-deadlock), then in ONE tx accepts the
// revision and materializes its children/edges. fromStatus guards the source
// status the caller permits.
func (s *DeliverableService) acceptAndMaterialize(ctx context.Context, revisionID, fromStatus string) (sqlc.AgentDlvRevision, error) {
	rev, err := getRevision(ctx, s.q, revisionID)
	if err != nil {
		return sqlc.AgentDlvRevision{}, err
	}
	if rev.Status != fromStatus {
		return sqlc.AgentDlvRevision{}, ErrInvalidTransition
	}
	parent, err := getDeliverable(ctx, s.q, rev.DeliverableID)
	if err != nil {
		return sqlc.AgentDlvRevision{}, err
	}
	var content DecompositionContent
	if err := unmarshalJSON(rev.Content, &content); err != nil {
		return sqlc.AgentDlvRevision{}, fmt.Errorf("%w: revision content: %w", ErrInvalidDecomposition, err)
	}
	if err := s.validateContent(ctx, parent, content); err != nil {
		return sqlc.AgentDlvRevision{}, err
	}

	// Pre-mint a session per child OUTSIDE the tx (same as Service.MaterializeRevision).
	if s.newSession == nil {
		return sqlc.AgentDlvRevision{}, fmt.Errorf("deliverable: no worker session minter configured")
	}
	childSessions := make(map[string]string, len(content.Children))
	for _, ch := range content.Children {
		sid, err := s.newSession(ctx, parent.UserID, parent.AgentID, parent.ProjectID.String)
		if err != nil {
			return sqlc.AgentDlvRevision{}, fmt.Errorf("mint child session %q: %w", ch.Key, err)
		}
		childSessions[ch.Key] = sid
	}

	var out sqlc.AgentDlvRevision
	err = s.withTx(ctx, func(q *sqlc.Queries) error {
		rows, err := q.AcceptRevision(ctx, revisionID)
		if err != nil {
			return fmt.Errorf("accept revision: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // raced out of draft/in_review
		}
		if err := s.Materialize(ctx, q, rev, parent, childSessions); err != nil {
			return err
		}
		out, err = getRevision(ctx, q, revisionID)
		return err
	})
	return out, err
}

// Approve accepts an in_review revision after a human approval: in_review→
// accepted, then Materialize (contract §2.3).
func (s *DeliverableService) Approve(ctx context.Context, revisionID string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.acceptAndMaterialize(ctx, revisionID, RevisionInReview)
}

// Reject rejects an in_review revision: in_review→rejected; the composite stays
// active and a new decomposition attempt is minted (rework) (contract §2.3).
func (s *DeliverableService) Reject(ctx context.Context, revisionID, reason string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.transitionRevision(ctx, revisionID, RevisionInReview, RevisionRejected)
}

// transitionRevision applies one guarded revision status move (in_review→rejected
// / in_review→draft) and returns the updated row. A 0-row update means the
// revision was not in the expected from-status (raced) → ErrInvalidTransition.
func (s *DeliverableService) transitionRevision(ctx context.Context, revisionID, from, to string) (sqlc.AgentDlvRevision, error) {
	var out sqlc.AgentDlvRevision
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		if _, err := getRevision(ctx, q, revisionID); err != nil {
			return err
		}
		rows, err := q.UpdateRevisionStatus(ctx, sqlc.UpdateRevisionStatusParams{
			ToStatus:   to,
			ID:         revisionID,
			FromStatus: from,
		})
		if err != nil {
			return fmt.Errorf("transition revision %s→%s: %w", from, to, err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		out, err = getRevision(ctx, q, revisionID)
		return err
	})
	return out, err
}

// RequestChanges sends an in_review revision back to draft, keeping content for
// re-submit (contract §2.3, in_review→draft).
func (s *DeliverableService) RequestChanges(ctx context.Context, revisionID, note string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.transitionRevision(ctx, revisionID, RevisionInReview, RevisionDraft)
}

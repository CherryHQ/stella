package goal

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DecompositionContent is a composite's proposed children + edges (contract
// §3.7). Stored inline on agent_goal.plan (jsonb). The materializer keys
// idempotency on (goal_id, child.Key).
type DecompositionContent struct {
	Children []ProposedChild `json:"children"`
	Edges    []ProposedEdge  `json:"edges"`
}

// ProposedChild is one child goal a plan proposes. Key is the stable id within
// the plan and the materialize idempotency key (replaces the old plan_item_id).
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

// ProposedEdge is one sibling dependency a plan proposes, by child Key.
type ProposedEdge struct {
	DownstreamKey string `json:"downstream_key"`
	UpstreamKey   string `json:"upstream_key"`
	Kind          string `json:"kind"`       // hard | soft
	OnFailure     string `json:"on_failure"` // block | fail | ignore
}

// maxDecompositionBreadth caps the children one decomposition may propose. A
// planner that emits thousands of children would fan out unbounded DB inserts,
// sessions, and memory; a single plan never
// legitimately needs more than this. Breadth-of-fanout per root is separately
// throttled at dispatch by ConvergencePolicy.MaxConcurrent.
const maxDecompositionBreadth = 64

// ValidateDecomposition checks a DecompositionContent's structural invariants
// (contract §6): ≥1 required child, at most maxDecompositionBreadth children,
// every child Key unique and non-empty, every edge key resolves to a child Key,
// no edge cycle (DFS over keys), known enum values, and a depth budget. A
// composite produces no executed output, so a composite child may not carry a
// deterministic acceptance item (its fold would stall forever) and must leave a
// level of depth under maxDepth for its own children — both are enforced here at
// the proposal boundary so a doomed plan is rejected before it consumes budget.
// Returns ErrInvalidDecomposition, ErrInvalidContract, ErrDepthExceeded, or
// ErrCycle.
//
// parentDepth is the composite's own depth; a child sits at parentDepth+1.
func ValidateDecomposition(c DecompositionContent, parentDepth, maxDepth int) error {
	return validateDecompositionError(c, parentDepth, maxDepth)
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

// ── Decomposition (contract §2.3). All writes via withTx. ────────────────────

// BeginDecomposition starts a composite's decomposition: draft→active, minting a
// purpose=decomposition attempt (contract §2.1). Guards kind=composite and not
// yet planned. The planning session is pre-minted outside the tx.
func (s *GoalService) BeginDecomposition(ctx context.Context, id string) (sqlc.AgentGoalAttempt, error) {
	return s.beginDecomposition(ctx, id, false, nil)
}

// BeginAutoDecomposition mints a headless decomposition attempt for a composite
// and enqueues its durable River job in ONE tx (mirrors Claim): on success both
// commit; on enqueue failure the whole thing rolls back, leaving the composite
// draft to be re-picked next tick — no orphaned active composite to recover.
// Unlike interactive BeginDecomposition the attempt carries a real claim-grace
// lease and is heartbeated by the River worker, so the reaper recovers it if the
// node crashes mid-plan. The dispatcher (scanAndDecompose) is the only caller.
//
// The eligibility guards are re-checked INSIDE the tx after LockGoalForWrite: the
// dispatcher's scan is a snapshot, so a concurrent Activate or a prior
// decomposition could have moved the goal between the scan and here. Re-checking
// under the row lock makes auto-decomposition never clobber an already-running or
// already-planned composite.
func (s *GoalService) BeginAutoDecomposition(ctx context.Context, id string, enqueue AttemptEnqueuer) (sqlc.AgentGoalAttempt, error) {
	return s.beginDecomposition(ctx, id, true, enqueue)
}

func (s *GoalService) beginDecomposition(ctx context.Context, id string, autonomous bool, enqueue AttemptEnqueuer) (sqlc.AgentGoalAttempt, error) {
	d, err := getGoal(ctx, s.q, id)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}
	if d.Kind != KindComposite || d.Lifecycle != LifecycleDraft || d.PlannedAt.Valid {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition
	}
	if s.newPlanningSession == nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("goal: no planning session minter configured")
	}
	sessionID, err := s.newPlanningSession(ctx, d.UserID, d.AgentID, d.ProjectID.String)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("mint planning session: %w", err)
	}
	lease := pgtype.Timestamptz{}
	if autonomous {
		lease = nullTime(s.nowTime().Add(claimGraceTTL))
	}
	out, err := s.beginAttempt(ctx, id, attemptSpec{
		purpose:       PurposeDecomposition,
		sessionID:     sessionID,
		executorAgent: d.AgentID,
		lease:         lease,
		enqueue:       enqueue,
		prepare: func(ctx context.Context, q *sqlc.Queries, cur sqlc.AgentGoal, attemptNo int) (AttemptInput, error) {
			if cur.Kind != KindComposite || cur.Lifecycle != LifecycleDraft || cur.PlannedAt.Valid {
				return AttemptInput{}, ErrInvalidTransition
			}
			timeline, err := s.recentTimelineContext(ctx, q, cur.ID)
			if err != nil {
				return AttemptInput{}, err
			}
			input := buildInputContext(cur, nil, nil, timeline, "", attemptNo)
			input.MaxDepth = rootMaxDepth(ctx, q, cur.RootID)
			return input, nil
		},
		transition: func(ctx context.Context, q *sqlc.Queries, cur sqlc.AgentGoal, att sqlc.AgentGoalAttempt) error {
			rows, err := s.transitionGoalLifecycle(ctx, q, cur, LifecycleActive, "")
			if err != nil {
				return fmt.Errorf("begin decomposition: %w", err)
			}
			if rows == 0 {
				return ErrInvalidTransition
			}
			return nil
		},
	})
	s.disposeOnRollback(ctx, err, d.UserID, d.AgentID, sessionID)
	return out, err
}

// validateContent runs the structural decomposition guards against a goal's
// own depth and the root's max_depth ceiling (contract §6). Shared by
// SubmitDecomposition and ApprovePlan so a malformed plan is rejected at the
// write boundary, not at materialize.
func (s *GoalService) validateContent(ctx context.Context, d sqlc.AgentGoal, content DecompositionContent) error {
	if err := ValidateDecomposition(content, int(d.Depth), rootMaxDepth(ctx, s.q, d.RootID)); err != nil {
		return err
	}
	if !s.canRunDeterministic() {
		for _, ch := range content.Children {
			kind := ch.Kind
			if kind == "" {
				kind = KindLeaf
			}
			if kind == KindLeaf && ch.AcceptanceContract.HasRequiredDeterministicItem() {
				return ErrDeterministicChecksUnsupported
			}
		}
	}
	return nil
}

// rootMaxDepth resolves the recursion ceiling from the root's convergence
// policy, falling back to the default if the root or its policy is unreadable.
// Shared by validateContent (the write-boundary guard) and the decomposition
// mint sites (which freeze it into AttemptInput for in-turn validation).
func rootMaxDepth(ctx context.Context, q *sqlc.Queries, rootID string) int {
	maxDepth := defaultMaxDepth
	if root, err := q.GetGoal(ctx, rootID); err == nil {
		var rp ConvergencePolicy
		if err := unmarshalJSON(root.ConvergencePolicy, &rp); err == nil {
			maxDepth = rp.Normalized().MaxDepth
		}
	}
	return maxDepth
}

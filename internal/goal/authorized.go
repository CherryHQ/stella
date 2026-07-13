package goal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Access is one Goal use case bound to exactly one Authorizer evaluation. The
// goal Service is the sole policy-enforcement point: transports and the agent
// tool pass a trusted authz.Authority and never a scoped query handle. A goal
// denial is opaque (authz.ErrNotFound) so a foreign goal cannot be told from a
// missing one; an agent-execute denial keeps its 403/404 visibility.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	authority authz.Authority
	userID    string
	// agentID is the executor confinement: empty for a plain user actor, the
	// bound agent for a delegated AgentActor.
	agentID string
}

// Begin opens exactly one evaluation for one Goal use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s.authz == nil || s.agents == nil {
		return nil, fmt.Errorf("goal authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("goal authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, authority: authority, userID: string(actor.UserID()), agentID: agentID}, nil
}

// GoalFilter narrows a root-goal list. The zero value lists active
// (non-archived) roots across all agents; populated fields AND together.
// Terminal is tri-state: nil = both, false = active only, true = history only.
type GoalFilter struct {
	AgentID    string
	Lifecycle  string
	ProjectID  string
	WorkflowID string
	Terminal   *bool
	Q          string
	Archived   bool
}

func (f GoalFilter) terminalArg() pgtype.Bool {
	if f.Terminal == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *f.Terminal, Valid: true}
}

// Get resolves a goal and authorizes reading it.
func (a *Access) Get(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	d, err := a.load(ctx, id)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.decide(authz.ActionRead, d); err != nil {
		return sqlc.AgentGoal{}, err
	}
	return d, nil
}

// Use resolves a goal, authorizes a state change on it, and confirms the caller
// may still execute its persisted agent — the pre-cutover loadGoalForUse gate.
func (a *Access) Use(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	d, err := a.load(ctx, id)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.decide(authz.ActionExecute, d); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.authorizeAgent(ctx, d.AgentID); err != nil {
		return sqlc.AgentGoal{}, err
	}
	return d, nil
}

// CreateGoal authorizes creating a goal (owner + agent execute) and mints it.
func (a *Access) CreateGoal(ctx context.Context, in CreateInput) (sqlc.AgentGoal, error) {
	if a.userID == "" {
		return sqlc.AgentGoal{}, authz.ErrUnauthenticated
	}
	if a.agentID != "" && a.agentID != in.AgentID {
		return sqlc.AgentGoal{}, authz.ErrForbidden
	}
	in.UserID = a.userID
	facts := policy.GoalFacts{
		Owner: a.userID, Agent: in.AgentID, State: "draft", IsOwner: true,
		IsExecutor: a.agentID != "" && a.agentID == in.AgentID,
	}
	req, err := policy.GoalRequest(authz.ActionCreate, a.userID, a.userID, facts)
	if err != nil {
		return sqlc.AgentGoal{}, authz.ErrForbidden
	}
	if err := a.decideReq(req); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.authorizeAgent(ctx, in.AgentID); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if in.IdempotencyKey != "" {
		existing, err := a.svc.Queries.GetGoalByIdempotencyKey(ctx, sqlc.GetGoalByIdempotencyKeyParams{UserID: a.userID, IdempotencyKey: pgnull.Text(in.IdempotencyKey)})
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AgentGoal{}, err
		}
	}
	return a.svc.Goal.CreateRoot(ctx, in)
}

// Cancel authorizes reading a goal and cancels it (cascade handled by the
// transition). Cancellation is a read-authorized owner action.
func (a *Access) Cancel(ctx context.Context, id, reason string) error {
	d, err := a.Get(ctx, id)
	if err != nil {
		return err
	}
	return a.svc.Goal.Cancel(ctx, id, reason, UserActor(d.UserID))
}

// ListGoals lists root goals in the caller's scope and filters every row through
// the same evaluation.
func (a *Access) ListGoals(ctx context.Context, filter GoalFilter, limit, offset int64) ([]sqlc.AgentGoal, error) {
	if err := a.decideList(); err != nil {
		return nil, err
	}
	agentID, err := a.scopeAgent(filter.AgentID)
	if err != nil {
		return nil, err
	}
	filter.AgentID = agentID
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.svc.Queries.ListRootGoal(ctx, sqlc.ListRootGoalParams{
		UserID:          a.userID,
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		WorkflowID:      pgnull.Text(filter.WorkflowID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.Archived,
		Limit:           int32(limit),
		Offset:          int32(offset),
	})
	if err != nil {
		return nil, err
	}
	return a.filterReadable(rows), nil
}

// CountGoals counts root goals in the caller's scope after a list decision.
func (a *Access) CountGoals(ctx context.Context, filter GoalFilter) (int64, error) {
	if err := a.decideList(); err != nil {
		return 0, err
	}
	agentID, err := a.scopeAgent(filter.AgentID)
	if err != nil {
		return 0, err
	}
	filter.AgentID = agentID
	return a.svc.Queries.CountRootGoal(ctx, sqlc.CountRootGoalParams{
		UserID:          a.userID,
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		WorkflowID:      pgnull.Text(filter.WorkflowID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.Archived,
	})
}

// ListChildren authorizes the parent goal, then lists its direct children.
func (a *Access) ListChildren(ctx context.Context, parentID string) ([]sqlc.AgentGoal, error) {
	if _, err := a.Get(ctx, parentID); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListGoalChildren(ctx, pgnull.Text(parentID))
}

// ListSubtree authorizes the root goal, then lists its whole tree.
func (a *Access) ListSubtree(ctx context.Context, rootID string) ([]sqlc.AgentGoal, error) {
	if _, err := a.Get(ctx, rootID); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListGoalByRoot(ctx, rootID)
}

// ListAttempts authorizes the goal, then lists its attempts.
func (a *Access) ListAttempts(ctx context.Context, id string) ([]sqlc.AgentGoalAttempt, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: id})
}

// GetAttempt authorizes the goal, then returns one attempt scoped to it.
func (a *Access) GetAttempt(ctx context.Context, id, attemptID string) (sqlc.AgentGoalAttempt, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}
	att, err := a.svc.Queries.GetAttempt(ctx, attemptID)
	if err != nil || att.GoalID != id {
		return sqlc.AgentGoalAttempt{}, authz.ErrNotFound
	}
	return att, nil
}

// ListAcceptanceEvents authorizes the goal, then lists its acceptance ledger.
func (a *Access) ListAcceptanceEvents(ctx context.Context, id string) ([]sqlc.AgentGoalAcceptanceEvent, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListAcceptanceEventByGoal(ctx, id)
}

// ListTimeline authorizes the goal, then returns a page of its L3 timeline.
func (a *Access) ListTimeline(ctx context.Context, id string, limit, offset int) ([]sqlc.AgentGoalEvent, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListGoalEventByGoal(ctx, sqlc.ListGoalEventByGoalParams{GoalID: id, Limit: int32(limit), Offset: int32(offset)})
}

// ListEdges authorizes the goal, then lists its upstream edges.
func (a *Access) ListEdges(ctx context.Context, id string) ([]sqlc.AgentGoalEdge, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListEdgeByGoal(ctx, id)
}

// Readiness authorizes the goal, then computes its dispatchability view.
func (a *Access) Readiness(ctx context.Context, id string, now time.Time) (Readiness, error) {
	d, err := a.Get(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	edges, err := a.svc.Queries.ListEdgeWithUpstreamState(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	return Compute(d, edges, now), nil
}

func (a *Access) load(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	d, err := getGoal(ctx, a.svc.Queries, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AgentGoal{}, authz.ErrNotFound
		}
		return sqlc.AgentGoal{}, err
	}
	return d, nil
}

func (a *Access) decide(action authz.Action, d sqlc.AgentGoal) error {
	facts := policy.GoalFacts{
		Owner: d.UserID, Agent: d.AgentID, State: d.Lifecycle,
		IsOwner:    a.userID != "" && a.userID == d.UserID,
		IsExecutor: a.agentID != "" && a.agentID == d.AgentID,
	}
	req, err := policy.GoalRequest(action, d.ID, d.UserID, facts)
	if err != nil {
		return authz.ErrForbidden
	}
	return a.decideReq(req)
}

func (a *Access) decideList() error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	req, err := policy.GoalListRequest()
	if err != nil {
		return authz.ErrForbidden
	}
	return a.decideReq(req)
}

func (a *Access) decideReq(req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("goal decide: %w", err)
	}
	if !dec.Allowed() {
		// Goals are opaque: a denial never reveals existence.
		return authz.ErrNotFound
	}
	return nil
}

// authorizeAgent folds the former requireAgentUse gate into this evaluation: a
// state change requires the caller may still execute the goal's persisted agent.
func (a *Access) authorizeAgent(ctx context.Context, agentID string) error {
	err := a.svc.agents.AuthorizeWithin(ctx, a.eval, a.authority, agentID, authz.ActionExecute)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentaccess.ErrNotFound):
		return authz.ErrNotFound
	case errors.Is(err, agentaccess.ErrForbidden):
		return authz.ErrForbidden
	default:
		return err
	}
}

// scopeAgent confines a delegated agent's list to its own agent; a user actor
// may filter by any agent it owns goals for.
func (a *Access) scopeAgent(requested string) (string, error) {
	if a.agentID == "" {
		return requested, nil
	}
	if requested != "" && requested != a.agentID {
		return "", authz.ErrForbidden
	}
	return a.agentID, nil
}

func (a *Access) filterReadable(rows []sqlc.AgentGoal) []sqlc.AgentGoal {
	out := rows[:0]
	for _, d := range rows {
		if err := a.decide(authz.ActionRead, d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

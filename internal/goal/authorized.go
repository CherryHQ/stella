package goal

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Authorized is the identity-scoped view of the service; all authorization
// checks live on its methods.
type Authorized struct {
	*Service
	ident authz.Identity
}

func (s *Service) As(ident authz.Identity) Authorized { return Authorized{Service: s, ident: ident} }

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

func (s Authorized) ListGoals(ctx context.Context, filter GoalFilter, limit, offset int64) ([]sqlc.AgentGoal, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return nil, err
	}
	agentID, err := ident.ResolveAgentScope(filter.AgentID)
	if err != nil {
		return nil, err
	}
	filter.AgentID = agentID
	if limit <= 0 {
		limit = 50
	}
	return s.Queries.ListRootGoal(ctx, sqlc.ListRootGoalParams{
		UserID:          ident.UserID,
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
}

func (s Authorized) GetGoal(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if ident.AgentScoped && ident.AgentID == "" {
		return sqlc.AgentGoal{}, authz.ErrForbidden
	}
	d, err := getGoal(ctx, s.Queries, id)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	if d.UserID != ident.UserID {
		return sqlc.AgentGoal{}, authz.ErrNotFound
	}
	if ident.AgentScoped && d.AgentID != ident.AgentID {
		return sqlc.AgentGoal{}, authz.ErrForbidden
	}
	return d, nil
}

func (s Authorized) CreateGoal(ctx context.Context, in CreateInput) (sqlc.AgentGoal, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := ident.RequireAgentMatch(in.AgentID); err != nil {
		return sqlc.AgentGoal{}, err
	}
	in.UserID = ident.UserID
	if in.IdempotencyKey != "" {
		existing, err := s.Queries.GetGoalByIdempotencyKey(ctx, sqlc.GetGoalByIdempotencyKeyParams{UserID: ident.UserID, IdempotencyKey: pgnull.Text(in.IdempotencyKey)})
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AgentGoal{}, err
		}
	}
	return s.Goal.CreateRoot(ctx, in)
}

func (s Authorized) Cancel(ctx context.Context, id, reason string) error {
	ident := s.ident
	if _, err := s.As(ident).GetGoal(ctx, id); err != nil {
		return err
	}
	return s.Goal.Cancel(ctx, id, reason, UserActor(ident.UserID))
}

func (s Authorized) ListChildren(ctx context.Context, parentID string) ([]sqlc.AgentGoal, error) {
	ident := s.ident
	if _, err := s.As(ident).GetGoal(ctx, parentID); err != nil {
		return nil, err
	}
	return s.Queries.ListGoalChildren(ctx, pgnull.Text(parentID))
}

func (s Authorized) ListSubtree(ctx context.Context, rootID string) ([]sqlc.AgentGoal, error) {
	ident := s.ident
	if _, err := s.As(ident).GetGoal(ctx, rootID); err != nil {
		return nil, err
	}
	return s.Queries.ListGoalByRoot(ctx, rootID)
}

func (s Authorized) CountGoals(ctx context.Context, filter GoalFilter) (int64, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return 0, err
	}
	agentID, err := ident.ResolveAgentScope(filter.AgentID)
	if err != nil {
		return 0, err
	}
	filter.AgentID = agentID
	return s.Queries.CountRootGoal(ctx, sqlc.CountRootGoalParams{
		UserID:          ident.UserID,
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		WorkflowID:      pgnull.Text(filter.WorkflowID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.Archived,
	})
}

package goal

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/toolctx"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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

func (s *Service) ListGoalsOwned(ctx context.Context, ident toolctx.Identity, filter GoalFilter, limit, offset int64) ([]sqlc.AgentGoal, error) {
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

func (s *Service) GetGoalOwned(ctx context.Context, ident toolctx.Identity, id string) (sqlc.AgentGoal, error) {
	if err := ident.RequireUser(); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if ident.AgentScoped && ident.AgentID == "" {
		return sqlc.AgentGoal{}, toolctx.ErrForbidden
	}
	d, err := getGoal(ctx, s.Queries, id)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	if d.UserID != ident.UserID {
		return sqlc.AgentGoal{}, toolctx.ErrNotFound
	}
	if ident.AgentScoped && d.AgentID != ident.AgentID {
		return sqlc.AgentGoal{}, toolctx.ErrForbidden
	}
	return d, nil
}

func (s *Service) CreateGoalOwned(ctx context.Context, ident toolctx.Identity, in CreateInput) (sqlc.AgentGoal, error) {
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

func (s *Service) CancelOwned(ctx context.Context, ident toolctx.Identity, id, reason string) error {
	if _, err := s.GetGoalOwned(ctx, ident, id); err != nil {
		return err
	}
	return s.Goal.Cancel(ctx, id, reason, UserActor(ident.UserID))
}

func (s *Service) ListChildrenOwned(ctx context.Context, ident toolctx.Identity, parentID string) ([]sqlc.AgentGoal, error) {
	if _, err := s.GetGoalOwned(ctx, ident, parentID); err != nil {
		return nil, err
	}
	return s.Queries.ListGoalChildren(ctx, pgnull.Text(parentID))
}

func (s *Service) ListSubtreeOwned(ctx context.Context, ident toolctx.Identity, rootID string) ([]sqlc.AgentGoal, error) {
	if _, err := s.GetGoalOwned(ctx, ident, rootID); err != nil {
		return nil, err
	}
	return s.Queries.ListGoalByRoot(ctx, rootID)
}

func (s *Service) CountGoalsOwned(ctx context.Context, ident toolctx.Identity, filter GoalFilter) (int64, error) {
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

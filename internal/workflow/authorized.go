package workflow

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Authorized is the identity-scoped view of the workflow service; all tool
// access flows through this facade so agent sessions cannot widen scope.
type Authorized struct {
	*Service
	ident authz.Identity
}

func (s *Service) As(ident authz.Identity) Authorized { return Authorized{Service: s, ident: ident} }

func (s Authorized) List(ctx context.Context) ([]sqlc.AgentWorkflow, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return nil, err
	}
	agentID, err := ident.ResolveAgentScope("")
	if err != nil {
		return nil, err
	}
	return s.Service.List(ctx, ident.UserID, agentID)
}

func (s Authorized) Get(ctx context.Context, id string) (sqlc.AgentWorkflow, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	agentID, err := ident.ResolveAgentScope("")
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	row, err := s.Service.Get(ctx, ident.UserID, agentID, id)
	return row, mapAuthzWorkflowError(err)
}

func (s Authorized) SaveGoalAsWorkflow(ctx context.Context, in SaveInput) (sqlc.AgentWorkflow, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	agentID, err := ident.ResolveAgentScope(in.AgentID)
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	in.UserID = ident.UserID
	in.AgentID = agentID
	row, err := s.Service.SaveGoalAsWorkflow(ctx, in)
	return row, mapAuthzWorkflowError(err)
}

func (s Authorized) Instantiate(ctx context.Context, in InstantiateInput) (sqlc.AgentWorkflowRun, bool, error) {
	ident := s.ident
	if err := ident.RequireUser(); err != nil {
		return sqlc.AgentWorkflowRun{}, false, err
	}
	agentID, err := ident.ResolveAgentScope(in.AgentID)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, err
	}
	in.UserID = ident.UserID
	in.AgentID = agentID
	run, created, err := s.Service.Instantiate(ctx, in)
	return run, created, mapAuthzWorkflowError(err)
}

func mapAuthzWorkflowError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return authz.ErrNotFound
	}
	return err
}

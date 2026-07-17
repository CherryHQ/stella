package workflow

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Errors returned by the Workflow access boundary. Denials are opaque
// (ErrNotFound) so a foreign or revoked workflow cannot be distinguished from a
// missing one, preserving the pre-cutover 404 contract.
var (
	ErrForbidden   = errors.New("workflow access forbidden")
	ErrNotFound    = errors.New("workflow not found")
	ErrUnavailable = errors.New("workflow authorization unavailable")
	ErrInvalidPage = errors.New("workflow pagination is outside the supported range")
)

// Access captures one validated authority for a Workflow use case. Workflow owns
// the direct rules; transports pass a trusted Authority, never a scoped query.
type Access struct {
	svc       *Service
	authority authz.Authority
	userID    string
	// agentID is the executor scope: empty for a plain user actor, the bound
	// agent for a delegated AgentActor.
	agentID string
}

// Begin captures validated authority for one Workflow use case.
func (s *Service) Begin(_ context.Context, authority authz.Authority) (*Access, error) {
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	return s.access(authority), nil
}

func (s *Service) access(authority authz.Authority) *Access {
	executor := ""
	if authority.Kind() == authz.ActorAgent {
		executor = string(authority.AgentID())
	}
	return &Access{svc: s, authority: authority, userID: string(authority.UserID()), agentID: executor}
}

// InstantiateAs authorizes and instantiates a workflow under authority. It is the
// narrow cross-domain entry point used by scheduler dispatch; request fields never
// establish ownership or the executor.
func (s *Service) InstantiateAs(ctx context.Context, authority authz.Authority, id string, inputs map[string]string, idempotencyKey string) (Run, bool, error) {
	if !authority.Valid() {
		return Run{}, false, ErrForbidden
	}
	return s.access(authority).Instantiate(ctx, id, inputs, idempotencyKey)
}

// List lists the caller's workflows and filters every durable row through the
// same direct read rule. A user actor may narrow the query to one agent; a
// delegated agent is always confined to its own bound agent regardless of the
// filter. The query remains owner-scoped even for admins by established contract.
func (a *Access) List(ctx context.Context, agentFilter string) ([]Workflow, error) {
	if err := a.authorize(authz.ActionList, sqlc.AgentWorkflow{}); err != nil {
		return nil, err
	}
	scope := a.agentID
	if scope == "" {
		scope = agentFilter
	}
	_, ownerUser, ownerAgent := ownerScope(a.userID, scope)
	rows, err := a.svc.q.ListWorkflows(ctx, sqlc.ListWorkflowsParams{UserID: ownerUser, AgentID: ownerAgent})
	if err != nil {
		return nil, fmt.Errorf("%w: list workflows: %w", ErrUnavailable, err)
	}
	out := rows[:0]
	for _, wf := range rows {
		if a.allowed(authz.ActionRead, wf) {
			out = append(out, wf)
		}
	}
	return workflowsFromRows(out), nil
}

// Get authorizes reading one workflow.
func (a *Access) Get(ctx context.Context, id string) (Workflow, error) {
	wf, err := a.load(ctx, id)
	if err != nil {
		return Workflow{}, err
	}
	if err := a.authorize(authz.ActionRead, wf); err != nil {
		return Workflow{}, err
	}
	return workflowFromRow(wf), nil
}

// ListRuns authorizes reading a workflow, then returns its run page.
func (a *Access) ListRuns(ctx context.Context, id string, limit, offset int) ([]RunListItem, int64, error) {
	if limit < 1 || limit > math.MaxInt32 || offset < 0 || int64(offset) > int64(math.MaxInt32)-int64(limit) {
		return nil, 0, ErrInvalidPage
	}
	wf, err := a.load(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	if err := a.authorize(authz.ActionRead, wf); err != nil {
		return nil, 0, err
	}
	total, err := a.svc.q.CountWorkflowRuns(ctx, id)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: count workflow runs: %w", ErrUnavailable, err)
	}
	rows, err := a.svc.q.ListWorkflowRuns(ctx, sqlc.ListWorkflowRunsParams{WorkflowID: id, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("%w: list workflow runs: %w", ErrUnavailable, err)
	}
	return runListItemsFromRows(rows), total, nil
}

// Delete authorizes deleting a workflow, then removes it (blocked when runs or
// enabled scheduler jobs reference it — those domain errors surface unchanged).
func (a *Access) Delete(ctx context.Context, id string) error {
	wf, err := a.load(ctx, id)
	if err != nil {
		return err
	}
	if err := a.authorize(authz.ActionDelete, wf); err != nil {
		return err
	}
	return a.svc.deleteLoaded(ctx, wf.ID)
}

// SaveGoalAsWorkflow freezes an accepted goal tree into a workflow. The source
// Goal owns its read decision; its persisted agent determines the workflow's
// target, and Agent owns the target execute decision.
func (a *Access) SaveGoalAsWorkflow(ctx context.Context, in SaveInput) (Workflow, error) {
	if a.svc.agents == nil {
		return Workflow{}, fmt.Errorf("%w: agent authorization is not configured", ErrUnavailable)
	}
	if a.userID == "" {
		return Workflow{}, ErrForbidden
	}
	if a.svc.goal == nil {
		return Workflow{}, fmt.Errorf("%w: goal authorization is not configured", ErrUnavailable)
	}
	// The source goal's durable identity is authority-bearing state. Do not trust
	// request ownership or target-agent fields.
	authorized, err := a.svc.goal.Authorize(ctx, a.authority, in.GoalID, authz.ActionRead)
	if err != nil {
		return Workflow{}, mapGoalAuthzError(err)
	}
	in.UserID = authorized.UserID
	in.AgentID = authorized.AgentID
	if a.agentID != "" && a.agentID != in.AgentID {
		return Workflow{}, ErrNotFound
	}
	if err := a.authorize(authz.ActionCreate, sqlc.AgentWorkflow{
		UserID:  pgtype.Text{String: a.userID, Valid: a.userID != ""},
		AgentID: pgtype.Text{String: in.AgentID, Valid: in.AgentID != ""},
	}); err != nil {
		return Workflow{}, err
	}
	if in.AgentID != "" {
		if err := a.svc.agents.Authorize(ctx, a.authority, in.AgentID, authz.ActionExecute); err != nil {
			return Workflow{}, mapAgentAuthzError(err)
		}
	}
	row, err := a.svc.saveGoalAsWorkflow(ctx, in)
	if err != nil {
		return Workflow{}, mapDomainError(err)
	}
	return workflowFromRow(row), nil
}

// Instantiate claims a run and materializes the workflow's goal tree. It checks
// both the loaded durable workflow and its persisted target agent before claim or
// materialization; neither request fields nor a stale route can widen access.
func (a *Access) Instantiate(ctx context.Context, id string, inputs map[string]string, idempotencyKey string) (Run, bool, error) {
	if a.svc.agents == nil {
		return Run{}, false, fmt.Errorf("%w: agent authorization is not configured", ErrUnavailable)
	}
	wf, err := a.load(ctx, id)
	if err != nil {
		return Run{}, false, err
	}
	if err := a.authorize(authz.ActionExecute, wf); err != nil {
		return Run{}, false, err
	}
	if !wf.AgentID.Valid || wf.AgentID.String == "" {
		return Run{}, false, ErrNotFound
	}
	if err := a.svc.agents.Authorize(ctx, a.authority, wf.AgentID.String, authz.ActionExecute); err != nil {
		return Run{}, false, mapAgentAuthzError(err)
	}
	// The raw materializer still scopes its lookup. It must receive the loaded
	// workflow owner, not the caller: an admin may execute another owner's
	// workflow after this direct check, and roots belong to that durable owner.
	run, created, err := a.svc.instantiate(ctx, InstantiateInput{UserID: wf.UserID.String, AgentID: a.agentID, WorkflowID: id, Inputs: inputs, IdempotencyKey: idempotencyKey})
	if err != nil {
		return Run{}, false, mapDomainError(err)
	}
	return runFromRow(run), created, nil
}

func (a *Access) load(ctx context.Context, id string) (sqlc.AgentWorkflow, error) {
	wf, err := a.svc.q.GetWorkflow(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AgentWorkflow{}, ErrNotFound
		}
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: get workflow: %w", ErrUnavailable, err)
	}
	return wf, nil
}

// authorize applies Workflow's fixed rules only to a loaded durable row. Denials
// are opaque so callers cannot distinguish a forbidden workflow from a missing one.
func (a *Access) authorize(action authz.Action, wf sqlc.AgentWorkflow) error {
	if a.allowed(action, wf) {
		return nil
	}
	return ErrNotFound
}

func (a *Access) allowed(action authz.Action, wf sqlc.AgentWorkflow) bool {
	if !action.Valid() {
		return false
	}
	if a.authority.IsAdmin() {
		return true
	}
	if !isWorkflowAction(action) {
		return false
	}
	switch a.authority.Kind() {
	case authz.ActorUser:
		if action == authz.ActionList {
			return true
		}
		return a.userID != "" && wf.UserID.Valid && a.userID == wf.UserID.String
	case authz.ActorAgent:
		if action == authz.ActionList {
			return true
		}
		switch action {
		case authz.ActionCreate, authz.ActionRead, authz.ActionExecute:
			return a.userID != "" && wf.UserID.Valid && a.userID == wf.UserID.String && a.agentID != "" && wf.AgentID.Valid && a.agentID == wf.AgentID.String
		default:
			return false
		}
	default:
		return false
	}
}

func isWorkflowAction(action authz.Action) bool {
	switch action {
	case authz.ActionList, authz.ActionCreate, authz.ActionRead, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute:
		return true
	default:
		return false
	}
}

// mapDomainError preserves the workflow domain's own typed errors (conflicts,
// validation) and maps a scoped miss to ErrNotFound.
func mapDomainError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// mapAgentAuthzError folds an Agent denial into Workflow's opaque contract.
func mapAgentAuthzError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentaccess.ErrNotFound), errors.Is(err, agentaccess.ErrForbidden):
		return ErrNotFound
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

// mapGoalAuthzError folds Goal's direct authorization result into Workflow's
// opaque contract: a foreign, revoked, or missing source goal is indistinguishable
// from a missing workflow.
func mapGoalAuthzError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrNotFound), errors.Is(err, authz.ErrForbidden):
		return ErrNotFound
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

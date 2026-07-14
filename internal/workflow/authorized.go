package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Errors returned by the Workflow policy-enforcement point. Denials are opaque
// (ErrNotFound) so a foreign or revoked workflow cannot be distinguished from a
// missing one, preserving the pre-cutover 404 contract.
var (
	ErrForbidden   = errors.New("workflow access forbidden")
	ErrNotFound    = errors.New("workflow not found")
	ErrUnavailable = errors.New("workflow authorization unavailable")
)

// Access is one Workflow use case bound to exactly one Authorizer evaluation.
// Do not retain it across use cases.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	authority authz.Authority
	userID    string
	// agentID is the executor scope: empty for a plain user actor (all their
	// workflows), the bound agent for a delegated AgentActor.
	agentID string
}

// Begin opens exactly one evaluation for one Workflow use case. The Workflow
// service is the sole policy-enforcement point; transports pass a trusted
// Authority and never a scoped query handle.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s.authz == nil {
		return nil, fmt.Errorf("%w: authorizer not configured", ErrUnavailable)
	}
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("%w: begin: %w", ErrUnavailable, err)
	}
	return s.accessWithin(eval, authority), nil
}

func (s *Service) accessWithin(eval authz.Evaluation, authority authz.Authority) *Access {
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, authority: authority, userID: string(actor.UserID()), agentID: agentID}
}

// InstantiateWithin executes the Workflow use case inside a caller's already-open
// evaluation. Scheduler durable fire uses it so Job, Workflow, and Agent decisions
// are pinned to one policy revision.
func (s *Service) InstantiateWithin(ctx context.Context, eval authz.Evaluation, authority authz.Authority, id string, inputs map[string]string, idempotencyKey string) (sqlc.AgentWorkflowRun, bool, error) {
	if eval == nil || !authority.Valid() {
		return sqlc.AgentWorkflowRun{}, false, ErrForbidden
	}
	return s.accessWithin(eval, authority).Instantiate(ctx, id, inputs, idempotencyKey)
}

// List lists the caller's workflows and filters every row through the same
// evaluation, so collection and individual visibility cannot drift apart. A
// user actor may narrow the query to one agent (agentFilter); a delegated agent
// is always confined to its own bound agent regardless of the filter.
func (a *Access) List(ctx context.Context, agentFilter string) ([]sqlc.AgentWorkflow, error) {
	req, err := policy.WorkflowListRequest()
	if err != nil {
		return nil, ErrForbidden
	}
	if err := a.decide(req); err != nil {
		return nil, err
	}
	scope := a.agentID
	if scope == "" {
		scope = agentFilter
	}
	rows, err := a.svc.List(ctx, a.userID, scope)
	if err != nil {
		return nil, fmt.Errorf("%w: list workflows: %w", ErrUnavailable, err)
	}
	out := rows[:0]
	for _, wf := range rows {
		req, err := policy.WorkflowRequest(authz.ActionRead, wf.ID, wf.UserID.String, a.factsFor(wf))
		if err != nil {
			return nil, ErrForbidden
		}
		if err := a.decide(req); err == nil {
			out = append(out, wf)
		} else if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return out, nil
}

// Get authorizes reading one workflow.
func (a *Access) Get(ctx context.Context, id string) (sqlc.AgentWorkflow, error) {
	wf, err := a.load(ctx, id)
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	if err := a.authorize(authz.ActionRead, wf); err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	return wf, nil
}

// ListRuns authorizes reading a workflow, then returns its run page.
func (a *Access) ListRuns(ctx context.Context, id string, limit, offset int32) ([]sqlc.ListWorkflowRunsRow, int64, error) {
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
	rows, err := a.svc.q.ListWorkflowRuns(ctx, sqlc.ListWorkflowRunsParams{WorkflowID: id, Limit: limit, Offset: offset})
	if err != nil {
		return nil, 0, fmt.Errorf("%w: list workflow runs: %w", ErrUnavailable, err)
	}
	return rows, total, nil
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

// SaveGoalAsWorkflow freezes an accepted goal tree into a workflow. It requires
// workflow-create authority plus Agent-domain execute authority on the workflow's
// bound agent.
func (a *Access) SaveGoalAsWorkflow(ctx context.Context, in SaveInput) (sqlc.AgentWorkflow, error) {
	if a.svc.agents == nil {
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: agent authorization is not configured", ErrUnavailable)
	}
	if a.userID == "" {
		return sqlc.AgentWorkflow{}, ErrForbidden
	}
	in.UserID = a.userID
	// Goal owns the source-read decision directly. The workflow binds to the
	// goal's persisted agent — never a caller-supplied one — and the raw service
	// re-verifies goal ownership/agent so it cannot be spoofed.
	if a.svc.goal == nil {
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: goal authorization is not configured", ErrUnavailable)
	}
	goalRow, err := a.svc.goal.Authorize(ctx, a.authority, in.GoalID, authz.ActionRead)
	if err != nil {
		return sqlc.AgentWorkflow{}, mapGoalAuthzError(err)
	}
	in.AgentID = goalRow.AgentID
	// A delegated agent may only turn its own agent's goals into workflows.
	if a.agentID != "" && a.agentID != in.AgentID {
		return sqlc.AgentWorkflow{}, ErrNotFound
	}
	facts := policy.WorkflowFacts{
		Owner: a.userID, Agent: in.AgentID, IsOwner: true,
		IsExecutor: a.agentID != "" && a.agentID == in.AgentID,
	}
	req, err := policy.WorkflowRequest(authz.ActionCreate, a.userID, a.userID, facts)
	if err != nil {
		return sqlc.AgentWorkflow{}, ErrForbidden
	}
	if err := a.decide(req); err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	// The workflow will execute under in.AgentID; require execute authority on it.
	if in.AgentID != "" {
		if err := a.svc.agents.Authorize(ctx, a.authority, in.AgentID, authz.ActionExecute); err != nil {
			return sqlc.AgentWorkflow{}, mapAgentAuthzError(err)
		}
	}
	row, err := a.svc.SaveGoalAsWorkflow(ctx, in)
	if err != nil {
		return sqlc.AgentWorkflow{}, mapDomainError(err)
	}
	return row, nil
}

// Instantiate claims a run and materializes the workflow's goal tree. The run
// executes under the workflow's own persisted agent; the caller must own the
// workflow (execute) and be able to execute that agent, both decided here.
func (a *Access) Instantiate(ctx context.Context, id string, inputs map[string]string, idempotencyKey string) (sqlc.AgentWorkflowRun, bool, error) {
	if a.svc.agents == nil {
		return sqlc.AgentWorkflowRun{}, false, fmt.Errorf("%w: agent authorization is not configured", ErrUnavailable)
	}
	wf, err := a.load(ctx, id)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, err
	}
	if err := a.authorize(authz.ActionExecute, wf); err != nil {
		return sqlc.AgentWorkflowRun{}, false, err
	}
	if !wf.AgentID.Valid || wf.AgentID.String == "" {
		return sqlc.AgentWorkflowRun{}, false, ErrNotFound
	}
	// The workflow's bound agent is persisted authority-bearing state, not a route
	// parameter; execute authority on it is required before claiming a run.
	if err := a.svc.agents.Authorize(ctx, a.authority, wf.AgentID.String, authz.ActionExecute); err != nil {
		return sqlc.AgentWorkflowRun{}, false, mapAgentAuthzError(err)
	}
	run, created, err := a.svc.Instantiate(ctx, InstantiateInput{UserID: a.userID, AgentID: a.agentID, WorkflowID: id, Inputs: inputs, IdempotencyKey: idempotencyKey})
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, mapDomainError(err)
	}
	return run, created, nil
}

// load fetches a workflow unscoped; the policy decision, not the query, is the
// authority. A missing row is opaque (ErrNotFound).
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

func (a *Access) authorize(action authz.Action, wf sqlc.AgentWorkflow) error {
	req, err := policy.WorkflowRequest(action, wf.ID, wf.UserID.String, a.factsFor(wf))
	if err != nil {
		return ErrForbidden
	}
	return a.decide(req)
}

func (a *Access) factsFor(wf sqlc.AgentWorkflow) policy.WorkflowFacts {
	return policy.WorkflowFacts{
		Owner:      wf.UserID.String,
		Agent:      wf.AgentID.String,
		IsOwner:    a.userID != "" && a.userID == wf.UserID.String,
		IsExecutor: a.agentID != "" && a.agentID == wf.AgentID.String,
	}
}

func (a *Access) decide(req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("%w: decide: %w", ErrUnavailable, err)
	}
	if !dec.Allowed() {
		// Workflows are opaque: a denial never reveals existence.
		return ErrNotFound
	}
	return nil
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

// mapAgentAuthzError folds an agentaccess denial into the workflow-opaque
// contract without leaking the agent layer's sentinels to transports.
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

// mapGoalAuthzError folds the Goal-owned direct authorization denial (authz sentinels)
// into the workflow-opaque contract: a foreign, revoked, or missing source goal
// is indistinguishable from a missing workflow (ErrNotFound).
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

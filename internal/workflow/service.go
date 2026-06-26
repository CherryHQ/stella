// Package workflow is the workflow entity: a goal whose decomposition plan was
// frozen from a once-accepted composite, so a later run skips the planner and
// materializes a deterministic subtree (issue #594). It owns the agent_workflow
// table and the three paths — save-as (freeze an accepted composite), create
// (hand-author a plan), instantiate (run a workflow into a live goal tree). The
// recursive tree mechanics live in internal/goal (FrozenPlan / SnapshotFrozenPlan
// / InstantiateFrozen); this package drives them and persists the entity.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Typed sentinel errors. Callers branch via errors.Is.
var (
	// ErrNotFound is returned when the workflow (or, on save-as, the source goal)
	// does not exist or is not owned by the caller.
	ErrNotFound = errors.New("workflow: not found")

	// ErrSourceNotEligible is returned by SaveGoalAsWorkflow when the source goal
	// is not an accepted composite — only a once-accepted decomposition freezes.
	ErrSourceNotEligible = errors.New("workflow: source goal is not an accepted composite")

	// ErrInvalidInput is returned when a create/save request is structurally
	// invalid (empty name, malformed plan, etc.).
	ErrInvalidInput = errors.New("workflow: invalid input")
)

// GoalTrees is the slice of the goal subsystem the workflow package drives. The
// boot-level *goal.Service satisfies it.
type GoalTrees interface {
	GetGoal(ctx context.Context, id string) (sqlc.AgentGoal, error)
	SnapshotFrozenPlan(ctx context.Context, goalID string) (goal.FrozenPlan, error)
	InstantiateFrozen(ctx context.Context, spec goal.FrozenRootSpec, plan goal.FrozenPlan) (sqlc.AgentGoal, error)
}

// Service is the workflow command + read surface bound by the server and CLI.
type Service struct {
	q     *sqlc.Queries
	trees GoalTrees
}

// New builds a workflow Service over the given querier and goal subsystem.
func New(q *sqlc.Queries, trees GoalTrees) *Service {
	return &Service{q: q, trees: trees}
}

// CreateInput hand-authors a workflow from an explicit frozen plan (the strict
// path: the plan is validated structurally before it is stored).
type CreateInput struct {
	AgentID     string
	Name        string
	Intent      string
	Contract    goal.AcceptanceContract
	Convergence goal.ConvergencePolicy
	Plan        goal.FrozenPlan
}

// Create stores a hand-authored workflow after strict validation of its plan.
func (s *Service) Create(ctx context.Context, userID string, in CreateInput) (sqlc.AgentWorkflow, error) {
	if in.Name == "" || in.AgentID == "" {
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: name and agent_id are required", ErrInvalidInput)
	}
	if in.Contract.HasDeterministicItem() {
		// A workflow root is a composite; a deterministic check on it has no output
		// source (mirrors goal's ErrCompositeDeterministicContract).
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: root contract cannot be deterministic", ErrInvalidInput)
	}
	if !in.Contract.Valid() || !in.Convergence.Valid() {
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: invalid contract or policy", ErrInvalidInput)
	}
	convergence := in.Convergence.Normalized()
	if err := goal.ValidateFrozenPlan(in.Plan, 0, convergence.MaxDepth); err != nil {
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return s.q.CreateWorkflow(ctx, sqlc.CreateWorkflowParams{
		OwnerKind:          ownerUser,
		UserID:             pgnull.Text(userID),
		AgentID:            pgnull.Text(in.AgentID),
		Name:               in.Name,
		Intent:             in.Intent,
		AcceptanceContract: marshal(in.Contract),
		ConvergencePolicy:  marshal(convergence),
		Plan:               marshal(in.Plan),
		Version:            1,
		SourceGoalID:       pgnull.Text(""),
	})
}

// SaveAsInput freezes an accepted composite goal into a reusable workflow.
type SaveAsInput struct {
	SourceGoalID string
	Name         string // defaults to the source goal's title when empty
}

// SaveGoalAsWorkflow freezes a once-accepted composite goal into a workflow. The
// source must be an accepted composite owned by the caller; its subtree is
// snapshotted into a FrozenPlan (reproducible spec, not result) and validated as
// instantiable before it is stored.
func (s *Service) SaveGoalAsWorkflow(ctx context.Context, userID string, in SaveAsInput) (sqlc.AgentWorkflow, error) {
	g, err := s.trees.GetGoal(ctx, in.SourceGoalID)
	if err != nil {
		if errors.Is(err, goal.ErrNotFound) {
			return sqlc.AgentWorkflow{}, ErrNotFound
		}
		return sqlc.AgentWorkflow{}, err
	}
	if g.UserID != userID {
		return sqlc.AgentWorkflow{}, ErrNotFound // don't leak existence across owners
	}
	if g.Kind != goal.KindComposite || g.Lifecycle != goal.LifecycleAccepted {
		return sqlc.AgentWorkflow{}, ErrSourceNotEligible
	}

	plan, err := s.trees.SnapshotFrozenPlan(ctx, g.ID)
	if err != nil {
		return sqlc.AgentWorkflow{}, mapGoalErr(err)
	}
	var convergence goal.ConvergencePolicy
	_ = json.Unmarshal(g.ConvergencePolicy, &convergence)
	convergence = convergence.Normalized()
	if err := goal.ValidateFrozenPlan(plan, 0, convergence.MaxDepth); err != nil {
		return sqlc.AgentWorkflow{}, fmt.Errorf("%w: frozen plan not instantiable: %w", ErrInvalidInput, err)
	}

	name := in.Name
	if name == "" {
		name = g.Title
	}
	return s.q.CreateWorkflow(ctx, sqlc.CreateWorkflowParams{
		OwnerKind:          ownerUser,
		UserID:             pgnull.Text(userID),
		AgentID:            pgnull.Text(g.AgentID),
		Name:               name,
		Intent:             g.Intent,
		AcceptanceContract: g.AcceptanceContract,
		ConvergencePolicy:  marshal(convergence),
		Plan:               marshal(plan),
		Version:            1,
		SourceGoalID:       pgnull.Text(g.ID),
	})
}

// Instantiate materializes a workflow into a live goal subtree, skipping the
// planner. The root's context records {workflow_id, version, plan_hash} so a run
// is traceable back to the exact spec it materialized. projectID scopes the new
// tree (empty for none).
func (s *Service) Instantiate(ctx context.Context, userID, workflowID, projectID string) (sqlc.AgentGoal, error) {
	wf, err := s.get(ctx, userID, workflowID)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	var plan goal.FrozenPlan
	if err := json.Unmarshal(wf.Plan, &plan); err != nil {
		return sqlc.AgentGoal{}, fmt.Errorf("%w: stored plan: %w", ErrInvalidInput, err)
	}
	var contract goal.AcceptanceContract
	_ = json.Unmarshal(wf.AcceptanceContract, &contract)
	var convergence goal.ConvergencePolicy
	_ = json.Unmarshal(wf.ConvergencePolicy, &convergence)

	rootCtx := marshal(map[string]any{
		"workflow_id": wf.ID,
		"version":     wf.Version,
		"plan_hash":   plan.Hash(),
	})
	root, err := s.trees.InstantiateFrozen(ctx, goal.FrozenRootSpec{
		UserID:      userID,
		AgentID:     wf.AgentID.String,
		ProjectID:   projectID,
		Title:       wf.Name,
		Intent:      wf.Intent,
		Contract:    contract,
		Convergence: convergence,
		Context:     rootCtx,
	}, plan)
	if err != nil {
		return sqlc.AgentGoal{}, mapGoalErr(err)
	}
	return root, nil
}

// ── Read + lifecycle surface ────────────────────────────────────────────────

// Filter narrows a workflow list. Empty strings match all rows.
type Filter struct {
	AgentID string
	Q       string
}

// List returns a user's workflows, newest first.
func (s *Service) List(ctx context.Context, userID string, filter Filter, limit, offset int64) ([]sqlc.AgentWorkflow, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.q.ListWorkflowsByUser(ctx, sqlc.ListWorkflowsByUserParams{
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(filter.AgentID),
		Q:       pgnull.Text(filter.Q),
		Limit:   int32(limit),
		Offset:  int32(offset),
	})
}

// Count returns the total workflows matching the same filter as List.
func (s *Service) Count(ctx context.Context, userID string, filter Filter) (int64, error) {
	return s.q.CountWorkflowsByUser(ctx, sqlc.CountWorkflowsByUserParams{
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(filter.AgentID),
		Q:       pgnull.Text(filter.Q),
	})
}

// Get returns one workflow owned by the caller.
func (s *Service) Get(ctx context.Context, userID, id string) (sqlc.AgentWorkflow, error) {
	return s.get(ctx, userID, id)
}

// UpdateMeta edits a workflow's name/intent (its plan is immutable — re-save to
// change the steps). Empty fields fall back to the current values.
func (s *Service) UpdateMeta(ctx context.Context, userID, id, name, intent string) (sqlc.AgentWorkflow, error) {
	wf, err := s.get(ctx, userID, id)
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	if name == "" {
		name = wf.Name
	}
	if intent == "" {
		intent = wf.Intent
	}
	return s.q.UpdateWorkflowMeta(ctx, sqlc.UpdateWorkflowMetaParams{Name: name, Intent: intent, ID: id, UserID: pgnull.Text(userID)})
}

// Delete removes a workflow owned by the caller.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if _, err := s.get(ctx, userID, id); err != nil {
		return err
	}
	rows, err := s.q.DeleteWorkflow(ctx, sqlc.DeleteWorkflowParams{ID: id, UserID: pgnull.Text(userID)})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// get loads a workflow scoped to the caller. The query filters by user_id, so a
// missing row and a cross-owner read both surface as ErrNotFound (no existence
// leak); isolation is enforced in the data layer, not just here.
func (s *Service) get(ctx context.Context, userID, id string) (sqlc.AgentWorkflow, error) {
	wf, err := s.q.GetWorkflow(ctx, sqlc.GetWorkflowParams{ID: id, UserID: pgnull.Text(userID)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AgentWorkflow{}, ErrNotFound
		}
		return sqlc.AgentWorkflow{}, err
	}
	return wf, nil
}

const ownerUser = "user"

// mapGoalErr seals the goal subsystem's error vocabulary at this boundary so the
// HTTP layer only ever sees workflow sentinels: goal validation sentinels become
// ErrInvalidInput (400) and a missing goal becomes ErrNotFound (404). Other
// errors pass through as internal failures.
func mapGoalErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, goal.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, goal.ErrInvalidDecomposition),
		errors.Is(err, goal.ErrInvalidContract),
		errors.Is(err, goal.ErrCompositeDeterministicContract),
		errors.Is(err, goal.ErrDepthExceeded),
		errors.Is(err, goal.ErrCycle):
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	default:
		return err
	}
}

// marshal encodes a value to json.RawMessage; an encode error degrades to "{}"
// (the same discipline as the goal package's marshalJSON).
func marshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

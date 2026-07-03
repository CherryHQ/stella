package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	OwnerUser        = "user"
	OwnerAgent       = "agent"
	RunClaimed       = "claimed"
	RunMaterializing = "materializing"
	RunDone          = "done"
	RunFailed        = "failed"
)

var ErrRunAlreadyFailed = errors.New("workflow run already failed")

type GoalWriter interface {
	CreateRoot(ctx context.Context, in goal.CreateInput) (sqlc.AgentGoal, error)
	MaterializeFrozenLayer(ctx context.Context, parentID string, content goal.DecompositionContent) error
	ActivateFrozenComposite(ctx context.Context, id string) error
}

type Service struct {
	db   *pgxpool.Pool
	q    *sqlc.Queries
	goal GoalWriter
}

func New(db *pgxpool.Pool, q *sqlc.Queries, goalSvc GoalWriter) *Service {
	return &Service{db: db, q: q, goal: goalSvc}
}

type SaveInput struct {
	UserID  string
	AgentID string
	GoalID  string
	Name    string
	Inputs  []InputSpec
}

type InstantiateInput struct {
	UserID         string
	AgentID        string
	WorkflowID     string
	Inputs         map[string]string
	IdempotencyKey string
}

func (s *Service) SaveGoalAsWorkflow(ctx context.Context, in SaveInput) (sqlc.AgentWorkflow, error) {
	root, err := s.q.GetGoal(ctx, in.GoalID)
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	if root.UserID != in.UserID || root.AgentID != in.AgentID {
		return sqlc.AgentWorkflow{}, pgx.ErrNoRows
	}
	if root.ParentID.Valid || root.Kind != goal.KindComposite || root.Lifecycle != goal.LifecycleDone || root.DoneReason != goal.DoneReasonAccepted {
		return sqlc.AgentWorkflow{}, goal.ErrInvalidTransition
	}
	plan, err := s.snapshot(ctx, root)
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	if err := plan.Validate(); err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	inputsJSON, err := json.Marshal(in.Inputs)
	if err != nil {
		return sqlc.AgentWorkflow{}, fmt.Errorf("marshal workflow inputs: %w", err)
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return sqlc.AgentWorkflow{}, fmt.Errorf("marshal frozen plan: %w", err)
	}
	ownerKind, ownerUser, ownerAgent := ownerScope(in.UserID, in.AgentID)
	latest, err := s.q.GetLatestWorkflowVersion(ctx, sqlc.GetLatestWorkflowVersionParams{OwnerKind: ownerKind, UserID: ownerUser, AgentID: ownerAgent, Name: in.Name})
	if err != nil {
		return sqlc.AgentWorkflow{}, fmt.Errorf("latest workflow version: %w", err)
	}
	return s.q.CreateWorkflow(ctx, sqlc.CreateWorkflowParams{
		ID:                 uuid.NewString(),
		OwnerKind:          ownerKind,
		UserID:             ownerUser,
		AgentID:            ownerAgent,
		Name:               in.Name,
		Version:            latest + 1,
		Intent:             root.Intent,
		AcceptanceContract: root.AcceptanceContract,
		ConvergencePolicy:  root.ConvergencePolicy,
		Inputs:             inputsJSON,
		PayloadFormat:      PayloadFormatFrozenV0,
		Payload:            payload,
		FullyFrozen:        plan.FullyFrozen(),
		SourceGoalID:       pgnull.Text(root.ID),
	})
}

func (s *Service) Instantiate(ctx context.Context, in InstantiateInput) (sqlc.AgentWorkflowRun, error) {
	wf, err := s.getScoped(ctx, in.WorkflowID, in.UserID, in.AgentID)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, err
	}
	var specs []InputSpec
	if err := json.Unmarshal(wf.Inputs, &specs); err != nil {
		return sqlc.AgentWorkflowRun{}, fmt.Errorf("decode workflow inputs: %w", err)
	}
	resolved, err := ResolveInputs(specs, in.Inputs)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, err
	}
	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, fmt.Errorf("marshal resolved inputs: %w", err)
	}
	plan, err := DecodeFrozenPlan(wf.Payload)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, err
	}
	plan, err = SubstituteInputs(plan, resolved)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, err
	}
	if err := plan.Validate(); err != nil {
		return sqlc.AgentWorkflowRun{}, err
	}
	hash := plan.Hash()
	claimed, err := s.q.ClaimWorkflowRun(ctx, sqlc.ClaimWorkflowRunParams{ID: uuid.NewString(), WorkflowID: wf.ID, WorkflowVersion: wf.Version, IdempotencyKey: in.IdempotencyKey, Status: RunClaimed, Inputs: resolvedJSON, PlanHash: hash})
	if err != nil {
		return sqlc.AgentWorkflowRun{}, fmt.Errorf("claim workflow run: %w", err)
	}
	run := claimRowToRun(claimed)
	if !claimed.Claimed {
		switch run.Status {
		case RunDone:
			return run, nil
		case RunFailed:
			return sqlc.AgentWorkflowRun{}, ErrRunAlreadyFailed
		}
		if len(run.Inputs) > 0 {
			if err := json.Unmarshal(run.Inputs, &resolved); err != nil {
				return sqlc.AgentWorkflowRun{}, fmt.Errorf("decode run inputs: %w", err)
			}
			plan, err = DecodeFrozenPlan(wf.Payload)
			if err != nil {
				return sqlc.AgentWorkflowRun{}, err
			}
			plan, err = SubstituteInputs(plan, resolved)
			if err != nil {
				return sqlc.AgentWorkflowRun{}, err
			}
			hash = plan.Hash()
		}
	}
	if !run.RootGoalID.Valid {
		root, err := s.createRunRoot(ctx, wf, in.UserID, in.AgentID)
		if err != nil {
			return sqlc.AgentWorkflowRun{}, err
		}
		rows, err := s.q.SetWorkflowRunRoot(ctx, sqlc.SetWorkflowRunRootParams{ID: run.ID, RootGoalID: pgnull.Text(root.ID), PlanHash: hash})
		if err != nil {
			return sqlc.AgentWorkflowRun{}, fmt.Errorf("set workflow run root: %w", err)
		}
		if rows == 0 {
			if _, err := s.q.DeleteDraftRootGoal(ctx, root.ID); err != nil {
				return sqlc.AgentWorkflowRun{}, fmt.Errorf("delete orphan workflow root: %w", err)
			}
			run, err = s.q.GetWorkflowRunByKey(ctx, sqlc.GetWorkflowRunByKeyParams{WorkflowID: wf.ID, IdempotencyKey: in.IdempotencyKey})
			if err != nil {
				return sqlc.AgentWorkflowRun{}, fmt.Errorf("reload workflow run: %w", err)
			}
		} else {
			run.RootGoalID = pgnull.Text(root.ID)
			run.Status = RunMaterializing
		}
	}
	if err := s.walk(ctx, run.RootGoalID.String, plan); err != nil {
		_ = s.q.SetWorkflowRunStatus(ctx, sqlc.SetWorkflowRunStatusParams{ID: run.ID, Status: RunFailed})
		return sqlc.AgentWorkflowRun{}, err
	}
	if err := s.q.SetWorkflowRunStatus(ctx, sqlc.SetWorkflowRunStatusParams{ID: run.ID, Status: RunDone}); err != nil {
		return sqlc.AgentWorkflowRun{}, fmt.Errorf("finish workflow run: %w", err)
	}
	return s.q.GetWorkflowRunByKey(ctx, sqlc.GetWorkflowRunByKeyParams{WorkflowID: wf.ID, IdempotencyKey: in.IdempotencyKey})
}

func (s *Service) Get(ctx context.Context, userID, agentID, id string) (sqlc.AgentWorkflow, error) {
	return s.getScoped(ctx, id, userID, agentID)
}

func (s *Service) List(ctx context.Context, userID, agentID string) ([]sqlc.AgentWorkflow, error) {
	ownerKind, ownerUser, ownerAgent := ownerScope(userID, agentID)
	return s.q.ListWorkflows(ctx, sqlc.ListWorkflowsParams{OwnerKind: ownerKind, UserID: ownerUser, AgentID: ownerAgent})
}

func (s *Service) ListVersions(ctx context.Context, userID, agentID, name string) ([]sqlc.AgentWorkflow, error) {
	ownerKind, ownerUser, ownerAgent := ownerScope(userID, agentID)
	return s.q.ListWorkflowVersions(ctx, sqlc.ListWorkflowVersionsParams{OwnerKind: ownerKind, UserID: ownerUser, AgentID: ownerAgent, Name: name})
}

func (s *Service) ListRuns(ctx context.Context, userID, agentID, id string, limit, offset int32) ([]sqlc.AgentWorkflowRun, error) {
	if _, err := s.getScoped(ctx, id, userID, agentID); err != nil {
		return nil, err
	}
	return s.q.ListWorkflowRuns(ctx, sqlc.ListWorkflowRunsParams{WorkflowID: id, Limit: limit, Offset: offset})
}

func (s *Service) Delete(ctx context.Context, userID, agentID, id string) error {
	if _, err := s.getScoped(ctx, id, userID, agentID); err != nil {
		return err
	}
	count, err := s.q.CountWorkflowRuns(ctx, id)
	if err != nil {
		return fmt.Errorf("count workflow runs: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("workflow delete refused: runs exist")
	}
	// Phase 4 must also reject scheduler jobs whose payload references this row.
	return s.q.DeleteWorkflow(ctx, id)
}

// snapshot relies on materialized child position matching the frozen plan child
// index; replanning is disabled, so a finished tree stays aligned with its plan.
func (s *Service) snapshot(ctx context.Context, parent sqlc.AgentGoal) (FrozenPlan, error) {
	if isEmptyJSON(parent.Plan) {
		return FrozenPlan{}, nil
	}
	content, err := DecodeDecomposition(parent.Plan)
	if err != nil {
		return FrozenPlan{}, err
	}
	children, err := s.q.ListGoalChildren(ctx, pgnull.Text(parent.ID))
	if err != nil {
		return FrozenPlan{}, fmt.Errorf("list goal children: %w", err)
	}
	out := FrozenPlan{Edges: content.Edges}
	for i, ch := range content.Children {
		node := FrozenNode{Child: ch}
		if ch.Kind == goal.KindComposite && i < len(children) {
			if !isEmptyJSON(children[i].Plan) {
				nested, err := s.snapshot(ctx, children[i])
				if err != nil {
					return FrozenPlan{}, err
				}
				node.Plan = &nested
			}
		}
		out.Children = append(out.Children, node)
	}
	return out, nil
}

func DecodeDecomposition(b []byte) (goal.DecompositionContent, error) {
	var c goal.DecompositionContent
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("decode decomposition: %w", err)
	}
	return c, nil
}

func (s *Service) createRunRoot(ctx context.Context, wf sqlc.AgentWorkflow, userID, agentID string) (sqlc.AgentGoal, error) {
	var contract goal.AcceptanceContract
	_ = json.Unmarshal(wf.AcceptanceContract, &contract)
	var convergence goal.ConvergencePolicy
	_ = json.Unmarshal(wf.ConvergencePolicy, &convergence)
	return s.goal.CreateRoot(ctx, goal.CreateInput{UserID: userID, AgentID: agentID, Title: wf.Name, Intent: wf.Intent, Kind: goal.KindComposite, Required: true, Contract: contract, Convergence: convergence, ReviewPolicy: goal.ReviewNone, WorkflowID: wf.ID, WorkflowVersion: wf.Version})
}

// walk relies on MaterializeFrozenLayer writing position=i and ListGoalChildren
// ordering by position, so children[i] corresponds to plan.Children[i].
func (s *Service) walk(ctx context.Context, parentID string, plan FrozenPlan) error {
	if err := s.goal.MaterializeFrozenLayer(ctx, parentID, plan.decomposition()); err != nil {
		return err
	}
	children, err := s.q.ListGoalChildren(ctx, pgnull.Text(parentID))
	if err != nil {
		return fmt.Errorf("list frozen children: %w", err)
	}
	for i, node := range plan.Children {
		if node.Child.Kind != goal.KindComposite || node.Plan == nil || i >= len(children) {
			continue
		}
		if err := s.walk(ctx, children[i].ID, *node.Plan); err != nil {
			return err
		}
		if err := s.goal.ActivateFrozenComposite(ctx, children[i].ID); err != nil {
			return err
		}
	}
	return s.goal.ActivateFrozenComposite(ctx, parentID)
}

func (s *Service) getScoped(ctx context.Context, id, userID, agentID string) (sqlc.AgentWorkflow, error) {
	wf, err := s.q.GetWorkflow(ctx, id)
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	ownerKind, ownerUser, ownerAgent := ownerScope(userID, agentID)
	if wf.OwnerKind != ownerKind || wf.UserID.String != ownerUser.String || wf.AgentID.String != ownerAgent.String {
		return sqlc.AgentWorkflow{}, pgx.ErrNoRows
	}
	return wf, nil
}

func ownerScope(userID, agentID string) (string, pgtype.Text, pgtype.Text) {
	uid := pgnull.Text(userID)
	if agentID != "" {
		return OwnerAgent, uid, pgnull.Text(agentID)
	}
	return OwnerUser, uid, pgtype.Text{}
}

func claimRowToRun(r sqlc.ClaimWorkflowRunRow) sqlc.AgentWorkflowRun {
	return sqlc.AgentWorkflowRun{ID: r.ID, WorkflowID: r.WorkflowID, WorkflowVersion: r.WorkflowVersion, IdempotencyKey: r.IdempotencyKey, RootGoalID: r.RootGoalID, Status: r.Status, Inputs: r.Inputs, PlanHash: r.PlanHash, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func isEmptyJSON(b []byte) bool {
	trimmed := string(b)
	return trimmed == "" || trimmed == "{}" || trimmed == "null"
}

package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
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

var (
	ErrRunAlreadyFailed        = errors.New("workflow run already failed")
	ErrWorkflowHasRuns         = errors.New("workflow has runs")
	ErrWorkflowHasSchedulerJob = errors.New("workflow has enabled scheduler jobs")
	ErrWorkflowVersionConflict = errors.New("workflow version conflict; retry")
	// ErrInvalidWorkflowInput marks input errors the caller can fix (bad spec
	// name, unknown or missing input, unresolved placeholder) -- mapped to 400.
	ErrInvalidWorkflowInput = errors.New("invalid workflow input")
)

// GoalWriter is the workflow domain's narrow port into Goal: frozen-tree writes
// plus Goal's direct source-read authorization. The port is consumer-owned here
// so no goal→workflow cycle forms; Goal exposes a narrow adapter that satisfies it.
type GoalWriter interface {
	CreateRoot(ctx context.Context, in goal.CreateInput) (goal.CreatedGoal, error)
	MaterializeFrozenLayer(ctx context.Context, parentID string, content goal.DecompositionContent, frozen goal.FrozenStamp) error
	ActivateFrozenComposite(ctx context.Context, id string) error
	Authorize(ctx context.Context, authority authz.Authority, goalID string, action authz.Action) (goal.AuthorizedGoal, error)
}

type Service struct {
	db     *pgxpool.Pool
	q      *sqlc.Queries
	goal   GoalWriter
	agents *agentaccess.Service
}

// New constructs the Workflow application service. Goal and Agent are the
// domain-owned ports used by the Authority-based Access boundary.
func New(db *pgxpool.Pool, goalSvc GoalWriter, agents *agentaccess.Service) *Service {
	return &Service{db: db, q: sqlc.New(db), goal: goalSvc, agents: agents}
}

// ScheduledWorkflow is the narrow immutable workflow fact the scheduler needs
// while validating a job. The scheduler has already established the durable
// owner and agent values it supplies to this worker-adapter port.
type ScheduledWorkflow struct {
	ID          string
	FullyFrozen bool
}

// RunState is the latest-run snapshot the scheduler adapter needs to decide
// whether a workflow run is in flight or has reached a terminal root goal.
type RunState struct {
	Found            bool
	Status           string
	IdempotencyKey   string
	RootGoalID       string
	RootGoalTerminal bool
}

// ValidateScheduledWorkflow returns only the durable workflow facts needed to
// validate a scheduler-owned workflow job.
func (s *Service) ValidateScheduledWorkflow(ctx context.Context, userID, agentID, workflowID string) (ScheduledWorkflow, error) {
	wf, err := s.getScoped(ctx, workflowID, userID, agentID)
	if err != nil {
		return ScheduledWorkflow{}, err
	}
	return ScheduledWorkflow{ID: wf.ID, FullyFrozen: wf.FullyFrozen}, nil
}

// LatestRunState returns the state of the most recent run for a workflow.
// Found is false when the workflow has no runs yet.
func (s *Service) LatestRunState(ctx context.Context, workflowID string) (RunState, error) {
	run, err := s.q.GetLatestWorkflowRun(ctx, workflowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunState{}, nil
	}
	if err != nil {
		return RunState{}, err
	}
	state := RunState{Found: true, Status: run.Status, IdempotencyKey: run.IdempotencyKey}
	if run.RootGoalID.Valid {
		state.RootGoalID = run.RootGoalID.String
		root, err := s.q.GetGoal(ctx, run.RootGoalID.String)
		if err != nil {
			return RunState{}, err
		}
		state.RootGoalTerminal = goal.IsTerminalLifecycle(root.Lifecycle)
	}
	return state, nil
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

func (s *Service) saveGoalAsWorkflow(ctx context.Context, in SaveInput) (sqlc.AgentWorkflow, error) {
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
	if err := ValidateSpecs(in.Inputs); err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	plan, err := s.snapshot(ctx, root)
	if err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	var convergence goal.ConvergencePolicy
	_ = json.Unmarshal(root.ConvergencePolicy, &convergence)
	if err := plan.ValidateMaxDepth(convergence.Normalized().MaxDepth); err != nil {
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
	if err := ValidatePlaceholders(in.Inputs, string(payload), root.Intent); err != nil {
		return sqlc.AgentWorkflow{}, err
	}
	ownerKind, ownerUser, ownerAgent := ownerScope(in.UserID, in.AgentID)
	for range 3 {
		latest, err := s.q.GetLatestWorkflowVersion(ctx, sqlc.GetLatestWorkflowVersionParams{OwnerKind: ownerKind, UserID: ownerUser, AgentID: ownerAgent, Name: in.Name})
		if err != nil {
			return sqlc.AgentWorkflow{}, fmt.Errorf("latest workflow version: %w", err)
		}
		wf, err := s.q.CreateWorkflow(ctx, sqlc.CreateWorkflowParams{
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
		if err == nil {
			return wf, nil
		}
		if !isUniqueViolation(err) {
			return sqlc.AgentWorkflow{}, err
		}
	}
	return sqlc.AgentWorkflow{}, ErrWorkflowVersionConflict
}

func (s *Service) instantiate(ctx context.Context, in InstantiateInput) (sqlc.AgentWorkflowRun, bool, error) {
	wf, err := s.getScoped(ctx, in.WorkflowID, in.UserID, in.AgentID)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, err
	}

	planForInputs := func(inputs map[string]string) (FrozenPlan, string, error) {
		plan, err := DecodeFrozenPlan(wf.Payload)
		if err != nil {
			return FrozenPlan{}, "", err
		}
		plan, err = SubstituteInputs(plan, inputs)
		if err != nil {
			return FrozenPlan{}, "", err
		}
		var convergence goal.ConvergencePolicy
		_ = json.Unmarshal(wf.ConvergencePolicy, &convergence)
		if err := plan.ValidateMaxDepth(convergence.Normalized().MaxDepth); err != nil {
			return FrozenPlan{}, "", err
		}
		return plan, plan.Hash(), nil
	}
	resume := func(run sqlc.AgentWorkflowRun) (sqlc.AgentWorkflowRun, error) {
		switch run.Status {
		case RunDone:
			return run, nil
		case RunFailed:
			return sqlc.AgentWorkflowRun{}, ErrRunAlreadyFailed
		}
		var resolved map[string]string
		if err := json.Unmarshal(run.Inputs, &resolved); err != nil {
			return sqlc.AgentWorkflowRun{}, fmt.Errorf("decode run inputs: %w", err)
		}
		plan, hash, err := planForInputs(resolved)
		if err != nil {
			return sqlc.AgentWorkflowRun{}, err
		}
		return s.materializeRun(ctx, wf, run, in.UserID, in.AgentID, resolved, plan, hash)
	}

	if run, err := s.q.GetWorkflowRunByKey(ctx, sqlc.GetWorkflowRunByKeyParams{WorkflowID: wf.ID, IdempotencyKey: in.IdempotencyKey}); err == nil {
		run, err = resume(run)
		return run, false, err
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.AgentWorkflowRun{}, false, fmt.Errorf("get workflow run: %w", err)
	}

	var specs []InputSpec
	if err := json.Unmarshal(wf.Inputs, &specs); err != nil {
		return sqlc.AgentWorkflowRun{}, false, fmt.Errorf("decode workflow inputs: %w", err)
	}
	resolved, err := ResolveInputs(specs, in.Inputs)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, err
	}
	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, fmt.Errorf("marshal resolved inputs: %w", err)
	}
	plan, hash, err := planForInputs(resolved)
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, err
	}
	claimed, err := s.q.ClaimWorkflowRun(ctx, sqlc.ClaimWorkflowRunParams{ID: uuid.NewString(), WorkflowID: wf.ID, WorkflowVersion: wf.Version, IdempotencyKey: in.IdempotencyKey, Status: RunClaimed, Inputs: resolvedJSON, PlanHash: hash})
	if err != nil {
		return sqlc.AgentWorkflowRun{}, false, fmt.Errorf("claim workflow run: %w", err)
	}
	run := claimRowToRun(claimed)
	if !claimed.Claimed {
		run, err = resume(run)
		return run, false, err
	}
	run, err = s.materializeRun(ctx, wf, run, in.UserID, in.AgentID, resolved, plan, hash)
	return run, true, err
}

// Authorize is Workflow's narrow direct port for another domain. It loads the
// durable row before applying the same fixed rules as Access; ownership and
// executor facts cannot be supplied by the caller. A missing or denied workflow
// is opaque to the caller through authz.ErrNotFound.
func (s *Service) Authorize(ctx context.Context, authority authz.Authority, workflowID string, action authz.Action) error {
	if !authority.Valid() {
		return authz.ErrForbidden
	}
	wf, err := s.q.GetWorkflow(ctx, workflowID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return authz.ErrNotFound
		}
		return fmt.Errorf("get workflow: %w", err)
	}
	if s.access(authority).allowed(action, wf) {
		return nil
	}
	return authz.ErrNotFound
}

// deleteLoaded removes a workflow whose access has already been authorized by the
// caller (the Access PEP). It still enforces the domain invariant that a
// workflow with runs or an enabled scheduler job cannot be deleted.
func (s *Service) deleteLoaded(ctx context.Context, id string) error {
	count, err := s.q.CountWorkflowRuns(ctx, id)
	if err != nil {
		return fmt.Errorf("count workflow runs: %w", err)
	}
	if count > 0 {
		return ErrWorkflowHasRuns
	}
	jobCount, err := s.q.CountEnabledSchedulerWorkflowJobs(ctx, id)
	if err != nil {
		return fmt.Errorf("count scheduler workflow jobs: %w", err)
	}
	if jobCount > 0 {
		return ErrWorkflowHasSchedulerJob
	}
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

func (s *Service) materializeRun(ctx context.Context, wf sqlc.AgentWorkflow, run sqlc.AgentWorkflowRun, userID, agentID string, resolved map[string]string, plan FrozenPlan, hash string) (sqlc.AgentWorkflowRun, error) {
	if !run.RootGoalID.Valid {
		root, err := s.createRunRoot(ctx, wf, run.ID, userID, agentID, resolved)
		if err != nil {
			return sqlc.AgentWorkflowRun{}, err
		}
		rows, err := s.q.SetWorkflowRunRoot(ctx, sqlc.SetWorkflowRunRootParams{ID: run.ID, RootGoalID: pgnull.Text(root.ID), PlanHash: hash})
		if err != nil {
			return sqlc.AgentWorkflowRun{}, fmt.Errorf("set workflow run root: %w", err)
		}
		if rows == 0 {
			run, err = s.q.GetWorkflowRunByKey(ctx, sqlc.GetWorkflowRunByKeyParams{WorkflowID: wf.ID, IdempotencyKey: run.IdempotencyKey})
			if err != nil {
				return sqlc.AgentWorkflowRun{}, fmt.Errorf("reload workflow run: %w", err)
			}
		} else {
			run.RootGoalID = pgnull.Text(root.ID)
			run.Status = RunMaterializing
		}
	}
	if err := s.walk(ctx, wf, run.RootGoalID.String, plan); err != nil {
		_ = s.q.SetWorkflowRunStatus(ctx, sqlc.SetWorkflowRunStatusParams{ID: run.ID, Status: RunFailed})
		return sqlc.AgentWorkflowRun{}, err
	}
	if err := s.q.SetWorkflowRunStatus(ctx, sqlc.SetWorkflowRunStatusParams{ID: run.ID, Status: RunDone}); err != nil {
		return sqlc.AgentWorkflowRun{}, fmt.Errorf("finish workflow run: %w", err)
	}
	return s.q.GetWorkflowRunByKey(ctx, sqlc.GetWorkflowRunByKeyParams{WorkflowID: wf.ID, IdempotencyKey: run.IdempotencyKey})
}

func (s *Service) createRunRoot(ctx context.Context, wf sqlc.AgentWorkflow, runID, userID, agentID string, inputs map[string]string) (goal.CreatedGoal, error) {
	var contract goal.AcceptanceContract
	_ = json.Unmarshal(wf.AcceptanceContract, &contract)
	var convergence goal.ConvergencePolicy
	_ = json.Unmarshal(wf.ConvergencePolicy, &convergence)
	// The root intent came from the source goal, so it may carry the same
	// {{inputs.*}} placeholders the children do.
	intent, err := substituteText(wf.Intent, inputs)
	if err != nil {
		return goal.CreatedGoal{}, err
	}
	// The run executes under the workflow's own agent; the caller's scope only
	// gates access. A user-session caller (empty agent scope) must still land
	// the tree on the agent the workflow was demonstrated on.
	if wf.AgentID.Valid && wf.AgentID.String != "" {
		agentID = wf.AgentID.String
	}
	return s.goal.CreateRoot(ctx, goal.CreateInput{ID: workflowRootID(runID), UserID: userID, AgentID: agentID, Title: wf.Name, Intent: intent, Kind: goal.KindComposite, Required: true, Contract: contract, Convergence: convergence, ReviewPolicy: goal.ReviewNone, WorkflowID: wf.ID, WorkflowVersion: wf.Version})
}

func workflowRootID(runID string) string {
	h := sha256.Sum256(append(append([]byte("workflow-root"), 0), []byte(runID)...))
	return hex.EncodeToString(h[:16])
}

// walk relies on MaterializeFrozenLayer writing position=i and ListGoalChildren
// ordering by position, so children[i] corresponds to plan.Children[i].
func (s *Service) walk(ctx context.Context, wf sqlc.AgentWorkflow, parentID string, plan FrozenPlan) error {
	frozen := goal.FrozenStamp{WorkflowID: wf.ID, WorkflowVersion: wf.Version}
	for _, node := range plan.Children {
		if node.Child.Kind == goal.KindComposite && node.Plan != nil {
			frozen.ChildKeys = append(frozen.ChildKeys, node.Child.Key)
		}
	}
	if err := s.goal.MaterializeFrozenLayer(ctx, parentID, plan.decomposition(), frozen); err != nil {
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
		if err := s.walk(ctx, wf, children[i].ID, *node.Plan); err != nil {
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
	if wf.UserID.String != userID {
		return sqlc.AgentWorkflow{}, pgx.ErrNoRows
	}
	if agentID != "" {
		if wf.OwnerKind != OwnerAgent || wf.AgentID.String != agentID {
			return sqlc.AgentWorkflow{}, pgx.ErrNoRows
		}
		return wf, nil
	}
	if wf.OwnerKind != OwnerUser && wf.OwnerKind != OwnerAgent {
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

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func claimRowToRun(r sqlc.ClaimWorkflowRunRow) sqlc.AgentWorkflowRun {
	return sqlc.AgentWorkflowRun{ID: r.ID, WorkflowID: r.WorkflowID, WorkflowVersion: r.WorkflowVersion, IdempotencyKey: r.IdempotencyKey, RootGoalID: r.RootGoalID, Status: r.Status, Inputs: r.Inputs, PlanHash: r.PlanHash, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func isEmptyJSON(b []byte) bool {
	trimmed := string(b)
	return trimmed == "" || trimmed == "{}" || trimmed == "null"
}

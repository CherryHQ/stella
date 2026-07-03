package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type workflowHarness struct {
	t       *testing.T
	ctx     context.Context
	db      *pgxpool.Pool
	q       *sqlc.Queries
	goals   *goal.GoalService
	svc     *Service
	userID  string
	agentID string
}

type racingGoalWriter struct {
	*goal.GoalService
	t        *testing.T
	ctx      context.Context
	q        *sqlc.Queries
	runID    string
	planHash string
	armed    bool
	winnerID string
	loserID  string
}

func (w *racingGoalWriter) CreateRoot(ctx context.Context, in goal.CreateInput) (sqlc.AgentGoal, error) {
	root, err := w.GoalService.CreateRoot(ctx, in)
	if err != nil || !w.armed {
		return root, err
	}
	w.armed = false
	w.loserID = root.ID
	winner, err := w.GoalService.CreateRoot(ctx, in)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	rows, err := w.q.SetWorkflowRunRoot(w.ctx, sqlc.SetWorkflowRunRootParams{ID: w.runID, RootGoalID: pgnull.Text(winner.ID), PlanHash: w.planHash})
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	if rows != 1 {
		w.t.Fatalf("winner set root rows = %d", rows)
	}
	w.winnerID = winner.ID
	return root, nil
}

func newWorkflowHarness(t *testing.T) *workflowHarness {
	t.Helper()
	db := dbtest.New(t)
	ctx := context.Background()
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, "wf-"+userID[:8]+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'workflow-agent', '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	q := sqlc.New(db)
	goals := goal.New(db, q)
	return &workflowHarness{t: t, ctx: ctx, db: db, q: q, goals: goals, svc: New(db, q, goals), userID: userID, agentID: agentID}
}

func TestSaveGoalAsWorkflowAndInstantiateIdempotently(t *testing.T) {
	h := newWorkflowHarness(t)
	root, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "demo", Intent: "demo intent", Kind: goal.KindComposite, Required: true})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	rootPlan := goal.DecompositionContent{Children: []goal.ProposedChild{
		{Key: "leaf", Title: "Leaf {{inputs.topic}}", Intent: "do leaf", Kind: goal.KindLeaf, Required: true},
		{Key: "comp", Title: "Comp", Intent: "do comp", Kind: goal.KindComposite, Required: true},
	}}
	if err := h.goals.MaterializeFrozenLayer(h.ctx, root.ID, rootPlan); err != nil {
		t.Fatalf("materialize root: %v", err)
	}
	children, err := h.q.ListGoalChildren(h.ctx, pgnull.Text(root.ID))
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	nestedPlan := goal.DecompositionContent{Children: []goal.ProposedChild{{Key: "nested", Title: "Nested", Intent: "do nested", Kind: goal.KindLeaf, Required: true}}}
	if err := h.goals.MaterializeFrozenLayer(h.ctx, children[1].ID, nestedPlan); err != nil {
		t.Fatalf("materialize nested: %v", err)
	}
	if err := h.goals.ActivateFrozenComposite(h.ctx, children[1].ID); err != nil {
		t.Fatalf("activate child composite: %v", err)
	}
	if err := h.goals.ActivateFrozenComposite(h.ctx, root.ID); err != nil {
		t.Fatalf("activate root: %v", err)
	}
	if _, err := h.db.Exec(h.ctx, `UPDATE agent_goal SET lifecycle='done', done_reason='accepted', acceptance_state='passed', accepted_output='{}', accepted_at=now() WHERE id=$1`, root.ID); err != nil {
		t.Fatalf("force accept root: %v", err)
	}

	wf, err := h.svc.SaveGoalAsWorkflow(h.ctx, SaveInput{UserID: h.userID, AgentID: h.agentID, GoalID: root.ID, Name: "daily", Inputs: []InputSpec{{Name: "topic", Required: true}}})
	if err != nil {
		t.Fatalf("save workflow: %v", err)
	}
	if !wf.FullyFrozen || wf.Version != 1 {
		t.Fatalf("workflow frozen/version = %v/%d", wf.FullyFrozen, wf.Version)
	}
	run1, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, Inputs: map[string]string{"topic": "launch"}, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	run2, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, Inputs: map[string]string{"topic": "ignored"}, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("instantiate replay: %v", err)
	}
	if run1.RootGoalID.String == "" || run1.RootGoalID.String != run2.RootGoalID.String {
		t.Fatalf("idempotent root mismatch: %q vs %q", run1.RootGoalID.String, run2.RootGoalID.String)
	}
	instChildren, err := h.q.ListGoalChildren(h.ctx, pgnull.Text(run1.RootGoalID.String))
	if err != nil {
		t.Fatalf("list instantiated children: %v", err)
	}
	if instChildren[0].Title != "Leaf launch" {
		t.Fatalf("stored resolved inputs not used: %q", instChildren[0].Title)
	}
}

func TestInstantiateDeletesLoserRootWhenRunRootRaceIsLost(t *testing.T) {
	h := newWorkflowHarness(t)
	plan := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "leaf", Title: "Leaf", Intent: "do leaf", Kind: goal.KindLeaf, Required: true}}}}
	payload, _ := json.Marshal(plan)
	wf, err := h.q.CreateWorkflow(h.ctx, sqlc.CreateWorkflowParams{ID: uuid.NewString(), OwnerKind: OwnerAgent, UserID: pgnull.Text(h.userID), AgentID: pgnull.Text(h.agentID), Name: "race", Version: 1, Intent: "race", AcceptanceContract: []byte(`{}`), ConvergencePolicy: []byte(`{}`), Inputs: []byte(`[]`), PayloadFormat: PayloadFormatFrozenV0, Payload: payload, FullyFrozen: true})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	runID := uuid.NewString()
	if _, err := h.q.ClaimWorkflowRun(h.ctx, sqlc.ClaimWorkflowRunParams{ID: runID, WorkflowID: wf.ID, WorkflowVersion: wf.Version, IdempotencyKey: "same", Status: RunClaimed, Inputs: []byte(`{}`), PlanHash: plan.Hash()}); err != nil {
		t.Fatalf("claim run: %v", err)
	}
	writer := &racingGoalWriter{GoalService: h.goals, t: t, ctx: h.ctx, q: h.q, runID: runID, planHash: plan.Hash(), armed: true}
	svc := New(h.db, h.q, writer)

	run, err := svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("instantiate loser path: %v", err)
	}
	if run.RootGoalID.String != writer.winnerID {
		t.Fatalf("winner root not returned: got %q want %q", run.RootGoalID.String, writer.winnerID)
	}
	if _, err := h.q.GetGoal(h.ctx, writer.loserID); err == nil {
		t.Fatalf("loser root still exists: %s", writer.loserID)
	}
	var roots int
	if err := h.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM agent_goal WHERE workflow_id=$1 AND parent_id IS NULL`, wf.ID).Scan(&roots); err != nil {
		t.Fatalf("count roots: %v", err)
	}
	if roots != 1 {
		t.Fatalf("workflow root count = %d", roots)
	}
}

func TestSetWorkflowRunRootIsConditional(t *testing.T) {
	h := newWorkflowHarness(t)
	plan := FrozenPlan{}
	payload, _ := json.Marshal(plan)
	wf, err := h.q.CreateWorkflow(h.ctx, sqlc.CreateWorkflowParams{ID: uuid.NewString(), OwnerKind: OwnerAgent, UserID: pgnull.Text(h.userID), AgentID: pgnull.Text(h.agentID), Name: "conditional", Version: 1, Intent: "conditional", AcceptanceContract: []byte(`{}`), ConvergencePolicy: []byte(`{}`), Inputs: []byte(`[]`), PayloadFormat: PayloadFormatFrozenV0, Payload: payload, FullyFrozen: true})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	runID := uuid.NewString()
	if _, err := h.q.ClaimWorkflowRun(h.ctx, sqlc.ClaimWorkflowRunParams{ID: runID, WorkflowID: wf.ID, WorkflowVersion: wf.Version, IdempotencyKey: "same", Status: RunClaimed, Inputs: []byte(`{}`), PlanHash: plan.Hash()}); err != nil {
		t.Fatalf("claim run: %v", err)
	}
	root1, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "root1", Intent: "root1", Kind: goal.KindComposite, Required: true, WorkflowID: wf.ID, WorkflowVersion: wf.Version})
	if err != nil {
		t.Fatalf("create root1: %v", err)
	}
	root2, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "root2", Intent: "root2", Kind: goal.KindComposite, Required: true, WorkflowID: wf.ID, WorkflowVersion: wf.Version})
	if err != nil {
		t.Fatalf("create root2: %v", err)
	}
	rows, err := h.q.SetWorkflowRunRoot(h.ctx, sqlc.SetWorkflowRunRootParams{ID: runID, RootGoalID: pgnull.Text(root1.ID), PlanHash: plan.Hash()})
	if err != nil {
		t.Fatalf("set root1: %v", err)
	}
	if rows != 1 {
		t.Fatalf("first set rows = %d", rows)
	}
	rows, err = h.q.SetWorkflowRunRoot(h.ctx, sqlc.SetWorkflowRunRootParams{ID: runID, RootGoalID: pgnull.Text(root2.ID), PlanHash: plan.Hash()})
	if err != nil {
		t.Fatalf("set root2: %v", err)
	}
	if rows != 0 {
		t.Fatalf("second set rows = %d", rows)
	}
}

func TestInstantiateLeavesNilPlanCompositeDraft(t *testing.T) {
	h := newWorkflowHarness(t)
	plan := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "dynamic", Title: "Dynamic", Intent: "planner decides", Kind: goal.KindComposite, Required: true}}}}
	payload, _ := json.Marshal(plan)
	wf, err := h.q.CreateWorkflow(h.ctx, sqlc.CreateWorkflowParams{ID: uuid.NewString(), OwnerKind: OwnerAgent, UserID: pgnull.Text(h.userID), AgentID: pgnull.Text(h.agentID), Name: "partial", Version: 1, Intent: "partial", AcceptanceContract: []byte(`{}`), ConvergencePolicy: []byte(`{}`), Inputs: []byte(`[]`), PayloadFormat: PayloadFormatFrozenV0, Payload: payload, FullyFrozen: false})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	run, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, IdempotencyKey: "once"})
	if err != nil {
		t.Fatalf("instantiate partial: %v", err)
	}
	children, err := h.q.ListGoalChildren(h.ctx, pgnull.Text(run.RootGoalID.String))
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 1 || children[0].Lifecycle != goal.LifecycleDraft || children[0].PlannedAt.Valid {
		t.Fatalf("nil-plan composite = len %d lifecycle %q planned %v", len(children), children[0].Lifecycle, children[0].PlannedAt.Valid)
	}
}

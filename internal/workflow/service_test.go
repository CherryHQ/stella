package workflow

import (
	"context"
	"encoding/json"
	"errors"
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
	return &workflowHarness{t: t, ctx: ctx, db: db, q: q, goals: goals, svc: New(db, goals, nil), userID: userID, agentID: agentID}
}

func TestSaveGoalAsWorkflowAndInstantiateIdempotently(t *testing.T) {
	h := newWorkflowHarness(t)
	root, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "demo", Intent: "demo intent for {{inputs.topic}}", Kind: goal.KindComposite, Required: true})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	rootPlan := goal.DecompositionContent{Children: []goal.ProposedChild{
		{Key: "leaf", Title: "Leaf {{inputs.topic}}", Intent: "do leaf", Kind: goal.KindLeaf, Required: true},
		{Key: "comp", Title: "Comp", Intent: "do comp", Kind: goal.KindComposite, Required: true},
	}}
	if err := h.goals.MaterializeFrozenLayer(h.ctx, root.ID, rootPlan, goal.FrozenStamp{}); err != nil {
		t.Fatalf("materialize root: %v", err)
	}
	children, err := h.q.ListGoalChildren(h.ctx, pgnull.Text(root.ID))
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	nestedPlan := goal.DecompositionContent{Children: []goal.ProposedChild{{Key: "nested", Title: "Nested", Intent: "do nested", Kind: goal.KindLeaf, Required: true}}}
	if err := h.goals.MaterializeFrozenLayer(h.ctx, children[1].ID, nestedPlan, goal.FrozenStamp{}); err != nil {
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

	if _, err := h.svc.SaveGoalAsWorkflow(h.ctx, SaveInput{UserID: h.userID, AgentID: h.agentID, GoalID: root.ID, Name: "bad", Inputs: []InputSpec{{Name: "bad name"}}}); !errors.Is(err, ErrInvalidWorkflowInput) {
		t.Fatalf("save with invalid input name = %v", err)
	}
	if _, err := h.svc.SaveGoalAsWorkflow(h.ctx, SaveInput{UserID: h.userID, AgentID: h.agentID, GoalID: root.ID, Name: "undeclared"}); !errors.Is(err, ErrInvalidWorkflowInput) {
		t.Fatalf("save with undeclared placeholder = %v", err)
	}
	wf, err := h.svc.SaveGoalAsWorkflow(h.ctx, SaveInput{UserID: h.userID, AgentID: h.agentID, GoalID: root.ID, Name: "daily", Inputs: []InputSpec{{Name: "topic", Required: true}}})
	if err != nil {
		t.Fatalf("save workflow: %v", err)
	}
	if !wf.FullyFrozen || wf.Version != 1 {
		t.Fatalf("workflow frozen/version = %v/%d", wf.FullyFrozen, wf.Version)
	}
	run1, created, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, Inputs: map[string]string{"topic": "launch"}, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if !created {
		t.Fatal("first instantiate created=false")
	}
	run2, created, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, Inputs: map[string]string{"topic": "ignored"}, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("instantiate replay: %v", err)
	}
	if created {
		t.Fatal("replay created=true")
	}
	if run1.RootGoalID.String == "" || run1.RootGoalID.String != run2.RootGoalID.String {
		t.Fatalf("idempotent root mismatch: %q vs %q", run1.RootGoalID.String, run2.RootGoalID.String)
	}
	run3, created, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, Inputs: map[string]string{}, IdempotencyKey: "same"})
	if err != nil || created || run3.ID != run1.ID {
		t.Fatalf("done replay with missing input = run %q created %v err %v", run3.ID, created, err)
	}
	run4, created, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, Inputs: map[string]string{"unknown": "value"}, IdempotencyKey: "same"})
	if err != nil || created || run4.ID != run1.ID {
		t.Fatalf("done replay with unknown input = run %q created %v err %v", run4.ID, created, err)
	}
	instRoot, err := h.q.GetGoal(h.ctx, run1.RootGoalID.String)
	if err != nil {
		t.Fatalf("get instantiated root: %v", err)
	}
	if instRoot.Intent != "demo intent for launch" {
		t.Fatalf("root intent not substituted: %q", instRoot.Intent)
	}
	instChildren, err := h.q.ListGoalChildren(h.ctx, pgnull.Text(run1.RootGoalID.String))
	if err != nil {
		t.Fatalf("list instantiated children: %v", err)
	}
	if instChildren[0].Title != "Leaf launch" {
		t.Fatalf("stored resolved inputs not used: %q", instChildren[0].Title)
	}
	// The composite child carries a frozen sub-plan, so it must be stamped with
	// the workflow identity (dispatcher exclusion); the leaf stays unstamped.
	if instChildren[1].WorkflowID.String != wf.ID || int(instChildren[1].WorkflowVersion.Int32) != int(wf.Version) {
		t.Fatalf("frozen composite child not stamped: %q v%d", instChildren[1].WorkflowID.String, instChildren[1].WorkflowVersion.Int32)
	}
	if instChildren[0].WorkflowID.Valid {
		t.Fatalf("leaf child unexpectedly stamped: %q", instChildren[0].WorkflowID.String)
	}
}

func TestInstantiateCrashResumeBindsPrecreatedRoot(t *testing.T) {
	h := newWorkflowHarness(t)
	plan := FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{Key: "leaf", Title: "Leaf", Intent: "do leaf", Kind: goal.KindLeaf, Required: true}}}}
	payload, _ := json.Marshal(plan)
	wf, err := h.q.CreateWorkflow(h.ctx, sqlc.CreateWorkflowParams{ID: uuid.NewString(), OwnerKind: OwnerAgent, UserID: pgnull.Text(h.userID), AgentID: pgnull.Text(h.agentID), Name: "resume", Version: 1, Intent: "resume", AcceptanceContract: []byte(`{}`), ConvergencePolicy: []byte(`{}`), Inputs: []byte(`[]`), PayloadFormat: PayloadFormatFrozenV0, Payload: payload, FullyFrozen: true})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	runID := uuid.NewString()
	if _, err := h.q.ClaimWorkflowRun(h.ctx, sqlc.ClaimWorkflowRunParams{ID: runID, WorkflowID: wf.ID, WorkflowVersion: wf.Version, IdempotencyKey: "same", Status: RunClaimed, Inputs: []byte(`{}`), PlanHash: plan.Hash()}); err != nil {
		t.Fatalf("claim run: %v", err)
	}
	rootID := workflowRootID(runID)
	if _, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{ID: rootID, UserID: h.userID, AgentID: h.agentID, Title: wf.Name, Intent: wf.Intent, Kind: goal.KindComposite, Required: true, WorkflowID: wf.ID, WorkflowVersion: wf.Version}); err != nil {
		t.Fatalf("precreate root: %v", err)
	}
	run, created, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("resume instantiate: %v", err)
	}
	if created {
		t.Fatal("resume created=true")
	}
	if run.RootGoalID.String != rootID || run.Status != RunDone {
		t.Fatalf("run root/status = %q/%q want %q/%q", run.RootGoalID.String, run.Status, rootID, RunDone)
	}
	var roots int
	if err := h.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM agent_goal WHERE workflow_id=$1 AND parent_id IS NULL`, wf.ID).Scan(&roots); err != nil {
		t.Fatalf("count roots: %v", err)
	}
	if roots != 1 {
		t.Fatalf("workflow root count = %d", roots)
	}
}

func TestInstantiateConvergesWhenRunRootRaceIsLost(t *testing.T) {
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
	svc := New(h.db, writer, nil)

	run, _, err := svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("instantiate loser path: %v", err)
	}
	if run.RootGoalID.String != writer.winnerID {
		t.Fatalf("winner root not returned: got %q want %q", run.RootGoalID.String, writer.winnerID)
	}
	if writer.loserID != writer.winnerID {
		t.Fatalf("deterministic loser/winner mismatch: %q vs %q", writer.loserID, writer.winnerID)
	}
	if _, err := h.q.GetGoal(h.ctx, writer.loserID); err != nil {
		t.Fatalf("shared root should remain: %v", err)
	}
	var roots int
	if err := h.db.QueryRow(h.ctx, `SELECT COUNT(*) FROM agent_goal WHERE workflow_id=$1 AND parent_id IS NULL`, wf.ID).Scan(&roots); err != nil {
		t.Fatalf("count roots: %v", err)
	}
	if roots != 1 {
		t.Fatalf("workflow root count = %d", roots)
	}
}

func TestListDecomposableCompositesExcludesWorkflowRoots(t *testing.T) {
	h := newWorkflowHarness(t)
	payload, _ := json.Marshal(FrozenPlan{})
	wf, err := h.q.CreateWorkflow(h.ctx, sqlc.CreateWorkflowParams{ID: uuid.NewString(), OwnerKind: OwnerAgent, UserID: pgnull.Text(h.userID), AgentID: pgnull.Text(h.agentID), Name: "exclude", Version: 1, Intent: "exclude", AcceptanceContract: []byte(`{}`), ConvergencePolicy: []byte(`{}`), Inputs: []byte(`[]`), PayloadFormat: PayloadFormatFrozenV0, Payload: payload, FullyFrozen: true})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	workflowRoot, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "workflow", Intent: "workflow", Kind: goal.KindComposite, Required: true, WorkflowID: wf.ID, WorkflowVersion: 1})
	if err != nil {
		t.Fatalf("create workflow root: %v", err)
	}
	plain, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "plain", Intent: "plain", Kind: goal.KindComposite, Required: true})
	if err != nil {
		t.Fatalf("create plain root: %v", err)
	}
	rows, err := h.q.ListDecomposableComposites(h.ctx, 100)
	if err != nil {
		t.Fatalf("list decomposable: %v", err)
	}
	seenPlain := false
	for _, row := range rows {
		if row.ID == workflowRoot.ID {
			t.Fatalf("workflow root %s returned as decomposable", workflowRoot.ID)
		}
		if row.ID == plain.ID {
			seenPlain = true
		}
	}
	if !seenPlain {
		t.Fatalf("plain composite %s not returned", plain.ID)
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

func TestDeleteRejectsEnabledSchedulerWorkflowJob(t *testing.T) {
	h := newWorkflowHarness(t)
	payload, _ := json.Marshal(FrozenPlan{})
	wf, err := h.q.CreateWorkflow(h.ctx, sqlc.CreateWorkflowParams{ID: uuid.NewString(), OwnerKind: OwnerAgent, UserID: pgnull.Text(h.userID), AgentID: pgnull.Text(h.agentID), Name: "scheduled", Version: 1, Intent: "scheduled", AcceptanceContract: []byte(`{}`), ConvergencePolicy: []byte(`{}`), Inputs: []byte(`[]`), PayloadFormat: PayloadFormatFrozenV0, Payload: payload, FullyFrozen: true})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	jobPayload, _ := json.Marshal(map[string]any{"workflow_id": wf.ID})
	_, err = h.q.CreateSchedulerJob(h.ctx, sqlc.CreateSchedulerJobParams{ID: "wfdel1", OwnerKind: "user", ExecScope: "user", Name: "run wf", ScheduleEvery: "1h", Payload: jobPayload, DispatchKind: "workflow", SessionMode: "reuse", Enabled: true, AgentID: pgnull.Text(h.agentID), UserID: pgnull.Text(h.userID)})
	if err != nil {
		t.Fatalf("create scheduler job: %v", err)
	}
	if err := h.svc.Delete(h.ctx, h.userID, h.agentID, wf.ID); !errors.Is(err, ErrWorkflowHasSchedulerJob) {
		t.Fatalf("delete err = %v, want ErrWorkflowHasSchedulerJob", err)
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
	run, _, err := h.svc.Instantiate(h.ctx, InstantiateInput{UserID: h.userID, AgentID: h.agentID, WorkflowID: wf.ID, IdempotencyKey: "once"})
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
	if children[0].WorkflowID.Valid {
		t.Fatalf("nil-plan composite must stay planner-eligible, got workflow stamp %q", children[0].WorkflowID.String)
	}
}

// The dispatcher-hijack window: after a parent layer materializes, a composite
// child whose sub-plan is frozen sits draft/unplanned until the walk's next tx.
// The stamp written in the parent's tx must keep it out of the dispatcher scan,
// while a sibling without a frozen sub-plan stays eligible.
func TestFrozenCompositeChildExcludedFromDispatcherMidWalk(t *testing.T) {
	h := newWorkflowHarness(t)
	payload, _ := json.Marshal(FrozenPlan{})
	wf, err := h.q.CreateWorkflow(h.ctx, sqlc.CreateWorkflowParams{ID: uuid.NewString(), OwnerKind: OwnerAgent, UserID: pgnull.Text(h.userID), AgentID: pgnull.Text(h.agentID), Name: "midwalk", Version: 1, Intent: "midwalk", AcceptanceContract: []byte(`{}`), ConvergencePolicy: []byte(`{}`), Inputs: []byte(`[]`), PayloadFormat: PayloadFormatFrozenV0, Payload: payload, FullyFrozen: true})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	root, err := h.goals.CreateRoot(h.ctx, goal.CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "midwalk", Intent: "midwalk", Kind: goal.KindComposite, Required: true, WorkflowID: wf.ID, WorkflowVersion: wf.Version})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	content := goal.DecompositionContent{Children: []goal.ProposedChild{
		{Key: "frozen", Title: "Frozen", Intent: "frozen sub-plan pending", Kind: goal.KindComposite, Required: true},
		{Key: "dynamic", Title: "Dynamic", Intent: "planner decides", Kind: goal.KindComposite, Required: true},
	}}
	if err := h.goals.MaterializeFrozenLayer(h.ctx, root.ID, content, goal.FrozenStamp{WorkflowID: wf.ID, WorkflowVersion: wf.Version, ChildKeys: []string{"frozen"}}); err != nil {
		t.Fatalf("materialize layer: %v", err)
	}
	children, err := h.q.ListGoalChildren(h.ctx, pgnull.Text(root.ID))
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if children[0].WorkflowID.String != wf.ID || children[1].WorkflowID.Valid {
		t.Fatalf("stamp mismatch: frozen %q dynamic %q", children[0].WorkflowID.String, children[1].WorkflowID.String)
	}
	rows, err := h.q.ListDecomposableComposites(h.ctx, 100)
	if err != nil {
		t.Fatalf("list decomposable: %v", err)
	}
	seenDynamic := false
	for _, row := range rows {
		if row.ID == children[0].ID {
			t.Fatalf("frozen child %s returned as decomposable", row.ID)
		}
		if row.ID == children[1].ID {
			seenDynamic = true
		}
	}
	if !seenDynamic {
		t.Fatalf("dynamic child %s not returned as decomposable", children[1].ID)
	}
}

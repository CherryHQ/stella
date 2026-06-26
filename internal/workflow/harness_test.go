package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// trivialExecutor submits a deterministic output so a claimed leaf accepts under
// a trivial contract — the workflow tests only need leaves to reach accepted.
type trivialExecutor struct{}

func (trivialExecutor) Execute(_ context.Context, req goal.ExecutorRequest) (goal.ExecutorResult, error) {
	return goal.ExecutorResult{
		Submitted: true,
		Evidence:  goal.AttemptEvidence{Summary: "ok"},
		Output:    goal.AttemptOutput{Summary: "ok", Hash: "h-" + req.Attempt.ID},
	}, nil
}

// harness wires a migrated DB, a seeded user/agent, a goal bundle, and a workflow
// service over the same querier.
type harness struct {
	t       *testing.T
	db      *pgxpool.Pool
	q       *sqlc.Queries
	goalSvc *goal.GoalService
	worker  *goal.Worker
	wf      *Service
	userID  string
	agentID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	ctx := context.Background()

	userID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`,
		userID, "test-"+userID[:8]+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'test-agent', '/tmp')`,
		agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	q := sqlc.New(db)
	h := &harness{t: t, db: db, q: q, userID: userID, agentID: agentID}
	minter := h.sessionMinter()
	h.goalSvc = goal.New(db, q,
		goal.WithSessionMinter(minter),
		goal.WithPlanningSessionMinter(minter),
		goal.WithExecutor(trivialExecutor{}),
	)
	h.worker = goal.NewWorker(h.goalSvc, q)
	h.worker.SetHeartbeat(0)
	bundle := &goal.Service{Queries: q, Goal: h.goalSvc}
	h.wf = New(q, bundle)
	return h
}

func (h *harness) sessionMinter() goal.SessionMinter {
	return func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		sessionID := "wf-" + uuid.NewString()
		now := time.Now().UTC()
		if _, err := h.db.Exec(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active, created_at, updated_at)
			VALUES ($1, $2, 'minted', 'task', 'task', $3, $4, $5, $6, $7)`,
			uuid.NewString(), sessionID, agentID, userID, now, now, now); err != nil {
			return "", err
		}
		return sessionID, nil
	}
}

// acceptedComposite builds a composite root with one trivial leaf child and
// drives the whole thing to accepted: decompose, run the leaf, roll the parent up.
func (h *harness) acceptedComposite(t *testing.T) sqlc.AgentGoal {
	t.Helper()
	ctx := context.Background()
	root, err := h.goalSvc.CreateRoot(ctx, goal.CreateInput{
		UserID: h.userID, AgentID: h.agentID, Title: "build report",
		Intent: "produce the daily report", Kind: goal.KindComposite, Required: true,
	})
	if err != nil {
		t.Fatalf("CreateRoot: %v", err)
	}
	att, err := h.goalSvc.BeginDecomposition(ctx, root.ID)
	if err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("PromoteAttempt: %v", err)
	}
	if err := h.goalSvc.SubmitDecomposition(ctx, att.ID, goal.AttemptEvidence{}, goal.DecompositionContent{
		Children: []goal.ProposedChild{{Key: "a", Title: "step a", Intent: "do a", Kind: goal.KindLeaf, Required: true}},
	}); err != nil {
		t.Fatalf("SubmitDecomposition: %v", err)
	}
	kids, err := h.q.ListGoalChildren(ctx, pgnull.Text(root.ID))
	if err != nil || len(kids) != 1 {
		t.Fatalf("ListGoalChildren: %v (n=%d)", err, len(kids))
	}
	child := kids[0]
	if _, err := h.goalSvc.Claim(ctx, child.ID, "w-1", nil); err != nil {
		t.Fatalf("Claim child: %v", err)
	}
	atts, _ := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: child.ID})
	if err := h.worker.Run(ctx, child.ID, atts[0].ID, goal.Actor{Type: goal.ActorWorker}); err != nil {
		t.Fatalf("worker run child: %v", err)
	}
	if err := h.goalSvc.RollupAccept(ctx, root.ID); err != nil {
		t.Fatalf("RollupAccept root: %v", err)
	}
	g, err := h.q.GetGoal(ctx, root.ID)
	if err != nil {
		t.Fatalf("reload root: %v", err)
	}
	if g.Lifecycle != goal.LifecycleAccepted {
		t.Fatalf("root lifecycle=%q want accepted", g.Lifecycle)
	}
	return g
}

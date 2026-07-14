package goal

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz/policy"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// allowAttempt is the permissive durable-attempt authorizer for executor routing
// tests, which exercise turn/callback plumbing rather than policy. Authorization
// itself is covered against the real workerAuthorizer in worker_authz_test.go.
func allowAttempt(context.Context, sqlc.AgentGoal, string) error { return nil }

// scriptedExecutor is the test executor: by default it submits a trivial output
// (hash derived from the attempt id so each attempt is distinct); a test swaps
// fn to script a specific submit/fail/decomposition result. It replaces the
// agent layer so the worker path runs without any IO.
type scriptedExecutor struct {
	fn func(ExecutorRequest) (ExecutorResult, error)
}

func (e *scriptedExecutor) Execute(_ context.Context, req ExecutorRequest) (ExecutorResult, error) {
	if e.fn != nil {
		res, err := e.fn(req)
		if err != nil || !res.Submitted || req.OnSandboxSession == nil {
			return res, err
		}
		if err := req.OnSandboxSession(sandbox.NopSession()); err != nil {
			return ExecutorResult{}, err
		}
		return res, nil
	}
	if req.OnSandboxSession != nil {
		if err := req.OnSandboxSession(sandbox.NopSession()); err != nil {
			return ExecutorResult{}, err
		}
	}
	return ExecutorResult{
		Submitted: true,
		Evidence:  AttemptEvidence{Summary: "ok"},
		Output:    AttemptOutput{Summary: "ok", Hash: "h-" + req.Attempt.ID},
	}, nil
}

// harness wires a fresh migrated SQLite + a seeded user/agent + a fully
// constructed goal bundle with a controllable executor. It is the single
// seam every goal package test binds to; helpers here own the FK-correct
// seeding (auth_user, agent, ctx_conversation) so a test never touches raw SQL.
type harness struct {
	t       *testing.T
	db      *pgxpool.Pool
	q       *sqlc.Queries
	svc     *GoalService
	worker  *Worker
	bundle  *Service
	exec    *scriptedExecutor
	userID  string
	agentID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)

	ctx := context.Background()
	userID := uuid.NewString()
	if _, err := db.Exec(ctx,
		`INSERT INTO auth_user (id, email) VALUES ($1, $2)`,
		userID, "test-"+userID[:8]+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentID := uuid.NewString()
	// System scope so the folded-in agent gate passes for the owner and the CRUD
	// tests isolate goal ownership boundaries.
	if _, err := db.Exec(ctx,
		`INSERT INTO agent (id, name, workspace, scope) VALUES ($1, 'test-agent', '/tmp', 'system')`,
		agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	q := sqlc.New(db)
	h := &harness{t: t, db: db, q: q, exec: &scriptedExecutor{}, userID: userID, agentID: agentID}
	h.svc = New(db, q,
		WithSessionMinter(h.sessionMinter()),
		WithPlanningSessionMinter(h.sessionMinter()),
		WithExecutor(h.exec),
	)
	h.worker = NewWorker(h.svc, q)
	h.worker.SetHeartbeat(0) // no heartbeat goroutine in tests
	az := policy.New(db)
	h.bundle = NewBundle(q, h.svc, az, agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db), az))
	return h
}

// sessionMinter seeds a real ctx_conversation row so a goal created
// through CreateRoot satisfies the session_id FK (REFERENCES ctx_conversation).
func (h *harness) sessionMinter() SessionMinter {
	return func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		sessionID := "goal-" + uuid.NewString()
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

// createRoot mints a root goal in 'draft' with the given kind and
// contract. The zero-value contract is the trivial auto-accept; pass a populated
// one for gated acceptance.
func (h *harness) createRoot(kind string, contract AcceptanceContract) sqlc.AgentGoal {
	h.t.Helper()
	d, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:   h.userID,
		AgentID:  h.agentID,
		Title:    "root",
		Intent:   "test goal",
		Kind:     kind,
		Required: true,
		Contract: contract,
	})
	if err != nil {
		h.t.Fatalf("createRoot(%s): %v", kind, err)
	}
	return d
}

// activate runs the plan gate draft→ready.
func (h *harness) activate(id string) {
	h.t.Helper()
	if _, err := h.svc.Activate(context.Background(), id); err != nil {
		h.t.Fatalf("activate %s: %v", id, err)
	}
}

// runLeaf drives a ready leaf through one full worker attempt (Claim → promote →
// execute via the scripted executor → Submit → fold). It returns the attempt id.
func (h *harness) runLeaf(id string) string {
	h.t.Helper()
	ctx := context.Background()
	att, err := h.svc.Claim(ctx, id, "w-1", nil)
	if err != nil {
		h.t.Fatalf("claim %s: %v", id, err)
	}
	if err := h.worker.Run(ctx, id, att.ID, Actor{Type: ActorWorker}); err != nil {
		h.t.Fatalf("worker run %s/%s: %v", id, att.ID, err)
	}
	return att.ID
}

// get loads a goal, failing the test on a missing row.
func (h *harness) get(id string) sqlc.AgentGoal {
	h.t.Helper()
	d, err := getGoal(context.Background(), h.q, id)
	if err != nil {
		h.t.Fatalf("get %s: %v", id, err)
	}
	return d
}

// humanJudgmentContract is a single required human-verdict item — the gate that
// blocks a leaf on needs_verdict until a human submits a verdict.
func humanJudgmentContract() AcceptanceContract {
	return AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items: []AcceptanceItem{
			{ID: "review", Kind: ItemJudgment, Required: true, Authority: AuthorityHuman, Prompt: "approve?"},
		},
	}
}

// TestHarness_CreateActivateLeaf is the smoke test that proves the seam: a
// trivial leaf is born draft, activates to ready, and one worker attempt drives
// it to accepted (trivial contract auto-accepts on submit).
func TestHarness_CreateActivateLeaf(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	if d.Lifecycle != LifecycleDraft {
		t.Fatalf("new leaf lifecycle=%q want draft", d.Lifecycle)
	}
	h.activate(d.ID)
	if got := h.get(d.ID).Lifecycle; got != LifecyclePending {
		t.Fatalf("after activate lifecycle=%q want ready", got)
	}
	h.runLeaf(d.ID)
	if got := h.get(d.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("after run lifecycle=%q want accepted", got)
	}
}

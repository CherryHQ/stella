package goal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// pr717_authz_test.go covers the Goal-side authorization fixes: mutations gate on
// execute, the durable-worker PEP fresh-authorizes every attempt, collections and
// count/health exclude per-row-denied goals without swallowing PDP errors, and
// idempotency replay re-authorizes the existing row rather than the request.

// denyGoal installs an active custom policy denying `action` on Goal resources for
// any actor, optionally narrowed to durable facts by predicates.
func (h *harness) denyGoal(t *testing.T, action authz.Action, preds ...policy.Predicate) {
	t.Helper()
	ps := policy.NewService(policy.New(h.db))
	if _, _, err := ps.CreatePolicy(context.Background(), policy.PolicyInput{
		Name:       "deny-goal-" + uuid.NewString()[:8],
		Resource:   authz.ResourceGoal,
		Action:     action,
		Effect:     policy.EffectDeny,
		Subjects:   policy.AnySubject(),
		Predicates: preds,
	}); err != nil {
		t.Fatalf("create deny policy: %v", err)
	}
}

// seedSystemAgent inserts a fresh system-scoped agent and returns its id.
func (h *harness) seedSystemAgent(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.db.Exec(context.Background(),
		`INSERT INTO agent (id, name, workspace, scope) VALUES ($1, 'seed-agent', '/tmp', 'system')`, id); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return id
}

// seedRoot mints a root goal bound to agentID and stamps its created_at so a test
// can control list ordering deterministically.
func (h *harness) seedRoot(t *testing.T, agentID string, createdAt time.Time) sqlc.AgentGoal {
	t.Helper()
	ctx := context.Background()
	d, err := h.svc.CreateRoot(ctx, CreateInput{UserID: h.userID, AgentID: agentID, Title: "g", Kind: KindComposite, Required: true})
	if err != nil {
		t.Fatalf("seed root: %v", err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE agent_goal SET created_at = $2 WHERE id = $1`, d.ID, createdAt.UTC()); err != nil {
		t.Fatalf("stamp created_at: %v", err)
	}
	return d
}

// TestGoalExecuteDeniedBlocksMutations proves lifecycle mutations gate on execute
// (not read): a caller who may still read a goal cannot cancel/archive/abandon it
// once an execute deny is in force, and the denial stays opaque.
func TestGoalExecuteDeniedBlocksMutations(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	g := h.createRoot(KindComposite, AcceptanceContract{})
	h.denyGoal(t, authz.ActionExecute)

	// Read still succeeds — the deny is scoped to execute.
	if _, err := h.begin(t, h.userAuth(t, h.userID)).Get(ctx, g.ID); err != nil {
		t.Fatalf("owner Get after execute-deny: %v", err)
	}
	for name, fn := range map[string]func(*Access) error{
		"Cancel":  func(a *Access) error { return a.Cancel(ctx, g.ID, "") },
		"Archive": func(a *Access) error { return a.Archive(ctx, g.ID) },
		"Abandon": func(a *Access) error { return a.Abandon(ctx, g.ID, "") },
	} {
		if err := fn(h.begin(t, h.userAuth(t, h.userID))); !errors.Is(err, authz.ErrNotFound) {
			t.Fatalf("%s under execute-deny err=%v, want opaque not-found", name, err)
		}
	}
}

// TestGoalAddEdgeGatesBothGoalsOnExecute proves AddEdge authorizes a state change
// on the caller-supplied upstream too, so a denied upstream cannot be wired in.
func TestGoalAddEdgeGatesBothGoalsOnExecute(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	deniedAgent := h.seedSystemAgent(t)
	down := h.createRoot(KindComposite, AcceptanceContract{})
	up := h.seedRoot(t, deniedAgent, time.Now())
	// Deny execute only on the upstream's agent.
	h.denyGoal(t, authz.ActionExecute, policy.Eq("agent", deniedAgent))

	if _, err := h.begin(t, h.userAuth(t, h.userID)).AddEdge(ctx, down.ID, up.ID, EdgeHard, OnFailureBlock); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("AddEdge with denied upstream err=%v, want opaque not-found", err)
	}
}

// TestWorkerAuthorizerDeniedFailsClosed proves the durable-attempt PEP fresh-
// authorizes the persisted goal's execute on every dequeue and denies when policy
// forbids it — a queued attempt never runs the model.
func TestWorkerAuthorizerDeniedFailsClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	g := h.createRoot(KindComposite, AcceptanceContract{})
	wa := newWorkerAuthorizer(h.bundle.authz, h.bundle.agents)

	// With no deny in force the worker authorizes the persisted goal + agent.
	if err := wa.authorize(ctx, g); err != nil {
		t.Fatalf("worker authorize clean goal: %v", err)
	}
	// A goal-execute deny fails the same attempt closed on the next dequeue.
	h.denyGoal(t, authz.ActionExecute)
	if err := wa.authorize(ctx, g); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("worker authorize under execute-deny err=%v, want denied", err)
	}
}

// TestWorkerAuthorizerNilPEPFailsClosed proves a missing PEP denies rather than
// silently permitting an unauthorized attempt.
func TestWorkerAuthorizerNilPEPFailsClosed(t *testing.T) {
	ctx := context.Background()
	g := sqlc.AgentGoal{ID: uuid.NewString(), UserID: uuid.NewString(), AgentID: uuid.NewString(), Lifecycle: LifecycleActive}
	cases := map[string]*workerAuthorizer{
		"nil receiver":   nil,
		"nil authorizer": newWorkerAuthorizer(nil, nil),
	}
	for name, wa := range cases {
		if err := wa.authorize(ctx, g); err == nil {
			t.Fatalf("%s: authorize returned nil, want fail closed", name)
		}
	}
}

// TestGoalListChildrenExcludesDeniedChild proves a collection authorizes every
// returned row: a denied child is dropped even when the parent is readable.
func TestGoalListChildrenExcludesDeniedChild(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	deniedAgent := h.seedSystemAgent(t)

	parent := h.createRoot(KindComposite, AcceptanceContract{})
	cmp_decompose(t, h, parent.ID, DecompositionContent{
		Children: []ProposedChild{
			cmp_child("keep", true),
			cmp_child("hide", true),
		},
	})
	kids := cmp_children(t, h, parent.ID)
	if len(kids) != 2 {
		t.Fatalf("want 2 children, got %d", len(kids))
	}
	// Rebind one child to a denied agent, then deny read on that agent.
	hidden := kids[1]
	if _, err := h.db.Exec(ctx, `UPDATE agent_goal SET agent_id = $2 WHERE id = $1`, hidden.ID, deniedAgent); err != nil {
		t.Fatalf("rebind child agent: %v", err)
	}
	h.denyGoal(t, authz.ActionRead, policy.Eq("agent", deniedAgent))

	got, err := h.begin(t, h.userAuth(t, h.userID)).ListChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(got) != 1 || got[0].ID != kids[0].ID {
		t.Fatalf("ListChildren returned %d rows, want only the readable child", len(got))
	}
}

// TestGoalListPaginationFillsAcrossDeniedRows proves the offset-token list fills a
// full page by scanning past denied candidates, never loses a visible row behind a
// denied one, and advances the cursor; CountGoals excludes denied rows too.
func TestGoalListPaginationFillsAcrossDeniedRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	deniedAgent := h.seedSystemAgent(t)

	// Visible order (created_at DESC): a0 d0 a1 d1 a2 d2 a3 a4.
	base := time.Now().UTC()
	layout := []bool{false, true, false, true, false, true, false, false} // true = denied
	allowed := map[string]bool{}
	for i, denied := range layout {
		agentID := h.agentID
		if denied {
			agentID = deniedAgent
		}
		d := h.seedRoot(t, agentID, base.Add(-time.Duration(i)*time.Minute))
		if !denied {
			allowed[d.ID] = true
		}
	}
	h.denyGoal(t, authz.ActionRead, policy.Eq("agent", deniedAgent))

	acc := h.begin(t, h.userAuth(t, h.userID))

	// First page must fill to the limit (2) despite the denied candidate d0.
	page, nextOffset, hasMore, err := acc.ListGoals(ctx, GoalFilter{}, 2, 0)
	if err != nil {
		t.Fatalf("ListGoals page 1: %v", err)
	}
	if len(page) != 2 || !hasMore {
		t.Fatalf("page 1 len=%d hasMore=%v, want 2 and more", len(page), hasMore)
	}

	// Walk the rest, collecting every visible id and checking the cursor advances.
	seen := map[string]bool{}
	for _, d := range page {
		seen[d.ID] = true
	}
	offset, more := nextOffset, hasMore
	prev := int64(0)
	for more {
		if offset <= prev {
			t.Fatalf("cursor did not advance: prev=%d next=%d", prev, offset)
		}
		prev = offset
		page, offset, more, err = acc.ListGoals(ctx, GoalFilter{}, 2, offset)
		if err != nil {
			t.Fatalf("ListGoals page: %v", err)
		}
		for _, d := range page {
			if !allowed[d.ID] {
				t.Fatalf("denied goal %s leaked into a page", d.ID)
			}
			seen[d.ID] = true
		}
	}
	if len(seen) != len(allowed) {
		t.Fatalf("collected %d visible goals, want %d", len(seen), len(allowed))
	}

	n, err := acc.CountGoals(ctx, GoalFilter{})
	if err != nil {
		t.Fatalf("CountGoals: %v", err)
	}
	if n != int64(len(allowed)) {
		t.Fatalf("CountGoals=%d, want %d (denied rows excluded)", n, len(allowed))
	}
}

// TestGoalHealthExcludesDeniedRows proves the health aggregate is computed only
// over goals the caller may read.
func TestGoalHealthExcludesDeniedRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	deniedAgent := h.seedSystemAgent(t)

	now := time.Now().UTC()
	h.seedRoot(t, h.agentID, now)
	h.seedRoot(t, h.agentID, now)
	h.seedRoot(t, deniedAgent, now)
	h.denyGoal(t, authz.ActionRead, policy.Eq("agent", deniedAgent))

	report, err := h.begin(t, h.userAuth(t, h.userID)).HealthReport(ctx, HealthFilter{UserID: h.userID})
	if err != nil {
		t.Fatalf("HealthReport: %v", err)
	}
	if report.TotalGoals != 2 {
		t.Fatalf("health total_goals=%d, want 2 (denied row excluded)", report.TotalGoals)
	}
}

// TestGoalListPropagatesDecisionError proves an unexpected PDP error during
// per-row authorization fails the whole collection closed rather than silently
// dropping undecidable rows.
func TestGoalListPropagatesDecisionError(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.createRoot(KindComposite, AcceptanceContract{})
	// Let the collection-list decision through, then fail the first per-row decide.
	h.bundle.authz = &erroringAuthorizer{Authorizer: policy.New(h.db), pass: 1}

	_, _, _, err := h.begin(t, h.userAuth(t, h.userID)).ListGoals(ctx, GoalFilter{}, 10, 0)
	if err == nil || errors.Is(err, authz.ErrNotFound) || errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("ListGoals err=%v, want a propagated backend error", err)
	}
}

// TestGoalIdempotencyDenyAfterCreateReplay proves an idempotency replay re-checks
// the EXISTING row: a goal since custom-denied is not handed back.
func TestGoalIdempotencyDenyAfterCreateReplay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	auth := h.userAuth(t, h.userID)
	if _, err := h.begin(t, auth).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "first", Kind: KindComposite, IdempotencyKey: "dak"}); err != nil {
		t.Fatalf("first CreateGoal: %v", err)
	}
	h.denyGoal(t, authz.ActionRead, policy.Eq("is_owner", "true"))

	if _, err := h.begin(t, auth).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "replay", Kind: KindComposite, IdempotencyKey: "dak"}); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("deny-after-create replay err=%v, want opaque not-found", err)
	}
}

// TestGoalIdempotencyWrongAgentReplay proves a reused key that names a different
// agent authorizes the EXISTING row's durable facts, not the requested route: a
// deny keyed on the existing agent blocks the replay even though a create for the
// requested agent alone would be allowed.
func TestGoalIdempotencyWrongAgentReplay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	auth := h.userAuth(t, h.userID)
	existingAgent := h.agentID
	requestedAgent := h.seedSystemAgent(t)

	if _, err := h.begin(t, auth).CreateGoal(ctx, CreateInput{AgentID: existingAgent, Title: "first", Kind: KindComposite, IdempotencyKey: "wak"}); err != nil {
		t.Fatalf("first CreateGoal: %v", err)
	}
	// Deny read only on the EXISTING row's agent; the requested agent is clean.
	h.denyGoal(t, authz.ActionRead, policy.Eq("agent", existingAgent))

	if _, err := h.begin(t, auth).CreateGoal(ctx, CreateInput{AgentID: requestedAgent, Title: "replay", Kind: KindComposite, IdempotencyKey: "wak"}); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("wrong-agent replay err=%v, want opaque not-found bound to the existing row", err)
	}
}

// erroringAuthorizer lets the first `pass` decisions through, then fails every
// subsequent Decide — modeling a policy-decision-point backend error mid-use-case.
type erroringAuthorizer struct {
	authz.Authorizer
	pass int
}

func (a *erroringAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	eval, err := a.Authorizer.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	return &erroringEvaluation{Evaluation: eval, remaining: a.pass}, nil
}

type erroringEvaluation struct {
	authz.Evaluation
	remaining int
}

func (e *erroringEvaluation) Decide(req authz.Request) (authz.Decision, error) {
	if e.remaining <= 0 {
		return authz.Decision{}, errors.New("pdp backend unavailable")
	}
	e.remaining--
	return e.Evaluation.Decide(req)
}

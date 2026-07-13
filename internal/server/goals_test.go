package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// noopExecutor is a no-op goal.Executor. The goal HTTP handlers
// under test (create / get / add-edge) never reach the executor, so it never
// runs; it exists only to satisfy WithExecutor.
type noopExecutor struct{}

func (noopExecutor) Execute(context.Context, goal.ExecutorRequest) (goal.ExecutorResult, error) {
	return goal.ExecutorResult{}, nil
}

// setupGoalEnv boots a goal service over env.db and wires it into
// the server. The agent ServiceManager that goal.Boot needs is too heavy
// here, so the bundle is constructed directly with a stub session minter that
// seeds a real ctx_conversation row (the session_id FK) — mirroring the
// package's own harness — and a no-op executor.
func setupGoalEnv(t *testing.T) *testEnv {
	t.Helper()
	return setupGoalEnvWithOptions(t)
}

func setupGoalEnvWithOptions(t *testing.T, opts ...goal.Option) *testEnv {
	t.Helper()
	env := setupAdmin(t)

	mint := func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		sessionID := "goal-" + uuid.NewString()
		now := time.Now().UTC()
		if _, err := env.db.Exec(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active, created_at, updated_at)
			VALUES ($1, $2, 'minted', 'task', 'task', $3, $4, $5, $6, $7)`,
			uuid.NewString(), sessionID, agentID, userID, now, now, now); err != nil {
			return "", err
		}
		return sessionID, nil
	}

	q := sqlc.New(env.db)
	baseOpts := []goal.Option{
		goal.WithSessionMinter(mint),
		goal.WithPlanningSessionMinter(mint),
		goal.WithExecutor(noopExecutor{}),
	}
	baseOpts = append(baseOpts, opts...)
	svc := goal.New(env.db, q, baseOpts...)
	bundle := &goal.Service{Queries: q, Goal: svc}
	env.rebuild(t, func(d *server.Deps) { d.Goal = bundle })
	return env
}

// createGoal POSTs a leaf goal for the given bearer token and
// returns its id, failing the test on a non-201.
func createGoal(t *testing.T, env *testEnv, token, agentID, title string) string {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, token, "POST", "/api/goals", apitypes.CreateGoalRequest{
		AgentId: agentID,
		Title:   title,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create goal %q: status = %d, want %d (body: %s)", title, rr.Code, http.StatusCreated, rr.Body.String())
	}
	var d apitypes.Goal
	if err := json.Unmarshal(parseResponse(t, rr).Data, &d); err != nil {
		t.Fatalf("unmarshal created goal: %v", err)
	}
	if d.Id == "" {
		t.Fatalf("create goal %q: empty id (body: %s)", title, rr.Body.String())
	}
	return d.Id
}

func TestGoals_CreateDeterministicWithoutSandboxReturnsStructuredError(t *testing.T) {
	env := setupGoalEnvWithOptions(t, goal.WithCapabilityProbe(goal.CapabilityProbeFunc(func() bool { return false })))
	agentID := findStellaID(t, env)
	required := true
	command := "true"
	rr := doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/goals", apitypes.CreateGoalRequest{
		AgentId: agentID,
		Title:   "deterministic",
		AcceptanceContract: &apitypes.AcceptanceContract{Items: &[]apitypes.AcceptanceItem{{
			Id:       "cmd",
			Kind:     apitypes.AcceptanceItemKindDeterministic,
			Required: &required,
			Command:  &command,
		}}},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	var body struct {
		Error struct {
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if body.Error.Details["code"] != "deterministic_checks_unsupported" {
		t.Fatalf("details=%v want code deterministic_checks_unsupported", body.Error.Details)
	}
	if body.Error.Message == "" || body.Error.Details["fix"] == "" {
		t.Fatalf("error is not actionable: %+v", body.Error)
	}
}

// TestGoals_AddEdge_CrossTenant_404 is the IDOR regression: the AddEdge
// handler gates the caller-supplied upstream through the same ownership check as
// the downstream. Without that gate, user A could wire user B's goal as an
// upstream dependency and pull B's frozen accepted_output into A's attempt input.
func TestGoals_AddEdge_CrossTenant_404(t *testing.T) {
	env := setupGoalEnv(t)
	agentID := findStellaID(t, env)

	// User B + token. Reuse the same agent — there is no per-user agent ownership
	// gate at create time, so the only tenant boundary exercised here is the
	// goal's user_id.
	_, tokenB := createTestUserWithToken(t, env.authStore, env.oidcStore, "tenant-b", "user")

	// A's two own goals and B's one.
	dA := createGoal(t, env, env.bearerToken, agentID, "A-root")
	dA2 := createGoal(t, env, env.bearerToken, agentID, "A-sibling")
	dB := createGoal(t, env, tokenB, agentID, "B-root")

	// As A, declare dA depends on B's goal as upstream → 404 (upstream gated).
	rr := doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/goals/"+dA+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dB,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("A edge upstream=B: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "not_found" {
		t.Fatalf("A edge upstream=B: error = %q, want %q", got, "not_found")
	}

	// Reverse: as B, declare dB depends on A's goal (the downstream is B's
	// own, the upstream is A's) → the downstream loads, the upstream gate fails → 404.
	rr = doRequestWithSession(t, env.srv, tokenB, "POST", "/api/goals/"+dB+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dA,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("B edge upstream=A: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	// Cross-tenant downstream: as A, target B's goal as the path id → the
	// downstream gate fails first → 404.
	rr = doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/goals/"+dB+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dA,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("A edge downstream=B: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	// Same-tenant edge between A's two own siblings → 201.
	rr = doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/goals/"+dA+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dA2,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("A edge upstream=A-sibling: status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var edge apitypes.Edge
	if err := json.Unmarshal(parseResponse(t, rr).Data, &edge); err != nil {
		t.Fatalf("unmarshal edge: %v", err)
	}
	if edge.GoalId != dA || edge.UpstreamId != dA2 {
		t.Fatalf("edge = {downstream:%q upstream:%q}, want {%q %q}", edge.GoalId, edge.UpstreamId, dA, dA2)
	}
}

// TestGoals_GetCrossTenant_404 proves a goal owned by another
// tenant is reported as not-found (existence is not leaked).
func TestGoals_GetCrossTenant_404(t *testing.T) {
	env := setupGoalEnv(t)
	agentID := findStellaID(t, env)

	_, tokenB := createTestUserWithToken(t, env.authStore, env.oidcStore, "tenant-b-get", "user")
	dB := createGoal(t, env, tokenB, agentID, "B-private")

	rr := doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/goals/"+dB, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("A GET B's goal: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "not_found" {
		t.Fatalf("A GET B's goal: error = %q, want %q", got, "not_found")
	}
}

// TestGoals_CreateAndGet covers the happy path: POST creates a composite
// (every goal is planned first) and GET returns it for the owner.

func TestGoals_TimelinePaginationAndPost(t *testing.T) {
	env := setupGoalEnv(t)
	agentID := findStellaID(t, env)
	id := createGoal(t, env, env.bearerToken, agentID, "timeline")

	for _, text := range []string{"one", "two", "three"} {
		rr := doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/goals/"+id+"/timeline", apitypes.GoalTimelineMessageRequest{Text: text})
		if rr.Code != http.StatusCreated {
			t.Fatalf("POST timeline %q: status=%d body=%s", text, rr.Code, rr.Body.String())
		}
	}

	rr := doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/goals/"+id+"/timeline?page_size=2", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET timeline page1: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var page1 apitypes.GoalTimeline
	if err := json.Unmarshal(parseResponse(t, rr).Data, &page1); err != nil {
		t.Fatalf("unmarshal page1: %v", err)
	}
	if len(page1.Events) != 2 || page1.NextPageToken == nil || *page1.NextPageToken == "" {
		t.Fatalf("page1 len=%d next=%v want 2+next", len(page1.Events), page1.NextPageToken)
	}

	rr = doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/goals/"+id+"/timeline?page_size=2&page_token="+*page1.NextPageToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET timeline page2: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var page2 apitypes.GoalTimeline
	if err := json.Unmarshal(parseResponse(t, rr).Data, &page2); err != nil {
		t.Fatalf("unmarshal page2: %v", err)
	}
	if len(page2.Events) != 1 || page2.NextPageToken != nil {
		t.Fatalf("page2 len=%d next=%v want 1 no-next", len(page2.Events), page2.NextPageToken)
	}
	if page2.Events[0].EventType != apitypes.HumanMessage || page2.Events[0].Payload["text"] != "three" {
		t.Fatalf("page2 event=%+v want third human message", page2.Events[0])
	}
}

// TestGoalContinuationCommandsRequireFreshAgentUse proves that every command
// which can resume or release goal work re-checks the persisted goal agent
// before it writes any lifecycle, event, edge, or attempt state.
func TestGoalContinuationCommandsRequireFreshAgentUse(t *testing.T) {
	type command struct {
		name string
		path func(string) string
		body any
	}
	commands := []command{
		{name: "activate", path: func(id string) string { return "/api/goals/" + id + "/activate" }},
		{name: "reattempt", path: func(id string) string { return "/api/goals/" + id + "/reattempt" }},
		{name: "human_message", path: func(id string) string { return "/api/goals/" + id + "/timeline" }, body: apitypes.GoalTimelineMessageRequest{Text: "resume"}},
		{name: "verdict", path: func(id string) string { return "/api/goals/" + id + "/verdict" }, body: apitypes.VerdictRequest{ItemId: "item", Result: apitypes.VerdictRequestResult("pass")}},
		{name: "waive_edge", path: func(id string) string { return "/api/goals/" + id + "/edges/upstream/waive" }},
		{name: "approve_plan", path: func(id string) string { return "/api/goals/" + id + "/plan/approve" }},
		{name: "reject_plan", path: func(id string) string { return "/api/goals/" + id + "/plan/reject" }, body: apitypes.DecisionRequest{}},
	}

	for _, denial := range []string{"assignment_revoked", "custom_deny"} {
		for _, command := range commands {
			t.Run(denial+"/"+command.name, func(t *testing.T) {
				env := setupGoalEnv(t)
				ctx := context.Background()
				user, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "goal-use-"+uuid.NewString(), auth.RoleUser)
				agentID := uuid.NewString()
				if err := env.store.CreateAgent(ctx, config.Agent{ID: agentID, Name: "restricted", Model: "test/model", Scope: config.AgentScopeRestricted, Enabled: true}); err != nil {
					t.Fatalf("create restricted agent: %v", err)
				}
				if err := env.authStore.AssignAgent(ctx, user.ID, agentID); err != nil {
					t.Fatalf("assign restricted agent: %v", err)
				}
				id := createGoal(t, env, token, agentID, command.name)

				switch denial {
				case "assignment_revoked":
					if err := env.authStore.RemoveAgent(ctx, user.ID, agentID); err != nil {
						t.Fatalf("revoke assignment: %v", err)
					}
				case "custom_deny":
					_, _, err := policy.NewService(policy.New(env.db)).CreatePolicy(ctx, policy.PolicyInput{
						Name:       "deny goal continuation use",
						Resource:   authz.ResourceAgent,
						Action:     authz.ActionExecute,
						Effect:     policy.EffectDeny,
						Subjects:   policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
						Predicates: []policy.Predicate{policy.Eq("scope", "user")},
					})
					if err != nil {
						t.Fatalf("create custom deny: %v", err)
					}
				}

				before := snapshotGoalCommandState(t, env, id)
				rr := doRequestWithSession(t, env.srv, token, http.MethodPost, command.path(id), command.body)
				if rr.Code != http.StatusForbidden {
					t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
				}
				after := snapshotGoalCommandState(t, env, id)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("denied %s mutated goal state\nbefore: %#v\nafter:  %#v", command.name, before, after)
				}
			})
		}
	}
}

type goalCommandState struct {
	goal       sqlc.AgentGoal
	events     []sqlc.AgentGoalEvent
	acceptance []sqlc.AgentGoalAcceptanceEvent
	edges      []sqlc.AgentGoalEdge
	attempts   []sqlc.AgentGoalAttempt
}

func snapshotGoalCommandState(t *testing.T, env *testEnv, id string) goalCommandState {
	t.Helper()
	ctx := context.Background()
	q := sqlc.New(env.db)
	state := goalCommandState{}
	var err error
	if state.goal, err = q.GetGoal(ctx, id); err != nil {
		t.Fatalf("get goal state: %v", err)
	}
	if state.events, err = q.ListGoalEventByGoal(ctx, sqlc.ListGoalEventByGoalParams{GoalID: id, Limit: 100}); err != nil {
		t.Fatalf("list goal events: %v", err)
	}
	if state.acceptance, err = q.ListAcceptanceEventByGoal(ctx, id); err != nil {
		t.Fatalf("list acceptance events: %v", err)
	}
	if state.edges, err = q.ListEdgeByGoal(ctx, id); err != nil {
		t.Fatalf("list goal edges: %v", err)
	}
	if state.attempts, err = q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: id}); err != nil {
		t.Fatalf("list goal attempts: %v", err)
	}
	return state
}

func TestGoals_CreateAndGet(t *testing.T) {
	env := setupGoalEnv(t)
	agentID := findStellaID(t, env)

	id := createGoal(t, env, env.bearerToken, agentID, "my-goal")

	rr := doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/goals/"+id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET own goal: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var d apitypes.Goal
	if err := json.Unmarshal(parseResponse(t, rr).Data, &d); err != nil {
		t.Fatalf("unmarshal goal: %v", err)
	}
	if d.Id != id {
		t.Fatalf("GET goal id = %q, want %q", d.Id, id)
	}
	if d.UserId != env.adminUser.ID {
		t.Fatalf("GET goal user_id = %q, want %q", d.UserId, env.adminUser.ID)
	}
	if d.Title != "my-goal" {
		t.Fatalf("GET goal title = %q, want %q", d.Title, "my-goal")
	}
	if d.Kind != apitypes.GoalKindComposite {
		t.Fatalf("GET goal kind = %q, want %q", d.Kind, apitypes.GoalKindComposite)
	}
}

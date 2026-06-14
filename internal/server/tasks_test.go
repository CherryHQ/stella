package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/tasks"
)

// withTasks attaches a real tasks.Service to the test env so the new flat
// /api/tasks handlers can be exercised end-to-end.
func withTasks(t *testing.T, env *testEnv) {
	t.Helper()
	svc := tasks.New(tasks.BootConfig{DB: env.db})
	env.srv.SetTasksService(svc)
}

func taskTestAgentID(t *testing.T, env *testEnv) string {
	t.Helper()
	agents, err := env.store.ListAgents(context.Background())
	if err != nil || len(agents) == 0 {
		t.Fatalf("ListAgents: %v", err)
	}
	return agents[0].ID
}

func taskCreateBody(t *testing.T, env *testEnv, title string) apitypes.CreateTaskRequest {
	t.Helper()
	agentID := taskTestAgentID(t, env)
	return apitypes.CreateTaskRequest{Title: title, AgentId: &agentID}
}

func goalCreateBody(t *testing.T, env *testEnv, title string) apitypes.CreateGoalRequest {
	t.Helper()
	return apitypes.CreateGoalRequest{Title: title, AgentId: taskTestAgentID(t, env)}
}

func TestTasks_CreateGetRoundTrip(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	body := taskCreateBody(t, env, "hello")
	rr := doRequest(t, env, http.MethodPost, "/api/tasks", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var task apitypes.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if task.Id == "" || task.Title != "hello" {
		t.Fatalf("unexpected task: %+v", task)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/tasks/"+task.Id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTasks_CreateWithGoalIDLinksGoalChild(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	agents, err := env.store.ListAgents(context.Background())
	if err != nil || len(agents) == 0 {
		t.Fatalf("ListAgents: %v", err)
	}
	agentID := agents[0].ID

	goalBody := goalCreateBody(t, env, "parent")
	rr := doRequest(t, env, http.MethodPost, "/api/goals", goalBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create goal: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var goal apitypes.Goal
	if err := json.Unmarshal(rr.Body.Bytes(), &goal); err != nil {
		t.Fatalf("decode goal: %v", err)
	}

	taskBody := apitypes.CreateTaskRequest{Title: "child", AgentId: &agentID, GoalId: &goal.Id}
	rr = doRequest(t, env, http.MethodPost, "/api/tasks", taskBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var task apitypes.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.GoalId == nil || *task.GoalId != goal.Id {
		t.Fatalf("task goal_id = %v, want %s", task.GoalId, goal.Id)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/goals/"+goal.Id+"/tasks", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list goal tasks: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.TaskList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].Id != task.Id {
		t.Fatalf("goal tasks = %+v, want task %s", list.Tasks, task.Id)
	}
}

func TestTasks_CreateRejectsGoalIDFromDifferentAgent(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	agents, err := env.store.ListAgents(context.Background())
	if err != nil || len(agents) == 0 {
		t.Fatalf("ListAgents: %v", err)
	}
	agentID := agents[0].ID

	goalBody := goalCreateBody(t, env, "parent")
	rr := doRequest(t, env, http.MethodPost, "/api/goals", goalBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create goal: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var goal apitypes.Goal
	if err := json.Unmarshal(rr.Body.Bytes(), &goal); err != nil {
		t.Fatalf("decode goal: %v", err)
	}

	otherAgentID := "not-" + agentID
	taskBody := apitypes.CreateTaskRequest{Title: "child", AgentId: &otherAgentID, GoalId: &goal.Id}
	rr = doRequest(t, env, http.MethodPost, "/api/tasks", taskBody)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTasks_CreateRequiresTitle(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	rr := doRequest(t, env, http.MethodPost, "/api/tasks", apitypes.CreateTaskRequest{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTasks_GetUnknownReturns404(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	rr := doRequest(t, env, http.MethodGet, "/api/tasks/does-not-exist", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTasks_OtherUserCannotAccessTaskByID(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	_, ownerToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "task-owner", auth.RoleUser)
	_, otherToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "task-other", auth.RoleUser)

	active := true
	body := taskCreateBody(t, env, "owned")
	body.ActivateOnCreate = &active
	rr := doBearerRequest(t, env.srv, ownerToken, http.MethodPost, "/api/tasks", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var owned apitypes.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &owned); err != nil {
		t.Fatalf("decode task: %v", err)
	}

	checks := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/tasks/" + owned.Id, nil},
		{http.MethodPost, "/api/tasks/" + owned.Id + "/cancel", apitypes.CancelRequest{}},
		{http.MethodPost, "/api/tasks/" + owned.Id + "/reopen", apitypes.ReopenRequest{}},
		{http.MethodGet, "/api/tasks/" + owned.Id + "/readiness", nil},
		{http.MethodGet, "/api/tasks/" + owned.Id + "/events", nil},
		{http.MethodGet, "/api/tasks/" + owned.Id + "/runs", nil},
		{http.MethodGet, "/api/tasks/" + owned.Id + "/deps", nil},
		{http.MethodGet, "/api/tasks/" + owned.Id + "/reviews", nil},
		{http.MethodPost, "/api/tasks/" + owned.Id + "/deps", apitypes.AddDepRequest{DepTaskId: owned.Id}},
	}
	for _, tc := range checks {
		rr := doBearerRequest(t, env.srv, otherToken, tc.method, tc.path, tc.body)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
}

func TestTasks_CannotAddDependencyOnOtherUsersTask(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	_, ownerToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "dep-owner", auth.RoleUser)
	_, otherToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "dep-other", auth.RoleUser)

	create := func(token, title string) apitypes.Task {
		rr := doBearerRequest(t, env.srv, token, http.MethodPost, "/api/tasks", taskCreateBody(t, env, title))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %q: status=%d body=%s", title, rr.Code, rr.Body.String())
		}
		var task apitypes.Task
		if err := json.Unmarshal(rr.Body.Bytes(), &task); err != nil {
			t.Fatalf("decode %q: %v", title, err)
		}
		return task
	}
	downstream := create(ownerToken, "downstream")
	upstream := create(otherToken, "upstream")

	rr := doBearerRequest(t, env.srv, ownerToken, http.MethodPost, "/api/tasks/"+downstream.Id+"/deps", apitypes.AddDepRequest{DepTaskId: upstream.Id})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTasks_ListReturnsCreatedTasks(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	for _, title := range []string{"one", "two", "three"} {
		rr := doRequest(t, env, http.MethodPost, "/api/tasks", taskCreateBody(t, env, title))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %q: status=%d body=%s", title, rr.Code, rr.Body.String())
		}
	}
	rr := doRequest(t, env, http.MethodGet, "/api/tasks", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.TaskList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Tasks) != 3 {
		t.Fatalf("list length = %d, want 3", len(list.Tasks))
	}
}

func TestTasks_ReadinessForDraftReportsDraft(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	rr := doRequest(t, env, http.MethodPost, "/api/tasks", taskCreateBody(t, env, "x"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var task apitypes.Task
	_ = json.Unmarshal(rr.Body.Bytes(), &task)

	rr = doRequest(t, env, http.MethodGet, "/api/tasks/"+task.Id+"/readiness", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("readiness: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rd apitypes.Readiness
	if err := json.Unmarshal(rr.Body.Bytes(), &rd); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if rd.State != "draft" || rd.Dispatchable {
		t.Fatalf("readiness=%+v want draft/!dispatchable", rd)
	}
}

func TestTasks_CancelMovesDraftToCancelled(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	rr := doRequest(t, env, http.MethodPost, "/api/tasks", taskCreateBody(t, env, "x"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var task apitypes.Task
	_ = json.Unmarshal(rr.Body.Bytes(), &task)

	rr = doRequest(t, env, http.MethodPost, "/api/tasks/"+task.Id+"/cancel", apitypes.CancelRequest{})
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cancelled apitypes.Task
	_ = json.Unmarshal(rr.Body.Bytes(), &cancelled)
	if cancelled.Status != "cancelled" {
		t.Fatalf("status after cancel = %q, want cancelled", cancelled.Status)
	}
}

func TestTasks_AddDepCycleReturns409(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	create := func(title string) string {
		rr := doRequest(t, env, http.MethodPost, "/api/tasks", taskCreateBody(t, env, title))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %q: %d %s", title, rr.Code, rr.Body.String())
		}
		var task apitypes.Task
		_ = json.Unmarshal(rr.Body.Bytes(), &task)
		return task.Id
	}
	a := create("a")
	b := create("b")

	// Add A -> B (A depends on B). Then trying B -> A would close a cycle.
	rr := doRequest(t, env, http.MethodPost, "/api/tasks/"+a+"/deps", apitypes.AddDepRequest{DepTaskId: b})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add a->b: %d %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPost, "/api/tasks/"+b+"/deps", apitypes.AddDepRequest{DepTaskId: a})
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for cycle, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTasks_EventsListReturnsActivateEvent(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	tt := true
	body := taskCreateBody(t, env, "x")
	body.ActivateOnCreate = &tt
	rr := doRequest(t, env, http.MethodPost, "/api/tasks", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var task apitypes.Task
	_ = json.Unmarshal(rr.Body.Bytes(), &task)
	if task.Status != "ready" {
		t.Fatalf("status after activate=%q want ready", task.Status)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/tasks/"+task.Id+"/events", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("events: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var events apitypes.EventList
	_ = json.Unmarshal(rr.Body.Bytes(), &events)
	if len(events.Events) == 0 {
		t.Fatalf("expected at least one event, got none")
	}
}

// ---------------------------------------------------------------------------
// Goal HTTP tests
// ---------------------------------------------------------------------------

func TestGoals_CreateGetRoundTrip(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	body := goalCreateBody(t, env, "ship it")
	rr := doRequest(t, env, http.MethodPost, "/api/goals", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var goal apitypes.Goal
	if err := json.Unmarshal(rr.Body.Bytes(), &goal); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if goal.Id == "" || goal.Status != "draft" {
		t.Fatalf("unexpected goal: %+v", goal)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/goals/"+goal.Id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
}

func TestGoals_ListFiltersByAgentID(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	firstAgentID := taskTestAgentID(t, env)
	secondAgentID := createTestAgent(t, env, config.Agent{Name: "goals-second-agent"})

	createGoal := func(title, agentID string) apitypes.Goal {
		t.Helper()
		body := apitypes.CreateGoalRequest{Title: title, AgentId: agentID}
		rr := doRequest(t, env, http.MethodPost, "/api/goals", body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %s: status=%d body=%s", title, rr.Code, rr.Body.String())
		}
		var goal apitypes.Goal
		if err := json.Unmarshal(rr.Body.Bytes(), &goal); err != nil {
			t.Fatalf("decode goal: %v", err)
		}
		return goal
	}
	first := createGoal("first-agent-goal", firstAgentID)
	second := createGoal("second-agent-goal", secondAgentID)

	rr := doRequest(t, env, http.MethodGet, "/api/goals?agent_id="+secondAgentID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.GoalList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Goals) != 1 || list.Goals[0].Id != second.Id {
		t.Fatalf("filtered goals=%+v want only %s", list.Goals, second.Id)
	}
	if list.Goals[0].Id == first.Id {
		t.Fatalf("agent filter leaked first agent goal %s", first.Id)
	}
}

func TestGoals_Activate_DraftToRunning(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	rr := doRequest(t, env, http.MethodPost, "/api/goals", goalCreateBody(t, env, "x"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var goal apitypes.Goal
	_ = json.Unmarshal(rr.Body.Bytes(), &goal)

	rr = doRequest(t, env, http.MethodPost, "/api/goals/"+goal.Id+"/activate", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", rr.Code, rr.Body.String())
	}
	var fresh apitypes.Goal
	_ = json.Unmarshal(rr.Body.Bytes(), &fresh)
	if fresh.Status != "running" {
		t.Fatalf("goal status=%q want running", fresh.Status)
	}
}

func TestGoals_GetUnknown_404(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	rr := doRequest(t, env, http.MethodGet, "/api/goals/missing", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

func TestGoals_CancelRunningCascade(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	rr := doRequest(t, env, http.MethodPost, "/api/goals", goalCreateBody(t, env, "x"))
	var goal apitypes.Goal
	_ = json.Unmarshal(rr.Body.Bytes(), &goal)
	_ = doRequest(t, env, http.MethodPost, "/api/goals/"+goal.Id+"/activate", nil)

	rr = doRequest(t, env, http.MethodPost, "/api/goals/"+goal.Id+"/cancel", apitypes.CancelGoalRequest{})
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rr.Code, rr.Body.String())
	}
	var cancelled apitypes.Goal
	_ = json.Unmarshal(rr.Body.Bytes(), &cancelled)
	if cancelled.Status != "cancelled" {
		t.Fatalf("status after cancel=%q", cancelled.Status)
	}
}

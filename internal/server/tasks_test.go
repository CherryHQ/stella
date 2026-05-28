package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/tasks"
)

// withTasks attaches a real tasks.Service to the test env so the new flat
// /api/tasks handlers can be exercised end-to-end.
func withTasks(t *testing.T, env *testEnv) {
	t.Helper()
	svc := tasks.New(tasks.BootConfig{DB: env.db})
	env.srv.SetTasksService(svc)
}

func TestTasks_CreateGetRoundTrip(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	body := apitypes.CreateTaskRequest{Title: "hello"}
	rr := doRequest(t, env, http.MethodPost, "/api/tasks", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var task apitypes.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if task.Id == "" || task.Title != "hello" || task.OrgId != env.orgID {
		t.Fatalf("unexpected task: %+v", task)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/tasks/"+task.Id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: status=%d body=%s", rr.Code, rr.Body.String())
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

func TestTasks_ListReturnsCreatedTasks(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	for _, title := range []string{"one", "two", "three"} {
		rr := doRequest(t, env, http.MethodPost, "/api/tasks", apitypes.CreateTaskRequest{Title: title})
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
	if len(list.Items) != 3 {
		t.Fatalf("list length = %d, want 3", len(list.Items))
	}
}

func TestTasks_ReadinessForDraftReportsDraft(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)

	rr := doRequest(t, env, http.MethodPost, "/api/tasks", apitypes.CreateTaskRequest{Title: "x"})
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

	rr := doRequest(t, env, http.MethodPost, "/api/tasks", apitypes.CreateTaskRequest{Title: "x"})
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
		rr := doRequest(t, env, http.MethodPost, "/api/tasks", apitypes.CreateTaskRequest{Title: title})
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
	rr := doRequest(t, env, http.MethodPost, "/api/tasks", apitypes.CreateTaskRequest{Title: "x", ActivateOnCreate: &tt})
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
	if len(events.Items) == 0 {
		t.Fatalf("expected at least one event, got none")
	}
}

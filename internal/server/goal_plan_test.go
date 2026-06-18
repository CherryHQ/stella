package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
)

// goal_plan_test.go exercises the structured-plan HTTP lifecycle (#525 follow-up):
// stage -> accept/review -> materialize, plus the guard that the generic
// goal-review API refuses a plan review.

func structuredPlanContent() apitypes.PlanContent {
	design := apitypes.PlanItemRoleDesign
	impl := apitypes.PlanItemRoleImpl
	verify := apitypes.PlanItemRoleVerify
	return apitypes.PlanContent{Items: []apitypes.PlanItem{
		{Id: "d", Title: "design", Role: &design},
		{Id: "i", Title: "impl", Role: &impl, Deps: &[]string{"d"}, Criteria: &[]string{"compiles"}},
		{Id: "v", Title: "verify", Role: &verify, Deps: &[]string{"i"}},
	}}
}

func createDeferredGoal(t *testing.T, env *testEnv, title string) apitypes.Goal {
	t.Helper()
	body := goalCreateBody(t, env, title)
	mode := apitypes.CreateGoalRequestPlanModeDeferred
	body.PlanMode = &mode
	rr := doRequest(t, env, http.MethodPost, "/api/goals", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create deferred goal: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var g apitypes.Goal
	if err := json.Unmarshal(rr.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode goal: %v", err)
	}
	if g.Status != apitypes.GoalStatusDraft {
		t.Fatalf("deferred goal status=%q want draft", g.Status)
	}
	return g
}

// The none-path: PUT a structured plan, accept it, materialize it; the goal ends
// 'planned' with one task per item and no plan exists before PUT.
func TestGoalPlan_DeferredAcceptMaterialize(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	g := createDeferredGoal(t, env, "structured")

	// No plan yet on a deferred goal.
	rr := doRequest(t, env, http.MethodGet, "/api/goals/"+g.Id+"/plan", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get missing plan: status=%d want 404 body=%s", rr.Code, rr.Body.String())
	}

	put := apitypes.PutGoalPlanRequest{Content: structuredPlanContent()}
	rr = doRequest(t, env, http.MethodPut, "/api/goals/"+g.Id+"/plan", put)
	if rr.Code != http.StatusOK {
		t.Fatalf("put plan: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var plan apitypes.GoalPlan
	if err := json.Unmarshal(rr.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if plan.Status != "draft" || plan.PendingContent == nil || len(plan.PendingContent.Items) != 3 {
		t.Fatalf("unexpected plan after put: %+v", plan)
	}

	rr = doRequest(t, env, http.MethodPost, "/api/goals/"+g.Id+"/plan/accept", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("accept: status=%d body=%s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &plan)
	if plan.Status != "accepted" {
		t.Fatalf("plan status after accept=%q want accepted", plan.Status)
	}

	rr = doRequest(t, env, http.MethodPost, "/api/goals/"+g.Id+"/plan/materialize", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("materialize: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var goal apitypes.Goal
	if err := json.Unmarshal(rr.Body.Bytes(), &goal); err != nil {
		t.Fatalf("decode goal: %v", err)
	}
	if goal.Status != apitypes.GoalStatusPlanned {
		t.Fatalf("goal status after materialize=%q want planned", goal.Status)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/goals/"+g.Id+"/tasks", nil)
	var list apitypes.TaskList
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Tasks) != 3 {
		t.Fatalf("goal tasks=%d want 3", len(list.Tasks))
	}
}

// The human-review path: PUT a human-policy plan, submit it for review, approve
// the plan review, then materialize.
func TestGoalPlan_HumanReviewLifecycle(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	g := createDeferredGoal(t, env, "reviewed")

	human := apitypes.PutGoalPlanRequestReviewPolicyHuman
	put := apitypes.PutGoalPlanRequest{Content: structuredPlanContent(), ReviewPolicy: &human}
	if rr := doRequest(t, env, http.MethodPut, "/api/goals/"+g.Id+"/plan", put); rr.Code != http.StatusOK {
		t.Fatalf("put human plan: status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr := doRequest(t, env, http.MethodPost, "/api/goals/"+g.Id+"/plan/submit-review", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("submit-review: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rev apitypes.Review
	if err := json.Unmarshal(rr.Body.Bytes(), &rev); err != nil {
		t.Fatalf("decode review: %v", err)
	}
	if rev.Id == "" || rev.Status != "requested" {
		t.Fatalf("unexpected plan review: %+v", rev)
	}

	rr = doRequest(t, env, http.MethodPost, "/api/goals/"+g.Id+"/plan/reviews/"+rev.Id+"/approve", apitypes.ReviewDecisionRequest{})
	if rr.Code != http.StatusOK {
		t.Fatalf("approve plan review: status=%d body=%s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &rev)
	if rev.Status != "approved" {
		t.Fatalf("review status after approve=%q want approved", rev.Status)
	}

	rr = doRequest(t, env, http.MethodPost, "/api/goals/"+g.Id+"/plan/materialize", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("materialize after approve: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var goal apitypes.Goal
	_ = json.Unmarshal(rr.Body.Bytes(), &goal)
	if goal.Status != apitypes.GoalStatusPlanned {
		t.Fatalf("goal status=%q want planned", goal.Status)
	}
}

// Regression guard at the HTTP boundary: the generic goal-review approve endpoint
// must refuse a subject='plan' review (409), so it can never finish a goal off a
// plan review.
func TestGoalPlan_GenericApproveOnPlanReview_Conflict(t *testing.T) {
	env := setupAdmin(t)
	withTasks(t, env)
	g := createDeferredGoal(t, env, "guard")

	human := apitypes.PutGoalPlanRequestReviewPolicyHuman
	put := apitypes.PutGoalPlanRequest{Content: structuredPlanContent(), ReviewPolicy: &human}
	if rr := doRequest(t, env, http.MethodPut, "/api/goals/"+g.Id+"/plan", put); rr.Code != http.StatusOK {
		t.Fatalf("put plan: status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr := doRequest(t, env, http.MethodPost, "/api/goals/"+g.Id+"/plan/submit-review", nil)
	var rev apitypes.Review
	_ = json.Unmarshal(rr.Body.Bytes(), &rev)

	// Generic goal-review approve on the plan review → refused.
	rr = doRequest(t, env, http.MethodPost, "/api/goals/"+g.Id+"/reviews/"+rev.Id+"/approve", apitypes.ReviewDecisionRequest{})
	if rr.Code != http.StatusConflict {
		t.Fatalf("generic approve on plan review: status=%d want 409 body=%s", rr.Code, rr.Body.String())
	}
}

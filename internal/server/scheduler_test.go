package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz/policy"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/server"
	storepkg "github.com/CherryHQ/stella/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestScheduler builds a PEP-enabled scheduler service for server tests: it
// shares the test DB's agents/assignments through a fresh agent-access gate so
// the folded-in agent-read decision runs the same way it does in production.
func newTestScheduler(t *testing.T, db *pgxpool.Pool) *scheduler.Service {
	t.Helper()
	az := policy.New()
	agents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db), az)
	svc, err := scheduler.New(db, scheduler.WithAuthorization(az, agents))
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	return svc
}

// setupSchedulerEnv creates a testEnv with a live scheduler service that has
// one registered template ("test-template") and is started with the test DB.
func setupSchedulerEnv(t *testing.T) (*testEnv, *scheduler.Service, string) {
	t.Helper()
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	svc := newTestScheduler(t, env.db)
	if err := svc.RegisterTemplate(scheduler.JobTemplate{
		Key:             "test-template",
		Name:            "Test Template",
		Description:     "A template for testing",
		Message:         "test prompt content",
		DefaultSchedule: scheduler.Schedule{Every: "1h"},
		SessionMode:     scheduler.SessionReuse,
	}); err != nil {
		t.Fatalf("RegisterTemplate: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	env.rebuild(t, func(d *server.Deps) { d.Scheduler = svc })

	return env, svc, agentID
}

// TestListJobTemplates_NoSubscription verifies the template list is returned
// when the user has no subscriptions.
func TestListJobTemplates_NoSubscription(t *testing.T) {
	env, _, _ := setupSchedulerEnv(t)

	rr := doRequest(t, env, "GET", "/api/scheduler/job-templates", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	raw := parseListItems(t, rr, "job_templates")
	var templates []apitypes.JobTemplate
	if err := json.Unmarshal(raw, &templates); err != nil {
		t.Fatalf("unmarshal templates: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("len(templates) = %d, want 1", len(templates))
	}
	if templates[0].Key != "test-template" {
		t.Errorf("Key = %q, want %q", templates[0].Key, "test-template")
	}
	if templates[0].SubscribedJobId != nil {
		t.Errorf("SubscribedJobId = %v, want nil (not subscribed)", templates[0].SubscribedJobId)
	}
}

// TestListJobTemplates_WithSubscription verifies subscribed_job_id is set
// after a subscription is created.
func TestListJobTemplates_WithSubscription(t *testing.T) {
	env, _, agentID := setupSchedulerEnv(t)

	// Subscribe via POST.
	body := map[string]any{
		"template_key": "test-template",
		"every":        "2h",
	}
	rr := doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("subscribe POST: status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var created apitypes.Job
	if err := json.Unmarshal(parseResponse(t, rr).Data, &created); err != nil {
		t.Fatalf("unmarshal created job: %v", err)
	}

	rr = doRequest(t, env, "GET", "/api/scheduler/job-templates", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	raw := parseListItems(t, rr, "job_templates")
	var templates []apitypes.JobTemplate
	if err := json.Unmarshal(raw, &templates); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("len(templates) = %d, want 1", len(templates))
	}
	if templates[0].SubscribedJobId == nil {
		t.Fatal("SubscribedJobId is nil after subscribing")
	}
	if *templates[0].SubscribedJobId != created.Id {
		t.Errorf("SubscribedJobId = %q, want %q", *templates[0].SubscribedJobId, created.Id)
	}
	if templates[0].SubscribedAgentId == nil {
		t.Fatal("SubscribedAgentId is nil after subscribing")
	}
	if *templates[0].SubscribedAgentId != agentID {
		t.Errorf("SubscribedAgentId = %q, want %q", *templates[0].SubscribedAgentId, agentID)
	}
}

// TestListJobTemplates_503WhenDisabled verifies 503 when scheduler is nil.
func TestListJobTemplates_503WhenDisabled(t *testing.T) {
	env := setupAdmin(t)
	// Do not call SetSchedulerService — schedulerSvc remains nil.

	rr := doRequest(t, env, "GET", "/api/scheduler/job-templates", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}

// TestSubscribeViaTemplateKey tests the happy path.
func TestSubscribeViaTemplateKey(t *testing.T) {
	env, _, agentID := setupSchedulerEnv(t)

	body := map[string]any{
		"template_key": "test-template",
	}
	rr := doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var job apitypes.Job
	if err := json.Unmarshal(parseResponse(t, rr).Data, &job); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if job.TemplateKey == nil || *job.TemplateKey != "test-template" {
		t.Errorf("TemplateKey = %v, want %q", job.TemplateKey, "test-template")
	}
	// Message should be resolved from template.
	if job.Message != "test prompt content" {
		t.Errorf("Message = %q, want %q", job.Message, "test prompt content")
	}
}

// TestSubscribeDuplicate409 verifies that a second subscription to the same
// template returns 409.
func TestSubscribeDuplicate409(t *testing.T) {
	env, _, agentID := setupSchedulerEnv(t)

	body := map[string]any{"template_key": "test-template"}
	rr := doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first subscribe: status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	rr = doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs", body)
	if rr.Code != http.StatusConflict {
		t.Fatalf("second subscribe: status = %d, want %d (body: %s)", rr.Code, http.StatusConflict, rr.Body.String())
	}
}

// TestSubscribeUnknownTemplate404 verifies 404 for an unregistered template.
func TestSubscribeUnknownTemplate404(t *testing.T) {
	env, _, agentID := setupSchedulerEnv(t)

	body := map[string]any{"template_key": "no-such-template"}
	rr := doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs", body)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
}

// TestSubscribeWithMessage400 verifies that setting message alongside
// template_key is rejected with 400.
func TestSubscribeWithMessage400(t *testing.T) {
	env, _, agentID := setupSchedulerEnv(t)

	body := map[string]any{
		"template_key": "test-template",
		"message":      "custom message",
	}
	rr := doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

// TestPatchSubscriptionMessage400 verifies that PATCH to change the message on
// a subscription instance returns 400.
func TestPatchSubscriptionMessage400(t *testing.T) {
	env, _, agentID := setupSchedulerEnv(t)

	// Create subscription.
	body := map[string]any{"template_key": "test-template"}
	rr := doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("subscribe: status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var job apitypes.Job
	if err := json.Unmarshal(parseResponse(t, rr).Data, &job); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}

	// Attempt PATCH to change message.
	patch := map[string]any{"message": "override message"}
	rr = doRequest(t, env, "PATCH", "/api/agents/"+agentID+"/scheduler/jobs/"+job.Id, patch)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

// setupSchedulerEnvWithBuiltin creates an env with a registered system builtin.
// The builtin must be registered BEFORE Start, so we can't reuse setupSchedulerEnv.
func setupSchedulerEnvWithBuiltin(t *testing.T, builtinName string) (*testEnv, *scheduler.Service, string) {
	t.Helper()
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	svc := newTestScheduler(t, env.db)
	// Register template.
	if err := svc.RegisterTemplate(scheduler.JobTemplate{
		Key:             "test-template",
		Name:            "Test Template",
		Description:     "A template for testing",
		Message:         "test prompt content",
		DefaultSchedule: scheduler.Schedule{Every: "1h"},
		SessionMode:     scheduler.SessionReuse,
	}); err != nil {
		t.Fatalf("RegisterTemplate: %v", err)
	}
	// Register builtin BEFORE Start.
	if err := svc.RegisterBuiltin(scheduler.BuiltinJob{
		Name:     builtinName,
		Handler:  func(_ context.Context, _ scheduler.Job) error { return nil },
		Schedule: scheduler.Schedule{Every: "24h"},
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	// Persist the builtin row.
	svc.EnsureBuiltinJobs()

	env.rebuild(t, func(d *server.Deps) { d.Scheduler = svc })
	return env, svc, agentID
}

// TestSystemJobReturns404 verifies that system/plugin jobs return 404 on all
// agent-scoped scheduler endpoints.
func TestSystemJobReturns404(t *testing.T) {
	env, svc, agentID := setupSchedulerEnvWithBuiltin(t, "test-system-job")

	// Find the system job ID.
	var systemJobID string
	for _, j := range svc.ListJobs() {
		if j.OwnerKind == scheduler.JobOwnerSystem && j.Name == "test-system-job" {
			systemJobID = j.ID
			break
		}
	}
	if systemJobID == "" {
		t.Fatal("system job not found after EnsureBuiltinJobs")
	}

	base := "/api/agents/" + agentID + "/scheduler/jobs/" + systemJobID

	endpoints := []struct {
		method string
		path   string
		body   any
	}{
		{"GET", base, nil},
		{"PATCH", base, map[string]any{"name": "x"}},
		{"DELETE", base, nil},
		{"POST", base + "/run", nil},
		{"GET", base + "/runs", nil},
	}
	for _, tc := range endpoints {
		rr := doRequest(t, env, tc.method, tc.path, tc.body)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want %d (body: %s)", tc.method, tc.path, rr.Code, http.StatusNotFound, rr.Body.String())
		}
	}
}

// TestSystemJobNotInList verifies system jobs do not appear in the jobs list.
func TestSystemJobNotInList(t *testing.T) {
	env, svc, agentID := setupSchedulerEnvWithBuiltin(t, "invisible-system-job")

	// Confirm the builtin is actually in the scheduler service.
	found := false
	for _, j := range svc.ListJobs() {
		if j.OwnerKind == scheduler.JobOwnerSystem {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no system job found in service — test prerequisite not met")
	}

	rr := doRequest(t, env, "GET", "/api/agents/"+agentID+"/scheduler/jobs", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	raw := parseListItems(t, rr, "jobs")
	var jobs []apitypes.Job
	if err := json.Unmarshal(raw, &jobs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, j := range jobs {
		if j.OwnerKind != nil && (*j.OwnerKind == scheduler.JobOwnerSystem || *j.OwnerKind == scheduler.JobOwnerPlugin) {
			t.Errorf("system/plugin job %q appears in list (should be hidden)", j.Id)
		}
	}
}

// TestWriteEndpoints503WhenSchedulerDisabled verifies that all write endpoints
// return 503 when the scheduler service is not configured.
func TestWriteEndpoints503WhenSchedulerDisabled(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	// schedulerSvc is nil — no SetSchedulerService call.

	// Create a fake job ID (doesn't need to exist in DB for 503 to fire).
	fakeJobID := "fakejobid1"
	base := "/api/agents/" + agentID + "/scheduler/jobs"

	endpoints := []struct {
		method string
		path   string
		body   any
	}{
		{"POST", base, map[string]any{"name": "x", "message": "y", "every": "1h"}},
		{"PATCH", base + "/" + fakeJobID, map[string]any{"name": "x"}},
		{"DELETE", base + "/" + fakeJobID, nil},
	}
	for _, tc := range endpoints {
		rr := doRequest(t, env, tc.method, tc.path, tc.body)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want %d (body: %s)", tc.method, tc.path, rr.Code, http.StatusServiceUnavailable, rr.Body.String())
		}
	}
}

// TestGetSchedulerJob_ReturnsTemplateMessage verifies that GET on a
// subscription instance returns the template-resolved message and template_key.
func TestGetSchedulerJob_ReturnsTemplateMessage(t *testing.T) {
	env, _, agentID := setupSchedulerEnv(t)

	// Subscribe.
	rr := doRequest(t, env, "POST", "/api/agents/"+agentID+"/scheduler/jobs",
		map[string]any{"template_key": "test-template"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("subscribe: status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var created apitypes.Job
	if err := json.Unmarshal(parseResponse(t, rr).Data, &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}

	// GET the job.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/scheduler/jobs/"+created.Id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var got apitypes.Job
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal GET: %v", err)
	}
	if got.Message != "test prompt content" {
		t.Errorf("Message = %q, want %q", got.Message, "test prompt content")
	}
	if got.TemplateKey == nil || *got.TemplateKey != "test-template" {
		t.Errorf("TemplateKey = %v, want %q", got.TemplateKey, "test-template")
	}
}

// Compile-time check that *server.Server still satisfies ServerInterface.
// This prevents a silent regression where a generated method is left unimplemented.
var _ interface {
	ListJobTemplates(http.ResponseWriter, *http.Request)
} = (*server.Server)(nil)

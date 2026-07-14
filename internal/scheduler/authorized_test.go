package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	storepkg "github.com/CherryHQ/stella/internal/store"
)

func TestSchedulerIdempotencyConflictMatchesOnlyItsIndex(t *testing.T) {
	idem := &pgconn.PgError{Code: "23505", ConstraintName: "idx_sched_job_idem"}
	primary := &pgconn.PgError{Code: "23505", ConstraintName: "sched_job_pkey"}
	if !isSchedulerIdempotencyConflict(idem) || isSchedulerIdempotencyConflict(primary) {
		t.Fatal("scheduler idempotency conflict classifier matched the wrong unique index")
	}
}

// pepEnv builds a PEP-enabled scheduler service with two system-scoped agents so
// the folded-in agent-read gate always passes and the tests isolate job-resource
// ownership boundaries. Two real users exercise owner vs foreign.
type pepEnv struct {
	svc            *Service
	ownerA, ownerB string
}

func newPEPEnv(t *testing.T, templates ...JobTemplate) *pepEnv {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()
	store := storepkg.NewDBStore(db)
	oidc := appdb.NewOIDCStore(db)
	assign := appdb.NewAuthStore(db)

	ownerA, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "a@sched.test", Name: "a", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	ownerB, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "b@sched.test", Name: "b", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agent-a", "agent-b"} {
		if err := store.CreateAgent(ctx, config.Agent{ID: id, Name: id, Model: "p/m", Workspace: "/tmp/" + id, Scope: config.AgentScopeSystem, CreatorID: ownerA.ID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	svc, err := New(db, WithAgentAccess(agentaccess.NewService(store, assign)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tmpl := range templates {
		if err := svc.RegisterTemplate(tmpl); err != nil {
			t.Fatalf("RegisterTemplate(%q): %v", tmpl.Key, err)
		}
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return &pepEnv{svc: svc, ownerA: ownerA.ID, ownerB: ownerB.ID}
}

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	rs, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority(authz.UserID(id), rs, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func agentAuthority(t *testing.T, userID, agentID string) authz.Authority {
	t.Helper()
	a, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *pepEnv) begin(t *testing.T, authority authz.Authority) *Access {
	t.Helper()
	acc, err := e.svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return acc
}

func TestSchedulerBeginRejectsInvalidAuthority(t *testing.T) {
	e := newPEPEnv(t)
	if _, err := e.svc.Begin(context.Background(), authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Begin(zero) err=%v, want forbidden", err)
	}
}

func TestSchedulerDirectRules(t *testing.T) {
	e := newPEPEnv(t)
	adminRoles, err := authz.NewRoleSet(authz.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := authz.NewUserAuthority(authz.UserID(uuid.NewString()), adminRoles, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	roleless, err := authz.NewUserAuthority(authz.UserID(e.ownerA), authz.RoleSet{}, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	group, err := authz.NewGroupAuthority("group", authz.RoleSet{}, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	system, err := agentaccess.SystemAgentAuthority("scheduler")
	if err != nil {
		t.Fatal(err)
	}
	owned := Job{OwnerKind: JobOwnerUser, UserID: e.ownerA, AgentID: "agent-a"}
	foreign := Job{OwnerKind: JobOwnerUser, UserID: e.ownerB, AgentID: "agent-a"}
	systemJob := Job{OwnerKind: JobOwnerSystem, AgentID: "agent-a"}

	cases := []struct {
		name      string
		authority authz.Authority
		action    authz.Action
		job       Job
		want      bool
	}{
		{"admin lists", admin, authz.ActionList, Job{}, true},
		{"admin creates", admin, authz.ActionCreate, owned, true},
		{"admin reads", admin, authz.ActionRead, foreign, true},
		{"admin writes", admin, authz.ActionWrite, foreign, true},
		{"admin deletes", admin, authz.ActionDelete, foreign, true},
		{"admin executes", admin, authz.ActionExecute, foreign, true},
		{"admin cannot manage", admin, authz.ActionManage, foreign, false},
		{"owner lists", userAuthority(t, e.ownerA), authz.ActionList, Job{}, true},
		{"owner reads", userAuthority(t, e.ownerA), authz.ActionRead, owned, true},
		{"owner writes", userAuthority(t, e.ownerA), authz.ActionWrite, owned, true},
		{"owner deletes", userAuthority(t, e.ownerA), authz.ActionDelete, owned, true},
		{"owner executes", userAuthority(t, e.ownerA), authz.ActionExecute, owned, true},
		{"foreign user denied", userAuthority(t, e.ownerB), authz.ActionRead, owned, false},
		{"exact executor lists", agentAuthority(t, e.ownerA, "agent-a"), authz.ActionList, Job{}, true},
		{"exact executor creates", agentAuthority(t, e.ownerA, "agent-a"), authz.ActionCreate, owned, true},
		{"exact executor writes", agentAuthority(t, e.ownerA, "agent-a"), authz.ActionWrite, owned, true},
		{"wrong executor denied", agentAuthority(t, e.ownerA, "agent-b"), authz.ActionRead, owned, false},
		{"foreign executor denied", agentAuthority(t, e.ownerB, "agent-a"), authz.ActionExecute, owned, false},
		{"roleless user denied", roleless, authz.ActionList, Job{}, false},
		{"group denied", group, authz.ActionRead, owned, false},
		{"system reads system job", system, authz.ActionRead, systemJob, true},
		{"system executes system job", system, authz.ActionExecute, systemJob, true},
		{"system cannot read user job", system, authz.ActionRead, owned, false},
		{"invalid action denied", admin, authz.ActionInvalid, owned, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := e.begin(t, tc.authority)
			if got := acc.allowed(tc.action, tc.job); got != tc.want {
				t.Fatalf("allowed(%s, %+v) = %v, want %v", tc.action, tc.job, got, tc.want)
			}
		})
	}
}

func TestSchedulerCreateJobIdempotencyReturnsExistingJob(t *testing.T) {
	e := newPEPEnv(t)
	ctx := context.Background()
	acc := e.begin(t, agentAuthority(t, e.ownerA, "agent-a"))
	first, err := acc.CreateJob(ctx, "first", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "job-key")
	if err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}
	acc2 := e.begin(t, agentAuthority(t, e.ownerA, "agent-a"))
	second, err := acc2.CreateJob(ctx, "second", "msg", Schedule{Every: "2h"}, SessionReuse, "agent-a", "job-key")
	if err != nil {
		t.Fatalf("second CreateJob: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second=%s first=%s, want existing job", second.ID, first.ID)
	}
}

func TestSchedulerEnforcesOwnerAndExecutorBoundaries(t *testing.T) {
	e := newPEPEnv(t)
	ctx := context.Background()
	job, err := e.begin(t, userAuthority(t, e.ownerA)).CreateJob(ctx, "job-a", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// A foreign user cannot see, mutate, or delete the loaded job.
	foreign := userAuthority(t, e.ownerB)
	if _, err := e.begin(t, foreign).GetJob(ctx, "agent-a", job.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign GetJob err=%v, want forbidden", err)
	}
	if _, err := e.begin(t, foreign).UpdateJob(ctx, "agent-a", job.ID, JobUpdate{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign UpdateJob err=%v, want forbidden", err)
	}
	if err := e.begin(t, foreign).DeleteJob(ctx, "agent-a", job.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign DeleteJob err=%v, want forbidden", err)
	}

	// A delegated agent confined to agent-a cannot create/list on agent-b.
	scoped := agentAuthority(t, e.ownerA, "agent-a")
	if _, err := e.begin(t, scoped).CreateJob(ctx, "bad", "msg", Schedule{Every: "3h"}, SessionReuse, "agent-b", ""); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped CreateJob other agent err=%v, want forbidden", err)
	}
	// A delegated agent is confined to its own agent; listing agent-b is refused.
	if _, err := e.begin(t, scoped).ListJobs(ctx, "agent-b"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped ListJobs other agent err=%v, want forbidden", err)
	}

	// The owner (user actor) can read their own job.
	if _, err := e.begin(t, userAuthority(t, e.ownerA)).GetJob(ctx, "agent-a", job.ID); err != nil {
		t.Fatalf("owner GetJob: %v", err)
	}
}

func TestSchedulerHidesPlatformJobsFromUsers(t *testing.T) {
	e := newPEPEnv(t)
	ctx := context.Background()
	owner := agentAuthority(t, e.ownerA, "agent-a")

	systemJob, err := e.svc.EnsureJob("system-job", "system msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", ExecScopeSystem)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	pluginJob, err := e.svc.AddPluginJob(ctx, "plugin-a", "key", "runtime", "plugin-job", "desc", Schedule{Every: "2h"}, nil)
	if err != nil {
		t.Fatalf("AddPluginJob: %v", err)
	}
	userJob, err := e.begin(t, owner).CreateJob(ctx, "user-job", "msg", Schedule{Every: "3h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	for _, id := range []string{systemJob.ID, pluginJob.ID} {
		if _, err := e.begin(t, owner).GetJob(ctx, "agent-a", id); !errors.Is(err, authz.ErrNotFound) {
			t.Fatalf("platform GetJob(%s) err=%v, want not found", id, err)
		}
		if err := e.begin(t, owner).DeleteJob(ctx, "agent-a", id); !errors.Is(err, authz.ErrNotFound) {
			t.Fatalf("platform DeleteJob(%s) err=%v, want not found", id, err)
		}
	}

	jobs, err := e.begin(t, owner).ListJobs(ctx, "agent-a")
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != userJob.ID {
		t.Fatalf("ListJobs returned %+v, want only %s", jobs, userJob.ID)
	}
}

// TestSchedulerAuthorizeDurableFire proves the durable fire worker API
// reconstructs authority from durable ownership and directly rechecks both the
// job and current executor: user and system jobs authorize, plugin rows are
// rejected, and a removed executor stops a later fire.
func TestSchedulerAuthorizeDurableFire(t *testing.T) {
	e := newPEPEnv(t)
	ctx := context.Background()

	userJob, err := e.begin(t, userAuthority(t, e.ownerA)).CreateJob(ctx, "job", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	authority, err := e.svc.AuthorizeDurableFire(ctx, userJob)
	if err != nil {
		t.Fatalf("durable fire (user job): %v", err)
	}
	if authority.Kind() != authz.ActorAgent || string(authority.Actor().UserID()) != userJob.UserID || string(authority.Actor().AgentID()) != userJob.AgentID {
		t.Fatalf("user fire authority = %+v, want persisted owner and executor", authority.Actor())
	}
	if _, err := e.svc.AuthorizeDurableFire(ctx, Job{ID: "no-executor", OwnerKind: JobOwnerUser, UserID: e.ownerA}); err == nil {
		t.Fatal("durable fire without persisted executor must fail closed")
	}

	sysJob, err := e.svc.EnsureJob("sys", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", ExecScopeSystem)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	if _, err := e.svc.AuthorizeDurableFire(ctx, sysJob); err != nil {
		t.Fatalf("durable fire (system job): %v", err)
	}
	// Handler-mode system jobs run under the scheduler maintenance grant without
	// an Agent target.
	handlerJob, err := e.svc.EnsureJob("handler", "", Schedule{Every: "2h"}, SessionReuse, "", ExecScopeSystem)
	if err != nil {
		t.Fatalf("EnsureJob(handler): %v", err)
	}
	if _, err := e.svc.AuthorizeDurableFire(ctx, handlerJob); err != nil {
		t.Fatalf("durable fire (handler-mode system job): %v", err)
	}

	// Plugin-owned rows never dispatch through the agent-execution path.
	if _, err := e.svc.AuthorizeDurableFire(ctx, Job{ID: "p", OwnerKind: JobOwnerPlugin, AgentID: "agent-a"}); err == nil {
		t.Fatal("durable fire (plugin job) must fail closed")
	}

	// Reconstructing an authority is not enough: the fresh agent decision must
	// reject an executor that was removed after the job was persisted.
	if _, err := e.svc.db.Exec(ctx, `DELETE FROM agent WHERE id = 'agent-a'`); err != nil {
		t.Fatalf("revoke scheduler executor: %v", err)
	}
	if _, err := e.svc.AuthorizeDurableFire(ctx, userJob); err == nil {
		t.Fatal("durable fire authorized a revoked executor")
	}
}

// TestSchedulerAuthorizeDurableFireMissingAgentAccessFailsClosed proves a
// scheduler without its required Agent-domain port refuses every durable fire.
func TestSchedulerAuthorizeDurableFireMissingAgentAccessFailsClosed(t *testing.T) {
	svc, err := New(testDB(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.AuthorizeDurableFire(context.Background(), Job{ID: "j", OwnerKind: JobOwnerUser, UserID: "u", AgentID: "a"}); err == nil {
		t.Fatal("missing agent access must fail the durable fire closed")
	}
}

// TestSchedulerCreateJobIdempotencyReauthorizes proves an idempotency-key hit
// authorizes the existing durable row and its target, not the new request's
// agent field.
func TestSchedulerCreateJobIdempotencyReauthorizes(t *testing.T) {
	e := newPEPEnv(t)
	ctx := context.Background()

	if _, err := e.begin(t, userAuthority(t, e.ownerA)).CreateJob(ctx, "first", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "key"); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Same key, different requested agent: replay is authorized against the
	// persisted agent-a row, so the original job is returned.
	replayed, err := e.begin(t, userAuthority(t, e.ownerA)).CreateJob(ctx, "second", "msg", Schedule{Every: "2h"}, SessionReuse, "agent-b", "key")
	if err != nil {
		t.Fatalf("wrong-agent idempotency replay: %v", err)
	}
	if replayed.AgentID != "agent-a" {
		t.Fatalf("replay agent=%q, want persisted agent-a", replayed.AgentID)
	}
}

// TestSchedulerCreateWorkflowJobAuthorizesWorkflow proves the dispatch target
// is authorized through Workflow's direct domain port: a denied workflow refuses
// job creation, and a missing runner fails closed.
func TestSchedulerCreateWorkflowJobAuthorizesWorkflow(t *testing.T) {
	ctx := context.Background()

	e := newPEPEnv(t)
	e.svc.SetWorkflowRunner(&fakeWorkflowRunner{authorizeErr: authz.ErrNotFound})
	if _, err := e.begin(t, userAuthority(t, e.ownerA)).CreateWorkflowJob(ctx, "wf", Schedule{Every: "1h"}, SessionReuse, "agent-a", "wf-1", nil, false); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("workflow-denied CreateWorkflowJob err=%v, want not found", err)
	}

	e2 := newPEPEnv(t)
	if _, err := e2.begin(t, userAuthority(t, e2.ownerA)).CreateWorkflowJob(ctx, "wf", Schedule{Every: "1h"}, SessionReuse, "agent-a", "wf-1", nil, false); err == nil {
		t.Fatal("missing workflow runner must fail closed")
	}
}

// TestSchedulerSubscribedTemplates proves template-subscription metadata is
// resolved through the scheduler PEP: the owner sees their subscription and a
// foreign user sees none.
func TestSchedulerSubscribedTemplates(t *testing.T) {
	tmpl := JobTemplate{Key: "digest", Name: "Digest", Message: "do digest", DefaultSchedule: Schedule{Every: "1h"}, SessionMode: SessionReuse}
	e := newPEPEnv(t, tmpl)
	ctx := context.Background()

	if _, err := e.begin(t, userAuthority(t, e.ownerA)).Subscribe(ctx, "agent-a", "digest", Schedule{}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	subs, err := e.begin(t, userAuthority(t, e.ownerA)).SubscribedTemplates(ctx)
	if err != nil {
		t.Fatalf("SubscribedTemplates: %v", err)
	}
	if len(subs) != 1 || subs[0].JobKey != "digest" || subs[0].AgentID != "agent-a" {
		t.Fatalf("owner subs = %+v, want one digest job on agent-a", subs)
	}

	foreignSubs, err := e.begin(t, userAuthority(t, e.ownerB)).SubscribedTemplates(ctx)
	if err != nil {
		t.Fatalf("foreign SubscribedTemplates: %v", err)
	}
	if len(foreignSubs) != 0 {
		t.Fatalf("foreign subs = %+v, want none", foreignSubs)
	}
}

package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	storepkg "github.com/CherryHQ/stella/internal/store"
)

// pepEnv builds a PEP-enabled scheduler service with two system-scoped agents so
// the folded-in agent-read gate always passes and the tests isolate job-resource
// ownership boundaries. Two real users exercise owner vs foreign.
type pepEnv struct {
	svc            *Service
	az             *countingAuthorizer
	ownerA, ownerB string
}

func newPEPEnv(t *testing.T) *pepEnv {
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

	az := &countingAuthorizer{Authorizer: policy.New(db)}
	svc, err := New(db, WithAuthorization(az, agentaccess.NewService(store, assign, az)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return &pepEnv{svc: svc, az: az, ownerA: ownerA.ID, ownerB: ownerB.ID}
}

// countingAuthorizer proves the PEP owns exactly one Begin per use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
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

	// A foreign user cannot see, mutate, or delete the job (opaque not-found).
	foreign := userAuthority(t, e.ownerB)
	before := e.az.begins
	if _, err := e.begin(t, foreign).GetJob(ctx, "agent-a", job.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign GetJob err=%v, want forbidden", err)
	}
	if e.az.begins != before+1 {
		t.Fatalf("Begin count = %d, want 1 per use case", e.az.begins-before)
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

// A custom deny overrides the owner built-in against the durable facts.
func TestSchedulerCustomDenyHidesOwnJob(t *testing.T) {
	e := newPEPEnv(t)
	ctx := context.Background()
	db := e.svc.db
	job, err := e.begin(t, userAuthority(t, e.ownerA)).CreateJob(ctx, "job", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	ps := policy.NewService(policy.New(db))
	if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
		Name: "deny own scheduler read", Resource: authz.ResourceScheduler, Action: authz.ActionRead,
		Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []policy.Predicate{policy.Eq("is_owner", "true")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.begin(t, userAuthority(t, e.ownerA)).GetJob(ctx, "agent-a", job.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("custom deny GetJob err=%v, want forbidden", err)
	}
}

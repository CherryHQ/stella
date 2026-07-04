package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestAuthorizedMethodsRejectUnauthenticatedIdentity(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := authz.Identity{}

	if _, err := svc.As(ident).CreateJob(ctx, "job", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", ""); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("CreateJob err=%v, want unauthenticated", err)
	}
	if _, err := svc.As(ident).ListJobs(ctx, "agent-a"); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("ListJobs err=%v, want unauthenticated", err)
	}
	if _, err := svc.As(ident).GetJob(ctx, "agent-a", "missing"); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("GetJob err=%v, want unauthenticated", err)
	}
}

func TestCreateJobIdempotencyReturnsExistingJob(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := authz.Identity{UserID: "user-a", AgentID: "agent-a", AgentScoped: true}
	first, err := svc.As(ident).CreateJob(ctx, "first", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "job-key")
	if err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}
	second, err := svc.As(ident).CreateJob(ctx, "second", "msg", Schedule{Every: "2h"}, SessionReuse, "agent-a", "job-key")
	if err != nil {
		t.Fatalf("second CreateJob: %v", err)
	}
	if second.ID != first.ID || second.Name != first.Name {
		t.Fatalf("second=%+v first=%+v, want existing job", second, first)
	}
}

func TestAuthorizedMethodsEnforceSchedulerUserAndAgentBoundaries(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := authz.Identity{UserID: "user-a"}
	job, err := svc.As(ident).CreateJob(ctx, "job-a", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	foreign := authz.Identity{UserID: "user-b"}
	if _, err := svc.As(foreign).GetJob(ctx, "agent-a", job.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign GetJob err=%v, want forbidden", err)
	}
	if _, err := svc.As(foreign).UpdateJob(ctx, "agent-a", job.ID, JobUpdate{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign UpdateJob err=%v, want forbidden", err)
	}
	if err := svc.As(foreign).DeleteJob(ctx, "agent-a", job.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign DeleteJob err=%v, want forbidden", err)
	}

	otherAgent, err := svc.As(ident).CreateJob(ctx, "job-b", "msg", Schedule{Every: "2h"}, SessionReuse, "agent-b", "")
	if err != nil {
		t.Fatalf("CreateJob other agent: %v", err)
	}
	scoped := authz.Identity{UserID: "user-a", AgentID: "agent-a", AgentScoped: true}
	if _, err := svc.As(scoped).GetJob(ctx, "agent-b", otherAgent.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped GetJob other agent err=%v, want forbidden", err)
	}
	if _, err := svc.As(scoped).CreateJob(ctx, "bad", "msg", Schedule{Every: "3h"}, SessionReuse, "agent-b", ""); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped CreateJob other agent err=%v, want forbidden", err)
	}
	if _, err := svc.As(scoped).ListJobs(ctx, "agent-b"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped ListJobs other agent err=%v, want forbidden", err)
	}
}

func TestAuthorizedMethodsHidePlatformSchedulerJobsFromAgents(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := authz.Identity{UserID: "user-a", AgentID: "agent-a", AgentScoped: true}

	systemJob, err := svc.EnsureJob("system-job", "system msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", ExecScopeSystem)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	pluginJob, err := svc.AddPluginJob(ctx, "plugin-a", "key", "runtime", "plugin-job", "desc", Schedule{Every: "2h"}, nil)
	if err != nil {
		t.Fatalf("AddPluginJob: %v", err)
	}
	userJob, err := svc.As(ident).CreateJob(ctx, "user-job", "msg", Schedule{Every: "3h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	for _, id := range []string{systemJob.ID, pluginJob.ID} {
		if _, err := svc.As(ident).GetJob(ctx, "agent-a", id); !errors.Is(err, authz.ErrNotFound) {
			t.Fatalf("platform GetJob(%s) err=%v, want not found", id, err)
		}
		if _, err := svc.As(ident).UpdateJob(ctx, "agent-a", id, JobUpdate{}); !errors.Is(err, authz.ErrNotFound) {
			t.Fatalf("platform UpdateJob(%s) err=%v, want not found", id, err)
		}
		if err := svc.As(ident).DeleteJob(ctx, "agent-a", id); !errors.Is(err, authz.ErrNotFound) {
			t.Fatalf("platform DeleteJob(%s) err=%v, want not found", id, err)
		}
	}

	jobs, err := svc.As(ident).ListJobs(ctx, "agent-a")
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != userJob.ID {
		t.Fatalf("ListJobs returned %+v, want only %s", jobs, userJob.ID)
	}
}

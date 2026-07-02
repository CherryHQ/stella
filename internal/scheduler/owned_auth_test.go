package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/toolctx"
)

func TestOwnedMethodsRejectUnauthenticatedIdentity(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := toolctx.Identity{}

	if _, err := svc.CreateJobOwned(ctx, ident, "job", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", ""); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("CreateJobOwned err=%v, want unauthenticated", err)
	}
	if _, err := svc.ListJobsOwned(ctx, ident, "agent-a"); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("ListJobsOwned err=%v, want unauthenticated", err)
	}
	if _, err := svc.GetJobOwned(ctx, ident, "agent-a", "missing"); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("GetJobOwned err=%v, want unauthenticated", err)
	}
}

func TestCreateJobOwnedIdempotencyReturnsExistingJob(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := toolctx.Identity{UserID: "user-a", AgentID: "agent-a", AgentScoped: true}
	first, err := svc.CreateJobOwned(ctx, ident, "first", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "job-key")
	if err != nil {
		t.Fatalf("first CreateJobOwned: %v", err)
	}
	second, err := svc.CreateJobOwned(ctx, ident, "second", "msg", Schedule{Every: "2h"}, SessionReuse, "agent-a", "job-key")
	if err != nil {
		t.Fatalf("second CreateJobOwned: %v", err)
	}
	if second.ID != first.ID || second.Name != first.Name {
		t.Fatalf("second=%+v first=%+v, want existing job", second, first)
	}
}

func TestOwnedMethodsEnforceSchedulerUserAndAgentBoundaries(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := toolctx.Identity{UserID: "user-a"}
	job, err := svc.CreateJobOwned(ctx, ident, "job-a", "msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJobOwned: %v", err)
	}

	foreign := toolctx.Identity{UserID: "user-b"}
	if _, err := svc.GetJobOwned(ctx, foreign, "agent-a", job.ID); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("foreign GetJobOwned err=%v, want forbidden", err)
	}
	if _, err := svc.UpdateJobOwned(ctx, foreign, "agent-a", job.ID, JobUpdate{}); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("foreign UpdateJobOwned err=%v, want forbidden", err)
	}
	if err := svc.DeleteJobOwned(ctx, foreign, "agent-a", job.ID); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("foreign DeleteJobOwned err=%v, want forbidden", err)
	}

	otherAgent, err := svc.CreateJobOwned(ctx, ident, "job-b", "msg", Schedule{Every: "2h"}, SessionReuse, "agent-b", "")
	if err != nil {
		t.Fatalf("CreateJobOwned other agent: %v", err)
	}
	scoped := toolctx.Identity{UserID: "user-a", AgentID: "agent-a", AgentScoped: true}
	if _, err := svc.GetJobOwned(ctx, scoped, "agent-b", otherAgent.ID); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("scoped GetJobOwned other agent err=%v, want forbidden", err)
	}
	if _, err := svc.CreateJobOwned(ctx, scoped, "bad", "msg", Schedule{Every: "3h"}, SessionReuse, "agent-b", ""); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("scoped CreateJobOwned other agent err=%v, want forbidden", err)
	}
	if _, err := svc.ListJobsOwned(ctx, scoped, "agent-b"); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("scoped ListJobsOwned other agent err=%v, want forbidden", err)
	}
}

func TestOwnedMethodsHidePlatformSchedulerJobsFromAgents(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	ident := toolctx.Identity{UserID: "user-a", AgentID: "agent-a", AgentScoped: true}

	systemJob, err := svc.EnsureJob("system-job", "system msg", Schedule{Every: "1h"}, SessionReuse, "agent-a", ExecScopeSystem)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	pluginJob, err := svc.AddPluginJob(ctx, "plugin-a", "key", "runtime", "plugin-job", "desc", Schedule{Every: "2h"}, nil)
	if err != nil {
		t.Fatalf("AddPluginJob: %v", err)
	}
	userJob, err := svc.CreateJobOwned(ctx, ident, "user-job", "msg", Schedule{Every: "3h"}, SessionReuse, "agent-a", "")
	if err != nil {
		t.Fatalf("CreateJobOwned: %v", err)
	}

	for _, id := range []string{systemJob.ID, pluginJob.ID} {
		if _, err := svc.GetJobOwned(ctx, ident, "agent-a", id); !errors.Is(err, toolctx.ErrNotFound) {
			t.Fatalf("platform GetJobOwned(%s) err=%v, want not found", id, err)
		}
		if _, err := svc.UpdateJobOwned(ctx, ident, "agent-a", id, JobUpdate{}); !errors.Is(err, toolctx.ErrNotFound) {
			t.Fatalf("platform UpdateJobOwned(%s) err=%v, want not found", id, err)
		}
		if err := svc.DeleteJobOwned(ctx, ident, "agent-a", id); !errors.Is(err, toolctx.ErrNotFound) {
			t.Fatalf("platform DeleteJobOwned(%s) err=%v, want not found", id, err)
		}
	}

	jobs, err := svc.ListJobsOwned(ctx, ident, "agent-a")
	if err != nil {
		t.Fatalf("ListJobsOwned: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != userJob.ID {
		t.Fatalf("ListJobsOwned returned %+v, want only %s", jobs, userJob.ID)
	}
}

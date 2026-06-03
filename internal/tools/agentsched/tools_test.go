package agentsched

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/tools/toolctx"
)

func newTestSvc(t *testing.T) *scheduler.Service {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := scheduler.New(db)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := svc.StartEphemeral(context.Background()); err != nil {
		t.Fatalf("StartEphemeral: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return svc
}

func ctxWith(userID, agentID string) context.Context {
	ctx := context.Background()
	if userID != "" {
		ctx = memory.WithUserID(ctx, userID)
	}
	if agentID != "" {
		ctx = memory.WithAgentID(ctx, agentID)
	}
	return ctx
}

func TestSchemasHaveNoIdentityProps(t *testing.T) {
	defs := map[string]map[string]any{
		"scheduler_add":    addDef().InputSchema,
		"scheduler_list":   listDef().InputSchema,
		"scheduler_remove": removeDef().InputSchema,
	}
	for name, schema := range defs {
		props, _ := schema["properties"].(map[string]any)
		for _, banned := range []string{"agent_id", "user_id", "session_id"} {
			if _, ok := props[banned]; ok {
				t.Errorf("%s schema exposes identity prop %q", name, banned)
			}
		}
	}
}

func TestAddRequiresUserAndAgent(t *testing.T) {
	svc := newTestSvc(t)
	tl := &impl{svc: svc}
	args := map[string]any{"name": "n", "message": "m", "every": "1h"}

	// No agent in ctx.
	if _, err := tl.add(ctxWith("u1", ""), args); err == nil {
		t.Fatal("add without agent should error")
	}
	// No user in ctx — must not create a system-scoped job.
	if _, err := tl.add(ctxWith("", "a1"), args); err == nil {
		t.Fatal("add without user should error")
	}
	if len(svc.ListJobs()) != 0 {
		t.Fatalf("no job should be persisted on identity failure, got %d", len(svc.ListJobs()))
	}
}

func TestAddListRemoveHappyPath(t *testing.T) {
	svc := newTestSvc(t)
	tl := &impl{svc: svc}
	ctx := ctxWith("u1", "a1")

	out, err := tl.add(ctx, map[string]any{"name": "daily", "message": "do it", "every": "1h"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	var added jobView
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatalf("unmarshal add: %v", err)
	}
	if added.ID == "" {
		t.Fatal("expected job id")
	}

	listOut, err := tl.list(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var jobs []jobView
	if err := json.Unmarshal([]byte(listOut), &jobs); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != added.ID {
		t.Fatalf("list = %+v, want single job %s", jobs, added.ID)
	}

	if _, err := tl.remove(ctx, map[string]any{"id": added.ID}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got, _ := tl.list(ctx, nil); got != "[]" {
		t.Fatalf("list after remove = %s, want []", got)
	}
}

func TestListFiltersByIdentity(t *testing.T) {
	svc := newTestSvc(t)
	tl := &impl{svc: svc}
	if _, err := tl.add(ctxWith("u1", "a1"), map[string]any{"name": "j1", "message": "m", "every": "1h"}); err != nil {
		t.Fatalf("add u1/a1: %v", err)
	}
	if _, err := tl.add(ctxWith("u1", "a2"), map[string]any{"name": "j2", "message": "m", "every": "1h"}); err != nil {
		t.Fatalf("add u1/a2: %v", err)
	}
	if _, err := tl.add(ctxWith("u2", "a1"), map[string]any{"name": "j3", "message": "m", "every": "1h"}); err != nil {
		t.Fatalf("add u2/a1: %v", err)
	}
	out, _ := tl.list(ctxWith("u1", "a1"), nil)
	var jobs []jobView
	_ = json.Unmarshal([]byte(out), &jobs)
	if len(jobs) != 1 || jobs[0].Name != "j1" {
		t.Fatalf("list u1/a1 = %+v, want only j1", jobs)
	}
}

func TestRemoveOwnershipDenied(t *testing.T) {
	svc := newTestSvc(t)
	tl := &impl{svc: svc}
	out, err := tl.add(ctxWith("u1", "a1"), map[string]any{"name": "j1", "message": "m", "every": "1h"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	var j jobView
	_ = json.Unmarshal([]byte(out), &j)

	// cross-user
	if _, err := tl.remove(ctxWith("u2", "a1"), map[string]any{"id": j.ID}); !errors.Is(err, toolctx.ErrPermission) {
		t.Fatalf("cross-user remove err = %v, want ErrPermission", err)
	}
	// same-user / different-agent — the exact identity leak this guards.
	if _, err := tl.remove(ctxWith("u1", "a2"), map[string]any{"id": j.ID}); !errors.Is(err, toolctx.ErrPermission) {
		t.Fatalf("same-user/diff-agent remove err = %v, want ErrPermission", err)
	}
	if len(svc.ListJobs()) != 1 {
		t.Fatalf("job must survive denied removes, got %d", len(svc.ListJobs()))
	}
}

func TestRemovePluginOwnedRejected(t *testing.T) {
	svc := newTestSvc(t)
	tl := &impl{svc: svc}
	job, err := svc.AddPluginJob(context.Background(), "plug1", "k1", "rt", "pjob", "desc",
		scheduler.Schedule{Every: "1h"}, map[string]any{})
	if err != nil {
		t.Fatalf("AddPluginJob: %v", err)
	}
	if _, err := tl.remove(ctxWith("u1", "a1"), map[string]any{"id": job.ID}); !errors.Is(err, toolctx.ErrPermission) {
		t.Fatalf("remove plugin-owned err = %v, want ErrPermission", err)
	}
}

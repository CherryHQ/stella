package agenttask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/internal/tools/toolctx"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type harness struct {
	t       *testing.T
	db      *sql.DB
	facade  *tasks.ServiceFacade
	tl      *impl
	userID  string
	agentID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	userID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO auth_user (id, email) VALUES (?, ?)`,
		userID, "u-"+userID[:8]+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agent (id, name, workspace) VALUES (?, 'test-agent', '/tmp')`,
		agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	q := sqlc.New(db)
	svc := tasks.NewTransitionService(db, q)
	minter := func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		sessionID := "task-" + uuid.NewString()
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active, created_at, updated_at)
			VALUES (?, ?, ?, 'task', 'task', ?, ?, ?, ?, ?)`,
			uuid.NewString(), sessionID, "t", agentID, userID, now, now, now); err != nil {
			return "", err
		}
		return sessionID, nil
	}
	facade := tasks.NewServiceFacade(db, q, svc, minter)
	return &harness{t: t, db: db, facade: facade, tl: &impl{f: facade}, userID: userID, agentID: agentID}
}

func (h *harness) ctx(userID, agentID string) context.Context {
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
		"task_list":        taskListDef().InputSchema,
		"task_get":         taskGetDef().InputSchema,
		"task_create":      taskCreateDef().InputSchema,
		"task_cancel":      taskCancelDef().InputSchema,
		"task_events":      taskEventsDef().InputSchema,
		"task_deps":        taskDepsDef().InputSchema,
		"task_goal_create": goalCreateDef().InputSchema,
		"task_goal_list":   goalListDef().InputSchema,
		"task_goal_get":    goalGetDef().InputSchema,
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

func TestMissingIdentityErrors(t *testing.T) {
	h := newHarness(t)
	// No agent in ctx.
	if _, err := h.tl.create(h.ctx("u1", ""), map[string]any{"title": "x"}); err == nil {
		t.Fatal("create without agent should error")
	}
	// No user in ctx.
	if _, err := h.tl.list(h.ctx("", "a1"), map[string]any{}); err == nil {
		t.Fatal("list without user should error")
	}
}

func TestCreateOwnedByCtxAgentDraftDefault(t *testing.T) {
	h := newHarness(t)
	out, err := h.tl.create(h.ctx(h.userID, h.agentID), map[string]any{"title": "do thing"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var res struct {
		Task taskView `json:"task"`
		Note string   `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Task.Status != tasks.StatusDraft {
		t.Errorf("status=%q want draft (no activate)", res.Task.Status)
	}
	// Ownership came from ctx, not args: fetching as the same identity works.
	got, err := h.facade.GetTask(context.Background(), res.Task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.UserID != h.userID || got.AgentID != h.agentID {
		t.Errorf("owner=(%s,%s) want (%s,%s)", got.UserID, got.AgentID, h.userID, h.agentID)
	}
}

func TestCreateActivate(t *testing.T) {
	h := newHarness(t)
	out, err := h.tl.create(h.ctx(h.userID, h.agentID), map[string]any{"title": "go", "activate": true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var res struct {
		Task taskView `json:"task"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	if res.Task.Status == tasks.StatusDraft {
		t.Errorf("activate:true should leave draft, got %q", res.Task.Status)
	}
}

func (h *harness) seedProject(name string) string {
	h.t.Helper()
	id := uuid.NewString()
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO project (id, agent_id, user_id, name, base_dir) VALUES (?, ?, ?, ?, '/tmp')`,
		id, h.agentID, h.userID, name); err != nil {
		h.t.Fatalf("seed project %q: %v", name, err)
	}
	return id
}

func TestProjectIDDefaultVsOverride(t *testing.T) {
	h := newHarness(t)
	ctxProj := h.seedProject("ctx-proj")
	argProj := h.seedProject("arg-proj")

	// ctx project default.
	ctx := memory.WithProjectID(h.ctx(h.userID, h.agentID), ctxProj)
	out, err := h.tl.create(ctx, map[string]any{"title": "a"})
	if err != nil {
		t.Fatalf("create (ctx default): %v", err)
	}
	var res struct {
		Task taskView `json:"task"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	if res.Task.ProjectID != ctxProj {
		t.Errorf("project=%q want %q (ctx default)", res.Task.ProjectID, ctxProj)
	}
	// arg override.
	out2, err := h.tl.create(ctx, map[string]any{"title": "b", "project_id": argProj})
	if err != nil {
		t.Fatalf("create (arg override): %v", err)
	}
	var res2 struct {
		Task taskView `json:"task"`
	}
	_ = json.Unmarshal([]byte(out2), &res2)
	if res2.Task.ProjectID != argProj {
		t.Errorf("project=%q want %q (arg override)", res2.Task.ProjectID, argProj)
	}
}

func TestGetOwnershipDenied(t *testing.T) {
	h := newHarness(t)
	out, err := h.tl.create(h.ctx(h.userID, h.agentID), map[string]any{"title": "secret"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var res struct {
		Task taskView `json:"task"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	id := res.Task.ID

	// cross-user
	if _, err := h.tl.get(h.ctx("other-user", h.agentID), map[string]any{"task_id": id}); !errors.Is(err, toolctx.ErrPermission) {
		t.Fatalf("cross-user get err = %v, want ErrPermission", err)
	}
	// same-user / different-agent — the identity leak this migration targets.
	if _, err := h.tl.get(h.ctx(h.userID, "other-agent"), map[string]any{"task_id": id}); !errors.Is(err, toolctx.ErrPermission) {
		t.Fatalf("same-user/diff-agent get err = %v, want ErrPermission", err)
	}
	// cancel must also be denied and leave the task intact.
	if _, err := h.tl.cancel(h.ctx(h.userID, "other-agent"), map[string]any{"task_id": id}); !errors.Is(err, toolctx.ErrPermission) {
		t.Fatalf("same-user/diff-agent cancel err = %v, want ErrPermission", err)
	}
	got, _ := h.facade.GetTask(context.Background(), id)
	if got.Status == tasks.StatusCancelled {
		t.Error("task should not be cancelled by a denied actor")
	}
}

func TestListFiltersByIdentity(t *testing.T) {
	h := newHarness(t)
	// Seed a second agent owned by the same user and give it a task.
	agent2 := uuid.NewString()
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO agent (id, name, workspace) VALUES (?, 'agent-2', '/tmp')`, agent2); err != nil {
		t.Fatalf("seed agent-2: %v", err)
	}

	mine, err := h.tl.create(h.ctx(h.userID, h.agentID), map[string]any{"title": "mine"})
	if err != nil {
		t.Fatalf("create mine: %v", err)
	}
	var mineRes struct {
		Task taskView `json:"task"`
	}
	_ = json.Unmarshal([]byte(mine), &mineRes)

	if _, err := h.facade.CreateTask(context.Background(), tasks.CreateTaskInput{
		UserID: h.userID, AgentID: agent2, Title: "other-agent-task",
	}); err != nil {
		t.Fatalf("seed other-agent task: %v", err)
	}

	out, err := h.tl.list(h.ctx(h.userID, h.agentID), map[string]any{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var views []taskView
	_ = json.Unmarshal([]byte(out), &views)
	if len(views) != 1 || views[0].ID != mineRes.Task.ID {
		t.Fatalf("list = %+v, want only the ctx-agent task %s", views, mineRes.Task.ID)
	}
}

func TestGoalGetOwnershipDenied(t *testing.T) {
	h := newHarness(t)
	g, err := h.facade.CreateGoal(context.Background(), tasks.CreateGoalInput{
		UserID: h.userID, AgentID: h.agentID, Title: "goal",
	})
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	if _, err := h.tl.goalGet(h.ctx(h.userID, "other-agent"), map[string]any{"goal_id": g.ID}); !errors.Is(err, toolctx.ErrPermission) {
		t.Fatalf("same-user/diff-agent goalGet err = %v, want ErrPermission", err)
	}
}

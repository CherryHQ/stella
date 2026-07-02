package domaintools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/toolctx"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestDomainToolsDenyForeignResourceAccess(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	ownerUser := uuid.NewString()
	foreignUser := uuid.NewString()
	agentID := uuid.NewString()
	for _, userID := range []string{ownerUser, foreignUser} {
		if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'agent', '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	goalSvc := goal.New(db, q, goal.WithSessionMinter(func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		sessionID := "goal-" + uuid.NewString()
		now := time.Now().UTC()
		_, err := db.Exec(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active, created_at, updated_at)
			VALUES ($1, $2, 'goal', 'task', 'task', $3, $4, $5, $6, $7)`,
			uuid.NewString(), sessionID, agentID, userID, now, now, now)
		return sessionID, err
	}))
	goalBundle := &goal.Service{Queries: q, Goal: goalSvc}
	ownerGoal, err := goalBundle.CreateGoalOwned(ctx, ownerIdentity(ownerUser, agentID), goal.CreateInput{AgentID: agentID, Title: "owner goal", Kind: goal.KindComposite})
	if err != nil {
		t.Fatalf("create owner goal: %v", err)
	}

	schedulerSvc, err := scheduler.New(db)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := schedulerSvc.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { _ = schedulerSvc.Stop() })
	ownerJob, err := schedulerSvc.CreateJobOwned(ctx, ownerIdentity(ownerUser, agentID), "owner job", "run", scheduler.Schedule{Every: "1h"}, scheduler.SessionReuse, agentID)
	if err != nil {
		t.Fatalf("create owner job: %v", err)
	}

	foreignCtx := memory.WithAgentID(memory.WithUserID(ctx, foreignUser), agentID)
	goalTool := NewGoalTool(goalBundle)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "get", args: map[string]any{"action": "get", "id": ownerGoal.ID}},
		{name: "cancel", args: map[string]any{"action": "cancel", "id": ownerGoal.ID}},
	} {
		t.Run("goal "+tc.name, func(t *testing.T) {
			if out, err := goalTool.Execute(foreignCtx, tc.args); err == nil || !strings.Contains(err.Error(), "not found") || out != "" {
				t.Fatalf("Execute out=%q err=%v, want not-found denial", out, err)
			}
		})
	}
	if out, err := goalTool.Execute(foreignCtx, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("goal list foreign err=%v", err)
	} else if strings.Contains(out, ownerGoal.ID) {
		t.Fatalf("goal list leaked owner goal: %s", out)
	}
	if out, err := goalTool.Execute(foreignCtx, map[string]any{"action": "create", "title": "foreign goal"}); err != nil {
		t.Fatalf("goal create foreign own resource err=%v", err)
	} else if strings.Contains(out, ownerGoal.ID) {
		t.Fatalf("goal create leaked owner goal: %s", out)
	}

	schedulerTool := NewSchedulerTool(schedulerSvc)
	for _, action := range []string{"get", "update", "delete", "pause", "resume"} {
		t.Run("scheduler "+action, func(t *testing.T) {
			args := map[string]any{"action": action, "id": ownerJob.ID}
			if action == "update" {
				args["name"] = "new name"
			}
			if out, err := schedulerTool.Execute(foreignCtx, args); err == nil || !strings.Contains(err.Error(), "access denied") || out != "" {
				t.Fatalf("Execute out=%q err=%v, want forbidden denial", out, err)
			}
		})
	}
	if out, err := schedulerTool.Execute(foreignCtx, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("scheduler list foreign err=%v", err)
	} else if strings.Contains(out, ownerJob.ID) {
		t.Fatalf("scheduler list leaked owner job: %s", out)
	}
	out, err := schedulerTool.Execute(foreignCtx, map[string]any{"action": "create", "name": "foreign job", "message": "run", "every": "1h"})
	if err != nil {
		t.Fatalf("scheduler create foreign own resource err=%v", err)
	}
	var created schedulerResponse
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == ownerJob.ID {
		t.Fatalf("scheduler create response=%s err=%v", out, err)
	}
}

func ownerIdentity(userID, agentID string) toolctx.Identity {
	return toolctx.Identity{UserID: userID, AgentID: agentID, AgentScoped: true}
}

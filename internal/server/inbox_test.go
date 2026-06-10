package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
)

func TestListAgentsIncludesLastActive(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	lastActive := "2026-06-10 03:04:05"

	_, err := env.db.Exec(`
		INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
		VALUES (?, ?, 'main', 'web', 'main', ?, ?, ?)
	`, uuid.NewString(), "last-active-session", agentID, env.adminUser.ID, lastActive)
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	rr := doRequest(t, env, http.MethodGet, "/api/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list agents: status=%d body=%s", rr.Code, rr.Body.String())
	}
	items := parseListItems(t, rr, "agents")
	var agents []config.Agent
	if err := json.Unmarshal(items, &agents); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	for _, agent := range agents {
		if agent.ID == agentID {
			if agent.LastActive == nil || agent.LastActive.UTC().Format("2006-01-02 15:04:05") != lastActive {
				t.Fatalf("last_active = %v, want %s", agent.LastActive, lastActive)
			}
			return
		}
	}
	t.Fatalf("agent %s not found in %+v", agentID, agents)
}

func TestListInboxAggregatesAttentionItems(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	seedInboxRows(t, env, agentID)

	rr := doRequest(t, env, http.MethodGet, "/api/inbox?page_size=3", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list inbox: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.InboxList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("items len = %d, want 3: %+v", len(list.Items), list.Items)
	}
	if list.NextPageToken == nil || *list.NextPageToken == "" {
		t.Fatalf("next_page_token missing: %+v", list)
	}

	seen := map[apitypes.InboxItemKind]bool{}
	for _, item := range list.Items {
		seen[item.Kind] = true
		if item.TargetPath == "" || item.SourceId == "" {
			t.Fatalf("item missing navigation fields: %+v", item)
		}
	}
	for _, kind := range []apitypes.InboxItemKind{
		apitypes.InboxItemKindBlocked,
		apitypes.InboxItemKindReview,
		apitypes.InboxItemKindFailed,
	} {
		if !seen[kind] {
			t.Fatalf("missing kind %q in %+v", kind, list.Items)
		}
	}

	rr = doRequest(t, env, http.MethodGet, "/api/inbox?agent_id=missing", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("filtered inbox: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if raw := parseListItems(t, rr, "items"); string(raw) != "[]" {
		t.Fatalf("filtered items = %s, want []", raw)
	}
}

func seedInboxRows(t *testing.T, env *testEnv, agentID string) {
	t.Helper()
	ctx := context.Background()
	userID := env.adminUser.ID

	insertTask := func(taskID, sessionID, title, createdAt string) {
		t.Helper()
		_, err := env.db.ExecContext(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
			VALUES (?, ?, ?, 'task', 'task', ?, ?, ?)
		`, uuid.NewString(), sessionID, title, agentID, userID, createdAt)
		if err != nil {
			t.Fatalf("seed conversation %s: %v", sessionID, err)
		}
		_, err = env.db.ExecContext(ctx, `
			INSERT INTO agent_task (id, user_id, agent_id, session_id, title, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'running', ?, ?)
		`, taskID, userID, agentID, sessionID, title, createdAt, createdAt)
		if err != nil {
			t.Fatalf("seed task %s: %v", taskID, err)
		}
	}

	insertTask("task-blocked", "task:block", "Blocked task", "2026-06-10 01:00:00")
	_, err := env.db.ExecContext(ctx, `
		INSERT INTO agent_task_blocker (id, task_id, kind, status, question, created_at)
		VALUES ('blocker-1', 'task-blocked', 'input', 'open', 'Need a choice', '2026-06-10 04:00:00')
	`)
	if err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	_, err = env.db.ExecContext(ctx, `UPDATE agent_task SET active_blocker_id = 'blocker-1' WHERE id = 'task-blocked'`)
	if err != nil {
		t.Fatalf("link blocker: %v", err)
	}

	insertTask("task-review", "task:review", "Review task", "2026-06-10 02:00:00")
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO agent_review (id, task_id, reviewer_type, status, summary, created_at, updated_at)
		VALUES ('review-1', 'task-review', 'human', 'requested', 'Check output', '2026-06-10 03:00:00', '2026-06-10 03:00:00')
	`)
	if err != nil {
		t.Fatalf("seed review: %v", err)
	}
	_, err = env.db.ExecContext(ctx, `UPDATE agent_task SET active_review_id = 'review-1' WHERE id = 'task-review'`)
	if err != nil {
		t.Fatalf("link review: %v", err)
	}

	insertTask("task-failed", "task:failed", "Failed task", "2026-06-10 01:30:00")
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO agent_task_run (
			id, task_id, user_id, agent_id, kind, attempt_no, status, session_id,
			error, started_at, finished_at, created_at, updated_at
		)
		VALUES (
			'run-1', 'task-failed', ?, ?, 'worker', 1, 'failed', 'task:failed',
			'boom', '2026-06-10 02:20:00', '2026-06-10 02:30:00', '2026-06-10 02:20:00', '2026-06-10 02:30:00'
		)
	`, userID, agentID)
	if err != nil {
		t.Fatalf("seed task run: %v", err)
	}

	_, err = env.db.ExecContext(ctx, `
		INSERT INTO sched_job (
			id, name, agent_id, user_id, created_at, updated_at
		)
		VALUES ('job-1', 'Daily check', ?, ?, '2026-06-10 01:00:00', '2026-06-10 01:00:00')
	`, agentID, userID)
	if err != nil {
		t.Fatalf("seed sched job: %v", err)
	}
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO sched_job_run (id, job_id, status, started_at, finished_at, error, user_id)
		VALUES ('sched-run-1', 'job-1', 'failed', '2026-06-10 02:40:00', '2026-06-10 02:50:00', 'schedule boom', ?)
	`, userID)
	if err != nil {
		t.Fatalf("seed sched run: %v", err)
	}
}

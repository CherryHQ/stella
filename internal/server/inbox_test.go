package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
)

// inboxTS renders a naive-UTC timestamp offset from now, matching the
// datetime('now') schema defaults; tests stay valid as the clock advances.
func inboxTS(offset time.Duration) string {
	return time.Now().UTC().Add(offset).Format("2006-01-02 15:04:05")
}

func TestListAgentsIncludesLastActive(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	lastActive := inboxTS(-3 * time.Hour)

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

func TestListInboxEmptyReturnsArray(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, http.MethodGet, "/api/inbox", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty inbox: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Fatalf("empty inbox must serialize items as [], got %s", body)
	}
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
	ids := map[string]bool{}
	for _, item := range list.Items {
		seen[item.Kind] = true
		ids[item.Id] = true
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

	// Second page: continues without overlap and exhausts the four seeded items.
	rr = doRequest(t, env, http.MethodGet, "/api/inbox?page_size=3&page_token="+*list.NextPageToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("second page: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var page2 apitypes.InboxList
	if err := json.Unmarshal(rr.Body.Bytes(), &page2); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(page2.Items) != 1 {
		t.Fatalf("second page len = %d, want 1: %+v", len(page2.Items), page2.Items)
	}
	if ids[page2.Items[0].Id] {
		t.Fatalf("second page repeats item %s", page2.Items[0].Id)
	}
	if page2.NextPageToken != nil {
		t.Fatalf("second page should be the last, got token %q", *page2.NextPageToken)
	}

	// Positive agent filter returns the same items as unfiltered.
	rr = doRequest(t, env, http.MethodGet, "/api/inbox?page_size=100&agent_id="+agentID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("agent filter: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var filtered apitypes.InboxList
	if err := json.Unmarshal(rr.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if len(filtered.Items) != 4 {
		t.Fatalf("agent-filtered items len = %d, want 4: %+v", len(filtered.Items), filtered.Items)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/inbox?agent_id=missing", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("filtered inbox: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if raw := parseListItems(t, rr, "items"); string(raw) != "[]" {
		t.Fatalf("filtered items = %s, want []", raw)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/inbox?page_token=not-a-token", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid page token: status=%d, want 400", rr.Code)
	}
}

// Agent-policy reviews and reviews left behind by cancelled tasks must not
// surface in the human inbox.
func TestListInboxExcludesNonActionableReviews(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	ctx := context.Background()
	userID := env.adminUser.ID

	seedReview := func(taskID, reviewID, taskStatus, reviewerType string) {
		t.Helper()
		created := inboxTS(-1 * time.Hour)
		_, err := env.db.ExecContext(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
			VALUES (?, ?, ?, 'task', 'task', ?, ?, ?)
		`, uuid.NewString(), "task:"+taskID, taskID, agentID, userID, created)
		if err != nil {
			t.Fatalf("seed conversation %s: %v", taskID, err)
		}
		_, err = env.db.ExecContext(ctx, `
			INSERT INTO agent_task (id, user_id, agent_id, session_id, title, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, taskID, userID, agentID, "task:"+taskID, taskID, taskStatus, created, created)
		if err != nil {
			t.Fatalf("seed task %s: %v", taskID, err)
		}
		_, err = env.db.ExecContext(ctx, `
			INSERT INTO agent_review (id, task_id, reviewer_type, status, summary, created_at, updated_at)
			VALUES (?, ?, ?, 'requested', 'check', ?, ?)
		`, reviewID, taskID, reviewerType, created, created)
		if err != nil {
			t.Fatalf("seed review %s: %v", reviewID, err)
		}
		_, err = env.db.ExecContext(ctx, `UPDATE agent_task SET active_review_id = ? WHERE id = ?`, reviewID, taskID)
		if err != nil {
			t.Fatalf("link review %s: %v", reviewID, err)
		}
	}

	seedReview("task-human", "rev-human", "reviewing", "human")
	seedReview("task-agent", "rev-agent", "reviewing", "agent")
	seedReview("task-cancelled", "rev-cancelled", "cancelled", "human")

	rr := doRequest(t, env, http.MethodGet, "/api/inbox?page_size=100", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list inbox: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.InboxList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items len = %d, want only the human review: %+v", len(list.Items), list.Items)
	}
	if list.Items[0].Id != "review:rev-human" {
		t.Fatalf("item = %s, want review:rev-human", list.Items[0].Id)
	}
}

// A task that failed once and then retried to success leaves a stale 'failed'
// run behind; that run must not keep nagging from the inbox.
func TestListInboxExcludesFailedRunsOfRecoveredTask(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	ctx := context.Background()
	userID := env.adminUser.ID

	created := inboxTS(-2 * time.Hour)
	_, err := env.db.ExecContext(ctx, `
		INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
		VALUES (?, 'task:recovered', 'Recovered task', 'task', 'task', ?, ?, ?)
	`, uuid.NewString(), agentID, userID, created)
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	// Task ultimately succeeded.
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO agent_task (id, user_id, agent_id, session_id, title, status, created_at, updated_at)
		VALUES ('task-recovered', ?, ?, 'task:recovered', 'Recovered task', 'done', ?, ?)
	`, userID, agentID, created, created)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	// ...but its first attempt is still on record as failed.
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO agent_task_run (
			id, task_id, user_id, agent_id, kind, attempt_no, status, session_id,
			error, started_at, finished_at, created_at, updated_at
		)
		VALUES (
			'run-recovered', 'task-recovered', ?, ?, 'worker', 1, 'failed', 'task:recovered',
			'transient boom', ?, ?, ?, ?
		)
	`, userID, agentID,
		inboxTS(-2*time.Hour),
		time.Now().UTC().Add(-90*time.Minute).Format(time.RFC3339Nano),
		inboxTS(-2*time.Hour), inboxTS(-90*time.Minute))
	if err != nil {
		t.Fatalf("seed task run: %v", err)
	}

	rr := doRequest(t, env, http.MethodGet, "/api/inbox?page_size=100", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list inbox: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.InboxList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("recovered task must leave an empty inbox, got %+v", list.Items)
	}
}

func seedInboxRows(t *testing.T, env *testEnv, agentID string) {
	t.Helper()
	ctx := context.Background()
	userID := env.adminUser.ID

	insertTask := func(taskID, sessionID, title, status, createdAt string) {
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
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, taskID, userID, agentID, sessionID, title, status, createdAt, createdAt)
		if err != nil {
			t.Fatalf("seed task %s: %v", taskID, err)
		}
	}

	insertTask("task-blocked", "task:block", "Blocked task", "blocked", inboxTS(-7*time.Hour))
	_, err := env.db.ExecContext(ctx, `
		INSERT INTO agent_task_blocker (id, task_id, kind, status, question, created_at)
		VALUES ('blocker-1', 'task-blocked', 'input', 'open', 'Need a choice', ?)
	`, inboxTS(-4*time.Hour))
	if err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	_, err = env.db.ExecContext(ctx, `UPDATE agent_task SET active_blocker_id = 'blocker-1' WHERE id = 'task-blocked'`)
	if err != nil {
		t.Fatalf("link blocker: %v", err)
	}

	insertTask("task-review", "task:review", "Review task", "reviewing", inboxTS(-6*time.Hour))
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO agent_review (id, task_id, reviewer_type, status, summary, created_at, updated_at)
		VALUES ('review-1', 'task-review', 'human', 'requested', 'Check output', ?, ?)
	`, inboxTS(-5*time.Hour), inboxTS(-5*time.Hour))
	if err != nil {
		t.Fatalf("seed review: %v", err)
	}
	_, err = env.db.ExecContext(ctx, `UPDATE agent_task SET active_review_id = 'review-1' WHERE id = 'task-review'`)
	if err != nil {
		t.Fatalf("link review: %v", err)
	}

	insertTask("task-failed", "task:failed", "Failed task", "failed", inboxTS(-7*time.Hour))
	// The transition service writes finished_at as RFC3339Nano, unlike the
	// naive datetime('now') defaults — keep this seed on the realistic format.
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO agent_task_run (
			id, task_id, user_id, agent_id, kind, attempt_no, status, session_id,
			error, started_at, finished_at, created_at, updated_at
		)
		VALUES (
			'run-1', 'task-failed', ?, ?, 'worker', 1, 'failed', 'task:failed',
			'boom', ?, ?, ?, ?
		)
	`, userID, agentID,
		inboxTS(-7*time.Hour),
		time.Now().UTC().Add(-6*time.Hour).Format(time.RFC3339Nano),
		inboxTS(-7*time.Hour), inboxTS(-6*time.Hour))
	if err != nil {
		t.Fatalf("seed task run: %v", err)
	}

	_, err = env.db.ExecContext(ctx, `
		INSERT INTO sched_job (
			id, name, agent_id, user_id, created_at, updated_at
		)
		VALUES ('job-1', 'Daily check', ?, ?, ?, ?)
	`, agentID, userID, inboxTS(-8*time.Hour), inboxTS(-8*time.Hour))
	if err != nil {
		t.Fatalf("seed sched job: %v", err)
	}
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO sched_job_run (id, job_id, status, started_at, finished_at, error, user_id)
		VALUES ('sched-run-1', 'job-1', 'failed', ?, ?, 'schedule boom', ?)
	`, inboxTS(-3*time.Hour), inboxTS(-2*time.Hour), userID)
	if err != nil {
		t.Fatalf("seed sched run: %v", err)
	}
}

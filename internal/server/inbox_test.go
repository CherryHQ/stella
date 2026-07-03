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

	_, err := env.db.Exec(context.Background(), `
		INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
		VALUES ($1, $2, 'main', 'web', 'main', $3, $4, $5)
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

// Only goals whose block has a human recovery action, plus recently failed
// roots, belong in the inbox. Draft/pending/active/done goals
// (they resume on their own), blocked children of dead trees, and stale
// failures must stay out.
func TestListInboxExcludesNonActionableGoals(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	seed := newGoalSeeder(t, env, agentID)

	// Non-actionable lifecycles never surface.
	seed.goal("goal-draft", "draft", "", "passed-unused", inboxTS(-1*time.Hour))
	seed.goal("goal-pending", "pending", "", "pending", inboxTS(-1*time.Hour))
	seed.goal("goal-active", "active", "", "pending", inboxTS(-1*time.Hour))
	seed.accepted("goal-accepted", inboxTS(-1*time.Hour))
	// A terminal failure older than the recency window is no longer nagging.
	seed.done("goal-stale-fail", "failed", "failed", nil, inboxTS(-10*24*time.Hour))
	// A blocked child of a live tree surfaces (the action lives on the child)...
	seed.child("goal-live-child", "goal-active", "blocked", "", "env_unavailable", inboxTS(-1*time.Hour))
	// ...a blocked child of a dead tree does not: nothing left to recover.
	seed.child("goal-zombie-child", "goal-stale-fail", "blocked", "", "env_unavailable", inboxTS(-1*time.Hour))
	// A child failure is covered by its root's outcome.
	seed.child("goal-child-fail", "goal-active", "done", "failed", "", inboxTS(-1*time.Hour))
	// Human-actionable block on a root surfaces.
	seed.goal("goal-open-block", "blocked", "budget_exhausted", "pending", inboxTS(-2*time.Hour))

	rr := doRequest(t, env, http.MethodGet, "/api/inbox?page_size=100", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list inbox: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list apitypes.InboxList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("items len = %d, want live child + open block: %+v", len(list.Items), list.Items)
	}
	if list.Items[0].Id != "blocked:goal-live-child" || list.Items[1].Id != "blocked:goal-open-block" {
		t.Fatalf("items = %s, %s, want blocked:goal-live-child then blocked:goal-open-block",
			list.Items[0].Id, list.Items[1].Id)
	}
	if list.Items[0].Detail == nil || *list.Items[0].Detail != "Environment unavailable" {
		t.Fatalf("live child detail = %v, want Environment unavailable", list.Items[0].Detail)
	}
}

func seedInboxRows(t *testing.T, env *testEnv, agentID string) {
	t.Helper()
	ctx := context.Background()
	userID := env.adminUser.ID
	seed := newGoalSeeder(t, env, agentID)

	// One of each inbox kind from goals, plus a failed scheduler run.
	// Timestamps are staggered so the newest three (failed scheduler, blocked,
	// review) land on page one and exercise all three kinds.
	seed.goal("goal-blocked", "blocked", "budget_exhausted", "pending", inboxTS(-3*time.Hour))
	seed.goal("goal-review", "blocked", "needs_verdict", "pending", inboxTS(-4*time.Hour))
	seed.done("goal-failed", "failed", "failed", nil, inboxTS(-6*time.Hour))

	_, err := env.db.Exec(ctx, `
		INSERT INTO sched_job (
			id, name, agent_id, user_id, created_at, updated_at
		)
		VALUES ('job-1', 'Daily check', $1, $2, $3, $4)
	`, agentID, userID, inboxTS(-8*time.Hour), inboxTS(-8*time.Hour))
	if err != nil {
		t.Fatalf("seed sched job: %v", err)
	}
	_, err = env.db.Exec(ctx, `
		INSERT INTO sched_job_run (id, job_id, status, started_at, finished_at, error, user_id)
		VALUES ('sched-run-1', 'job-1', 'failed', $1, $2, 'schedule boom', $3)
	`, inboxTS(-3*time.Hour), inboxTS(-2*time.Hour), userID)
	if err != nil {
		t.Fatalf("seed sched run: %v", err)
	}
}

// goalSeeder inserts root goals (each with its own persistent
// session) directly, bypassing the service so tests can pin lifecycle states.
type goalSeeder struct {
	t       *testing.T
	env     *testEnv
	agentID string
}

func newGoalSeeder(t *testing.T, env *testEnv, agentID string) *goalSeeder {
	return &goalSeeder{t: t, env: env, agentID: agentID}
}

// goal seeds a root goal with the given lifecycle/block_reason.
func (s *goalSeeder) goal(id, lifecycle, blockReason, acceptanceState, updatedAt string) {
	s.insert(id, lifecycle, "", blockReason, acceptanceState, nil, updatedAt)
}

func (s *goalSeeder) done(id, doneReason, acceptanceState string, acceptedOutput *string, updatedAt string) {
	s.insert(id, "done", doneReason, "", acceptanceState, acceptedOutput, updatedAt)
}

// child seeds a direct child of an already-seeded root goal.
func (s *goalSeeder) child(id, rootID, lifecycle, doneReason, blockReason, updatedAt string) {
	s.t.Helper()
	state := "pending"
	if lifecycle == "done" {
		state = "failed"
	}
	s.insert(id, lifecycle, doneReason, blockReason, state, nil, updatedAt)
	if _, err := s.env.db.Exec(context.Background(), `
		UPDATE agent_goal SET parent_id = $2, root_id = $2, depth = 1 WHERE id = $1
	`, id, rootID); err != nil {
		s.t.Fatalf("reparent goal %s: %v", id, err)
	}
}

// accepted seeds an accepted root, satisfying the schema's anti-drift CHECK
// (acceptance_state='passed' AND accepted_output IS NOT NULL).
func (s *goalSeeder) accepted(id, updatedAt string) {
	output := "done"
	s.insert(id, "done", "accepted", "", "passed", &output, updatedAt)
}

func (s *goalSeeder) insert(id, lifecycle, doneReason, blockReason, acceptanceState string, acceptedOutput *string, updatedAt string) {
	s.t.Helper()
	ctx := context.Background()
	userID := s.env.adminUser.ID
	_, err := s.env.db.Exec(ctx, `
		INSERT INTO agent_goal (
			id, user_id, agent_id, root_id, title,
			lifecycle, done_reason, block_reason, acceptance_state, accepted_output,
			created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, id, userID, s.agentID, id, id,
		lifecycle, doneReason, blockReason, acceptanceState, acceptedOutput,
		updatedAt, updatedAt)
	if err != nil {
		s.t.Fatalf("seed goal %s: %v", id, err)
	}
}

//go:build system

package system

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// createWebGroup exercises the production owner-authorized group API. Group
// journeys use it rather than inserting rows, so they cover HTTP auth, durable
// membership, and the asynchronous dispatcher as one seam.
func (h *harness) createWebGroup(t *testing.T, ctx context.Context, name string, agentIDs ...string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/groups", map[string]any{"group_name": name, "agent_ids": agentIDs})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST group = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(40))
	}
	var group struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		t.Fatalf("decode group: %v", err)
	}
	if group.ID == "" {
		t.Fatal("created group has empty id")
	}
	return group.ID
}

func (h *harness) sendGroupMessage(t *testing.T, ctx context.Context, groupID, content string) {
	t.Helper()
	resp := h.postJSON(t, ctx, fmt.Sprintf("/api/groups/%s/messages", groupID), map[string]any{"content": content})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST group message = %d, want 200\n%s", resp.StatusCode, h.proc.LogTail(40))
	}
}

func (h *harness) testGroupIngest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	providerID := h.createFakeProviderNamed(t, ctx, "http://127.0.0.1:1", "group-ingest-"+h.runID)
	agentID := h.createAgentNamed(t, ctx, providerID+"/claude-sonnet-4-6", "group-ingest-agent-"+h.runID)
	groupID := h.createWebGroup(t, ctx, "group-ingest-"+h.runID, agentID)

	// Subscribe before ingest so this asserts the live group SSE seam, not only
	// a direct database row after the request has returned.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+fmt.Sprintf("/api/groups/%s/events", groupID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET group events=%d", resp.StatusCode)
	}
	h.sendGroupMessage(t, ctx, groupID, "group ingest "+h.runID)
	// Both frame kinds over the real transport: the canonical message, then the
	// presence frame the browser needs to know who is generating. The provider is
	// unreachable on purpose, so this turn starts (running) and then fails; the
	// running frame is what proves the presence seam is wired end to end.
	var sawMessage bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: message") && !strings.HasPrefix(line, "event: turn") {
			continue
		}
		if !scanner.Scan() {
			break
		}
		data := scanner.Text()
		if strings.HasPrefix(line, "event: message") {
			if !strings.Contains(data, "group ingest "+h.runID) {
				t.Fatalf("unexpected group message frame: %q", data)
			}
			sawMessage = true
			continue
		}
		if !strings.Contains(data, `"state":"running"`) {
			continue
		}
		if !sawMessage {
			t.Fatalf("running turn frame arrived before the canonical message: %q", data)
		}
		if !strings.Contains(data, `"agent_id":"`+agentID+`"`) {
			t.Fatalf("running turn frame names the wrong agent: %q", data)
		}
		return
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("group event stream ended before the running turn frame")
}

func (h *harness) testGroupConcurrentCounting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fake := newFakeAnthropic(t)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "group-count-"+h.runID)
	const modelA, modelB = "count-a", "count-b"
	// Each model receives its own deterministic turn lane. The first accepted
	// post is 1; the held peer's successor is scripted to post 2.
	for _, model := range []string{modelA, modelB} {
		fake.enqueueTextForModel(model, "1")
		fake.enqueueTextForModel(model, "2")
		fake.setTrailingTextForModel(model, "done counting")
	}
	a := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelA, providerID+"/"+modelA, "count-a-"+h.runID)
	b := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelB, providerID+"/"+modelB, "count-b-"+h.runID)
	groupID := h.createWebGroup(t, ctx, "count-"+h.runID, a, b)
	h.sendGroupMessage(t, ctx, groupID, "count from 1")
	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	for {
		var ones, twos int
		var oneAuthor, twoAuthor string
		err := h.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE content='1'), count(*) FILTER (WHERE content='2'), COALESCE(max(actor_id) FILTER (WHERE content='1'), ''), COALESCE(max(actor_id) FILTER (WHERE content='2'), '') FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent'`, groupID).Scan(&ones, &twos, &oneAuthor, &twoAuthor)
		if err != nil {
			t.Fatal(err)
		}
		if ones == 1 && twos == 1 && oneAuthor != twoAuthor {
			// Nothing may ask a model whether an agent should speak: the gate in
			// front of a turn is deterministic, and the only model call a group
			// makes is the turn itself, which carries the agent tool schema.
			for _, request := range fake.requests() {
				if len(request.ToolNames) == 0 {
					t.Fatalf("no-tools model request in a group journey: %+v", request)
				}
			}
			fake.discardModelScripts()
			return
		}
		select {
		case <-ctx.Done():
			var dispatches, posts string
			_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(string_agg(agent_id || '/' || status || '/trig' || trigger_seq || '/held' || COALESCE(held_up_to_seq, -1) || '/att' || attempt_count, ' | ' ORDER BY created_at), '') FROM ctx_group_dispatch WHERE group_id=$1`, groupID).Scan(&dispatches)
			_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(string_agg(seq || ':' || actor_type || ':' || actor_id || ':' || content, ' | ' ORDER BY seq), '') FROM ctx_group_message WHERE group_id=$1`, groupID).Scan(&posts)
			t.Fatalf("counting posts 1/2=%d/%d: %v\nmessages=%s\ndispatches=%s\n%s", ones, twos, ctx.Err(), posts, dispatches, h.proc.LogTail(60))
		case <-deadline.C:
		}
	}
}

func (h *harness) testGroupPingPongHardCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fake := newFakeAnthropic(t)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "group-ping-"+h.runID)
	const modelA, modelB = "count-a", "count-b"
	for _, model := range []string{modelA, modelB} {
		for i := range 8 {
			fake.enqueueTextForModel(model, fmt.Sprintf("%s-%d", model, i))
		}
	}
	a := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelA, providerID+"/"+modelA, "ping-a-"+h.runID)
	b := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelB, providerID+"/"+modelB, "ping-b-"+h.runID)
	groupID := h.createWebGroup(t, ctx, "ping-"+h.runID, a, b)
	// Set the cap below the two-agent lapping floor. The lapping guard naturally
	// stops an agent pair after its first lap, so it cannot prove D7's hard
	// cap by itself.
	// Keep the cap low enough that the journey remains bounded while exercising
	// the production persisted-cap API rather than a test-only dispatcher knob.
	payload, _ := json.Marshal(map[string]any{"agent_chain_hard_limit": 1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, h.baseURL+fmt.Sprintf("/api/groups/%s", groupID), strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH group caps = %d", resp.StatusCode)
	}
	h.sendGroupMessage(t, ctx, groupID, "start ping pong")
	waitAgentPosts := func(want int) {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			var got int
			if err := h.db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent'`, groupID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got >= want {
				return
			}
			select {
			case <-ctx.Done():
				var states string
				_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(string_agg(status || ':' || kind || ':' || COALESCE(last_error, ''), ',' ORDER BY created_at), '') FROM ctx_group_dispatch WHERE group_id=$1`, groupID).Scan(&states)
				t.Fatalf("agent posts=%d, want %d: %v dispatches=%s\n%s", got, want, ctx.Err(), states, h.proc.LogTail(60))
			case <-ticker.C:
			}
		}
	}
	waitAgentPosts(1)
	// Let pending wakes traverse the gate; the cap must prevent a fourth post.
	time.Sleep(1500 * time.Millisecond)
	var capped int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent'`, groupID).Scan(&capped); err != nil {
		t.Fatal(err)
	}
	if capped != 1 {
		t.Fatalf("hard cap allowed %d agent posts, want 1", capped)
	}
	beforeCalls := fake.requestCount()
	h.sendGroupMessage(t, ctx, groupID, "human reset")
	waitAgentPosts(2)
	// Both members may run a turn on the reset while still under the cap, and an
	// accepted post wakes the peer once more. Anything past a handful of turns is
	// an unbounded chain, whatever the models chose to say.
	if calls := fake.requestCount() - beforeCalls; calls > 6 {
		t.Fatalf("post-reset model calls=%d, want <=6", calls)
	}
	fake.discardModelScripts()
}

// testGroupModelPass covers the seam a Go test cannot reach: a full turn runs
// in the real server, the model decides it has nothing to add, and the turn has
// to leave the group untouched while still recording what the agent read.
func (h *harness) testGroupModelPass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fake := newFakeAnthropic(t)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "group-pass-"+h.runID)
	const modelA, modelB = "pass-a", "pass-b"
	// B is woken by A's post with no rule to stop it: only the model itself can
	// decide that the answer belongs to A. That is the whole point of the pass.
	fake.enqueueTextForModel(modelA, "the deploy finished at 14:02")
	fake.enqueueTextForModel(modelB, "PASS")
	fake.setTrailingTextForModel(modelA, "PASS")
	fake.setTrailingTextForModel(modelB, "PASS")
	a := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelA, providerID+"/"+modelA, "pass-a-"+h.runID)
	b := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelB, providerID+"/"+modelB, "pass-b-"+h.runID)
	groupID := h.createWebGroup(t, ctx, "pass-"+h.runID, a, b)
	h.sendGroupMessage(t, ctx, groupID, "@"+a+" when did the deploy finish?")

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var answers, passerPosts, passed int
		var cursor int64
		err := h.db.QueryRow(ctx, `
			SELECT
			  (SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent' AND actor_id=$2),
			  (SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent' AND actor_id=$3),
			  (SELECT count(*) FROM ctx_group_dispatch WHERE group_id=$1 AND agent_id=$3 AND status='silent' AND last_error='model_pass'),
			  (SELECT COALESCE(max(last_seq), 0) FROM ctx_group_ingest_cursor WHERE group_id=$1 AND pipeline='lcm:' || $3)
		`, groupID, a, b).Scan(&answers, &passerPosts, &passed, &cursor)
		if err != nil {
			t.Fatal(err)
		}
		if answers == 1 && passed >= 1 {
			if passerPosts != 0 {
				t.Fatalf("the passing agent posted %d messages, want none", passerPosts)
			}
			// It read the group even though it said nothing; without the cursor
			// it would re-read the same messages on every later turn.
			if cursor < 1 {
				t.Fatalf("passer ingest cursor=%d, want the trigger committed", cursor)
			}
			fake.discardModelScripts()
			return
		}
		select {
		case <-ctx.Done():
			var messages, dispatches string
			_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(string_agg(seq || ':' || actor_type || ':' || actor_id || ':' || content, ' | ' ORDER BY seq), '') FROM ctx_group_message WHERE group_id=$1`, groupID).Scan(&messages)
			_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(string_agg(agent_id || '/' || status || '/' || last_error, ' | ' ORDER BY created_at), '') FROM ctx_group_dispatch WHERE group_id=$1`, groupID).Scan(&dispatches)
			t.Fatalf("answers=%d passes=%d: %v\nmessages=%s\ndispatches=%s\n%s", answers, passed, ctx.Err(), messages, dispatches, h.proc.LogTail(60))
		case <-ticker.C:
		}
	}
}

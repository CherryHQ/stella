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
		t.Fatalf("POST group = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
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
		t.Fatalf("POST group message = %d, want 200\n%s", resp.StatusCode, h.proc.logTail(40))
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
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: message") {
			continue
		}
		if !scanner.Scan() {
			break
		}
		if !strings.Contains(scanner.Text(), "group ingest "+h.runID) {
			t.Fatalf("unexpected group message frame: %q", scanner.Text())
		}
		return
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("group event stream ended before canonical message")
}

func (h *harness) testGroupConcurrentCounting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fake := newFakeAnthropic(t)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "group-count-"+h.runID)
	const modelA, modelB = "count-a", "count-b"
	// Each model receives its own deterministic classifier/turn lane. The first
	// accepted post is 1; the held peer's successor is scripted to post 2.
	for _, model := range []string{modelA, modelB} {
		fake.setTriageForModel(model, true, "count")
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
			// Triage is the only no-tools model request in this fixture; full group
			// turns carry the agent tool schema. The durable wake rows give the
			// denominator: every human can classify every member, while an agent
			// message can classify only non-author members that reached step 5.
			requests := fake.requests()
			triageCalls := 0
			for _, request := range requests {
				if len(request.ToolNames) == 0 {
					triageCalls++
				}
			}
			var humanMessages, agentWakeCandidates int
			if err := h.db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='human'`, groupID).Scan(&humanMessages); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(ctx, `
				SELECT count(*)
				FROM ctx_group_dispatch dispatch
				JOIN ctx_group_message message ON message.id = dispatch.group_message_id
				WHERE dispatch.group_id=$1 AND dispatch.kind='wake' AND message.actor_type='agent'
			`, groupID).Scan(&agentWakeCandidates); err != nil {
				t.Fatal(err)
			}
			bound := humanMessages*2 + agentWakeCandidates
			if triageCalls > bound {
				t.Fatalf("triage calls=%d exceed bound=%d (human=%d members=2 agent wake candidates=%d)", triageCalls, bound, humanMessages, agentWakeCandidates)
			}
			t.Logf("group triage calls=%d, bound=%d (human=%d, agent wake candidates=%d)", triageCalls, bound, humanMessages, agentWakeCandidates)
			fake.discardModelScripts()
			return
		}
		select {
		case <-ctx.Done():
			var dispatches, posts string
			_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(string_agg(agent_id || '/' || status || '/trig' || trigger_seq || '/held' || COALESCE(held_up_to_seq, -1) || '/att' || attempt_count, ' | ' ORDER BY created_at), '') FROM ctx_group_dispatch WHERE group_id=$1`, groupID).Scan(&dispatches)
			_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(string_agg(seq || ':' || actor_type || ':' || actor_id || ':' || content, ' | ' ORDER BY seq), '') FROM ctx_group_message WHERE group_id=$1`, groupID).Scan(&posts)
			t.Fatalf("counting posts 1/2=%d/%d: %v\nmessages=%s\ndispatches=%s\n%s", ones, twos, ctx.Err(), posts, dispatches, h.proc.logTail(60))
		case <-deadline.C:
		}
	}
}

func (h *harness) testGroupWorkClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fake := newFakeAnthropic(t)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "group-claim-"+h.runID)
	const modelA, modelB = "claim-a", "claim-b"
	a := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelA, providerID+"/"+modelA, "claim-a-"+h.runID)
	b := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelB, providerID+"/"+modelB, "claim-b-"+h.runID)
	// A is directly assigned. Its accepted post names B, so B's attempted claim
	// runs after A's durable lease rather than racing the assignment itself.
	fake.enqueueToolForModel(modelA, "toolu_claim_a", "group_claim", `{"key":"report","note":"write the report"}`)
	fake.setContinuationTextForModel(modelA, "I will write the report. @"+b)
	// Both agents are driven by mentions here, so triage only decides the
	// trailing wakes: neither may open a second report turn.
	fake.setTriageForModel(modelA, false, "peer moved on")
	fake.setTriageForModel(modelB, false, "peer moved on")
	fake.enqueueToolForModel(modelB, "toolu_claim_b", "group_claim", `{"key":"report","note":"write the report"}`)
	fake.setContinuationTextForModel(modelB, "A owns the report, so I moved on.")
	fake.setTrailingTextForModel(modelA, "nothing further")
	fake.setTrailingTextForModel(modelB, "nothing further")
	groupID := h.createWebGroup(t, ctx, "claim-"+h.runID, a, b)
	h.sendGroupMessage(t, ctx, groupID, "@"+a+" write the report")

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var claims, authored, moved int
		err := h.db.QueryRow(ctx, `
			SELECT
			  (SELECT count(*) FROM ctx_group_claim WHERE group_id=$1),
			  (SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent' AND content LIKE 'I will write the report%'),
			  (SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent' AND content LIKE 'A owns the report%')
		`, groupID).Scan(&claims, &authored, &moved)
		if err != nil {
			t.Fatal(err)
		}
		if claims == 1 && authored == 1 && moved == 1 {
			var owner, key string
			if err := h.db.QueryRow(ctx, `SELECT owner_agent_id, key FROM ctx_group_claim WHERE group_id=$1`, groupID).Scan(&owner, &key); err != nil {
				t.Fatal(err)
			}
			if owner != a || key != "report" {
				t.Fatalf("claim owner/key=%s/%s, want %s/report", owner, key, a)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("claim journey claims/authored/moved=%d/%d/%d: %v\n%s", claims, authored, moved, ctx.Err(), h.proc.logTail(60))
		case <-ticker.C:
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
		fake.setTriageForModel(model, true, "reply")
		for i := range 8 {
			fake.enqueueTextForModel(model, fmt.Sprintf("%s-%d", model, i))
		}
	}
	a := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelA, providerID+"/"+modelA, "ping-a-"+h.runID)
	b := h.createAgentNamedWithFast(t, ctx, providerID+"/"+modelB, providerID+"/"+modelB, "ping-b-"+h.runID)
	groupID := h.createWebGroup(t, ctx, "ping-"+h.runID, a, b)
	// Set the cap below the two-agent lapping floor. The lapping guard naturally
	// stops an unclaimed pair after its first lap, so it cannot prove D7's hard
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
				t.Fatalf("agent posts=%d, want %d: %v dispatches=%s\n%s", got, want, ctx.Err(), states, h.proc.logTail(60))
			case <-ticker.C:
			}
		}
	}
	waitAgentPosts(1)
	// Let pending wakes traverse triage; the cap must prevent a fourth post.
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
	// Both members classify the reset, both may still be under the cap when they
	// do, and each accepted post wakes the peer once more: two triage calls, two
	// turns, two triage calls. Anything above that is an unbounded chain.
	if calls := fake.requestCount() - beforeCalls; calls > 6 {
		t.Fatalf("post-reset triage/model calls=%d, want <=6", calls)
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
	// B is woken and its classifier says act: only the model itself can decide
	// that the answer belongs to A. That is the whole point of the pass.
	fake.setTriageForModel(modelA, true, "asked")
	fake.setTriageForModel(modelB, true, "worth a look")
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
			t.Fatalf("answers=%d passes=%d: %v\nmessages=%s\ndispatches=%s\n%s", answers, passed, ctx.Err(), messages, dispatches, h.proc.logTail(60))
		case <-ticker.C:
		}
	}
}

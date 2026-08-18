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
		fake.enqueueTextForModel(model, `{"act":true,"reason":"count"}`)
		fake.enqueueTextForModel(model, "1")
		fake.enqueueTextForModel(model, `{"act":true,"reason":"continue"}`)
		fake.enqueueTextForModel(model, "2")
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
			fake.discardModelScripts()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("counting posts 1/2=%d/%d: %v\n%s", ones, twos, ctx.Err(), h.proc.logTail(60))
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
			fake.enqueueTextForModel(model, `{"act":true,"reason":"reply"}`)
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
	if calls := fake.requestCount() - beforeCalls; calls > 4 {
		t.Fatalf("post-reset triage/model calls=%d, want <=4", calls)
	}
	fake.discardModelScripts()
}

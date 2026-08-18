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

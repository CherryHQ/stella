//go:build system

package system

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testChatSSE drives one chat turn end to end over the wire: it configures the
// scripted fake as the Anthropic provider, creates an agent bound to it, opens
// a session, sends a message, and consumes the real SSE stream incrementally.
// It proves three seams a single in-process test cannot: the provider base_url
// carries to the SDK so no LLM traffic leaves the host, the SSE framing reaches
// an HTTP client as separate flushed events, and the subprocess persists the
// turn's rows to the shared database.
func (h *harness) testChatSSE(t *testing.T) {
	fake := newFakeAnthropic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The reply is run-scoped so a stray match from other state cannot satisfy
	// the final-content assertion by accident.
	reply := "hello from the scripted fake, run " + h.runID
	fake.enqueueText(reply)

	// A default "anthropic" provider is seeded, so the fixture provider carries
	// the run id both to avoid the primary-key collision and to keep the model
	// ref pointed unambiguously at our fake rather than the seed.
	const modelID = "claude-sonnet-4-6"
	providerID := h.createFakeProvider(t, ctx, fake.baseURL())
	agentID := h.createAgent(t, ctx, providerID+"/"+modelID)
	sessionID := h.createSession(t, ctx, agentID)

	userText := "ping " + h.runID
	events, assistantText := h.streamChatTurn(t, ctx, agentID, sessionID, userText)

	assertTurnEventOrder(t, events)
	if assistantText != reply {
		t.Fatalf("assembled assistant text = %q, want scripted %q\n%s", assistantText, reply, h.proc.LogTail(40))
	}

	// Every request the fake saw is, by construction, every model request the
	// system made: the provider base_url is the fake's loopback address. Exactly
	// one turn was scripted and sent, so exactly one request must have arrived,
	// carrying the agent's model.
	reqs := fake.requests()
	if len(reqs) != 1 {
		t.Fatalf("fake received %d model request(s), want exactly 1", len(reqs))
	}
	if reqs[0].Model != modelID {
		t.Fatalf("model in request = %q, want %q", reqs[0].Model, modelID)
	}

	h.assertChatRowsPersisted(t, ctx, sessionID, agentID, userText, reply)
}

// createFakeProvider registers a run-scoped Anthropic-type provider pointed at
// the fake's loopback base_url and returns its id. The API key is a non-secret
// test value; the fake never checks it. Providers are global and admin-only,
// which the bootstrap user (first-registered, hence admin) satisfies. The type
// is "anthropic" so the Anthropic adapter is selected; the id is run-scoped so
// it never collides with the seeded default provider.
func (h *harness) createFakeProvider(t *testing.T, ctx context.Context, baseURL string) string {
	t.Helper()
	return h.createFakeProviderNamed(t, ctx, baseURL, "anthropic-"+h.runID)
}

func (h *harness) createFakeProviderNamed(t *testing.T, ctx context.Context, baseURL, id string) string {
	return h.createFakeProviderNamedWithKey(t, ctx, baseURL, id, "system-test-not-a-secret")
}

func (h *harness) createFakeProviderNamedWithKey(t *testing.T, ctx context.Context, baseURL, id, apiKey string) string {
	t.Helper()
	body := map[string]any{
		"id":       id,
		"type":     "anthropic",
		"name":     id,
		"enabled":  true,
		"api_key":  apiKey,
		"base_url": baseURL,
		"models": map[string]any{
			"claude-sonnet-4-6": map[string]any{
				"id":      "claude-sonnet-4-6",
				"enabled": true,
				"input":   []string{"text", "image"},
			},
			"count-a": map[string]any{"id": "count-a", "enabled": true, "input": []string{"text"}},
			"count-b": map[string]any{"id": "count-b", "enabled": true, "input": []string{"text"}},
		},
	}
	resp := h.postJSON(t, ctx, "/api/providers", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/providers = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(40))
	}
	return id
}

// createAgent creates an agent bound to the given model ref and returns its
// server-assigned id (slugified from the name, so it is read from the response
// rather than assumed).
func (h *harness) createAgent(t *testing.T, ctx context.Context, model string) string {
	t.Helper()
	return h.createAgentNamed(t, ctx, model, "sys-test-agent-"+h.runID)
}

func (h *harness) createAgentNamed(t *testing.T, ctx context.Context, model, name string) string {
	t.Helper()
	body := map[string]any{
		"name":    name,
		"model":   model,
		"enabled": true,
	}
	resp := h.postJSON(t, ctx, "/api/agents", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/agents = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created agent has empty id")
	}
	return created.ID
}

func (h *harness) createAgentNamedWithFast(t *testing.T, ctx context.Context, model, fast, name string) string {
	t.Helper()
	body := map[string]any{"name": name, "model": model, "model_fast": fast, "enabled": true}
	resp := h.postJSON(t, ctx, "/api/agents", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/agents = %d, want 201\n%s", resp.StatusCode, h.proc.LogTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

// createSession opens a chat session on the agent and returns its session id,
// the value used as {sessionId} in later calls.
func (h *harness) createSession(t *testing.T, ctx context.Context, agentID string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, fmt.Sprintf("/api/agents/%s/sessions", agentID), map[string]any{"kind": "chat"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST create session = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created session has empty id")
	}
	return created.ID
}

// turnEvent is the minimal shape of a UI-message-stream frame the test asserts
// on: its type and, for text-delta frames, the chunk.
type turnEvent struct {
	Type      string `json:"type"`
	Delta     string `json:"delta"`
	ErrorText string `json:"errorText"`
	// The tool frames carry the name only on tool-input-start and the result
	// only on the settled frame, so a journey that asserts on a tool result has
	// to pair them by call id. tool_smoke does exactly that.
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Output     string `json:"output"`
}

// streamChatTurn sends a user message and consumes the SSE response
// incrementally, returning every frame in arrival order and the assistant text
// assembled from text-delta chunks. It reads the stream as a real client does:
// frame by frame until the [DONE] sentinel, so the test observes streaming
// rather than a buffered whole.
func (h *harness) streamChatTurn(t *testing.T, ctx context.Context, agentID, sessionID, text string) ([]turnEvent, string) {
	t.Helper()
	return h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{{"type": "text", "text": text}})
}

func (h *harness) streamChatParts(t *testing.T, ctx context.Context, agentID, sessionID string, parts []map[string]any) ([]turnEvent, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"parts": parts})
	if err != nil {
		t.Fatalf("marshal send-message body: %v", err)
	}
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("build send-message request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST send message: %v\n%s", err, h.proc.LogTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		t.Fatalf("send message = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.LogTail(40))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("send message Content-Type = %q, want text/event-stream", ct)
	}

	var (
		events []turnEvent
		text2  strings.Builder
		done   bool
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue // blank separators and any non-data lines
		}
		if data == "[DONE]" {
			done = true
			break
		}
		var evt turnEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			t.Fatalf("SSE frame is not valid JSON: %q: %v", data, err)
		}
		events = append(events, evt)
		if evt.Type == "error" {
			t.Fatalf("turn emitted an error frame: %q\n%s", data, h.proc.LogTail(40))
		}
		if evt.Type == "text-delta" {
			text2.WriteString(evt.Delta)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE stream: %v\n%s", err, h.proc.LogTail(40))
	}
	if !done {
		t.Fatalf("SSE stream ended without a [DONE] sentinel; frames: %v\n%s", eventTypes(events), h.proc.LogTail(40))
	}
	return events, text2.String()
}

// assertTurnEventOrder proves the three lifecycle events a streamed turn must
// deliver, in order, before EOF: a start frame, at least one non-empty
// text-delta, and a finish frame.
func assertTurnEventOrder(t *testing.T, events []turnEvent) {
	t.Helper()
	startIdx, textIdx, finishIdx := -1, -1, -1
	for i, evt := range events {
		switch {
		case evt.Type == "start" && startIdx == -1:
			startIdx = i
		case evt.Type == "text-delta" && evt.Delta != "" && textIdx == -1:
			textIdx = i
		case evt.Type == "finish" && finishIdx == -1:
			finishIdx = i
		}
	}
	if startIdx == -1 || textIdx == -1 || finishIdx == -1 {
		t.Fatalf("turn missing required events (start=%d, non-empty text-delta=%d, finish=%d); got %v",
			startIdx, textIdx, finishIdx, eventTypes(events))
	}
	if startIdx >= textIdx || textIdx >= finishIdx {
		t.Fatalf("turn events out of order: start=%d, text=%d, finish=%d; got %v",
			startIdx, textIdx, finishIdx, eventTypes(events))
	}
}

// assertChatRowsPersisted proves the subprocess wrote the turn to the shared
// database: the conversation exists and is not archived, the user message
// carries the sent text, and the assistant message carries the scripted reply.
// Persistence happens as the turn flushes, so the assistant row is polled with
// a bounded deadline rather than read once.
func (h *harness) assertChatRowsPersisted(t *testing.T, ctx context.Context, sessionID, agentID, userText, reply string) {
	t.Helper()

	var (
		kind      string
		archived  bool
		convAgent string
	)
	err := h.db.QueryRow(ctx,
		`SELECT kind, archived, coalesce(agent_id, '') FROM ctx_conversation WHERE session_id = $1`,
		sessionID).Scan(&kind, &archived, &convAgent)
	if err != nil {
		t.Fatalf("query ctx_conversation for session %s: %v\n%s", sessionID, err, h.proc.LogTail(40))
	}
	if archived {
		t.Errorf("conversation %s archived = true, want false", sessionID)
	}
	if kind != "chat" {
		t.Errorf("conversation kind = %q, want %q", kind, "chat")
	}
	if convAgent != agentID {
		t.Errorf("conversation agent_id = %q, want %q", convAgent, agentID)
	}

	if got := h.messageContent(t, ctx, sessionID, "user"); got != userText {
		t.Errorf("persisted user message = %q, want %q", got, userText)
	}

	// The assistant row is written as the turn's blocks flush, which may trail
	// the [DONE] sentinel. Poll to a hard deadline and report the last-seen state
	// on failure.
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := h.messageContent(t, ctx, sessionID, "assistant")
		if got == reply {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("persisted assistant message = %q, want %q (session %s)\n%s", got, reply, sessionID, h.proc.LogTail(40))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// messageContent returns the content of the first message with the given role
// in the session, or "" if no such row exists yet. Only a missing row maps to
// ""; any other database error is fatal so a broken connection cannot
// masquerade as "message not persisted".
func (h *harness) messageContent(t *testing.T, ctx context.Context, sessionID, role string) string {
	t.Helper()
	var content string
	err := h.db.QueryRow(ctx,
		`SELECT m.content
		   FROM ctx_message m
		   JOIN ctx_conversation c ON c.id = m.conversation_id
		  WHERE c.session_id = $1 AND m.role = $2
		  ORDER BY m.seq ASC
		  LIMIT 1`,
		sessionID, role).Scan(&content)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ""
	case err != nil:
		t.Fatalf("query %s message for session %s: %v", role, sessionID, err)
	}
	return content
}

func eventTypes(events []turnEvent) []string {
	types := make([]string, len(events))
	for i, evt := range events {
		types[i] = evt.Type
	}
	return types
}

func drainBody(r io.Reader) { _, _ = io.Copy(io.Discard, r) }

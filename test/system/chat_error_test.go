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

// testChatProviderError proves the send-message SSE contract when the model
// call fails: the server still opens a 200 event-stream, surfaces the provider
// failure as an in-band error frame, then closes the turn with finish and the
// [DONE] sentinel — it never hangs and never returns a bare HTTP error to the
// client mid-turn. This is a seam a single in-process test cannot reach: it
// needs the real SSE transport plus the async turn runner that maps a provider
// error onto the wire format.
//
// The fake answers with a 400 invalid_request_error, which the Anthropic SDK
// does not retry, so exactly one model request is expected; the sticky error
// script would absorb retries if the SDK ever changed that, and the assertion
// below records the count it actually observed.
func (h *harness) testChatProviderError(t *testing.T) {
	fake := newFakeAnthropic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	forced := "system-test forced provider error, run " + h.runID
	fake.enqueueError(http.StatusBadRequest, "invalid_request_error", forced)

	// Run-scoped, distinct from chat_sse's fixtures so the two journeys never
	// share a provider, agent, or session.
	const modelID = "claude-sonnet-4-6"
	providerID := h.createErrorProvider(t, ctx, fake.baseURL())
	agentID := h.createAgent(t, ctx, providerID+"/"+modelID)
	sessionID := h.createSession(t, ctx, agentID)

	events := h.streamTurnExpectingError(t, ctx, agentID, sessionID, "ping "+h.runID)
	assertErrorTurnShape(t, events)

	// Every request the fake saw is every model request the system made. A
	// non-retried 400 yields exactly one; assert it and log it for the record.
	reqs := fake.requests()
	t.Logf("chat_provider_error: fake received %d /v1/messages request(s) for a scripted 400", len(reqs))
	if len(reqs) != 1 {
		t.Fatalf("fake received %d model request(s) for a non-retried 400, want exactly 1", len(reqs))
	}
}

// createErrorProvider registers a run-scoped Anthropic-type provider for the
// provider-error journey, pointed at the fake's loopback base_url. It mirrors
// createFakeProvider but with a distinct id so the two chat journeys never
// collide on the provider primary key.
func (h *harness) createErrorProvider(t *testing.T, ctx context.Context, baseURL string) string {
	t.Helper()
	id := "anthropic-err-" + h.runID
	body := map[string]any{
		"id":       id,
		"type":     "anthropic",
		"name":     id,
		"enabled":  true,
		"api_key":  "system-test-not-a-secret",
		"base_url": baseURL,
	}
	resp := h.postJSON(t, ctx, "/api/providers", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/providers (error provider) = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	return id
}

// streamTurnExpectingError sends a message and consumes the SSE stream to its
// [DONE] sentinel, returning every frame in order. Unlike streamChatTurn it does
// not fail on an error frame — surfacing one is the behavior under test. It
// still requires the 200 event-stream response and a terminating [DONE], and the
// context deadline guarantees the read cannot hang.
func (h *harness) streamTurnExpectingError(t *testing.T, ctx context.Context, agentID, sessionID, text string) []turnEvent {
	t.Helper()
	body := map[string]any{"parts": []map[string]any{{"type": "text", "text": text}}}
	payload, err := json.Marshal(body)
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
		t.Fatalf("POST send message: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		t.Fatalf("send message = %d, want %d (a provider error must surface in-stream, not as a bare HTTP error)\n%s",
			resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("send message Content-Type = %q, want text/event-stream", ct)
	}

	var (
		events []turnEvent
		done   bool
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
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
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE stream: %v\n%s", err, h.proc.logTail(40))
	}
	if !done {
		t.Fatalf("SSE stream ended without a [DONE] sentinel; frames: %v\n%s", eventTypes(events), h.proc.logTail(40))
	}
	return events
}

// assertErrorTurnShape proves the failed turn delivered, in order: an error
// frame carrying non-empty errorText, then a finish frame, before the stream's
// [DONE]. The error text is asserted non-empty rather than equal to the scripted
// message, because the provider adapter may wrap the upstream error — the
// contract is that a reason reaches the client, not its exact wording.
func assertErrorTurnShape(t *testing.T, events []turnEvent) {
	t.Helper()
	errIdx, finishIdx := -1, -1
	for i, evt := range events {
		switch {
		case evt.Type == "error" && errIdx == -1:
			errIdx = i
			if evt.ErrorText == "" {
				t.Errorf("error frame at %d has empty errorText; the failure reason did not reach the client", i)
			}
		case evt.Type == "finish" && finishIdx == -1:
			finishIdx = i
		}
	}
	if errIdx == -1 || finishIdx == -1 {
		t.Fatalf("failed turn missing required frames (error=%d, finish=%d); got %v", errIdx, finishIdx, eventTypes(events))
	}
	if errIdx >= finishIdx {
		t.Fatalf("failed turn out of order: error=%d must precede finish=%d; got %v", errIdx, finishIdx, eventTypes(events))
	}
}

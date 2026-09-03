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

// testChatDisconnectResume proves the cross-request seam the Web UI relies on:
// closing the initiating SSE connection does not cancel the turn, and a fresh
// events subscription replays the first half before streaming the remainder.
func (h *harness) testChatDisconnectResume(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	first := "before disconnect " + h.runID
	second := " after reconnect " + h.runID
	gate := fake.enqueueGatedText(first, second)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-resume-"+h.runID)
	agentID := h.createAgentNamed(t, ctx, providerID+"/claude-sonnet-4-6", "sys-test-resume-agent-"+h.runID)
	sessionID := h.createSession(t, ctx, agentID)
	userText := "resume me " + h.runID

	sendCtx, disconnect := context.WithCancel(ctx)
	resp := h.openChatStream(t, sendCtx, agentID, sessionID, userText)
	scanner := newSSEScanner(resp)
	for {
		event, done := scanTurnEvent(t, scanner)
		if done {
			t.Fatal("initiating stream finished before the gated first delta")
		}
		if event.Type == "text-delta" && event.Delta != "" {
			if event.Delta != first {
				t.Fatalf("first delta = %q, want %q", event.Delta, first)
			}
			break
		}
	}
	disconnect()
	_ = resp.Body.Close()

	eventsPath := fmt.Sprintf("/api/agents/%s/sessions/%s/events", agentID, sessionID)
	eventsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+eventsPath, nil)
	if err != nil {
		t.Fatalf("build resume request: %v", err)
	}
	eventsReq.Header.Set("Accept", "text/event-stream")
	resumed, err := h.client.Do(eventsReq)
	if err != nil {
		t.Fatalf("GET resume stream: %v\n%s", err, h.proc.LogTail(40))
	}
	defer func() { _ = resumed.Body.Close() }()
	if resumed.StatusCode != http.StatusOK {
		t.Fatalf("GET resume stream = %d, want 200\n%s", resumed.StatusCode, h.proc.LogTail(40))
	}

	close(gate.release)
	var replayed strings.Builder
	resumedScanner := newSSEScanner(resumed)
	for {
		event, done := scanTurnEvent(t, resumedScanner)
		if event.Type == "error" {
			t.Fatalf("resumed stream error: %s\n%s", event.ErrorText, h.proc.LogTail(40))
		}
		if event.Type == "text-delta" {
			replayed.WriteString(event.Delta)
		}
		if done {
			break
		}
	}
	if got, want := replayed.String(), first+second; got != want {
		t.Fatalf("resumed assistant text = %q, want %q", got, want)
	}

	h.assertChatRowsPersisted(t, ctx, sessionID, agentID, userText, first+second)
}

func (h *harness) openChatStream(t *testing.T, ctx context.Context, agentID, sessionID, text string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"parts": []map[string]any{{"type": "text", "text": text}},
	})
	if err != nil {
		t.Fatalf("marshal send body: %v", err)
	}
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("build send request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST send message: %v\n%s", err, h.proc.LogTail(40))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("send message = %d, want 200\n%s", resp.StatusCode, h.proc.LogTail(40))
	}
	return resp
}

func newSSEScanner(resp *http.Response) *bufio.Scanner {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return scanner
}

func scanTurnEvent(t *testing.T, scanner *bufio.Scanner) (turnEvent, bool) {
	t.Helper()
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			return turnEvent{}, true
		}
		var event turnEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("invalid SSE frame %q: %v", data, err)
		}
		return event, false
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE stream: %v", err)
	}
	t.Fatal("SSE stream ended without [DONE]")
	return turnEvent{}, true
}

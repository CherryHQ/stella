//go:build system

package system

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAnthropic is an in-process stand-in for the Anthropic Messages API. It
// exists so a chat turn can be driven end to end without a real model call:
// deterministic, offline, and free of secrets. The subprocess reaches it over
// loopback because the provider is configured with the fake's base_url, so
// every request the fake records is, by construction, every model request the
// system made — no LLM traffic can leave the host.
//
// The contract is intentionally rigid: tests enqueue an ordered script of
// responses, the fake replays them in order, and it refuses to invent behavior
// for an unscripted request. It never branches on prompt prose; only stable
// request fields (model, tool names) are recorded for assertions, so ordinary
// prompt edits can never turn into a system-test failure.
type fakeAnthropic struct {
	t      *testing.T
	server *httptest.Server

	mu      sync.Mutex
	scripts []fakeResponse // FIFO queue of not-yet-served responses
	reqs    []fakeRequest  // every request received, in arrival order
}

// fakeResponse is one scripted assistant turn. Exactly one shape is set: a
// plain text reply, or a single tool call. Keeping the two explicit (rather
// than a free-form event list) keeps the SSE framing an implementation detail
// the tests never have to spell out.
type fakeResponse struct {
	text string

	toolID   string
	toolName string
	toolArgs string // raw JSON object, e.g. `{"query":"x"}`
}

// fakeRequest captures only the stable fields worth asserting on. The prompt
// body is deliberately not retained: asserting on it would couple the suite to
// prompt wording.
type fakeRequest struct {
	Model     string
	ToolNames []string
}

// newFakeAnthropic starts the fake and registers cleanup that fails the test if
// any scripted response went unused — an unconsumed script means the system
// made fewer model calls than the test assumed, which is a silent contract
// drift the suite must catch.
func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(func() {
		f.server.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.scripts) != 0 {
			t.Errorf("fake anthropic: %d scripted response(s) never consumed; the system made fewer model calls than expected", len(f.scripts))
		}
	})
	return f
}

// baseURL is the loopback address to hand the provider as base_url. The SDK
// appends /v1/messages to it.
func (f *fakeAnthropic) baseURL() string { return f.server.URL }

// enqueueText scripts the next turn as a plain-text assistant reply.
func (f *fakeAnthropic) enqueueText(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, fakeResponse{text: text})
}

// enqueueToolCall scripts the next turn as a single tool call. args must be a
// JSON object literal. It completes the fake's text-or-tool contract; the Goal
// journey (Phase 2) drives tool turns through it.
//
//nolint:unused // consumed by the Phase 2 Goal journey; part of the fake's contract.
func (f *fakeAnthropic) enqueueToolCall(id, name, args string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, fakeResponse{toolID: id, toolName: name, toolArgs: args})
}

// requests returns a copy of every request the fake received, in order, so a
// test can assert the model and tool names it observed.
func (f *fakeAnthropic) requests() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// handle serves one Messages request. It records the stable request fields,
// pops the next scripted response, and streams it as SDK-valid SSE. An
// unscripted request fails the test and returns 500 so the caller sees an
// error rather than a hang.
func (f *fakeAnthropic) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
		f.t.Errorf("fake anthropic: unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusNotFound)
		return
	}

	model, toolNames := parseMessagesRequest(f.t, r)
	f.mu.Lock()
	f.reqs = append(f.reqs, fakeRequest{Model: model, ToolNames: toolNames})
	var resp fakeResponse
	if len(f.scripts) == 0 {
		f.mu.Unlock()
		f.t.Errorf("fake anthropic: unscripted request (model=%q); no response was enqueued", model)
		http.Error(w, "no scripted response", http.StatusInternalServerError)
		return
	}
	resp, f.scripts = f.scripts[0], f.scripts[1:]
	f.mu.Unlock()

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Errorf, not Fatalf: this runs on the server goroutine, where FailNow
		// must not be called. httptest writers always flush, so this is a guard.
		f.t.Errorf("fake anthropic: ResponseWriter is not a Flusher; SSE cannot be streamed")
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, frame := range resp.frames(model) {
		if _, err := io.WriteString(w, frame); err != nil {
			return // client hung up; nothing left to do
		}
		flusher.Flush()
	}
}

// frames renders the scripted response as the ordered SSE event sequence the
// Anthropic SDK's stream decoder consumes: message_start, one content block
// (text or tool_use) with its deltas, then message_delta and message_stop. The
// stop_reason mirrors real API behavior so the provider maps the turn's outcome
// correctly.
func (r fakeResponse) frames(model string) []string {
	var frames []string
	emit := func(event string, data map[string]any) {
		payload, err := json.Marshal(data)
		if err != nil {
			// The data maps are built from literals here, so a marshal error is a
			// programming bug in the fake, not a runtime condition.
			panic(fmt.Sprintf("fake anthropic: marshal %s event: %v", event, err))
		}
		frames = append(frames, fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload))
	}

	emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            "msg_" + strings.TrimPrefix(r.toolID, "toolu_"),
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 1, "output_tokens": 1},
		},
	})

	stopReason := "end_turn"
	if r.toolName != "" {
		stopReason = "tool_use"
		emit("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    r.toolID,
				"name":  r.toolName,
				"input": map[string]any{},
			},
		})
		emit("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": r.toolArgs},
		})
	} else {
		emit("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		emit("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": r.text},
		})
	}
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})

	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 5},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
	return frames
}

// parseMessagesRequest extracts the stable fields the fake is allowed to record
// and assert on. A body it cannot parse is a real defect (the system sent
// something the Anthropic API would reject), so it fails the test rather than
// guessing.
func parseMessagesRequest(t *testing.T, r *http.Request) (model string, toolNames []string) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("fake anthropic: read request body: %v", err)
		return "", nil
	}
	var parsed struct {
		Model string `json:"model"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Errorf("fake anthropic: request body is not valid Messages JSON: %v", err)
		return "", nil
	}
	names := make([]string, 0, len(parsed.Tools))
	for _, tool := range parsed.Tools {
		names = append(names, tool.Name)
	}
	return parsed.Model, names
}

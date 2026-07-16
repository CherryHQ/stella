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
// exists so a chat turn — or a whole Goal run — can be driven end to end without
// a real model call: deterministic, offline, and free of secrets. The
// subprocess reaches it over loopback because the provider is configured with
// the fake's base_url, so every request the fake records is, by construction,
// every model request the system made — no LLM traffic can leave the host.
//
// It supports two scripting modes, one per journey:
//
//   - Chat (Phase 1): an ordered FIFO script of text/tool responses, replayed in
//     arrival order, failing on any unscripted request. See enqueueText.
//   - Goal (Phase 2): responses keyed by the goal_control action the server
//     advertises in the request's tool schema (decompose/submit), matched on
//     that stable structural field rather than arrival order — because a Goal
//     attempt's agent tool loop makes a racy tool-result follow-up call whose
//     arrival is not deterministic. See enqueueGoalControl.
//
// It never branches on prompt prose; only stable request fields (model, tool
// names, the goal_control action enum) drive it, so ordinary prompt edits can
// never turn into a system-test failure.
type fakeAnthropic struct {
	t      *testing.T
	server *httptest.Server

	mu      sync.Mutex
	scripts []fakeResponse // Phase 1 FIFO queue of not-yet-served responses
	reqs    []fakeRequest  // every request received, in arrival order
	// controls holds Phase 2 responses keyed by goal_control action variant
	// ("decompose"/"submit"). Each is served once (the stage's terminal tool_use);
	// a later same-variant request is the racy trailing turn and gets a benign
	// end_turn text so the agent loop terminates without consuming another stage.
	controls map[string]*controlResponse
}

// goalTrailingReply is the benign end_turn text served for the tool-result
// follow-up turn of an already-satisfied goal_control stage. Its only job is to
// end the agent loop cleanly; the terminal action was already recorded.
const goalTrailingReply = "acknowledged"

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

// controlResponse is a goal_control stage's scripted tool_use plus whether it
// has already been served.
type controlResponse struct {
	resp   fakeResponse
	served bool
}

// fakeRequest captures only the stable fields worth asserting on. The prompt
// body is deliberately not retained: asserting on it would couple the suite to
// prompt wording.
type fakeRequest struct {
	Model     string
	ToolNames []string
	// GoalControl is the non-fail goal_control action the request advertised
	// ("decompose"/"submit"/"verdict"), or "" when the request carries no
	// goal_control tool. It is the Goal-stage discriminator.
	GoalControl string
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
		for action, cs := range f.controls {
			if !cs.served {
				t.Errorf("fake anthropic: goal_control %q stage never requested; the Goal run did not reach it", action)
			}
		}
	})
	return f
}

// baseURL is the loopback address to hand the provider as base_url. The SDK
// appends /v1/messages to it.
func (f *fakeAnthropic) baseURL() string { return f.server.URL }

// enqueueText scripts the next Phase 1 turn as a plain-text assistant reply.
func (f *fakeAnthropic) enqueueText(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, fakeResponse{text: text})
}

// enqueueGoalControl scripts the tool_use reply for one goal_control stage,
// matched by the action enum the server advertises in the request's goal_control
// tool schema — not by prompt text or arrival order. args is the full
// goal_control input JSON, e.g. `{"action":"submit","summary":"done"}`.
func (f *fakeAnthropic) enqueueGoalControl(action, args string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.controls == nil {
		f.controls = make(map[string]*controlResponse)
	}
	f.controls[action] = &controlResponse{resp: fakeResponse{
		toolID:   "toolu_" + action,
		toolName: "goal_control",
		toolArgs: args,
	}}
}

// requests returns a copy of every request the fake received, in order, so a
// test can assert the model, tool names, and goal_control stages it observed.
func (f *fakeAnthropic) requests() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// handle serves one Messages request: it records the stable request fields,
// selects a response, and streams it as SDK-valid SSE. An unscripted request
// fails the test and returns 500 so the caller sees an error rather than a hang.
func (f *fakeAnthropic) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
		f.t.Errorf("fake anthropic: unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusNotFound)
		return
	}

	model, toolNames, control := parseMessagesRequest(f.t, r)

	f.mu.Lock()
	f.reqs = append(f.reqs, fakeRequest{Model: model, ToolNames: toolNames, GoalControl: control})
	resp, ok := f.selectResponse(model, control)
	f.mu.Unlock()
	if !ok {
		http.Error(w, "no scripted response", http.StatusInternalServerError)
		return
	}

	flusher, isFlusher := w.(http.Flusher)
	if !isFlusher {
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

// selectResponse picks the response for one request under f.mu. Goal mode (any
// enqueued goal_control stage) matches on the request's goal_control action;
// otherwise the Phase 1 FIFO applies. It records a test failure and returns
// ok=false for any request it cannot answer.
func (f *fakeAnthropic) selectResponse(model, control string) (fakeResponse, bool) {
	if len(f.controls) > 0 {
		cs := f.controls[control]
		switch {
		case cs != nil && !cs.served:
			cs.served = true
			return cs.resp, true
		case cs != nil:
			// The stage's terminal action was already recorded; this is the racy
			// tool-result follow-up turn. End the loop with a benign reply.
			return fakeResponse{text: goalTrailingReply}, true
		default:
			f.t.Errorf("fake anthropic: unscripted goal request (goal_control=%q, model=%q); no stage was enqueued for it", control, model)
			return fakeResponse{}, false
		}
	}

	if len(f.scripts) == 0 {
		f.t.Errorf("fake anthropic: unscripted request (model=%q); no response was enqueued", model)
		return fakeResponse{}, false
	}
	resp := f.scripts[0]
	f.scripts = f.scripts[1:]
	return resp, true
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
// and match on: the model, the tool names, and the non-fail goal_control action
// the request's tool schema advertises. A body it cannot parse is a real defect
// (the system sent something the Anthropic API would reject), so it fails the
// test rather than guessing.
func parseMessagesRequest(t *testing.T, r *http.Request) (model string, toolNames []string, goalControl string) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("fake anthropic: read request body: %v", err)
		return "", nil, ""
	}
	var parsed struct {
		Model string `json:"model"`
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties struct {
					Action struct {
						Enum []string `json:"enum"`
					} `json:"action"`
				} `json:"properties"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Errorf("fake anthropic: request body is not valid Messages JSON: %v", err)
		return "", nil, ""
	}
	names := make([]string, 0, len(parsed.Tools))
	for _, tool := range parsed.Tools {
		names = append(names, tool.Name)
		if tool.Name == "goal_control" {
			goalControl = nonFailAction(tool.InputSchema.Properties.Action.Enum)
		}
	}
	return parsed.Model, names, goalControl
}

// nonFailAction returns the goal_control stage discriminator: the first action
// enum value that is not "fail" (decompose/submit/verdict). The action set the
// server sends distinguishes the decomposition, execution, and review stages
// even though the tool name is always "goal_control".
func nonFailAction(enum []string) string {
	for _, a := range enum {
		if a != "fail" {
			return a
		}
	}
	return ""
}

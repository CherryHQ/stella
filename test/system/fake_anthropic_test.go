//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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
//     arrival order, failing on any unscripted request. See enqueueText and
//     enqueueTool (a tool-using turn is scripted as the call plus the text that
//     ends the turn once the tool result comes back).
//   - Goal (Phase 2): responses keyed by the goal_control action the server
//     advertises (decompose/submit), matched on that stable structural field
//     rather than arrival order — because a Goal attempt's agent tool loop makes
//     a racy tool-result follow-up call whose arrival is not deterministic. See
//     enqueueGoalControl.
//
// It never branches on prompt prose; only stable structural fields (model, tool
// names, the goal_control action enum, and the fake's own marker echoed back in
// a tool result) drive it, so ordinary prompt edits can never turn into a
// system-test failure.
type fakeAnthropic struct {
	t      *testing.T
	server *httptest.Server

	mu           sync.Mutex
	scripts      []fakeResponse            // Phase 1 FIFO queue of not-yet-served responses
	modelScripts map[string][]fakeResponse // stable model-id lanes for concurrent journeys
	// modelTrailing answers a model once its scripted turns are spent. Fan-out
	// makes the number of turns nondeterministic, so a journey that asserts what
	// was said must not also assert that nothing further was ever asked.
	modelTrailing map[string]fakeResponse
	reqs          []fakeRequest // every request received, in arrival order
	// controls holds Phase 2 goal_control arguments keyed by action variant
	// ("decompose"/"submit"). A stage is marked served the moment the fake emits
	// its script, not when a marker comes back: the tool-result follow-up turn is
	// racy, and in the measured run neither attempt took one, so waiting for it
	// would leave decompose recorded as never requested. See enqueueGoalControl.
	controls map[string]*controlResponse
	// errScript, when set, makes the fake answer every request with the same HTTP
	// error instead of an SSE turn. It is sticky (not FIFO-popped) on purpose: the
	// Anthropic SDK may retry a failed call, so one scripted error must satisfy an
	// unknown number of requests without tripping the unscripted-request guard.
	// Used by the chat_provider_error journey. See enqueueError.
	errScript *errorScript
}

// errorScript is a sticky HTTP error the fake returns for every request until
// the fake is torn down. served records that at least one request hit it, so
// cleanup can catch a journey that scripted an error the system never called.
type errorScript struct {
	status int
	body   string
	served bool
}

// turnGate holds a gated turn in flight: handle() flushes the first text-delta,
// then blocks on release until the test opens the gate (or a backstop fires).
// It exists so graceful_drain can pin one turn mid-stream across SIGTERM.
type turnGate struct {
	release chan struct{}
}

// Release opens the gate so the fake finishes the pending turn. Safe to call
// once; the drain journey owns the single gate it created.
func (g *turnGate) Release() { close(g.release) }

// gateBackstop bounds how long a gated turn blocks waiting for release, so a
// test bug can never deadlock the whole suite on a gate that is never opened. It
// is shorter than the harness graceful-shutdown budget, so the backstop fires
// (and fails the test) well before teardown would kill the process group.
const gateBackstop = 30 * time.Second

// goalTrailingReply is the benign end_turn text served once a goal turn's code
// call has come back. Its only job is to end the agent loop cleanly; the
// terminal action was already recorded inside that call.
const goalTrailingReply = "acknowledged"

// The goal scripts report back through these markers. They are the fake's own
// output echoed to it in the next request's tool_result, not prompt prose: the
// fake reads only what it planted.
const (
	goalActionsMarker = "stella-goal-actions:"
	goalStageMarker   = "stella-goal-stage:"
	goalErrorMarker   = "stella-goal-error:"
)

// Code Mode is the only tool path (#1182), so goal_control is a cold tool: it no
// longer appears in the request's tool list, and nothing in the provider-facing
// tool surface tells a decomposition turn from an execution one. The prompts and
// histories do differ, but those are prose the fake must never branch on. The
// stage discriminator did not disappear, it moved — the per-attempt goal_control
// schema now reaches the model through the code catalog — so the fake fetches it
// from there in two steps and keeps matching on exactly the field it always did.
//
// goalProbeScript is step one: read the action enum out of the catalog and hand
// it back. Nothing terminal happens, so the attempt is guaranteed to ask again.
func goalProbeScript() (string, error) {
	return codeCallArguments(fmt.Sprintf(`
let schema;
try {
  schema = tools.describe("goal_control").inputSchema;
} catch (error) {
  return %q + "describe goal_control: " + String(error);
}
const action = schema && schema.properties && schema.properties.action;
return %q + (((action && action.enum) || []).join(","));
`, goalErrorMarker, goalActionsMarker))
}

// goalStageScript is step two: run the stage the probe identified. The attempt
// ends on this call, so the marker it returns is read only if the agent loop
// happens to take one more racy turn — which is why the fake records the stage
// when it serves this script rather than when the marker comes back.
func goalStageScript(action, args string) (string, error) {
	return codeCallArguments(fmt.Sprintf(`
try {
  await tools.invoke("goal_control", %s);
} catch (error) {
  return %q + "goal_control %s: " + String(error);
}
return %q;
`, args, goalErrorMarker, action, goalStageMarker+action))
}

func codeCallArguments(source string) (string, error) {
	args, err := json.Marshal(map[string]string{"code": source})
	if err != nil {
		return "", fmt.Errorf("marshal code arguments: %w", err)
	}
	return string(args), nil
}

// goalTurn is what one request tells the fake about a Goal run: whether it is
// answering a code call, and which of the fake's own markers came back in it.
type goalTurn struct {
	continuation bool
	// actions is the goal_control action enum the probe read out of this
	// attempt's catalog, set only on the turn answering the probe.
	actions []string
	// stage is the action a stage script reported running, set only on a trailing
	// turn after that script.
	stage string
	// failure is a script-reported error, or the reason the fake could not read
	// its own marker back.
	failure string
}

// parseGoalTurn reads the fake's own marker out of the request's last message.
// Only the last message counts: a session's later turns still carry the marker
// in their history, and each marker must drive exactly one decision.
func parseGoalTurn(messages []string, continuation bool) goalTurn {
	turn := goalTurn{continuation: continuation}
	if !continuation || len(messages) == 0 {
		return turn
	}
	last := messages[len(messages)-1]
	if value, ok := markerValue(last, goalErrorMarker); ok {
		turn.failure = value
		return turn
	}
	if value, ok := markerValue(last, goalActionsMarker); ok {
		if value != "" {
			turn.actions = strings.Split(value, ",")
		}
		return turn
	}
	if value, ok := markerValue(last, goalStageMarker); ok {
		turn.stage = value
		return turn
	}
	turn.failure = "code result carried none of the fake's goal markers: " + last
	return turn
}

// markerValue extracts one marker's payload from a flattened message. The code
// result is a JSON string, so the value ends at the closing quote.
func markerValue(text, marker string) (string, bool) {
	_, rest, ok := strings.Cut(text, marker)
	if !ok {
		return "", false
	}
	if j := strings.IndexAny(rest, "\"\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest, true
}

// nonFailAction returns the goal_control stage discriminator: the first action
// enum value that is not "fail" (decompose/submit/verdict). The action set the
// server advertises distinguishes the decomposition, execution, and review
// stages even though the tool name is always "goal_control".
func nonFailAction(enum []string) string {
	for _, a := range enum {
		if a != "fail" {
			return a
		}
	}
	return ""
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

	// errStatus, when non-zero, makes this response an HTTP error rather than an
	// SSE turn: handle() writes errBody with this status and returns. Set only via
	// the sticky errScript path, never enqueued in the FIFO.
	errStatus int
	errBody   string

	// gate, when non-nil, splits this text turn: handle() flushes the first
	// text-delta (text), blocks on the gate, then flushes the remainder (text2).
	gate  *turnGate
	text2 string

	// goalStage names the goal_control stage this response runs, for the request
	// log. It is not part of the SSE turn.
	goalStage string
}

// controlResponse is one goal_control stage's scripted input JSON plus whether
// the attempt that advertised that action has already run it.
type controlResponse struct {
	args   string
	served bool
}

// fakeRequest captures the model-request fields a journey may assert. Response
// selection never uses message text, so ordinary prompt edits cannot alter fake
// behavior; GitHub compatibility alone checks that its delivery body survives.
type fakeRequest struct {
	Model     string
	Messages  []string
	Images    []fakeImage
	ToolNames []string
	// APIKey is the Anthropic x-api-key header. Tests use only explicit,
	// non-production fixture values and never print it from generic diagnostics.
	APIKey string
	// GoalStage is the goal_control action the fake answered this request with,
	// selected from the action enum the attempt advertised. It is "" for the probe
	// and trailing turns of a Goal run, and for every non-Goal request.
	GoalStage string
}

type fakeImage struct {
	MediaType string
	Data      string
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
		for model, scripts := range f.modelScripts {
			if len(scripts) != 0 {
				t.Errorf("fake anthropic: %d scripted response(s) for model %q never consumed", len(scripts), model)
			}
		}
		for action, cs := range f.controls {
			if !cs.served {
				t.Errorf("fake anthropic: goal_control %q stage never requested; the Goal run did not reach it", action)
			}
		}
		if f.errScript != nil && !f.errScript.served {
			t.Errorf("fake anthropic: scripted provider error was never requested; the system made no model call")
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

func (f *fakeAnthropic) enqueueTextForModel(model, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.modelScripts == nil {
		f.modelScripts = make(map[string][]fakeResponse)
	}
	f.modelScripts[model] = append(f.modelScripts[model], fakeResponse{text: text})
}

// setTrailingTextForModel makes a model's spent lane answer benign text rather
// than an unscripted-request failure. Only journeys whose turn count is
// genuinely nondeterministic may use it; a fixed-sequence journey keeps the
// strict guard.
func (f *fakeAnthropic) setTrailingTextForModel(model, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.modelTrailing == nil {
		f.modelTrailing = make(map[string]fakeResponse)
	}
	f.modelTrailing[model] = fakeResponse{text: text}
}

// discardModelScripts closes the mutually-exclusive branch of a concurrent
// journey after its observable outcome has selected the winning agent.
func (f *fakeAnthropic) discardModelScripts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modelScripts = nil
	// Wakes still in flight always run a turn now, so the caller must have set a
	// trailing reply; without one they would hit the unscripted-request guard.
}

func (f *fakeAnthropic) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func TestFakeAnthropicModelScriptsDoNotConsumeFIFO(t *testing.T) {
	f := newFakeAnthropic(t)
	f.enqueueText("fifo")
	f.enqueueTextForModel("agent-a", "a")
	f.mu.Lock()
	a, okA := f.selectResponse("agent-a", goalTurn{})
	b, okB := f.selectResponse("agent-b", goalTurn{})
	f.mu.Unlock()
	if !okA || !okB || a.text != "a" || b.text != "fifo" {
		t.Fatalf("model/FIFO responses = %q/%q", a.text, b.text)
	}
}

func (f *fakeAnthropic) enqueueTool(id, name, args string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, fakeResponse{toolID: id, toolName: name, toolArgs: args})
}

// enqueueGoalControl scripts one goal_control stage, selected by the action enum
// the server advertises to that attempt — not by prompt text or arrival order.
// args is the full goal_control input JSON, e.g.
// `{"action":"submit","summary":"done"}`. The selection happens inside the
// attempt's own code catalog; see goalProbeScript and goalStageScript.
func (f *fakeAnthropic) enqueueGoalControl(action, args string) {
	var parsed struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		f.t.Fatalf("fake anthropic: goal_control %q arguments are not valid JSON: %s", action, args)
	}
	// The key selects the stage and the body performs it; a mismatch would script
	// one stage under another's name and only surface as a confusing goal_control
	// rejection deep inside an attempt.
	if parsed.Action != action {
		f.t.Fatalf("fake anthropic: goal_control stage %q carries action %q", action, parsed.Action)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.controls == nil {
		f.controls = make(map[string]*controlResponse)
	}
	f.controls[action] = &controlResponse{args: args}
}

// goalCodeCall wraps one built script as the code tool_use the fake serves.
// Called under f.mu.
func (f *fakeAnthropic) goalCodeCall(id string, build func() (string, error)) (fakeResponse, bool) {
	args, err := build()
	if err != nil {
		f.t.Errorf("fake anthropic: build goal code call: %v", err)
		return fakeResponse{}, false
	}
	return fakeResponse{toolID: id, toolName: "code", toolArgs: args}, true
}

// enqueueError makes the fake answer every subsequent request with the given
// HTTP status and an Anthropic-shaped error body, until the fake is torn down.
// It is sticky rather than FIFO so an SDK-level retry of the failed call is
// absorbed by the same script instead of hitting the unscripted-request guard;
// the chat_provider_error journey measures the actual request count. Picking a
// non-retried status (400) keeps that count at one, but stickiness makes the
// journey robust regardless.
func (f *fakeAnthropic) enqueueError(status int, errType, message string) {
	body, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
	if err != nil {
		// Built from literals, so a marshal failure is a bug in the test.
		f.t.Fatalf("fake anthropic: marshal error body: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errScript = &errorScript{status: status, body: string(body)}
}

// enqueueGatedText scripts the next FIFO turn as a two-part text reply split by
// a gate: the fake flushes first, then blocks until the returned gate is
// released, then flushes second. The caller releases the gate to let the pinned
// turn finish. Used by graceful_drain to hold a turn in flight across SIGTERM.
func (f *fakeAnthropic) enqueueGatedText(first, second string) *turnGate {
	gate := &turnGate{release: make(chan struct{})}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, fakeResponse{text: first, text2: second, gate: gate})
	return gate
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

// waitForRequests synchronizes an async journey with the fake without putting
// backpressure on unrelated model requests.
func (f *fakeAnthropic) waitForRequests(ctx context.Context, want int) []fakeRequest {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if reqs := f.requests(); len(reqs) >= want {
			return reqs
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			f.t.Fatalf("fake anthropic: waited for %d request(s): %v", want, ctx.Err())
			return nil
		}
	}
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

	model, messages, images, toolNames, continuation := parseMessagesRequest(f.t, r)
	goal := parseGoalTurn(messages, continuation)

	f.mu.Lock()
	index := len(f.reqs)
	f.reqs = append(f.reqs, fakeRequest{
		Model: model, Messages: messages, Images: images, ToolNames: toolNames,
		APIKey: r.Header.Get("x-api-key"),
	})
	resp, ok := f.selectResponse(model, goal)
	f.reqs[index].GoalStage = resp.goalStage
	f.mu.Unlock()
	if !ok {
		http.Error(w, "no scripted response", http.StatusInternalServerError)
		return
	}

	// A scripted provider error is an HTTP error response, not a turn: the system
	// must surface it as an error frame on the SSE stream it already opened.
	if resp.errStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.errStatus)
		_, _ = io.WriteString(w, resp.errBody)
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

	// A gated turn flushes its first half, then blocks until the test opens the
	// gate (or the backstop fires), then flushes the rest — pinning the turn in
	// flight so a drain can be observed while it is mid-stream.
	if resp.gate != nil {
		before, after := resp.gatedFrames(model)
		if !f.writeFrames(w, flusher, before) {
			return
		}
		select {
		case <-resp.gate.release:
		case <-time.After(gateBackstop):
			f.t.Errorf("fake anthropic: gated turn never released within %s; finishing it to avoid a suite deadlock", gateBackstop)
		}
		f.writeFrames(w, flusher, after)
		return
	}

	f.writeFrames(w, flusher, resp.frames(model))
}

// writeFrames flushes each SSE frame in order, stopping early (ok=false) if the
// client hung up so the caller does not keep writing to a dead connection.
func (f *fakeAnthropic) writeFrames(w http.ResponseWriter, flusher http.Flusher, frames []string) bool {
	for _, frame := range frames {
		if _, err := io.WriteString(w, frame); err != nil {
			return false
		}
		flusher.Flush()
	}
	return true
}

// selectResponse picks the response for one request under f.mu. Goal mode (any
// enqueued goal_control stage) hands every fresh turn the stage-selecting code
// call and closes the turn once its marker comes back; otherwise the Phase 1
// FIFO applies. It records a test failure and returns ok=false for any request
// it cannot answer.
func (f *fakeAnthropic) selectResponse(model string, goal goalTurn) (fakeResponse, bool) {
	// A sticky provider error answers every request (including SDK retries) so a
	// journey testing the failure path never trips the unscripted-request guard.
	if f.errScript != nil {
		f.errScript.served = true
		return fakeResponse{errStatus: f.errScript.status, errBody: f.errScript.body}, true
	}

	if len(f.controls) > 0 {
		return f.selectGoalResponse(model, goal)
	}

	if scripts := f.modelScripts[model]; len(scripts) > 0 {
		resp := scripts[0]
		f.modelScripts[model] = scripts[1:]
		return resp, true
	}
	if resp, ok := f.modelTrailing[model]; ok {
		return resp, true
	}
	if len(f.scripts) == 0 {
		f.t.Errorf("fake anthropic: unscripted request (model=%q); no response was enqueued", model)
		return fakeResponse{}, false
	}
	resp := f.scripts[0]
	f.scripts = f.scripts[1:]
	return resp, true
}

// selectGoalResponse answers one Goal turn. Called under f.mu.
//
//   - A fresh turn gets the probe: nothing terminal has run, so the attempt is
//     guaranteed to come back with its action enum.
//   - The turn answering the probe names the stage; the fake serves that stage's
//     goal_control call and records it as requested right there. Recording on the
//     serve is what makes this deterministic: a stage script ends the attempt, so
//     whether its own marker ever comes back is up to the racy trailing turn.
//   - A turn answering a stage script, when one happens, is that trailing turn:
//     the terminal action is already recorded, so a benign end_turn ends the loop
//     without consuming another stage.
func (f *fakeAnthropic) selectGoalResponse(model string, goal goalTurn) (fakeResponse, bool) {
	if !goal.continuation {
		return f.goalCodeCall("toolu_goal_probe", goalProbeScript)
	}
	if goal.failure != "" {
		f.t.Errorf("fake anthropic: goal code call failed (model=%q): %s", model, goal.failure)
		return fakeResponse{}, false
	}
	if goal.stage != "" {
		return fakeResponse{text: goalTrailingReply}, true
	}
	action := nonFailAction(goal.actions)
	cs := f.controls[action]
	if cs == nil {
		f.t.Errorf("fake anthropic: goal attempt advertised actions %v (model=%q); no stage was enqueued for %q", goal.actions, model, action)
		return fakeResponse{}, false
	}
	if cs.served {
		// The same stage asked twice: its terminal action is already recorded, so
		// running it again would fail inside goal_control. End the loop instead.
		return fakeResponse{text: goalTrailingReply}, true
	}
	cs.served = true
	resp, ok := f.goalCodeCall("toolu_goal_"+action, func() (string, error) {
		return goalStageScript(action, cs.args)
	})
	resp.goalStage = action
	return resp, ok
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

// gatedFrames renders a two-part text turn as the frames to flush before the
// gate (message_start, the text block's start, and the first text delta) and
// the frames to flush after it (the second text delta, then the block and
// message close). Splitting at the first delta means the client has received
// real streamed text — proving the turn is genuinely in flight — before the
// turn is pinned. It never carries a tool call; only text turns are gated.
func (r fakeResponse) gatedFrames(model string) (before, after []string) {
	full := fakeResponse{text: r.text}.frames(model)
	second := fakeResponse{text: r.text2}.textDeltaFrame()
	// full is: message_start, content_block_start, content_block_delta(text),
	// content_block_stop, message_delta, message_stop. Gate right after the first
	// text delta (index 3), inserting the second delta into the tail.
	before = full[:3]
	after = append([]string{second}, full[3:]...)
	return before, after
}

// textDeltaFrame renders a single text content_block_delta frame — the unit a
// gated turn resumes with after release.
func (r fakeResponse) textDeltaFrame() string {
	data, err := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "text_delta", "text": r.text},
	})
	if err != nil {
		panic(fmt.Sprintf("fake anthropic: marshal text delta: %v", err))
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", "content_block_delta", data)
}

// parseMessagesRequest extracts the stable fields the fake is allowed to record
// and match on: the model, the tool names, and the message payloads. A body it
// cannot parse is a real defect (the system sent something the Anthropic API
// would reject), so it fails the test rather than guessing.
func parseMessagesRequest(t *testing.T, r *http.Request) (model string, messages []string, images []fakeImage, toolNames []string, continuation bool) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("fake anthropic: read request body: %v", err)
		return "", nil, nil, nil, false
	}
	var parsed struct {
		Model    string `json:"model"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Errorf("fake anthropic: request body is not valid Messages JSON: %v", err)
		return "", nil, nil, nil, false
	}
	// Only the request that *answers* a tool call is a continuation. A session's
	// later turns still carry that tool result in their history, so matching the
	// whole body would pin every following turn to the continuation lane.
	if len(parsed.Messages) > 0 {
		continuation = bytes.Contains(parsed.Messages[len(parsed.Messages)-1].Content, []byte(`"tool_result"`))
	}
	messages = make([]string, 0, len(parsed.Messages))
	for _, message := range parsed.Messages {
		text, messageImages, ok := messagePayload(message.Content)
		if !ok {
			t.Errorf("fake anthropic: unsupported message content %s", message.Content)
			continue
		}
		messages = append(messages, text)
		images = append(images, messageImages...)
	}
	names := make([]string, 0, len(parsed.Tools))
	for _, tool := range parsed.Tools {
		names = append(names, tool.Name)
	}
	return parsed.Model, messages, images, names, continuation
}

// messagePayload extracts text and images from top-level message blocks and
// nested Anthropic tool_result content. Response selection never uses either;
// journeys inspect them only after the fake has recorded the request.
func messagePayload(content json.RawMessage) (string, []fakeImage, bool) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, nil, true
	}
	var out strings.Builder
	var images []fakeImage
	if !collectMessageBlocks(content, &out, &images) {
		return "", nil, false
	}
	return out.String(), images, true
}

func collectMessageBlocks(content json.RawMessage, text *strings.Builder, images *[]fakeImage) bool {
	var shorthand string
	if json.Unmarshal(content, &shorthand) == nil {
		text.WriteString(shorthand)
		return true
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
		Source  struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return false
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "image":
			if block.Source.Type != "base64" || block.Source.MediaType == "" || block.Source.Data == "" {
				return false
			}
			*images = append(*images, fakeImage{MediaType: block.Source.MediaType, Data: block.Source.Data})
		case "tool_result":
			if len(block.Content) > 0 && !collectMessageBlocks(block.Content, text, images) {
				return false
			}
		}
	}
	return true
}

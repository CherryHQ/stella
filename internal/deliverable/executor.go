package deliverable

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/tools"
)

// executor.go ports the old internal/tasks executor + control-tool + execution
// protocol onto the deliverable entity. The Executor boundary is preserved
// verbatim from today's design: Execute is PURE with respect to durable state —
// it pumps an agent turn, captures the FIRST terminal action the agent declares
// through the deliverable_control tool, and returns it. The WORKER applies the
// single transition via DeliverableService. The agent never writes lifecycle.

// TerminalAction is the durable outcome an executor reports for one attempt. The
// worker maps it to exactly one service transition; the agent never mutates
// deliverable/attempt state directly (the contract's non-negotiable).
type TerminalAction string

const (
	// terminalNone is the absence of a declared action — a silent or text-only
	// turn. It is unexported because callers branch on the typed ExecutorResult,
	// not on this internal action.
	terminalNone      TerminalAction = ""
	terminalSubmit    TerminalAction = "submit"
	terminalBlock     TerminalAction = "block"
	terminalFail      TerminalAction = "fail"
	terminalDecompose TerminalAction = "decompose"
)

// Blocker carries a block action's payload. The deliverable model resolves
// blocks through the service (dep / needs_verdict / budget_exhausted), so an
// agent-declared block surfaces to the worker as a non-retryable failure whose
// reason embeds this payload; the structured form is kept here for the worker
// and for callers that introspect a recorded result.
type Blocker struct {
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Detail   any    `json:"detail,omitempty"`
}

// Failure carries a fail action's payload.
type Failure struct {
	Reason    string `json:"reason"`
	Retryable bool   `json:"retryable"`
}

// Result is the executor's rich internal outcome for one attempt: exactly one
// terminal action (or terminalNone when the agent ended without declaring one).
// Execute folds this down to the frozen ExecutorResult the worker consumes.
type Result struct {
	Action        TerminalAction
	Evidence      AttemptEvidence
	Output        AttemptOutput
	Decomposition *DecompositionContent // purpose=decomposition only
	Blocker       *Blocker
	Failure       *Failure
	// RepairAttempted is set when a text-only first turn triggered one bounded
	// repair turn that still produced no terminal action. It only carries meaning
	// for terminalNone and lets the worker distinguish a silent miss from a
	// failed repair.
	RepairAttempted bool
}

// TaskChatParams / TaskChatFunc — the persisted-worker-turn callback — are
// declared in boot.go (BootConfig.Chat is a TaskChatFunc). This file consumes
// them; it does not re-declare them.

// terminalRecorder captures the first terminal action declared during an attempt.
// Later terminal declarations are rejected so a stray second tool call cannot
// change the outcome.
type terminalRecorder struct {
	mu     sync.Mutex
	done   bool
	result Result
}

// record stores the first terminal action. A second call returns an error the
// agent tool surfaces back to the model.
func (r *terminalRecorder) record(res Result) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return fmt.Errorf("deliverable_control: terminal action already recorded")
	}
	r.done = true
	r.result = res
	return nil
}

func (r *terminalRecorder) isDone() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

// snapshot returns the recorded result and whether a terminal action fired.
func (r *terminalRecorder) snapshot() (Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.done
}

// workerExecutor is the agent-backed Executor for execution and decomposition
// attempts. It wires a recording deliverable_control tool, runs each turn
// through ChatFunc (which persists the transcript to the deliverable session),
// and pumps the chat loop until a terminal action fires or the channel closes.
type workerExecutor struct {
	chat TaskChatFunc
	log  *slog.Logger
}

// newWorkerExecutor builds the default agent-backed executor.
func newWorkerExecutor(chat TaskChatFunc, log *slog.Logger) *workerExecutor {
	if log == nil {
		log = slog.Default().With("component", "deliverable/executor")
	}
	return &workerExecutor{chat: chat, log: log}
}

// Execute runs one attempt and returns the frozen ExecutorResult. All outcomes
// are encoded so the worker applies a single transition uniformly:
//   - agent declared submit       -> Submitted (+ Decomposition for purpose=decomposition)
//   - agent declared fail         -> Failed with reason/retryable
//   - agent declared block        -> Failed, non-retryable, reason embeds the block payload
//   - misconfigured attempt       -> Failed, non-retryable
//   - runner setup / stream error -> Failed, retryable
//   - clean exit without action   -> Failed, non-retryable protocol miss
//
// The agent never mutates durable state — the worker reads this and applies the
// matching transition through DeliverableService.
func (e *workerExecutor) Execute(ctx context.Context, req ExecutorRequest) (ExecutorResult, error) {
	res, err := e.run(ctx, req)
	if err != nil {
		return ExecutorResult{}, err
	}
	return foldResult(res, req), nil
}

// run pumps the attempt's chat loop and returns the rich recorded Result.
func (e *workerExecutor) run(ctx context.Context, req ExecutorRequest) (Result, error) {
	agentID := req.Attempt.ExecutorAgentID.String
	if !req.Attempt.ExecutorAgentID.Valid || agentID == "" {
		agentID = req.Attempt.AgentID.String
	}
	if agentID == "" {
		return failResult("no executor agent on attempt", false), nil
	}

	decompose := req.Attempt.Purpose == PurposeDecomposition
	rec := &terminalRecorder{}
	ctTool := newRecordingControlTool(rec, decompose, e.log)
	projectID := req.Deliverable.ProjectID.String

	turn := func(prompt string) <-chan agent.Event {
		return e.chat(ctx, TaskChatParams{
			AgentID:    agentID,
			UserID:     req.Attempt.UserID,
			SessionID:  req.Attempt.SessionID,
			ProjectID:  projectID,
			Prompt:     prompt,
			ExtraTools: []tools.Tool{ctTool},
		})
	}

	// First turn against the frozen input context.
	text, res, done, fail := e.runTurn(ctx, turn(buildAttemptPrompt(req, decompose)), rec)
	if fail != nil {
		return *fail, nil
	}
	if done {
		return res, nil
	}

	// The turn ended without a terminal action. A silent exit (no assistant text)
	// is an unrecoverable protocol miss; a text-only answer gets exactly one
	// bounded repair turn that re-states the protocol with the prior text as
	// context. There is no auto-submit of free text.
	if strings.TrimSpace(text) == "" {
		return Result{Action: terminalNone}, nil
	}

	_, res, done, fail = e.runTurn(ctx, turn(buildRepairPrompt(text, decompose)), rec)
	if fail != nil {
		return *fail, nil
	}
	if done {
		return res, nil
	}
	return Result{Action: terminalNone, RepairAttempted: true}, nil
}

// runTurn pumps one chat turn until a terminal action is recorded or the event
// channel closes. It returns the assistant text emitted during the turn, the
// recorded Result (when done), whether a terminal action fired, and a non-nil
// fail Result if the stream errored before any terminal action.
func (e *workerExecutor) runTurn(ctx context.Context, events <-chan agent.Event, rec *terminalRecorder) (text string, res Result, done bool, fail *Result) {
	var buf strings.Builder
	for ev := range events {
		if ev.Err != nil {
			if rec.isDone() {
				go drainEvents(events)
				r, _ := rec.snapshot()
				return buf.String(), r, true, nil
			}
			e.log.Warn("deliverable executor stream error", "err", ev.Err)
			f := failResult(fmt.Sprintf("runner error: %v", ev.Err), true)
			return buf.String(), Result{}, false, &f
		}
		if ev.Text != "" {
			buf.WriteString(ev.Text)
		}
		if rec.isDone() {
			go drainEvents(events)
			r, _ := rec.snapshot()
			return buf.String(), r, true, nil
		}
	}
	if rec.isDone() {
		r, _ := rec.snapshot()
		return buf.String(), r, true, nil
	}
	return buf.String(), Result{}, false, nil
}

// foldResult maps the rich internal Result onto the frozen ExecutorResult the
// worker applies. The deliverable model has no executor-driven block path, so a
// block (and any unhandled protocol miss) collapses to a non-retryable failure;
// the block's structured payload is embedded in FailReason for the worker.
func foldResult(res Result, req ExecutorRequest) ExecutorResult {
	switch res.Action {
	case terminalSubmit:
		return ExecutorResult{
			Submitted: true,
			Evidence:  res.Evidence,
			Output:    res.Output,
		}
	case terminalDecompose:
		return ExecutorResult{
			Submitted:     true,
			Evidence:      res.Evidence,
			Output:        res.Output,
			Decomposition: res.Decomposition,
		}
	case terminalFail:
		f := res.Failure
		if f == nil {
			f = &Failure{Reason: "agent reported failure"}
		}
		return ExecutorResult{Failed: true, FailReason: f.Reason, Retryable: f.Retryable}
	case terminalBlock:
		return ExecutorResult{Failed: true, FailReason: blockReason(res.Blocker), Retryable: false}
	default: // terminalNone — silent or failed-repair protocol miss
		reason := "agent ended without a deliverable_control terminal action"
		if res.RepairAttempted {
			reason = "agent failed to call deliverable_control after one repair turn"
		}
		return ExecutorResult{Failed: true, FailReason: reason, Retryable: false}
	}
}

// blockReason renders an agent block as a stable, parseable failure reason.
func blockReason(b *Blocker) string {
	if b == nil {
		return "block"
	}
	payload, err := json.Marshal(b)
	if err != nil {
		return "block: " + b.Question
	}
	return "block: " + string(payload)
}

// failResult is a constructor for a non-agent failure outcome.
func failResult(reason string, retryable bool) Result {
	return Result{Action: terminalFail, Failure: &Failure{Reason: reason, Retryable: retryable}}
}

// drainEvents consumes remaining events so the runner can close cleanly after a
// terminal action has been recorded.
func drainEvents(ch <-chan agent.Event) {
	for range ch { //nolint:revive // intentional drain
	}
}

// ── prompt assembly ─────────────────────────────────────────────────────────

// buildAttemptPrompt assembles the worker turn from the frozen AttemptInput. The
// terminal deliverable_control rule is repeated in the user message because
// workers run as normal agents with extra tools; relying on the tool description
// alone makes protocol errors too easy.
func buildAttemptPrompt(req ExecutorRequest, decompose bool) string {
	in := req.Input
	var b strings.Builder

	if decompose {
		b.WriteString(`You are decomposing a Stella composite deliverable into child deliverables.

Protocol:
- Plan the smallest set of child deliverables that, once all accepted, complete this deliverable.
- You may use tools normally while planning.
- Before ending this turn, you MUST call deliverable_control exactly once with one terminal action:
  - action="decompose" with a "decomposition" object {children, edges} when the plan is ready.
  - action="fail" with reason/retryable if the deliverable cannot be decomposed.
- Each child needs key, title, intent, kind (leaf|composite), required, acceptance_contract, convergence_policy.
- Edges (by child key) declare hard/soft dependencies; only accepted upstream output flows downstream.

Deliverable:
`)
	} else {
		b.WriteString(`You are executing a durable Stella deliverable.

Protocol:
- You may use tools normally while working.
- Before ending this turn, you MUST call deliverable_control exactly once with one terminal action:
  - action="submit" with evidence (summary + optional artifacts) and output when the work is complete.
  - action="block" with kind/question when you need input or an external dependency.
  - action="fail" with reason/retryable when the work cannot be completed.
- Do not just answer in chat. A final text response without deliverable_control is treated as a protocol failure.

Deliverable:
`)
	}

	b.WriteString(strings.TrimSpace(in.Intent))

	if c := in.Contract; len(c.Items) > 0 {
		b.WriteString("\n\nAcceptance criteria (your work is accepted only when these are met):\n")
		for _, it := range c.Items {
			line := "- "
			switch {
			case it.Command != "":
				line += it.Command
			case it.Prompt != "":
				line += it.Prompt
			case it.Rubric != "":
				line += it.Rubric
			default:
				line += it.ID
			}
			b.WriteString(line + "\n")
		}
	}

	if len(in.UpstreamOutputs) > 0 {
		b.WriteString("\nUpstream accepted outputs you build on:\n")
		for _, up := range in.UpstreamOutputs {
			s := strings.TrimSpace(up.Summary)
			if s == "" {
				continue
			}
			b.WriteString("- " + s + "\n")
		}
	}

	if in.PriorGaps != nil && len(in.PriorGaps.Gaps) > 0 {
		b.WriteString("\nYour previous attempt fell short on:\n")
		for _, g := range in.PriorGaps.Gaps {
			line := "- " + g.ItemID
			if g.Reason != "" {
				line += ": " + g.Reason
			}
			b.WriteString(line + "\n")
		}
	}

	if v := strings.TrimSpace(in.ResolvedVerdict); v != "" {
		b.WriteString("\nA reviewer's resolution for this work:\n" + v + "\n")
	}

	return b.String()
}

// buildRepairPrompt is the single bounded correction turn for a worker that
// answered in plain text without calling deliverable_control. It echoes the
// prior answer as context and demands exactly one terminal action — it never
// submits the text automatically.
func buildRepairPrompt(priorText string, decompose bool) string {
	action := `  - action="submit" with evidence + output if the work is complete.
  - action="block" with kind/question if you need input or an external dependency.
  - action="fail" with reason/retryable if the work cannot be completed.`
	if decompose {
		action = `  - action="decompose" with a "decomposition" object {children, edges} if the plan is ready.
  - action="fail" with reason/retryable if the deliverable cannot be decomposed.`
	}
	return `Your previous response did not call deliverable_control, so this deliverable is not yet resolved.

Your previous message was:
"""
` + priorText + `
"""

You MUST now call deliverable_control exactly once with one terminal action:
` + action + `

Do not answer in plain text again.`
}

// ── recording control tool ──────────────────────────────────────────────────

// recordingControlTool is the agent-facing deliverable_control tool. It is PURE
// with respect to durable state: submit/block/fail/decompose record one terminal
// action into the recorder for the worker to apply. Unlike the old task_control
// it has no progress side-effect — durable writes belong to the service.
type recordingControlTool struct {
	rec       *terminalRecorder
	decompose bool
	log       *slog.Logger
}

// newRecordingControlTool wires a recording tool for one attempt. decompose
// switches the accepted action set to decomposition.
func newRecordingControlTool(rec *terminalRecorder, decompose bool, log *slog.Logger) *recordingControlTool {
	return &recordingControlTool{rec: rec, decompose: decompose, log: log}
}

func (t *recordingControlTool) Definition() tools.Definition {
	if t.decompose {
		return ai.ToolDefinition{
			Name:        "deliverable_control",
			Description: "Report decomposition outcome. Call exactly one of decompose/fail before exiting.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []string{"decompose", "fail"},
						"description": "Which terminal action to take.",
					},
					"summary": map[string]any{
						"type":        "string",
						"description": "decompose: one-line description of the plan.",
					},
					"decomposition": map[string]any{
						"type":        "object",
						"description": "decompose: {children:[...], edges:[...]} per the decomposition schema.",
					},
					"reason":    map[string]any{"type": "string", "description": "fail: error message."},
					"retryable": map[string]any{"type": "boolean", "description": "fail: true if a retry may succeed."},
				},
				"required": []string{"action"},
			},
		}
	}
	return ai.ToolDefinition{
		Name:        "deliverable_control",
		Description: "Report deliverable lifecycle. Call exactly one of submit/block/fail before exiting.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"submit", "block", "fail"},
					"description": "Which terminal action to take.",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "submit: handoff summary describing what you produced (required for child deliverables).",
				},
				"artifacts": map[string]any{
					"type":        "array",
					"description": "submit: hash-addressed artifact refs (diffs/files/stdout).",
				},
				"output": map[string]any{
					"type":        "object",
					"description": "submit: structured result the acceptance contract evaluates.",
				},
				"notes": map[string]any{
					"type":        "object",
					"description": "submit: free-form evidence notes.",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "block: blocker kind (user_input|external_dependency|tool_error|policy_hold).",
				},
				"question": map[string]any{
					"type":        "string",
					"description": "block: human-readable explanation of what's needed.",
				},
				"detail":    map[string]any{"type": "object", "description": "block: structured detail."},
				"reason":    map[string]any{"type": "string", "description": "fail: error message."},
				"retryable": map[string]any{"type": "boolean", "description": "fail: true if a retry may succeed."},
			},
			"required": []string{"action"},
		},
	}
}

func (t *recordingControlTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	if t.decompose {
		return t.executeDecompose(action, args)
	}
	switch action {
	case "submit":
		ev := AttemptEvidence{
			Summary: stringArg(args, "summary"),
			Notes:   mapArg(args, "notes"),
		}
		if arts := decodeArtifacts(args["artifacts"]); len(arts) > 0 {
			ev.Artifacts = arts
		}
		out := AttemptOutput{
			Summary: stringArg(args, "summary"),
			Result:  mapArg(args, "output"),
		}
		out.Hash = HashWithArtifacts(out, ev.Artifacts)
		if err := t.rec.record(Result{Action: terminalSubmit, Evidence: ev, Output: out}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"submit"}`, nil
	case "block":
		if err := t.rec.record(Result{Action: terminalBlock, Blocker: &Blocker{
			Kind:     stringArg(args, "kind"),
			Question: stringArg(args, "question"),
			Detail:   args["detail"],
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"block"}`, nil
	case "fail":
		if err := t.rec.record(Result{Action: terminalFail, Failure: &Failure{
			Reason:    stringArg(args, "reason"),
			Retryable: boolArg(args, "retryable"),
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"fail"}`, nil
	default:
		return "", fmt.Errorf("deliverable_control: unknown action %q", action)
	}
}

// executeDecompose handles the purpose=decomposition action set.
func (t *recordingControlTool) executeDecompose(action string, args map[string]any) (string, error) {
	switch action {
	case "decompose":
		content, err := decodeDecomposition(args["decomposition"])
		if err != nil {
			return "", err
		}
		ev := AttemptEvidence{Summary: stringArg(args, "summary")}
		if err := t.rec.record(Result{Action: terminalDecompose, Evidence: ev, Decomposition: &content}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"decompose"}`, nil
	case "fail":
		if err := t.rec.record(Result{Action: terminalFail, Failure: &Failure{
			Reason:    stringArg(args, "reason"),
			Retryable: boolArg(args, "retryable"),
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"fail"}`, nil
	default:
		return "", fmt.Errorf("deliverable_control: unknown action %q", action)
	}
}

// ── arg decoders ────────────────────────────────────────────────────────────

func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func boolArg(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

func mapArg(args map[string]any, key string) map[string]any {
	m, _ := args[key].(map[string]any)
	return m
}

// decodeArtifacts round-trips the loosely-typed tool arg into []ArtifactRef.
func decodeArtifacts(v any) []ArtifactRef {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var arts []ArtifactRef
	if err := json.Unmarshal(b, &arts); err != nil {
		return nil
	}
	return arts
}

// decodeDecomposition round-trips the tool arg into DecompositionContent. An
// absent or malformed object is an agent protocol error surfaced back to the
// model so it can retry within the same turn.
func decodeDecomposition(v any) (DecompositionContent, error) {
	if v == nil {
		return DecompositionContent{}, fmt.Errorf("deliverable_control: decompose requires a decomposition object")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return DecompositionContent{}, fmt.Errorf("deliverable_control: decomposition not serialisable: %w", err)
	}
	var c DecompositionContent
	if err := json.Unmarshal(b, &c); err != nil {
		return DecompositionContent{}, fmt.Errorf("deliverable_control: decomposition not valid: %w", err)
	}
	return c, nil
}

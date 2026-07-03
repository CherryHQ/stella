package goal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

// executor.go ports the old internal/tasks executor + control-tool + execution
// protocol onto the goal entity. The Executor boundary is preserved
// verbatim from today's design: Execute is PURE with respect to durable state —
// it pumps an agent turn, captures the FIRST terminal action the agent declares
// through the goal_control tool, and returns it. The WORKER applies the
// single transition via GoalService. The agent never writes lifecycle.

// TerminalAction is the durable outcome an executor reports for one attempt. The
// worker maps it to exactly one service transition; the agent never mutates
// goal/attempt state directly (the contract's non-negotiable).
type TerminalAction string

const (
	// terminalNone is the absence of a declared action — a silent or text-only
	// turn. It is unexported because callers branch on the typed ExecutorResult,
	// not on this internal action.
	terminalNone      TerminalAction = ""
	terminalSubmit    TerminalAction = "submit"
	terminalFail      TerminalAction = "fail"
	terminalDecompose TerminalAction = "decompose"
	terminalVerdict   TerminalAction = "verdict"
)

// Failure carries a fail action's payload.
type Failure struct {
	Reason       string `json:"reason"`
	BlockedBy    string `json:"blocked_by"`
	FailureClass string `json:"-"`
}

// Result is the executor's rich internal outcome for one attempt: exactly one
// terminal action (or terminalNone when the agent ended without declaring one).
// Execute folds this down to the frozen ExecutorResult the worker consumes.
type Result struct {
	Action        TerminalAction
	Evidence      AttemptEvidence
	Output        AttemptOutput
	Decomposition *DecompositionContent // purpose=decomposition only
	Verdicts      []ReviewVerdict       // purpose=review only
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

type executorTurn struct {
	events <-chan agent.Event
	cancel context.CancelFunc
}

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
		return fmt.Errorf("goal_control: terminal action already recorded")
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

func terminalSubmitSandboxCallback(rec *terminalRecorder, cb func(sandbox.Session) error) func(sandbox.Session) error {
	if cb == nil {
		return nil
	}
	return func(sess sandbox.Session) error {
		res, ok := rec.snapshot()
		if !ok || res.Action != terminalSubmit {
			return nil
		}
		return cb(sess)
	}
}

// workerExecutor is the agent-backed Executor for execution and decomposition
// attempts. It wires a recording goal_control tool, runs each turn
// through ChatFunc (which persists the transcript to the goal session),
// and pumps the chat loop until a terminal action fires or the channel closes.
type workerExecutor struct {
	chat TaskChatFunc
	log  *slog.Logger
}

// newWorkerExecutor builds the default agent-backed executor.
func newWorkerExecutor(chat TaskChatFunc, log *slog.Logger) *workerExecutor {
	if log == nil {
		log = slog.Default().With("component", "goal/executor")
	}
	return &workerExecutor{chat: chat, log: log}
}

// Execute runs one attempt and returns the frozen ExecutorResult. All outcomes
// are encoded so the worker applies a single transition uniformly:
//   - agent declared submit       -> Submitted (+ Decomposition for purpose=decomposition)
//   - agent declared fail         -> Failed with responsibility class
//   - misconfigured attempt       -> Failed with environment responsibility
//   - runner setup / stream error -> Failed with flaky responsibility
//   - clean exit without action   -> Failed with model responsibility
//
// The agent never mutates durable state — the worker reads this and applies the
// matching transition through GoalService.
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
		return failResult("no executor agent on attempt", FailureClassEnvironment, BlockEnvUnavailable), nil
	}

	decompose := req.Attempt.Purpose == PurposeDecomposition
	review := req.Attempt.Purpose == PurposeReview
	rec := &terminalRecorder{}
	var ctTool *recordingControlTool
	if review {
		ctTool = newReviewControlTool(rec, req.Input.ReviewItems, e.log)
	} else {
		ctTool = newRecordingControlTool(rec, decompose, int(req.Goal.Depth), req.Input.MaxDepth, e.log)
	}
	projectID := req.Goal.ProjectID.String

	turn := func(prompt string) executorTurn {
		turnCtx, cancel := context.WithCancel(ctx)
		return executorTurn{
			events: e.chat(turnCtx, TaskChatParams{
				AgentID:          agentID,
				UserID:           req.Attempt.UserID,
				SessionID:        req.Attempt.SessionID,
				ProjectID:        projectID,
				Prompt:           prompt,
				Decompose:        decompose,
				ExtraTools:       []tools.Tool{ctTool},
				OnSandboxSession: terminalSubmitSandboxCallback(rec, req.OnSandboxSession),
			}),
			cancel: cancel,
		}
	}

	firstPrompt := buildAttemptPrompt(req, decompose)
	repairPrompt := func(text string) string { return buildRepairPrompt(text, decompose) }
	if review {
		firstPrompt = buildReviewPrompt(req)
		repairPrompt = buildReviewRepairPrompt
	}

	// First turn against the frozen input context.
	text, res, done, fail, err := e.runTurn(ctx, turn(firstPrompt), rec)
	if err != nil {
		return Result{}, err
	}
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

	_, res, done, fail, err = e.runTurn(ctx, turn(repairPrompt(text)), rec)
	if err != nil {
		return Result{}, err
	}
	if fail != nil {
		return *fail, nil
	}
	if done {
		return res, nil
	}
	return Result{Action: terminalNone, RepairAttempted: true}, nil
}

// runTurn pumps one chat turn until the event channel closes. Once a terminal
// action is recorded, it cancels the turn but keeps draining so the one-shot
// session can run its pre-close sandbox callback and close before Execute returns.
func (e *workerExecutor) runTurn(ctx context.Context, turn executorTurn, rec *terminalRecorder) (text string, res Result, done bool, fail *Result, err error) {
	cancelled := false
	cancelTurn := func() {
		if !cancelled {
			cancelled = true
			turn.cancel()
		}
	}
	defer cancelTurn()

	var buf strings.Builder
	for ev := range turn.events {
		if ev.Err != nil {
			if done {
				e.log.Warn("goal executor cleanup error", "err", ev.Err)
				f := failResult(fmt.Sprintf("runner cleanup error: %v", ev.Err), FailureClassFlaky, "")
				return buf.String(), Result{}, false, &f, nil
			}
			e.log.Warn("goal executor stream error", "err", ev.Err)
			f := failResult(fmt.Sprintf("runner error: %v", ev.Err), FailureClassFlaky, "")
			return buf.String(), Result{}, false, &f, nil
		}
		if ev.Text != "" {
			buf.WriteString(ev.Text)
		}
		if !done && rec.isDone() {
			res, _ = rec.snapshot()
			done = true
			cancelTurn()
		}
	}
	if done {
		return buf.String(), res, true, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return buf.String(), Result{}, false, nil, err
	}
	return buf.String(), Result{}, false, nil, nil
}

// foldResult maps the rich internal Result onto the frozen ExecutorResult the
// worker applies. Unhandled protocol misses collapse to protocol failures.
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
	case terminalVerdict:
		return ExecutorResult{
			Submitted: true,
			Evidence:  res.Evidence,
			Verdicts:  res.Verdicts,
		}
	case terminalFail:
		f := res.Failure
		if f == nil {
			f = &Failure{Reason: "agent reported failure"}
		}
		failureClass, blockedBy := failureResponsibility(f.FailureClass, f.BlockedBy)
		return ExecutorResult{Failed: true, FailReason: f.Reason, FailureClass: failureClass, BlockedBy: blockedBy}
	default: // terminalNone — silent or failed-repair protocol miss
		reason := "agent ended without a goal_control terminal action"
		if res.RepairAttempted {
			reason = "agent failed to call goal_control after one repair turn"
		}
		return ExecutorResult{Failed: true, FailReason: reason, FailureClass: FailureClassModel}
	}
}

func failureResponsibility(failureClass, blockedBy string) (string, string) {
	if ValidFailureClass(failureClass) && failureClass != "" {
		if failureClass == FailureClassEnvironment {
			return failureClass, BlockEnvUnavailable
		}
		if failureClass == FailureClassContract {
			return failureClass, BlockContractConflict
		}
		return failureClass, ""
	}
	switch blockedBy {
	case BlockEnvUnavailable:
		return FailureClassEnvironment, BlockEnvUnavailable
	case BlockContractConflict:
		return FailureClassContract, BlockContractConflict
	default:
		return FailureClassModel, ""
	}
}

// failResult is a constructor for a non-agent failure outcome.
func failResult(reason, failureClass, blockedBy string) Result {
	return Result{Action: terminalFail, Failure: &Failure{Reason: reason, FailureClass: failureClass, BlockedBy: blockedBy}}
}

// ── prompt assembly ─────────────────────────────────────────────────────────

// buildAttemptPrompt assembles the worker turn from the frozen AttemptInput. The
// terminal goal_control rule is repeated in the user message because
// workers run as normal agents with extra tools; relying on the tool description
// alone makes protocol errors too easy.
func buildAttemptPrompt(req ExecutorRequest, decompose bool) string {
	in := req.Input
	var b strings.Builder

	if decompose {
		b.WriteString(`You are decomposing a Stella composite goal into child goals.

Protocol:
- Plan the smallest set of child goals that, once all accepted, complete this goal.
- You may use tools normally while planning.
- Before ending this turn, you MUST call goal_control exactly once with one terminal action:
  - action="decompose" with a "decomposition" object {children, edges} when the plan is ready.
  - action="fail" with reason and optional blocked_by="contract_conflict" if the goal cannot be decomposed.
- Each child needs key, title, intent, kind (leaf|composite), required, acceptance_contract, convergence_policy.
- Edges (by child key) declare hard/soft dependencies; only accepted upstream output flows downstream.

Goal:
`)
	} else {
		b.WriteString(`You are executing a durable Stella goal.

Protocol:
- You may use tools normally while working.
- Before ending this turn, you MUST call goal_control exactly once with one terminal action:
  - action="submit" with evidence (summary + optional artifacts) and output when the work is complete.
  - action="fail" with reason and optional blocked_by="env_unavailable" or "contract_conflict" when the work cannot be completed.
- Do not just answer in chat. A final text response without goal_control is treated as a protocol failure.

Goal:
`)
	}

	if title := strings.TrimSpace(in.Title); title != "" {
		b.WriteString("Title: " + title + "\n")
	}
	b.WriteString(strings.TrimSpace(in.Intent))

	if len(in.Context) > 0 && string(in.Context) != "{}" {
		b.WriteString("\n\nGoal context:\n")
		b.WriteString(string(in.Context))
		b.WriteString("\n")
	}

	renderTimelineContext(&b, in)

	if len(in.PriorErrors) > 0 {
		b.WriteString("\nYour previous decomposition was structurally invalid. Fix these errors and call goal_control again.\n")
		b.WriteString("prior_errors JSON:\n")
		b.WriteString(RenderErrorsJSON(in.PriorErrors))
		b.WriteString("\n\nprior_errors text:\n")
		b.WriteString(RenderErrorsText(in.PriorErrors))
		b.WriteString("\n")
	}

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

func renderTimelineContext(b *strings.Builder, in AttemptInput) {
	var human []TimelineContextEvent
	var facts []TimelineContextEvent
	for _, ev := range in.TimelineContext {
		switch ev.EventType {
		case GoalEventHumanMessage:
			if strings.TrimSpace(ev.Text) != "" {
				human = append(human, ev)
			}
		case GoalEventAttemptFinished:
			if strings.TrimSpace(ev.Reason) != "" || strings.TrimSpace(ev.Status) != "" {
				facts = append(facts, ev)
			}
		case GoalEventAcceptanceRecorded:
			if in.PriorGaps == nil && ev.Result == ResultFail && strings.TrimSpace(ev.Reason) != "" {
				facts = append(facts, ev)
			}
		}
	}

	if len(human) > 0 {
		b.WriteString("\n\nHuman guidance for this attempt — follow it; where it conflicts with the original framing above, the human guidance wins:\n")
		for _, ev := range human {
			line := "- "
			if ev.CreatedAt != "" {
				line += ev.CreatedAt + ": "
			}
			line += strings.TrimSpace(ev.Text)
			b.WriteString(line + "\n")
		}
	}

	if len(facts) > 0 {
		b.WriteString("\nRecent execution facts to account for:\n")
		for _, ev := range facts {
			switch ev.EventType {
			case GoalEventAttemptFinished:
				line := "- attempt"
				if ev.AttemptID != "" {
					line += " " + ev.AttemptID
				}
				if ev.Status != "" {
					line += " ended " + ev.Status
				}
				if ev.FailureClass != "" {
					line += " (" + ev.FailureClass + ")"
				}
				if ev.Reason != "" {
					line += ": " + ev.Reason
				}
				b.WriteString(line + "\n")
			case GoalEventAcceptanceRecorded:
				line := "- acceptance item " + ev.ItemID + " failed"
				if ev.Reason != "" {
					line += ": " + ev.Reason
				}
				b.WriteString(line + "\n")
			}
		}
	}
}

// buildRepairPrompt is the single bounded correction turn for a worker that
// answered in plain text without calling goal_control. It echoes the
// prior answer as context and demands exactly one terminal action — it never
// submits the text automatically.
func buildRepairPrompt(priorText string, decompose bool) string {
	action := `  - action="submit" with evidence + output if the work is complete.
  - action="fail" with reason and optional blocked_by="env_unavailable" or "contract_conflict" if the work cannot be completed.`
	if decompose {
		action = `  - action="decompose" with a "decomposition" object {children, edges} if the plan is ready.
  - action="fail" with reason and optional blocked_by="contract_conflict" if the goal cannot be decomposed.`
	}
	return `Your previous response did not call goal_control, so this goal is not yet resolved.

Your previous message was:
"""
` + priorText + `
"""

You MUST now call goal_control exactly once with one terminal action:
` + action + `

Do not answer in plain text again.`
}

// buildReviewPrompt assembles the reviewer turn for a purpose=review attempt: the
// goal intent, the submitted output under review, and each required
// agent-authority item's rubric. The reviewer judges the output against each
// item and reports a verdict via goal_control. It never edits the work — a
// failing verdict feeds the next execution attempt as a gap.
func buildReviewPrompt(req ExecutorRequest) string {
	in := req.Input
	var b strings.Builder
	b.WriteString(`You are reviewing a completed Stella goal's output against its acceptance criteria.

Protocol:
- Judge ONLY whether the output below meets each criterion. Do not redo or edit the work.
- You may use tools to verify claims.
- Before ending this turn, you MUST call goal_control exactly once:
  - action="verdict" with a "verdicts" array — one {item_id, pass, rationale} per criterion below.
  - action="fail" with reason and optional blocked_by="contract_conflict" only if the output cannot be judged at all.

Goal:
`)
	if title := strings.TrimSpace(in.Title); title != "" {
		b.WriteString("Title: " + title + "\n")
	}
	b.WriteString(strings.TrimSpace(in.Intent))

	if len(in.Context) > 0 && string(in.Context) != "{}" {
		b.WriteString("\n\nGoal context:\n")
		b.WriteString(string(in.Context))
		b.WriteString("\n")
	}

	if out := in.ReviewOutput; out != nil {
		b.WriteString("\n\nOutput under review:\n")
		if s := strings.TrimSpace(out.Summary); s != "" {
			b.WriteString(s + "\n")
		}
		if len(out.Result) > 0 {
			if rb, err := json.Marshal(out.Result); err == nil {
				b.WriteString("Structured result: " + string(rb) + "\n")
			}
		}
	}

	b.WriteString("\nCriteria to judge (return one verdict per item_id):\n")
	for _, it := range in.ReviewItems {
		line := "- item_id=" + it.ID
		if r := strings.TrimSpace(it.Rubric); r != "" {
			line += ": " + r
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// buildReviewRepairPrompt is the single bounded correction turn for a reviewer
// that answered in plain text without calling goal_control.
func buildReviewRepairPrompt(priorText string) string {
	return `Your previous response did not call goal_control, so this review is not recorded.

Your previous message was:
"""
` + priorText + `
"""

You MUST now call goal_control exactly once:
  - action="verdict" with a "verdicts" array of {item_id, pass, rationale} covering every criterion.
  - action="fail" with reason and optional blocked_by="contract_conflict" only if the output cannot be judged.

Do not answer in plain text again.`
}

// ── recording control tool ──────────────────────────────────────────────────

// recordingControlTool is the agent-facing goal_control tool. It is PURE
// with respect to durable state: submit/fail/decompose record one terminal
// action into the recorder for the worker to apply. Unlike the old task_control
// it has no progress side-effect — durable writes belong to the service.
type recordingControlTool struct {
	rec       *terminalRecorder
	decompose bool
	log       *slog.Logger
	// parentDepth/maxDepth let a decomposition action validate its proposed plan
	// in-turn (the same ValidateDecomposition the write boundary runs), so a
	// structurally-doomed plan is rejected back to the model for self-correction
	// in the same turn instead of failing out-of-turn and burning budget.
	parentDepth int
	maxDepth    int
	// review switches the accepted action set to verdict/fail; reviewItems are the
	// required agent-authority judgment items the verdict must cover, validated
	// in-turn (the same coverage the SubmitReview boundary expects).
	review      bool
	reviewItems []AcceptanceItem
}

// newRecordingControlTool wires a recording tool for one attempt. decompose
// switches the accepted action set to decomposition; parentDepth/maxDepth feed
// in-turn decomposition validation (ignored for non-decomposition attempts).
func newRecordingControlTool(rec *terminalRecorder, decompose bool, parentDepth, maxDepth int, log *slog.Logger) *recordingControlTool {
	return &recordingControlTool{rec: rec, decompose: decompose, log: log, parentDepth: parentDepth, maxDepth: maxDepth}
}

// newReviewControlTool wires a recording tool for a purpose=review attempt: the
// accepted action set is verdict/fail and the proposed verdicts are validated
// in-turn against the required agent-authority items.
func newReviewControlTool(rec *terminalRecorder, reviewItems []AcceptanceItem, log *slog.Logger) *recordingControlTool {
	return &recordingControlTool{rec: rec, review: true, reviewItems: reviewItems, log: log}
}

func (t *recordingControlTool) Definition() tools.Definition {
	if t.review {
		return t.reviewDefinition()
	}
	if t.decompose {
		return ai.ToolDefinition{
			Name:        "goal_control",
			Description: "Report decomposition outcome. Call exactly one of decompose/fail before exiting.",
			InputSchema: goalControlDecomposeInputSchema(),
		}
	}
	return ai.ToolDefinition{
		Name:        "goal_control",
		Description: "Report goal lifecycle. Call exactly one of submit/fail before exiting.",
		InputSchema: goalControlExecuteInputSchema(),
	}
}

// reviewDefinition is the goal_control schema for a purpose=review attempt: a
// verdict array covering each required agent-authority item, or a fail.
func (t *recordingControlTool) reviewDefinition() tools.Definition {
	return ai.ToolDefinition{
		Name:        "goal_control",
		Description: "Report review outcome. Call exactly one of verdict/fail before exiting.",
		InputSchema: goalControlReviewInputSchema(),
	}
}

func (t *recordingControlTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	if t.review {
		return t.executeReview(action, args)
	}
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
	case "fail":
		if err := t.rec.record(Result{Action: terminalFail, Failure: &Failure{
			Reason:    stringArg(args, "reason"),
			BlockedBy: stringArg(args, "blocked_by"),
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"fail"}`, nil
	default:
		return "", fmt.Errorf("goal_control: unknown action %q", action)
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
		// Validate in-turn against the same structural guards the write boundary
		// runs (ValidateDecomposition), so a doomed plan is returned to the model
		// for self-correction now rather than recorded ok and rejected out-of-turn.
		maxDepth := t.maxDepth
		if maxDepth <= 0 { // unset on an attempt minted before MaxDepth was frozen
			maxDepth = defaultMaxDepth
		}
		if errs := validateDecompositionDetailed(content, t.parentDepth, maxDepth); len(errs) > 0 {
			return "", fmt.Errorf("goal_control: decomposition rejected:\n%s\nprior_errors JSON:\n%s", RenderErrorsText(errs), RenderErrorsJSON(errs))
		}
		ev := AttemptEvidence{Summary: stringArg(args, "summary")}
		if err := t.rec.record(Result{Action: terminalDecompose, Evidence: ev, Decomposition: &content}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"decompose"}`, nil
	case "fail":
		if err := t.rec.record(Result{Action: terminalFail, Failure: &Failure{
			Reason:    stringArg(args, "reason"),
			BlockedBy: stringArg(args, "blocked_by"),
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"fail"}`, nil
	default:
		return "", fmt.Errorf("goal_control: unknown action %q", action)
	}
}

// executeReview handles the purpose=review action set: a verdict covering every
// required agent-authority item, or a fail when the output cannot be judged.
func (t *recordingControlTool) executeReview(action string, args map[string]any) (string, error) {
	switch action {
	case "verdict":
		verdicts, err := decodeVerdicts(args["verdicts"])
		if err != nil {
			return "", err
		}
		// Validate in-turn: every required agent item must get exactly one verdict
		// and no verdict may name an unknown item, so an incomplete review is
		// returned to the model for self-correction now rather than appending a
		// partial ledger that strands the goal pending out-of-turn.
		if err := ValidateReviewVerdicts(verdicts, t.reviewItems); err != nil {
			return "", fmt.Errorf("goal_control: review rejected: %w; provide exactly one verdict "+
				"{item_id,pass,rationale} for each required item and call goal_control again", err)
		}
		ev := AttemptEvidence{Summary: stringArg(args, "summary")}
		if err := t.rec.record(Result{Action: terminalVerdict, Evidence: ev, Verdicts: verdicts}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"verdict"}`, nil
	case "fail":
		if err := t.rec.record(Result{Action: terminalFail, Failure: &Failure{
			Reason:    stringArg(args, "reason"),
			BlockedBy: stringArg(args, "blocked_by"),
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"fail"}`, nil
	default:
		return "", fmt.Errorf("goal_control: unknown action %q", action)
	}
}

// ── arg decoders ────────────────────────────────────────────────────────────

func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
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
		return DecompositionContent{}, fmt.Errorf("goal_control: decompose requires a decomposition object")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return DecompositionContent{}, fmt.Errorf("goal_control: decomposition not serialisable: %w", err)
	}
	// Strict decode: an unknown or misnamed field (e.g. edges using from/to/type
	// instead of downstream_key/upstream_key/kind) is rejected in-turn with an
	// actionable message rather than silently dropped — a dropped field used to
	// produce empty edge keys that failed validation out-of-turn and burned budget.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c DecompositionContent
	if err := dec.Decode(&c); err != nil {
		return DecompositionContent{}, fmt.Errorf("goal_control: decomposition has invalid or unknown fields "+
			"(use children[].{key,title,intent,kind,required,acceptance_contract,convergence_policy} and "+
			"edges[].{downstream_key,upstream_key,kind,on_failure}): %w", err)
	}
	return c, nil
}

// decodeVerdicts round-trips the loosely-typed verdicts arg into []ReviewVerdict.
// A strict decode rejects unknown/misnamed fields in-turn (use item_id/pass/
// rationale) rather than silently dropping them into an incomplete review.
func decodeVerdicts(v any) ([]ReviewVerdict, error) {
	if v == nil {
		return nil, fmt.Errorf("goal_control: verdict requires a verdicts array")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("goal_control: verdicts not serialisable: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var out []ReviewVerdict
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("goal_control: verdicts have invalid or unknown fields "+
			"(use verdicts[].{item_id,pass,rationale}): %w", err)
	}
	return out, nil
}

// ValidateReviewVerdicts checks a review covers exactly the required
// agent-authority items: every required item has one verdict, no verdict names an
// unknown item, and no item is judged twice. It is the in-turn mirror of the
// coverage SubmitReview relies on so an incomplete review never reaches the
// ledger (contract §10.13).
func ValidateReviewVerdicts(verdicts []ReviewVerdict, items []AcceptanceItem) error {
	want := make(map[string]bool, len(items))
	for _, it := range items {
		want[it.ID] = true
	}
	seen := make(map[string]bool, len(verdicts))
	for _, v := range verdicts {
		if v.ItemID == "" {
			return fmt.Errorf("a verdict is missing item_id")
		}
		if !want[v.ItemID] {
			return fmt.Errorf("verdict for unknown item %q", v.ItemID)
		}
		if seen[v.ItemID] {
			return fmt.Errorf("duplicate verdict for item %q", v.ItemID)
		}
		seen[v.ItemID] = true
	}
	for id := range want {
		if !seen[id] {
			return fmt.Errorf("missing verdict for required item %q", id)
		}
	}
	return nil
}

package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

// TerminalAction is the durable outcome an executor reports for one run. The
// worker applies exactly one transition based on it; the agent never mutates
// final task state directly (D3).
type TerminalAction string

const (
	TerminalNone   TerminalAction = ""
	TerminalSubmit TerminalAction = "submit"
	TerminalBlock  TerminalAction = "block"
	TerminalFail   TerminalAction = "fail"
)

// BlockerResult carries a block action's payload.
type BlockerResult struct {
	Kind     string
	Question string
	Detail   any
}

// FailureResult carries a fail action's payload.
type FailureResult struct {
	Reason    string
	Retryable bool
}

// Request is the executor input for one claimed run. Task is required for
// worker runs; it carries the prompt body and review policy.
type Request struct {
	Run  sqlc.AgentTaskRun
	Task *sqlc.AgentTask
}

// Result is the executor output: exactly one terminal action (or TerminalNone
// when the agent ended without declaring one). The worker reads this and
// applies the matching transition through TransitionService.
type Result struct {
	Action  TerminalAction
	Output  any
	Blocker *BlockerResult
	Failure *FailureResult
	// RepairAttempted is set when a text-only first turn triggered one bounded
	// repair turn that still produced no terminal action. It only carries
	// meaning for TerminalNone and lets the worker distinguish a silent miss
	// from a failed repair in the protocol_error detail.
	RepairAttempted bool
}

// Executor owns agent interaction and the terminal protocol for one run. It
// must not mutate durable terminal task/run state — the worker applies the
// single transition implied by Result. Progress updates may persist during
// execution (D3).
type Executor interface {
	Execute(ctx context.Context, req Request) (Result, error)
}

// executorFunc adapts a function to the Executor interface.
type executorFunc func(ctx context.Context, req Request) (Result, error)

func (f executorFunc) Execute(ctx context.Context, req Request) (Result, error) { return f(ctx, req) }

// TaskChatParams describes one persisted worker turn on a task session.
type TaskChatParams struct {
	AgentID    string
	UserID     string
	SessionID  string
	ProjectID  string
	Prompt     string
	ExtraTools []tools.Tool
}

// TaskChatFunc runs one worker turn through the agent service layer so the
// transcript persists to the task session and prior turns load as history.
// Implementations resolve AgentID to a service; an unknown agent surfaces as
// an Err event on the returned channel.
type TaskChatFunc func(ctx context.Context, p TaskChatParams) <-chan agent.Event

// terminalRecorder captures the first terminal action declared during an
// execution attempt. Later terminal declarations are rejected so a stray
// second tool call can't change the outcome.
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
		return fmt.Errorf("task_control: terminal action already recorded")
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

// workerExecutor is the agent-backed Executor for worker runs. It wires a
// recording task_control tool, runs each turn through TaskChatFunc (which
// persists the transcript to the task session), and pumps the chat loop until
// a terminal action fires or the channel closes.
type workerExecutor struct {
	chat    TaskChatFunc
	q       *sqlc.Queries
	svc     *TransitionService
	packets *GoalContextPacketBuilder
	log     *slog.Logger
}

// newWorkerExecutor builds the default agent-backed worker executor. q and svc
// are used only to persist progress patches during execution (D3).
func newWorkerExecutor(chat TaskChatFunc, q *sqlc.Queries, svc *TransitionService, log *slog.Logger) *workerExecutor {
	if log == nil {
		log = slog.Default().With("component", "tasks/worker-executor")
	}
	return &workerExecutor{chat: chat, q: q, svc: svc, packets: NewGoalContextPacketBuilder(q), log: log}
}

// Execute runs one worker turn. All outcomes are encoded in Result so the
// worker can apply a single transition uniformly:
//   - agent declared a terminal action  -> that action
//   - misconfigured run (no agent/pool)  -> TerminalFail, non-retryable
//   - runner setup / stream error        -> TerminalFail, retryable
//   - clean exit without terminal action -> TerminalNone (worker handles it)
func (e *workerExecutor) Execute(ctx context.Context, req Request) (Result, error) {
	agentID := ""
	if req.Run.ExecutorAgentID.Valid {
		agentID = req.Run.ExecutorAgentID.String
	}
	if agentID == "" {
		return failResult("no executor agent on run", false), nil
	}
	if req.Task == nil {
		return failResult("no task on worker request", false), nil
	}

	rec := &terminalRecorder{}
	ctTool := newRecordingControlTool(rec, req.Task.ID, e.log).withProgress(e.q, e.svc)
	turn := func(prompt string) <-chan agent.Event {
		projectID := ""
		if req.Task.ProjectID.Valid {
			projectID = req.Task.ProjectID.String
		}
		return e.chat(ctx, TaskChatParams{
			AgentID:    agentID,
			UserID:     req.Run.UserID,
			SessionID:  req.Run.SessionID,
			ProjectID:  projectID,
			Prompt:     prompt,
			ExtraTools: []tools.Tool{ctTool},
		})
	}

	// First turn. The goal-context packet is advisory: a build error degrades to
	// no packet (the standalone prompt) rather than failing the run.
	var packet *GoalContextPacket
	if e.packets != nil {
		var perr error
		if packet, perr = e.packets.Build(ctx, req.Task.ID); perr != nil {
			e.log.Warn("worker executor: goal context build failed", "task_id", req.Task.ID, "err", perr)
			packet = nil
		}
	}
	text, res, done, fail := e.runTurn(ctx, turn(buildTaskPrompt(*req.Task, e.latestResolution(ctx, req.Task.ID), packet)), rec)
	if fail != nil {
		return *fail, nil
	}
	if done {
		return res, nil
	}

	// The turn ended without a terminal action. A silent exit (no assistant
	// text) is an unrecoverable protocol miss; a text-only answer gets exactly
	// one bounded repair turn that re-states the protocol with the prior text
	// as context. There is no auto-submit of free text (D5).
	if strings.TrimSpace(text) == "" {
		return Result{Action: TerminalNone}, nil
	}

	_, res, done, fail = e.runTurn(ctx, turn(buildRepairPrompt(text)), rec)
	if fail != nil {
		return *fail, nil
	}
	if done {
		return res, nil
	}
	return Result{Action: TerminalNone, RepairAttempted: true}, nil
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
			e.log.Warn("worker executor stream error", "err", ev.Err)
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

// failResult is a small constructor for a non-agent failure outcome.
func failResult(reason string, retryable bool) Result {
	return Result{Action: TerminalFail, Failure: &FailureResult{Reason: reason, Retryable: retryable}}
}

// drainEvents consumes remaining events so the runner can close cleanly after a
// terminal action has been recorded.
func drainEvents(ch <-chan agent.Event) {
	for range ch {
	}
}

// latestResolution returns the question+answer of the task's most recently
// resolved blocker, or "" when there is none or the answer is empty. Without
// this the resume turn rebuilds the prompt from title+description only and
// the user's answer never reaches the agent, so it re-asks the same question.
// dep_failure blockers are skipped: their resolution is a waiver record, not
// an answer.
func (e *workerExecutor) latestResolution(ctx context.Context, taskID string) string {
	b, err := e.q.GetLatestResolvedBlockerForTask(ctx, taskID)
	if err != nil || b.Kind == BlockerKindDepFailure {
		return ""
	}
	answer := strings.TrimSpace(b.Resolution)
	if answer == "" || answer == "{}" {
		return ""
	}
	if q := strings.TrimSpace(b.Question); q != "" {
		return "You previously blocked this task asking:\n" + q + "\n\nThe user answered:\n" + answer
	}
	return "The user resolved your earlier blocker with:\n" + answer
}

// recordingControlTool is the agent-facing task_control tool. progress
// persists immediately into agent_task.context; submit/block/fail record one
// terminal action into the recorder for the worker to apply.
type recordingControlTool struct {
	rec    *terminalRecorder
	taskID string
	q      *sqlc.Queries
	svc    *TransitionService
	log    *slog.Logger
}

// newRecordingControlTool wires a recording tool for one execution attempt.
// progress persistence is wired separately via withProgress.
func newRecordingControlTool(rec *terminalRecorder, taskID string, log *slog.Logger) *recordingControlTool {
	return &recordingControlTool{rec: rec, taskID: taskID, log: log}
}

// withProgress attaches the queries/service used to persist progress patches.
// Without it, progress calls are accepted but not persisted.
func (t *recordingControlTool) withProgress(q *sqlc.Queries, svc *TransitionService) *recordingControlTool {
	t.q = q
	t.svc = svc
	return t
}

func (t *recordingControlTool) Definition() tools.Definition {
	return ai.ToolDefinition{
		Name: "task_control",
		Description: "Report task lifecycle. Call exactly one of submit/block/fail before exiting. " +
			"Use progress freely while working.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"progress", "submit", "block", "fail"},
					"description": "Which lifecycle action to take.",
				},
				"patch": map[string]any{
					"type":        "object",
					"description": "progress: shallow-merge JSON into task.context.",
				},
				"output": map[string]any{
					"type":        "object",
					"description": "submit: final output written to task.output.",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "block: blocker kind (user_input|external_dependency|tool_error|policy_hold).",
				},
				"question": map[string]any{
					"type":        "string",
					"description": "block: human-readable explanation of what's needed.",
				},
				"detail": map[string]any{
					"type":        "object",
					"description": "block/fail: structured detail.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "fail: error message.",
				},
				"retryable": map[string]any{
					"type":        "boolean",
					"description": "fail: true if dispatcher should retry within budget.",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *recordingControlTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "progress":
		if t.rec.isDone() {
			return "", fmt.Errorf("task_control: progress after terminal action")
		}
		if err := t.persistProgress(ctx, args["patch"]); err != nil {
			return "", err
		}
		return `{"ok":true}`, nil
	case "submit":
		output := args["output"]
		if output == nil {
			output = map[string]any{}
		}
		if err := t.rec.record(Result{Action: TerminalSubmit, Output: output}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"submit"}`, nil
	case "block":
		kind, _ := args["kind"].(string)
		if kind == "" {
			kind = BlockerKindUserInput
		}
		if !validBlockerKind(kind) {
			return "", fmt.Errorf("task_control: invalid blocker kind %q", kind)
		}
		question, _ := args["question"].(string)
		if err := t.rec.record(Result{Action: TerminalBlock, Blocker: &BlockerResult{
			Kind: kind, Question: question, Detail: args["detail"],
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"block"}`, nil
	case "fail":
		reason, _ := args["reason"].(string)
		retryable, _ := args["retryable"].(bool)
		if err := t.rec.record(Result{Action: TerminalFail, Failure: &FailureResult{
			Reason: reason, Retryable: retryable,
		}}); err != nil {
			return "", err
		}
		return `{"ok":true,"recorded":"fail"}`, nil
	default:
		return "", fmt.Errorf("task_control: unknown action %q", action)
	}
}

// persistProgress shallow-merges patch into agent_task.context. No-op when the
// tool was built without progress wiring.
func (t *recordingControlTool) persistProgress(ctx context.Context, patch any) error {
	if t.q == nil || t.svc == nil {
		return nil
	}
	patchMap, err := normalizePatch(patch)
	if err != nil {
		return err
	}
	task, err := t.q.GetAgentTask(ctx, t.taskID)
	if err != nil {
		return err
	}
	now := t.svc.now()
	return t.q.UpdateAgentTaskMeta(ctx, sqlc.UpdateAgentTaskMetaParams{
		Title:       task.Title,
		Description: task.Description,
		Priority:    task.Priority,
		NotBefore:   task.NotBefore,
		DeadlineAt:  task.DeadlineAt,
		Context:     mergeContext(task.Context, patchMap),
		UpdatedAt:   now,
		ID:          t.taskID,
	})
}

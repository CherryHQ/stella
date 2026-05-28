package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TaskControlTool is the surface a worker (agent runner) sees to communicate
// task lifecycle decisions. Each method routes through TransitionService so
// status writes obey the single-source-of-truth invariant (D14).
//
// All four methods are idempotent within reason: a second Submit returns
// ErrInvalidTransition cleanly, callers can branch on it.
type TaskControlTool struct {
	svc      *TransitionService
	q        *sqlc.Queries
	log      *slog.Logger
	taskID   string
	runID    string
	actor    Actor
	finished bool // worker may inspect this to know if it called a terminal action
}

// NewTaskControlTool wires a control tool for one claimed run.
func NewTaskControlTool(svc *TransitionService, q *sqlc.Queries, taskID, runID string, actor Actor) *TaskControlTool {
	return &TaskControlTool{
		svc: svc, q: q,
		log:    slog.Default().With("component", "tasks/control_tool"),
		taskID: taskID, runID: runID, actor: actor,
	}
}

// logLostToSweep records a structured warning when a control-tool call hits
// ErrInvalidTransition — almost always because the dispatcher's stale-run
// sweep interrupted the run after a heartbeat delay (M3). Surfacing this
// distinctly from generic transition errors makes lease-budget mistuning
// diagnosable.
func (t *TaskControlTool) logLostToSweep(action string) {
	t.log.Warn("worker lost to dispatcher sweep",
		"action", action, "task_id", t.taskID, "run_id", t.runID,
		"hint", "lease/heartbeat margin may be too tight; raise LeaseDuration or lower HeartbeatInterval")
}

// Finished reports whether the tool has seen a terminal action (submit, block,
// fail). The worker uses this to decide whether to apply the protocol-error
// fallback when the agent loop exits.
func (t *TaskControlTool) Finished() bool { return t.finished }

// MaxProgressPatchBytes caps a single Progress patch (M7). Patches above
// this size are rejected — the worker is feeding the model's output back as
// state, and unbounded blobs amplify into self-DoS as the merged context
// grows on every tick.
const MaxProgressPatchBytes = 64 * 1024

// MaxTaskContextBytes caps the merged task.context document. Above this we
// refuse the merge rather than commit an unbounded row (M7).
const MaxTaskContextBytes = 256 * 1024

// reservedProgressKeyPrefixes are namespaces the worker cannot write to via
// Progress. Reserved for future system fields and to avoid collisions with
// downstream JS consumers (M7).
var reservedProgressKeyPrefixes = []string{"_system", "__"}

// Progress merges patch into agent_task.context as a shallow JSON merge
// (HP6 / D14). Existing keys not in patch are preserved.
// patch may be raw JSON bytes or a Go map; both produce the same merge.
//
// Hardened (M7): patch size capped, reserved-prefix keys rejected, total
// merged size capped. Worker-driven Progress is model-generated and cannot
// be trusted to stay bounded on its own.
func (t *TaskControlTool) Progress(ctx context.Context, patch any) error {
	if t.finished {
		return fmt.Errorf("task_control: progress after terminal action")
	}
	patchMap, err := normalizePatch(patch)
	if err != nil {
		return err
	}
	if b, err := json.Marshal(patchMap); err == nil && len(b) > MaxProgressPatchBytes {
		return fmt.Errorf("task_control: progress patch exceeds %d bytes (got %d)", MaxProgressPatchBytes, len(b))
	}
	for k := range patchMap {
		for _, p := range reservedProgressKeyPrefixes {
			if len(k) >= len(p) && k[:len(p)] == p {
				return fmt.Errorf("task_control: progress key %q uses reserved prefix %q", k, p)
			}
		}
	}
	task, err := t.q.GetAgentTask(ctx, t.taskID)
	if err != nil {
		return err
	}
	merged := mergeContext(task.Context, patchMap)
	if len(merged) > MaxTaskContextBytes {
		return fmt.Errorf("task_control: merged context would exceed %d bytes (got %d)", MaxTaskContextBytes, len(merged))
	}
	now := t.svc.now()
	return t.q.UpdateAgentTaskMeta(ctx, sqlc.UpdateAgentTaskMetaParams{
		Title:       task.Title,
		Description: task.Description,
		Priority:    task.Priority,
		NotBefore:   task.NotBefore,
		DeadlineAt:  task.DeadlineAt,
		Context:     merged,
		UpdatedAt:   now,
		ID:          t.taskID,
	})
}

// Block creates a blocker and moves the task to StatusBlocked.
func (t *TaskControlTool) Block(ctx context.Context, kind, question string, detail any) error {
	if t.finished {
		return fmt.Errorf("task_control: block after terminal action")
	}
	if kind == "" {
		kind = BlockerKindUserInput
	}
	detailStr := detailJSON(detail)
	if err := t.svc.Block(ctx, BlockParams{
		TaskID: t.taskID, Kind: kind, Question: question, Detail: detailStr,
		RunID: t.runID, Actor: t.actor,
	}); err != nil {
		return err
	}
	t.finished = true
	return nil
}

// Submit completes the task. In Slice 1 this short-circuits straight to
// StatusDone (no review pipeline). Output is stored on agent_task.output.
func (t *TaskControlTool) Submit(ctx context.Context, output any) error {
	if t.finished {
		return fmt.Errorf("task_control: submit after terminal action")
	}
	outputStr := detailJSON(output)
	if err := t.svc.Submit(ctx, t.taskID, t.runID, outputStr, t.actor); err != nil {
		if errors.Is(err, ErrInvalidTransition) {
			t.logLostToSweep("submit")
		}
		return err
	}
	t.finished = true
	return nil
}

// Fail marks the run as failed. Retryable=true returns the task to ready (if
// retry budget remains); false forces StatusFailed.
func (t *TaskControlTool) Fail(ctx context.Context, reason string, retryable bool) error {
	if t.finished {
		return fmt.Errorf("task_control: fail after terminal action")
	}
	if err := t.svc.Fail(ctx, FailParams{
		TaskID: t.taskID, RunID: t.runID, Reason: reason, Retryable: retryable, Actor: t.actor,
	}); err != nil {
		return err
	}
	t.finished = true
	return nil
}

// normalizePatch accepts a Go map, a struct, or raw JSON bytes and returns a
// map[string]any.
func normalizePatch(patch any) (map[string]any, error) {
	if patch == nil {
		return map[string]any{}, nil
	}
	switch p := patch.(type) {
	case map[string]any:
		return p, nil
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(p, &m); err != nil {
			return nil, fmt.Errorf("task_control: patch is not valid JSON: %w", err)
		}
		return m, nil
	case string:
		if p == "" {
			return map[string]any{}, nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(p), &m); err != nil {
			return nil, fmt.Errorf("task_control: patch is not valid JSON: %w", err)
		}
		return m, nil
	default:
		// Round-trip through JSON for arbitrary structs.
		b, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("task_control: patch not serialisable: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("task_control: patch not a JSON object: %w", err)
		}
		return m, nil
	}
}

// mergeContext performs a shallow merge of patch into the current context
// JSON. Per D14 / HP6: existing top-level keys not present in patch are
// preserved. If existing is unparseable, patch wins entirely.
func mergeContext(existing string, patch map[string]any) string {
	var doc map[string]any
	if existing != "" && existing != "{}" {
		_ = json.Unmarshal([]byte(existing), &doc)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	maps.Copy(doc, patch)
	b, err := json.Marshal(doc)
	if err != nil {
		return existing
	}
	return string(b)
}

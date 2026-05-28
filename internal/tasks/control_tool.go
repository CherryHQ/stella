package tasks

import (
	"context"
	"encoding/json"
	"fmt"
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
	svc         *TransitionService
	q           *sqlc.Queries
	taskID      string
	runID       string
	actor       Actor
	finished    bool   // worker may inspect this to know if it called a terminal action
	finalStatus string // task status after the terminal action (e.g. "done" or "reviewing")
}

// NewTaskControlTool wires a control tool for one claimed run.
func NewTaskControlTool(svc *TransitionService, q *sqlc.Queries, taskID, runID string, actor Actor) *TaskControlTool {
	return &TaskControlTool{svc: svc, q: q, taskID: taskID, runID: runID, actor: actor}
}

// Finished reports whether the tool has seen a terminal action (submit, block,
// fail). The worker uses this to decide whether to apply the protocol-error
// fallback when the agent loop exits.
func (t *TaskControlTool) Finished() bool { return t.finished }

// FinalStatus returns the task status observed right after the terminal
// action. Empty before any terminal call. Worker-facing callers use this to
// report the real outcome (e.g. "reviewing" vs "done") instead of assuming.
func (t *TaskControlTool) FinalStatus() string { return t.finalStatus }

// Progress merges patch into agent_task.context as a shallow JSON merge
// (HP6 / D14). Existing keys not in patch are preserved.
// patch may be raw JSON bytes or a Go map; both produce the same merge.
func (t *TaskControlTool) Progress(ctx context.Context, patch any) error {
	if t.finished {
		return fmt.Errorf("task_control: progress after terminal action")
	}
	patchMap, err := normalizePatch(patch)
	if err != nil {
		return err
	}
	task, err := t.q.GetAgentTask(ctx, t.taskID)
	if err != nil {
		return err
	}
	merged := mergeContext(task.Context, patchMap)
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

// Submit completes the worker run. Per review_policy on the task, the final
// status may be "done" (none / auto) or "reviewing" (agent / human). Output is
// stored on agent_task.output. FinalStatus() exposes the resulting status.
func (t *TaskControlTool) Submit(ctx context.Context, output any) error {
	if t.finished {
		return fmt.Errorf("task_control: submit after terminal action")
	}
	outputStr := detailJSON(output)
	if err := t.svc.Submit(ctx, t.taskID, t.runID, outputStr, t.actor); err != nil {
		return err
	}
	t.finished = true
	if task, err := t.q.GetAgentTask(ctx, t.taskID); err == nil {
		t.finalStatus = task.Status
	}
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

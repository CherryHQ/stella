package tasks

import (
	"context"
	"database/sql"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Scheduler encapsulates task eligibility checks for dispatch.
type Scheduler struct {
	q              *sqlc.Queries
	maxConcurrency int
}

// NewScheduler creates a Scheduler.
func NewScheduler(q *sqlc.Queries, maxConcurrency int) *Scheduler {
	return &Scheduler{q: q, maxConcurrency: maxConcurrency}
}

// EligibleForWorker returns true if a task can be claimed by a worker run.
// Conditions: status==ready, all deps done, no active worker_run, user concurrency below limit.
func (s *Scheduler) EligibleForWorker(ctx context.Context, task sqlc.AgentTask) bool {
	if task.Status != "ready" {
		return false
	}
	if !s.DepsAllDone(ctx, task) {
		return false
	}
	if s.hasActiveRun(ctx, task.ID, "worker_run") {
		return false
	}
	count, err := s.q.CountActiveRunsByUser(ctx, task.UserID)
	if err != nil || count >= int64(s.maxConcurrency) {
		return false
	}
	return true
}

// EligibleForReviewer returns true if a task is ready for reviewer dispatch.
// Conditions: status==reviewing, has a completed worker_run, no active reviewer_run.
func (s *Scheduler) EligibleForReviewer(ctx context.Context, task sqlc.AgentTask) bool {
	if task.Status != "reviewing" {
		return false
	}
	_, err := s.q.GetLatestCompletedRunByTaskAndKind(ctx, sqlc.GetLatestCompletedRunByTaskAndKindParams{
		TaskID: task.ID,
		Kind:   "worker_run",
	})
	if err != nil {
		return false
	}
	return !s.hasActiveRun(ctx, task.ID, "reviewer_run")
}

// EligibleForSynthesis returns true if a goal is ready for manager synthesis.
// Conditions: task_type==goal, all required children done, no active/completed synthesis run
// after the latest child completion.
func (s *Scheduler) EligibleForSynthesis(ctx context.Context, goal sqlc.AgentTask) bool {
	if goal.TaskType != "goal" {
		return false
	}
	if goal.Status != "running" && goal.Status != "ready" {
		return false
	}
	children, err := s.q.ListChildTasks(ctx, sqlc.ListChildTasksParams{
		ParentID: sql.NullString{String: goal.ID, Valid: true},
		UserID:   goal.UserID,
	})
	if err != nil || len(children) == 0 {
		return false
	}
	for _, child := range children {
		if child.Required && child.Status != "done" {
			return false
		}
	}
	return !s.hasActiveRun(ctx, goal.ID, "manager_run")
}

// EligibleForFailureAssessment returns true if a goal needs failure assessment.
// Conditions: task_type==goal, any required child failed, no active failure_assessment run.
func (s *Scheduler) EligibleForFailureAssessment(ctx context.Context, goal sqlc.AgentTask) bool {
	if goal.TaskType != "goal" {
		return false
	}
	children, err := s.q.ListChildTasks(ctx, sqlc.ListChildTasksParams{
		ParentID: sql.NullString{String: goal.ID, Valid: true},
		UserID:   goal.UserID,
	})
	if err != nil {
		return false
	}
	hasFailedRequired := false
	for _, child := range children {
		if child.Required && child.Status == "failed" {
			hasFailedRequired = true
			break
		}
	}
	if !hasFailedRequired {
		return false
	}
	return !s.hasActiveRun(ctx, goal.ID, "manager_run")
}

// RollupParentStatus determines what a goal's status should be based on its children.
// Returns empty string if no change is needed.
func (s *Scheduler) RollupParentStatus(ctx context.Context, goal sqlc.AgentTask) string {
	if goal.TaskType != "goal" {
		return ""
	}
	children, err := s.q.ListChildTasks(ctx, sqlc.ListChildTasksParams{
		ParentID: sql.NullString{String: goal.ID, Valid: true},
		UserID:   goal.UserID,
	})
	if err != nil || len(children) == 0 {
		return ""
	}

	allRequiredDone := true
	anyRequiredFailed := false
	anyRequiredBlocked := false

	for _, child := range children {
		if !child.Required {
			continue
		}
		switch child.Status {
		case "done":
			// ok
		case "failed":
			anyRequiredFailed = true
			allRequiredDone = false
		case "blocked":
			anyRequiredBlocked = true
			allRequiredDone = false
		default:
			allRequiredDone = false
		}
	}

	if allRequiredDone {
		return "done"
	}
	if anyRequiredFailed {
		return "blocked"
	}
	if anyRequiredBlocked {
		return "blocked"
	}
	return ""
}

func (s *Scheduler) DepsAllDone(ctx context.Context, task sqlc.AgentTask) bool {
	depIDs, err := s.q.ListAgentTaskDeps(ctx, task.ID)
	if err != nil {
		return false
	}
	for _, depID := range depIDs {
		dep, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: depID, UserID: task.UserID})
		if err != nil || dep.Status != "done" {
			return false
		}
	}
	return true
}

func (s *Scheduler) hasActiveRun(ctx context.Context, taskID, kind string) bool {
	_, err := s.q.GetActiveRunByTaskAndKind(ctx, sqlc.GetActiveRunByTaskAndKindParams{
		TaskID: taskID,
		Kind:   kind,
	})
	return err == nil
}

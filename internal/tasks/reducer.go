package tasks

import (
	"fmt"
	"slices"
)

type Role string

const (
	RoleManager  Role = "manager"
	RoleWorker   Role = "worker"
	RoleReviewer Role = "reviewer"
	RoleSystem   Role = "system"
	RoleUser     Role = "user"
)

// goalTransitions defines allowed status transitions for goal-type tasks.
var goalTransitions = map[string][]string{
	"draft":   {"ready", "cancelled"},
	"ready":   {"running", "blocked", "done", "cancelled"},
	"running": {"blocked", "done", "failed", "cancelled"},
	"blocked": {"ready", "failed", "cancelled"},
	"done":    {"ready"}, // reopen
	"failed":  {"ready", "cancelled"},
}

// taskTransitions defines allowed status transitions for executable tasks.
var taskTransitions = map[string][]string{
	"draft":             {"ready", "cancelled"},
	"ready":             {"running", "blocked", "cancelled"},
	"running":           {"reviewing", "blocked", "failed", "cancelled"},
	"blocked":           {"ready", "failed", "cancelled"},
	"reviewing":         {"done", "changes_requested", "failed"},
	"changes_requested": {"ready", "failed", "cancelled"},
	"done":              {"ready"}, // reopen
	"failed":            {"ready", "cancelled"},
}

// runTransitions defines allowed status transitions for task runs.
var runTransitions = map[string][]string{
	"queued":  {"running", "cancelled"},
	"running": {"completed", "failed", "cancelled", "interrupted"},
}

// workerAllowed is the set of task transitions a worker role can trigger.
var workerAllowed = map[[2]string]bool{
	{"running", "reviewing"}: true,
	{"running", "blocked"}:   true,
	{"running", "failed"}:    true,
}

// reviewerAllowed is the set of task transitions a reviewer role can trigger.
var reviewerAllowed = map[[2]string]bool{
	{"reviewing", "done"}:              true,
	{"reviewing", "changes_requested"}: true,
	{"reviewing", "failed"}:            true,
}

// ValidateTaskTransition checks whether a task status transition is allowed
// for the given task type and role.
func ValidateTaskTransition(taskType, from, to string, role Role) error {
	var allowed map[string][]string
	switch taskType {
	case "goal":
		allowed = goalTransitions
	case "task":
		allowed = taskTransitions
	default:
		return fmt.Errorf("reducer: unknown task_type %q", taskType)
	}

	targets, ok := allowed[from]
	if !ok {
		return fmt.Errorf("reducer: no transitions from %q for %s", from, taskType)
	}
	if !slices.Contains(targets, to) {
		return fmt.Errorf("reducer: %s cannot transition from %q to %q", taskType, from, to)
	}

	switch role {
	case RoleWorker:
		if !workerAllowed[[2]string{from, to}] {
			return fmt.Errorf("reducer: worker cannot trigger %q → %q", from, to)
		}
	case RoleReviewer:
		if !reviewerAllowed[[2]string{from, to}] {
			return fmt.Errorf("reducer: reviewer cannot trigger %q → %q", from, to)
		}
	case RoleManager, RoleSystem, RoleUser:
		// Manager/System/User can trigger any structurally valid transition.
	default:
		return fmt.Errorf("reducer: unknown role %q", role)
	}

	return nil
}

// ValidateRunTransition checks whether a run status transition is allowed.
func ValidateRunTransition(from, to string) error {
	targets, ok := runTransitions[from]
	if !ok {
		return fmt.Errorf("reducer: no run transitions from %q (terminal state)", from)
	}
	if !slices.Contains(targets, to) {
		return fmt.Errorf("reducer: run cannot transition from %q to %q", from, to)
	}
	return nil
}

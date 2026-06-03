// Package agenttask exposes the agent's task and goal operations as native
// tools. Identity (user + agent) is read from context, never from arguments,
// and every ID-based operation enforces ownership against the acting identity
// because the task facade methods are not ownership-scoped.
package agenttask

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/internal/tools/toolctx"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

// NewTools builds the task/goal native tools bound to the given facade. Returns
// nil when the facade is unavailable so callers can append unconditionally.
func NewTools(facade *tasks.ServiceFacade) []tools.Tool {
	if facade == nil {
		return nil
	}
	t := &impl{f: facade}
	return []tools.Tool{
		fnTool{taskListDef(), t.list},
		fnTool{taskGetDef(), t.get},
		fnTool{taskCreateDef(), t.create},
		fnTool{taskCancelDef(), t.cancel},
		fnTool{taskEventsDef(), t.events},
		fnTool{taskDepsDef(), t.deps},
		fnTool{goalCreateDef(), t.goalCreate},
		fnTool{goalListDef(), t.goalList},
		fnTool{goalGetDef(), t.goalGet},
	}
}

type impl struct{ f *tasks.ServiceFacade }

// --- task tools -------------------------------------------------------------

func (t *impl) list(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	agentID, err := toolctx.AgentID(ctx)
	if err != nil {
		return "", err
	}
	status, _ := args["status"].(string)
	rows, err := t.f.ListTasksByUser(ctx, userID, agentID, projectID(ctx, args), status, limit(args), 0)
	if err != nil {
		return "", err
	}
	views := make([]taskView, 0, len(rows))
	for _, r := range rows {
		views = append(views, toTaskView(r))
	}
	return marshal(views)
}

func (t *impl) get(ctx context.Context, args map[string]any) (string, error) {
	task, err := t.loadOwnedTask(ctx, args)
	if err != nil {
		return "", err
	}
	return marshal(toTaskView(task))
}

func (t *impl) create(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	agentID, err := toolctx.AgentID(ctx)
	if err != nil {
		return "", err
	}
	title, _ := args["title"].(string)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	desc, _ := args["description"].(string)
	activate, _ := args["activate"].(bool)
	in := tasks.CreateTaskInput{
		UserID:           userID,
		AgentID:          agentID,
		Title:            title,
		Description:      desc,
		ProjectID:        projectID(ctx, args),
		GoalID:           stringArg(args, "goal_id"),
		Deps:             depInputs(args),
		ActivateOnCreate: activate,
	}
	task, err := t.f.CreateTask(ctx, in)
	if err != nil {
		return "", err
	}
	note := "Task created in draft — it will NOT run until activated. Re-create with activate:true (or activate it) to schedule it."
	if activate {
		note = "Task created and activated (ready to run)."
	}
	out := struct {
		Task taskView `json:"task"`
		Note string   `json:"note"`
	}{toTaskView(task), note}
	return marshal(out)
}

func (t *impl) cancel(ctx context.Context, args map[string]any) (string, error) {
	task, err := t.loadOwnedTask(ctx, args)
	if err != nil {
		return "", err
	}
	reason, _ := args["reason"].(string)
	if err := t.f.CancelTask(ctx, task.ID, reason, tasks.Actor{Type: tasks.ActorAgent, ID: task.AgentID}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Task %s cancelled.", task.ID), nil
}

func (t *impl) events(ctx context.Context, args map[string]any) (string, error) {
	task, err := t.loadOwnedTask(ctx, args)
	if err != nil {
		return "", err
	}
	rows, err := t.f.ListEvents(ctx, task.ID, limit(args), 0)
	if err != nil {
		return "", err
	}
	return marshal(rows)
}

func (t *impl) deps(ctx context.Context, args map[string]any) (string, error) {
	task, err := t.loadOwnedTask(ctx, args)
	if err != nil {
		return "", err
	}
	rows, err := t.f.ListDeps(ctx, task.ID, limit(args), 0)
	if err != nil {
		return "", err
	}
	return marshal(rows)
}

// loadOwnedTask fetches a task by the "task_id" arg and rejects it unless the
// acting user and agent own it.
func (t *impl) loadOwnedTask(ctx context.Context, args map[string]any) (sqlc.AgentTask, error) {
	id, _ := args["task_id"].(string)
	if id == "" {
		return sqlc.AgentTask{}, fmt.Errorf("task_id is required")
	}
	task, err := t.f.GetTask(ctx, id)
	if err != nil {
		return sqlc.AgentTask{}, err
	}
	if err := toolctx.RequireOwner(ctx, task.UserID, task.AgentID); err != nil {
		return sqlc.AgentTask{}, err
	}
	return task, nil
}

// --- goal tools -------------------------------------------------------------

func (t *impl) goalCreate(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	agentID, err := toolctx.AgentID(ctx)
	if err != nil {
		return "", err
	}
	title, _ := args["title"].(string)
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	desc, _ := args["description"].(string)
	goal, err := t.f.CreateGoal(ctx, tasks.CreateGoalInput{
		UserID:      userID,
		AgentID:     agentID,
		ProjectID:   projectID(ctx, args),
		Title:       title,
		Description: desc,
	})
	if err != nil {
		return "", err
	}
	return marshal(toGoalView(goal))
}

func (t *impl) goalList(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	agentID, err := toolctx.AgentID(ctx)
	if err != nil {
		return "", err
	}
	rows, err := t.f.ListGoals(ctx, userID, limit(args), 0)
	if err != nil {
		return "", err
	}
	// ListGoals filters by user only; restrict to the acting agent.
	views := make([]goalView, 0, len(rows))
	for _, g := range rows {
		if g.AgentID != agentID {
			continue
		}
		views = append(views, toGoalView(g))
	}
	return marshal(views)
}

func (t *impl) goalGet(ctx context.Context, args map[string]any) (string, error) {
	id, _ := args["goal_id"].(string)
	if id == "" {
		return "", fmt.Errorf("goal_id is required")
	}
	goal, err := t.f.GetGoal(ctx, id)
	if err != nil {
		return "", err
	}
	if err := toolctx.RequireOwner(ctx, goal.UserID, goal.AgentID); err != nil {
		return "", err
	}
	return marshal(toGoalView(goal))
}

// --- shared helpers ---------------------------------------------------------

type fnTool struct {
	def tools.Definition
	fn  func(context.Context, map[string]any) (string, error)
}

func (t fnTool) Definition() tools.Definition { return t.def }
func (t fnTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}

func projectID(ctx context.Context, args map[string]any) string {
	if v, ok := args["project_id"].(string); ok && v != "" {
		return v
	}
	return memory.ProjectIDFromContext(ctx)
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func limit(args map[string]any) int64 {
	if v, ok := args["limit"].(float64); ok && v > 0 {
		return int64(v)
	}
	return 50
}

func depInputs(args map[string]any) []tasks.DepInput {
	raw, ok := args["deps"].([]any)
	if !ok {
		return nil
	}
	out := make([]tasks.DepInput, 0, len(raw))
	for _, v := range raw {
		if id, ok := v.(string); ok && id != "" {
			out = append(out, tasks.DepInput{DepTaskID: id})
		}
	}
	return out
}

func marshal(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

type taskView struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	GoalID      string `json:"goal_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toTaskView(t sqlc.AgentTask) taskView {
	return taskView{
		ID:          t.ID,
		Title:       t.Title,
		Status:      t.Status,
		Priority:    t.Priority,
		GoalID:      t.GoalID.String,
		ProjectID:   t.ProjectID.String,
		Description: t.Description,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

type goalView struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	ProjectID   string `json:"project_id,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toGoalView(g sqlc.AgentGoal) goalView {
	return goalView{
		ID:          g.ID,
		Title:       g.Title,
		Status:      g.Status,
		Priority:    g.Priority,
		ProjectID:   g.ProjectID.String,
		Description: g.Description,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
	}
}

package domaintools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/toolctx"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultToolPageSize = 20
	maxToolPageSize     = 100
)

type GoalTool struct {
	svc *goal.Service
}

func NewGoalTool(svc *goal.Service) *GoalTool {
	return &GoalTool{svc: svc}
}

func (t *GoalTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "goal",
		Description: "Manage durable background goals for work that must survive turns, decompose into accepted subwork, or be cancelled later. Actions: create a goal, list goals, get status, cancel. Use scheduler for recurring or future timed prompts, not goal.",
		InputSchema: GoalInputSchema(),
	}
}

func (t *GoalTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("goal service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "goal")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "goal")
	if err != nil {
		return "", err
	}
	out, err := DispatchGoal(ctx, goalHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("goal", err)
	}
	return marshalToolResult(out)
}

type goalHandler struct {
	svc   *goal.Service
	ident toolctx.Identity
}

func (h goalHandler) Create(ctx context.Context, in GoalCreateInput) (any, error) {
	create := goal.CreateInput{
		AgentID: h.ident.AgentID,
		Title:   in.Title,
		Intent:  in.Intent,
		Kind:    goal.KindComposite,
	}
	if in.ProjectId != "" {
		create.ProjectID = in.ProjectId
	}
	if in.Priority != "" {
		create.Priority = in.Priority
	}
	if in.ReviewPolicy != "" {
		create.ReviewPolicy = in.ReviewPolicy
	}
	if len(in.AcceptanceContract) > 0 {
		if err := decodeMap(in.AcceptanceContract, &create.Contract); err != nil {
			return nil, fmt.Errorf("acceptance_contract is invalid — fix the JSON object and retry")
		}
	}
	if len(in.ConvergencePolicy) > 0 {
		if err := decodeMap(in.ConvergencePolicy, &create.Convergence); err != nil {
			return nil, fmt.Errorf("convergence_policy is invalid — fix the JSON object and retry")
		}
	}
	row, err := h.svc.CreateGoalOwned(ctx, h.ident, create)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

func (h goalHandler) Get(ctx context.Context, in GoalGetInput) (any, error) {
	row, err := h.svc.GetGoalOwned(ctx, h.ident, in.Id)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

func (h goalHandler) List(ctx context.Context, in GoalListInput) (any, error) {
	limit, offset, err := parseToolPage(in.PageSize, in.PageToken)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	filter := goal.GoalFilter{
		Lifecycle: in.Lifecycle,
		ProjectID: in.ProjectId,
		Terminal:  in.Terminal,
		Q:         in.Q,
	}
	if in.Archived != nil {
		filter.Archived = *in.Archived
	}
	var rows []sqlc.AgentGoal
	switch {
	case in.Parent != "":
		if _, err := h.svc.GetGoalOwned(ctx, h.ident, in.Parent); err != nil {
			return nil, err
		}
		rows, err = h.svc.ListChildren(ctx, in.Parent)
	case in.Root != "":
		if _, err := h.svc.GetGoalOwned(ctx, h.ident, in.Root); err != nil {
			return nil, err
		}
		rows, err = h.svc.ListSubtree(ctx, in.Root)
	default:
		rows, err = h.svc.ListGoalsOwned(ctx, h.ident, filter, int64(limit+1), int64(offset))
	}
	if err != nil {
		return nil, err
	}
	page, next := pageRows(rows, limit, offset)
	items := make([]goalResponse, 0, len(page))
	for _, row := range page {
		items = append(items, goalSummary(row))
	}
	return listResponse[goalResponse]{Items: items, HasMore: next != "", NextPageToken: next}, nil
}

func (h goalHandler) Cancel(ctx context.Context, in GoalCancelInput) (any, error) {
	if err := h.svc.CancelOwned(ctx, h.ident, in.Id, in.Reason); err != nil {
		return nil, err
	}
	row, err := h.svc.GetGoalOwned(ctx, h.ident, in.Id)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

type SchedulerTool struct {
	svc *scheduler.Service
}

func NewSchedulerTool(svc *scheduler.Service) *SchedulerTool {
	return &SchedulerTool{svc: svc}
}

func (t *SchedulerTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "scheduler",
		Description: "Manage this agent's scheduled prompts. Actions: create one-time/interval/cron jobs or template subscriptions, list/get/update/delete jobs, pause, resume. Use goal for durable acceptance-tracked work; use scheduler only for when something should run later or repeatedly.",
		InputSchema: SchedulerInputSchema(),
	}
}

func (t *SchedulerTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("scheduler service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "scheduler")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "scheduler")
	if err != nil {
		return "", err
	}
	out, err := DispatchScheduler(ctx, schedulerHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("scheduler", err)
	}
	return marshalToolResult(out)
}

type schedulerHandler struct {
	svc   *scheduler.Service
	ident toolctx.Identity
}

func (h schedulerHandler) Create(ctx context.Context, in SchedulerCreateInput) (any, error) {
	sched := scheduler.Schedule{Cron: in.Cron, Every: in.Every, At: in.At}
	var job scheduler.Job
	var err error
	if in.TemplateKey != "" {
		job, err = h.svc.SubscribeOwned(ctx, h.ident, h.ident.AgentID, in.TemplateKey, sched)
	} else {
		job, err = h.svc.CreateJobOwned(ctx, h.ident, in.Name, in.Message, sched, in.SessionMode, h.ident.AgentID)
		if err == nil && in.Enabled != nil && !*in.Enabled {
			job, err = h.svc.SetJobEnabledOwned(ctx, h.ident, h.ident.AgentID, job.ID, false)
		}
	}
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Get(ctx context.Context, in SchedulerGetInput) (any, error) {
	job, err := h.svc.GetJobOwned(ctx, h.ident, h.ident.AgentID, in.Id)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) List(ctx context.Context, _ SchedulerListInput) (any, error) {
	jobs, err := h.svc.ListJobsOwned(ctx, h.ident, h.ident.AgentID)
	if err != nil {
		return nil, err
	}
	items := make([]schedulerResponse, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, schedulerSummary(job))
	}
	return listResponse[schedulerResponse]{Items: items, HasMore: false}, nil
}

func (h schedulerHandler) Update(ctx context.Context, in SchedulerUpdateInput) (any, error) {
	update := scheduler.JobUpdate{}
	if in.Name != "" {
		update.Name = &in.Name
	}
	if in.Message != "" {
		update.Message = &in.Message
	}
	if in.Cron != "" || in.Every != "" || in.At != "" {
		sched := scheduler.Schedule{Cron: in.Cron, Every: in.Every, At: in.At}
		update.Schedule = &sched
	}
	if in.SessionMode != "" {
		update.SessionMode = &in.SessionMode
	}
	if in.Enabled != nil {
		update.Enabled = in.Enabled
	}
	job, err := h.svc.UpdateJobOwned(ctx, h.ident, h.ident.AgentID, in.Id, update)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Delete(ctx context.Context, in SchedulerDeleteInput) (any, error) {
	if err := h.svc.DeleteJobOwned(ctx, h.ident, h.ident.AgentID, in.Id); err != nil {
		return nil, err
	}
	return map[string]any{"id": in.Id, "status": "deleted"}, nil
}

func (h schedulerHandler) Pause(ctx context.Context, in SchedulerPauseInput) (any, error) {
	job, err := h.svc.SetJobEnabledOwned(ctx, h.ident, h.ident.AgentID, in.Id, false)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Resume(ctx context.Context, in SchedulerResumeInput) (any, error) {
	job, err := h.svc.SetJobEnabledOwned(ctx, h.ident, h.ident.AgentID, in.Id, true)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

type goalResponse struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Lifecycle       string `json:"lifecycle"`
	AcceptanceState string `json:"acceptance_state"`
	Kind            string `json:"kind"`
	Priority        string `json:"priority"`
	UpdatedAt       string `json:"updated_at"`
}

type schedulerResponse struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Enabled     bool          `json:"enabled"`
	Schedule    schedulerView `json:"schedule"`
	SessionMode string        `json:"session_mode"`
	UpdatedAt   string        `json:"updated_at"`
	TemplateKey string        `json:"template_key,omitempty"`
}

type schedulerView struct {
	Cron  string `json:"cron,omitempty"`
	Every string `json:"every,omitempty"`
	At    string `json:"at,omitempty"`
}

type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func goalSummary(row sqlc.AgentGoal) goalResponse {
	return goalResponse{
		ID:              row.ID,
		Title:           row.Title,
		Lifecycle:       row.Lifecycle,
		AcceptanceState: row.AcceptanceState,
		Kind:            row.Kind,
		Priority:        row.Priority,
		UpdatedAt:       row.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func schedulerSummary(job scheduler.Job) schedulerResponse {
	return schedulerResponse{
		ID:          job.ID,
		Name:        job.Name,
		Enabled:     job.Enabled,
		Schedule:    schedulerView{Cron: job.Schedule.Cron, Every: job.Schedule.Every, At: job.Schedule.At},
		SessionMode: job.SessionMode,
		UpdatedAt:   job.UpdatedAt.UTC().Format(time.RFC3339),
		TemplateKey: job.JobKey,
	}
}

func toolIdentity(ctx context.Context, tool string) (toolctx.Identity, error) {
	ident, err := toolctx.FromContext(ctx)
	if err != nil {
		return toolctx.Identity{}, fmt.Errorf("this session has no user identity — %s tools are unavailable here", tool)
	}
	if ident.AgentID == "" {
		return toolctx.Identity{}, fmt.Errorf("this session has no agent identity — %s tools are unavailable here", tool)
	}
	return ident, nil
}

func actionArg(args map[string]any, tool string) (string, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return "", fmt.Errorf("%s action is required — choose an action from the tool schema", tool)
	}
	return action, nil
}

func mapToolError(tool string, err error) error {
	switch {
	case errors.Is(err, toolctx.ErrUnauthenticated):
		return fmt.Errorf("this session has no user identity — %s tools are unavailable here", tool)
	case errors.Is(err, toolctx.ErrNotFound), errors.Is(err, goal.ErrNotFound):
		return fmt.Errorf("%s not found — check the id with action=list", tool)
	case errors.Is(err, toolctx.ErrForbidden):
		return fmt.Errorf("%s access denied — use action=list to see resources available to this agent", tool)
	default:
		return err
	}
}

func marshalToolResult(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeMap(src map[string]any, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func parseToolPage(pageSize int, pageToken string) (int, int, error) {
	limit := defaultToolPageSize
	if pageSize != 0 {
		if pageSize < 1 || pageSize > maxToolPageSize {
			return 0, 0, fmt.Errorf("invalid page_size")
		}
		limit = pageSize
	}
	if pageToken == "" {
		return limit, 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(pageToken)
	if err != nil {
		return 0, 0, err
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, 0, fmt.Errorf("invalid page_token")
	}
	return limit, offset, nil
}

func pageRows[T any](rows []T, limit, offset int) ([]T, string) {
	if len(rows) > limit {
		return rows[:limit], base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
	}
	return rows, ""
}

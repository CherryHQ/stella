package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }
func (t *Tool) Definition() tools.Definition {
	return tools.Definition{Name: ToolName, Description: "Manage this agent's scheduled prompts. Actions: create one-time/interval/cron jobs or template subscriptions, list/get/update/delete jobs, pause, resume. Use goal for durable acceptance-tracked work; use scheduler only for when something should run later or repeatedly.", InputSchema: InputSchema()}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("scheduler service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, "scheduler")
	if err != nil {
		return "", err
	}
	action, err := tools.ActionArg(args, "scheduler")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, schedulerHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", authz.MapError("scheduler", err)
	}
	return tools.MarshalResult(out)
}

type schedulerHandler struct {
	svc   *Service
	ident authz.Identity
}

func (h schedulerHandler) Create(ctx context.Context, in CreateInput) (any, error) {
	sched := Schedule{Cron: in.Cron, Every: in.Every, At: in.At}
	var job Job
	var err error
	if in.TemplateKey != "" {
		job, err = h.svc.As(h.ident).Subscribe(ctx, h.ident.AgentID, in.TemplateKey, sched)
	} else {
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		job, err = h.svc.As(h.ident).CreateJobWithEnabled(ctx, in.Name, in.Message, sched, in.SessionMode, h.ident.AgentID, in.IdempotencyKey, enabled)
	}
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Get(ctx context.Context, in GetInput) (any, error) {
	job, err := h.svc.As(h.ident).GetJob(ctx, h.ident.AgentID, in.Id)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) List(ctx context.Context, _ ListInput) (any, error) {
	jobs, err := h.svc.As(h.ident).ListJobs(ctx, h.ident.AgentID)
	if err != nil {
		return nil, err
	}
	items := make([]schedulerResponse, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, schedulerSummary(job))
	}
	return listResponse[schedulerResponse]{Items: items, HasMore: false}, nil
}

func (h schedulerHandler) Update(ctx context.Context, in UpdateInput) (any, error) {
	update := JobUpdate{}
	if in.Name != "" {
		update.Name = &in.Name
	}
	if in.Message != "" {
		update.Message = &in.Message
	}
	if in.Cron != "" || in.Every != "" || in.At != "" {
		sched := Schedule{Cron: in.Cron, Every: in.Every, At: in.At}
		update.Schedule = &sched
	}
	if in.SessionMode != "" {
		update.SessionMode = &in.SessionMode
	}
	if in.Enabled != nil {
		update.Enabled = in.Enabled
	}
	job, err := h.svc.As(h.ident).UpdateJob(ctx, h.ident.AgentID, in.Id, update)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Delete(ctx context.Context, in DeleteInput) (any, error) {
	if err := h.svc.As(h.ident).DeleteJob(ctx, h.ident.AgentID, in.Id); err != nil {
		return nil, err
	}
	return map[string]any{"id": in.Id, "status": "deleted"}, nil
}

func (h schedulerHandler) Pause(ctx context.Context, in PauseInput) (any, error) {
	job, err := h.svc.As(h.ident).SetJobEnabled(ctx, h.ident.AgentID, in.Id, false)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Resume(ctx context.Context, in ResumeInput) (any, error) {
	job, err := h.svc.As(h.ident).SetJobEnabled(ctx, h.ident.AgentID, in.Id, true)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
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

func schedulerSummary(job Job) schedulerResponse {
	return schedulerResponse{ID: job.ID, Name: job.Name, Enabled: job.Enabled, Schedule: schedulerView{Cron: job.Schedule.Cron, Every: job.Schedule.Every, At: job.Schedule.At}, SessionMode: job.SessionMode, UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339), TemplateKey: job.JobKey}
}

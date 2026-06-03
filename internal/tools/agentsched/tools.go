// Package agentsched exposes the agent's scheduler operations as native tools.
// Jobs are scoped to the acting user and agent; plugin- and system-owned jobs
// are read-only to agents, mirroring the admin HTTP handlers.
package agentsched

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/tools/toolctx"
	"github.com/CherryHQ/stella/pkg/tools"
)

// NewTools builds the scheduler native tools bound to the given service.
// Returns nil when the service is unavailable.
func NewTools(svc *scheduler.Service) []tools.Tool {
	if svc == nil {
		return nil
	}
	t := &impl{svc: svc}
	return []tools.Tool{
		fnTool{addDef(), t.add},
		fnTool{listDef(), t.list},
		fnTool{removeDef(), t.remove},
	}
}

type impl struct{ svc *scheduler.Service }

func (t *impl) add(ctx context.Context, args map[string]any) (string, error) {
	// scheduler_add is user-bound: never fall back to a system-scoped job.
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	agentID, err := toolctx.AgentID(ctx)
	if err != nil {
		return "", err
	}
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	message, _ := args["message"].(string)
	if message == "" {
		return "", fmt.Errorf("message is required (the instruction the agent runs on schedule)")
	}
	sched, err := parseSchedule(args)
	if err != nil {
		return "", err
	}
	sessionMode, _ := args["session_mode"].(string)
	job, err := t.svc.AddJobWithOwner(name, message, sched, sessionMode, agentID, userID)
	if err != nil {
		return "", err
	}
	return marshal(toView(job))
}

func (t *impl) list(ctx context.Context, _ map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	agentID, err := toolctx.AgentID(ctx)
	if err != nil {
		return "", err
	}
	views := make([]jobView, 0)
	for _, j := range t.svc.ListJobs() {
		if j.OwnerKind != scheduler.JobOwnerUser || j.UserID != userID || j.AgentID != agentID {
			continue
		}
		views = append(views, toView(j))
	}
	return marshal(views)
}

func (t *impl) remove(ctx context.Context, args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	job, ok := t.svc.GetJob(id)
	if !ok {
		return "", fmt.Errorf("job %q not found", id)
	}
	// Plugin/system-owned jobs are read-only to agents.
	if job.OwnerKind != scheduler.JobOwnerUser {
		return "", fmt.Errorf("%w: job %q is %s-owned and read-only", toolctx.ErrPermission, id, job.OwnerKind)
	}
	if err := toolctx.RequireOwner(ctx, job.UserID, job.AgentID); err != nil {
		return "", err
	}
	if err := t.svc.RemoveJob(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("Job %s removed.", id), nil
}

func parseSchedule(args map[string]any) (scheduler.Schedule, error) {
	cron, _ := args["cron"].(string)
	every, _ := args["every"].(string)
	at, _ := args["at"].(string)
	n := 0
	for _, v := range []string{cron, every, at} {
		if v != "" {
			n++
		}
	}
	if n == 0 {
		return scheduler.Schedule{}, fmt.Errorf("one of cron, every, or at is required")
	}
	if n > 1 {
		return scheduler.Schedule{}, fmt.Errorf("only one of cron, every, or at may be set")
	}
	return scheduler.Schedule{Cron: cron, Every: every, At: at}, nil
}

type fnTool struct {
	def tools.Definition
	fn  func(context.Context, map[string]any) (string, error)
}

func (t fnTool) Definition() tools.Definition { return t.def }
func (t fnTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}

type jobView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Schedule    string `json:"schedule"`
	Message     string `json:"message,omitempty"`
	SessionMode string `json:"session_mode"`
	Enabled     bool   `json:"enabled"`
}

func toView(j scheduler.Job) jobView {
	sched := ""
	switch {
	case j.Schedule.Cron != "":
		sched = "cron " + j.Schedule.Cron
	case j.Schedule.Every != "":
		sched = "every " + j.Schedule.Every
	case j.Schedule.At != "":
		sched = "at " + j.Schedule.At
	}
	return jobView{
		ID:          j.ID,
		Name:        j.Name,
		Schedule:    sched,
		Message:     j.Message,
		SessionMode: j.SessionMode,
		Enabled:     j.Enabled,
	}
}

func marshal(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

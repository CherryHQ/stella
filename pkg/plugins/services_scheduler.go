package plugins

import (
	"context"
	"time"
)

// SchedulerSchedule identifies when a plugin-owned job should run.
type SchedulerSchedule struct {
	Cron  string `json:"cron,omitempty"`
	Every string `json:"every,omitempty"`
	At    string `json:"at,omitempty"`
}

// SchedulerJobSpec is the desired state for one plugin-owned scheduled job.
type SchedulerJobSpec struct {
	Key         string            `json:"key"`
	RuntimeName string            `json:"runtime_name"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Schedule    SchedulerSchedule `json:"schedule"`
	Payload     map[string]any    `json:"payload,omitempty"`
	Enabled     bool              `json:"enabled,omitempty"`
}

// SchedulerJob is the host view of one reconciled plugin-owned scheduled job.
type SchedulerJob struct {
	ID          string            `json:"id"`
	PluginID    string            `json:"plugin_id"`
	Key         string            `json:"key"`
	RuntimeName string            `json:"runtime_name"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Schedule    SchedulerSchedule `json:"schedule"`
	Payload     map[string]any    `json:"payload,omitempty"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	LastRunAt   *time.Time        `json:"last_run_at,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
}

// ScheduledJobRunner is implemented by managed runtimes that can handle plugin-owned scheduled jobs.
type ScheduledJobRunner interface {
	RunScheduledJob(ctx context.Context, key string, payload map[string]any) error
}

// SchedulerService exposes plugin-owned scheduled job reconciliation through the host.
type SchedulerService interface {
	ReconcilePluginJobs(ctx context.Context, pluginID string, jobs []SchedulerJobSpec) error
	DeletePluginJobs(ctx context.Context, pluginID string) error
	DeletePluginJob(ctx context.Context, pluginID string, key string) error
	ListPluginJobs(ctx context.Context, pluginID string) ([]SchedulerJob, error)
}

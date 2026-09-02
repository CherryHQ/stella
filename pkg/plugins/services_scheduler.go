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

// Scheduler exposes plugin-owned scheduled job reconciliation through the host.
type Scheduler interface {
	ReconcileJobs(ctx context.Context, jobs []SchedulerJobSpec) error
	DeleteJobs(ctx context.Context) error
	DeleteJob(ctx context.Context, key string) error
	ListJobs(ctx context.Context) ([]SchedulerJob, error)
}

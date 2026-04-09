package main

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	internalscheduler "github.com/vaayne/anna/internal/scheduler"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type schedulerServiceAdapter struct {
	service *internalscheduler.Service
	host    pkgplugins.ServiceHost
}

func newSchedulerServiceAdapter(service *internalscheduler.Service, host pkgplugins.ServiceHost) schedulerServiceAdapter {
	adapter := schedulerServiceAdapter{service: service, host: host}
	if service != nil {
		service.AddOnJobListener(adapter.dispatchPluginJob)
	}
	return adapter
}

func (a schedulerServiceAdapter) ReconcilePluginJobs(ctx context.Context, pluginID string, jobs []pkgplugins.SchedulerJobSpec) error {
	desired := make(map[string]pkgplugins.SchedulerJobSpec, len(jobs))
	for _, job := range jobs {
		if err := validatePluginJobSpec(job); err != nil {
			return fmt.Errorf("scheduler job %q: %w", job.Key, err)
		}
		if _, exists := desired[job.Key]; exists {
			return fmt.Errorf("duplicate scheduler job key %q", job.Key)
		}
		desired[job.Key] = normalizedPluginJobSpec(job)
	}

	existing, err := a.ListPluginJobs(ctx, pluginID)
	if err != nil {
		return err
	}
	existingByKey := make(map[string]pkgplugins.SchedulerJob, len(existing))
	for _, job := range existing {
		existingByKey[job.Key] = job
	}

	for key, current := range existingByKey {
		spec, ok := desired[key]
		if !ok || !spec.Enabled {
			if err := a.service.RemoveJob(current.ID); err != nil {
				return err
			}
			continue
		}
		if schedulerJobsEqual(current, spec) {
			continue
		}
		if err := a.service.RemoveJob(current.ID); err != nil {
			return err
		}
		if _, err := a.createPluginJob(pluginID, spec); err != nil {
			return err
		}
	}

	for key, spec := range desired {
		if !spec.Enabled {
			continue
		}
		if _, exists := existingByKey[key]; exists {
			continue
		}
		if _, err := a.createPluginJob(pluginID, spec); err != nil {
			return err
		}
	}

	return nil
}

func (a schedulerServiceAdapter) DeletePluginJobs(ctx context.Context, pluginID string) error {
	jobs, err := a.ListPluginJobs(ctx, pluginID)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err := a.service.RemoveJob(job.ID); err != nil {
			return err
		}
	}
	return nil
}

func (a schedulerServiceAdapter) DeletePluginJob(ctx context.Context, pluginID string, key string) error {
	jobs, err := a.ListPluginJobs(ctx, pluginID)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Key == key {
			return a.service.RemoveJob(job.ID)
		}
	}
	return nil
}

func (a schedulerServiceAdapter) ListPluginJobs(ctx context.Context, pluginID string) ([]pkgplugins.SchedulerJob, error) {
	_ = ctx
	jobs := a.service.ListJobs()
	out := make([]pkgplugins.SchedulerJob, 0, len(jobs))
	for _, job := range jobs {
		owner, key, runtimeName, description, payload, ok := internalscheduler.DecodePluginJob(job)
		if !ok || owner != pluginID {
			continue
		}
		out = append(out, pkgplugins.SchedulerJob{
			ID:          job.ID,
			PluginID:    owner,
			Key:         key,
			RuntimeName: runtimeName,
			Name:        job.Name,
			Description: description,
			Schedule: pkgplugins.SchedulerSchedule{
				Cron:  job.Schedule.Cron,
				Every: job.Schedule.Every,
				At:    job.Schedule.At,
			},
			Payload:   payload,
			Enabled:   job.Enabled,
			CreatedAt: job.CreatedAt,
		})
	}
	return out, nil
}

func (a schedulerServiceAdapter) createPluginJob(pluginID string, spec pkgplugins.SchedulerJobSpec) (pkgplugins.SchedulerJob, error) {
	message, err := internalscheduler.EncodePluginJobMessage(pluginID, spec.Key, spec.RuntimeName, spec.Description, spec.Payload)
	if err != nil {
		return pkgplugins.SchedulerJob{}, err
	}
	job, err := a.service.AddJob(spec.Name, message, internalscheduler.Schedule{
		Cron:  spec.Schedule.Cron,
		Every: spec.Schedule.Every,
		At:    spec.Schedule.At,
	}, internalscheduler.SessionReuse)
	if err != nil {
		return pkgplugins.SchedulerJob{}, err
	}
	return pkgplugins.SchedulerJob{
		ID:          job.ID,
		PluginID:    pluginID,
		Key:         spec.Key,
		RuntimeName: spec.RuntimeName,
		Name:        spec.Name,
		Description: spec.Description,
		Schedule:    spec.Schedule,
		Payload:     clonePayload(spec.Payload),
		Enabled:     job.Enabled,
		CreatedAt:   job.CreatedAt,
	}, nil
}

func (a schedulerServiceAdapter) dispatchPluginJob(ctx context.Context, job internalscheduler.Job) {
	pluginID, key, runtimeName, _, payload, ok := internalscheduler.DecodePluginJob(job)
	if !ok {
		return
	}
	if a.host == nil {
		slog.Error("scheduler plugin job dropped: host unavailable", "plugin_id", pluginID, "key", key)
		return
	}
	handle, ok := a.host.Runtime().Get(pluginID, runtimeName)
	if !ok {
		slog.Warn("scheduler plugin job dropped: runtime unavailable", "plugin_id", pluginID, "runtime", runtimeName, "key", key)
		return
	}
	accessor, ok := handle.(interface{ RuntimeAccessor() any })
	if !ok {
		slog.Warn("scheduler plugin job dropped: runtime accessor unavailable", "plugin_id", pluginID, "runtime", runtimeName, "key", key)
		return
	}
	runner, ok := accessor.RuntimeAccessor().(pkgplugins.ScheduledJobRunner)
	if !ok {
		slog.Warn("scheduler plugin job dropped: runtime does not handle scheduled jobs", "plugin_id", pluginID, "runtime", runtimeName, "key", key)
		return
	}
	if err := runner.RunScheduledJob(ctx, key, clonePayload(payload)); err != nil {
		slog.Error("scheduler plugin job failed", "plugin_id", pluginID, "runtime", runtimeName, "key", key, "error", err)
	}
}

func validatePluginJobSpec(job pkgplugins.SchedulerJobSpec) error {
	if job.Key == "" {
		return fmt.Errorf("key is required")
	}
	if job.RuntimeName == "" {
		return fmt.Errorf("runtime_name is required")
	}
	if job.Name == "" {
		return fmt.Errorf("name is required")
	}
	setCount := 0
	if job.Schedule.Cron != "" {
		setCount++
	}
	if job.Schedule.Every != "" {
		setCount++
	}
	if job.Schedule.At != "" {
		setCount++
	}
	if setCount != 1 {
		return fmt.Errorf("schedule must set exactly one of cron, every, or at")
	}
	return nil
}

func normalizedPluginJobSpec(job pkgplugins.SchedulerJobSpec) pkgplugins.SchedulerJobSpec {
	job.Payload = clonePayload(job.Payload)
	return job
}

func schedulerJobsEqual(current pkgplugins.SchedulerJob, desired pkgplugins.SchedulerJobSpec) bool {
	return current.RuntimeName == desired.RuntimeName &&
		current.Name == desired.Name &&
		current.Description == desired.Description &&
		current.Schedule == desired.Schedule &&
		reflect.DeepEqual(current.Payload, desired.Payload)
}

func clonePayload(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

package main

import (
	"context"
	"fmt"
	"maps"
	"reflect"

	internalscheduler "github.com/CherryHQ/stella/internal/scheduler"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type schedulerServiceAdapter struct {
	service *internalscheduler.Service
	lookup  pkgplugins.RuntimeLookup
}

func newSchedulerServiceAdapter(service *internalscheduler.Service, lookup pkgplugins.RuntimeLookup) schedulerServiceAdapter {
	adapter := schedulerServiceAdapter{service: service, lookup: lookup}
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
			if err := a.service.RemoveJobContext(ctx, current.ID); err != nil {
				return err
			}
			continue
		}
		if schedulerJobsEqual(current, spec) {
			continue
		}
		if err := a.service.RemoveJobContext(ctx, current.ID); err != nil {
			return err
		}
		if _, err := a.createPluginJob(ctx, pluginID, spec); err != nil {
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
		if _, err := a.createPluginJob(ctx, pluginID, spec); err != nil {
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
		if err := a.service.RemoveJobContext(ctx, job.ID); err != nil {
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
			return a.service.RemoveJobContext(ctx, job.ID)
		}
	}
	return nil
}

func (a schedulerServiceAdapter) ListPluginJobs(ctx context.Context, pluginID string) ([]pkgplugins.SchedulerJob, error) {
	_ = ctx
	jobs := a.service.ListJobs()
	out := make([]pkgplugins.SchedulerJob, 0, len(jobs))
	for _, job := range jobs {
		if job.OwnerKind != internalscheduler.JobOwnerPlugin || job.PluginID != pluginID {
			continue
		}
		out = append(out, pluginJobFromInternal(job))
	}
	return out, nil
}

func (a schedulerServiceAdapter) createPluginJob(ctx context.Context, pluginID string, spec pkgplugins.SchedulerJobSpec) (pkgplugins.SchedulerJob, error) {
	job, err := a.service.AddPluginJob(
		ctx,
		pluginID,
		spec.Key,
		spec.RuntimeName,
		spec.Name,
		spec.Description,
		internalscheduler.Schedule{
			Cron:  spec.Schedule.Cron,
			Every: spec.Schedule.Every,
			At:    spec.Schedule.At,
		},
		spec.Payload,
	)
	if err != nil {
		return pkgplugins.SchedulerJob{}, err
	}
	return pluginJobFromInternal(job), nil
}

func (a schedulerServiceAdapter) dispatchPluginJob(ctx context.Context, job internalscheduler.Job) error {
	if job.OwnerKind != internalscheduler.JobOwnerPlugin {
		return nil
	}
	if a.lookup == nil {
		return fmt.Errorf("host unavailable for plugin %s job %s", job.PluginID, job.JobKey)
	}

	handle, ok := a.lookup.Lookup(ctx, job.PluginID, job.RuntimeName)
	if !ok {
		return fmt.Errorf("runtime unavailable for plugin %s runtime %s", job.PluginID, job.RuntimeName)
	}
	accessor, ok := handle.(interface{ RuntimeAccessor() any })
	if !ok {
		return fmt.Errorf("runtime accessor unavailable for plugin %s runtime %s", job.PluginID, job.RuntimeName)
	}
	runner, ok := accessor.RuntimeAccessor().(pkgplugins.ScheduledJobRunner)
	if !ok {
		return fmt.Errorf("runtime %s/%s does not handle scheduled jobs", job.PluginID, job.RuntimeName)
	}
	return runner.RunScheduledJob(ctx, job.JobKey, clonePayload(job.Payload))
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

func pluginJobFromInternal(job internalscheduler.Job) pkgplugins.SchedulerJob {
	return pkgplugins.SchedulerJob{
		ID:          job.ID,
		PluginID:    job.PluginID,
		Key:         job.JobKey,
		RuntimeName: job.RuntimeName,
		Name:        job.Name,
		Description: job.Description,
		Schedule: pkgplugins.SchedulerSchedule{
			Cron:  job.Schedule.Cron,
			Every: job.Schedule.Every,
			At:    job.Schedule.At,
		},
		Payload:   clonePayload(job.Payload),
		Enabled:   job.Enabled,
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
		LastRunAt: job.LastRunAt,
		LastError: job.LastError,
	}
}

func clonePayload(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}

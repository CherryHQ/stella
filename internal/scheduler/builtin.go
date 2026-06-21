package scheduler

import "fmt"

// BuiltinJob defines a handler-mode job that is automatically seeded on
// scheduler startup. The Handler is invoked directly when the job fires,
// bypassing the agent dispatch. Used for internal subsystems (e.g. reflect)
// that run native Go code on a schedule.
type BuiltinJob struct {
	Name        string
	Schedule    Schedule
	SessionMode string
	AgentID     string
	// Handler is invoked directly when the job fires instead of dispatching
	// through the default OnJob agent path. Required.
	Handler OnJobFunc
}

// RegisterBuiltin registers a builtin job spec on this Service instance.
//
// Called from gateway wiring during setup. Returns an error on a malformed
// spec so the gateway can fail startup cleanly. Must be called BEFORE
// (*Service).Start — handler-mode dispatch is keyed on Name, so persisted
// jobs loaded by Start can only be routed correctly if the handler is
// already registered.
//
// Mutual exclusion with templates: RegisterBuiltin rejects any name that
// already appears as a template key or name, and RegisterTemplate likewise
// rejects the reverse. Whichever registers second for a conflicting
// key/name errors.
func (s *Service) RegisterBuiltin(job BuiltinJob) error {
	if err := validateBuiltin(job); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("scheduler: RegisterBuiltin(%q) called after Start", job.Name)
	}
	if s.runtimeBuiltins == nil {
		s.runtimeBuiltins = make(map[string]BuiltinJob)
	}
	if _, dup := s.runtimeBuiltins[job.Name]; dup {
		return fmt.Errorf("scheduler: builtin %q already registered", job.Name)
	}
	// Reject if a template with the same key or name was registered first.
	for _, t := range s.templates {
		if t.Key == job.Name || t.Name == job.Name {
			return fmt.Errorf("scheduler: builtin name %q conflicts with a registered template", job.Name)
		}
	}
	s.runtimeBuiltins[job.Name] = job
	return nil
}

func validateBuiltin(job BuiltinJob) error {
	if job.Name == "" {
		return fmt.Errorf("builtin job: Name is required")
	}
	if job.Handler == nil {
		return fmt.Errorf("builtin job %q: Handler is required", job.Name)
	}
	return nil
}

// retiredBuiltinNames is the set of legacy system job names that should be
// deleted on startup. These were Message-mode all_users builtins replaced by
// the template + subscription model. Deletion is idempotent: if the rows are
// already gone the loop is a no-op.
var retiredBuiltinNames = map[string]bool{
	"recally-rss":    true,
	"recally-digest": true,
}

// EnsureBuiltinJobs creates or updates all registered builtin jobs, and
// idempotently retires legacy system job rows whose names are in
// retiredBuiltinNames (cascade deletes their run history too, via the
// sched_job_run FK ON DELETE CASCADE).
func (s *Service) EnsureBuiltinJobs() {
	// Retire legacy system rows first. s.jobs is already populated by Start.
	s.mu.Lock()
	var toRetire []Job
	for _, j := range s.jobs {
		if j.OwnerKind == JobOwnerSystem && retiredBuiltinNames[j.Name] {
			toRetire = append(toRetire, j)
		}
	}
	s.mu.Unlock()

	for _, j := range toRetire {
		// Tear down the live River registration before deleting from DB.
		s.mu.Lock()
		s.unscheduleJob(j.ID)
		delete(s.jobs, j.ID)
		s.mu.Unlock()

		if err := s.deleteJob(s.ctx, j.ID); err != nil {
			s.log.Warn("failed to retire legacy system job", "name", j.Name, "id", j.ID, "error", err)
		} else {
			s.log.Info("retired legacy system job", "name", j.Name, "id", j.ID)
		}
	}

	s.mu.Lock()
	jobs := make([]BuiltinJob, 0, len(s.runtimeBuiltins))
	for _, j := range s.runtimeBuiltins {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()

	for _, j := range jobs {
		if _, err := s.EnsureJob(j.Name, "", j.Schedule, j.SessionMode, j.AgentID, ExecScopeSystem); err != nil {
			s.log.Warn("failed to ensure builtin job", "name", j.Name, "error", err)
		}
	}
}

// nameIsReservedBuiltin reports whether name matches any registered runtime
// builtin or template. Used to reject user- or plugin-owned jobs that would
// otherwise hijack a builtin's handler dispatch or collide with a template name.
func (s *Service) nameIsReservedBuiltin(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reservedName(name)
}

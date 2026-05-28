package scheduler

import "fmt"

// BuiltinJob defines a job that is automatically seeded on scheduler startup.
//
// A spec must run in exactly one mode:
//   - Message mode: Message != "" — the scheduler fires the default OnJob
//     callback, which routes the message through the agent pool. Used for
//     prompt-driven builtins like recally-rss and recally-digest.
//   - Handler mode: Handler != nil — the scheduler invokes the Go callback
//     directly, bypassing the agent dispatch. Used for internal subsystems
//     (e.g. reflect) that run native Go code on a schedule.
//
// Setting both, or neither, is rejected at registration time.
type BuiltinJob struct {
	Name        string
	Message     string
	Schedule    Schedule
	SessionMode string
	AgentID     string
	// ExecScope controls how the job runs: ExecScopeSystem (once, no user context),
	// ExecScopeUser (once for a specific user), or ExecScopeAllUsers (fan-out per active user).
	// Defaults to ExecScopeSystem when empty.
	ExecScope string
	// Handler, when set, is invoked directly when the job fires instead of
	// dispatching the Message through the default OnJob agent path.
	Handler OnJobFunc
}

// RegisterBuiltin registers a builtin job spec on this Service instance.
//
// Called from gateway wiring during setup. Returns an error on a malformed
// spec so the gateway can fail startup cleanly. Must be called BEFORE
// (*Service).Start — handler-mode dispatch is keyed on Name, so persisted
// jobs loaded by Start can only be routed correctly if the handler is
// already registered.
func (s *Service) RegisterBuiltin(job BuiltinJob) error {
	if err := validateBuiltin(job); err != nil {
		return err
	}
	if job.ExecScope == "" {
		job.ExecScope = ExecScopeSystem
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
	s.runtimeBuiltins[job.Name] = job
	return nil
}

func validateBuiltin(job BuiltinJob) error {
	if job.Name == "" {
		return fmt.Errorf("builtin job: Name is required")
	}
	hasMessage := job.Message != ""
	hasHandler := job.Handler != nil
	if hasMessage == hasHandler {
		return fmt.Errorf("builtin job %q: exactly one of Message or Handler must be set", job.Name)
	}
	return nil
}

// EnsureBuiltinJobs creates or updates all registered builtin jobs for the
// given org. For ExecScopeAllUsers jobs the scheduler fans out to all
// active users at execution time.
func (s *Service) EnsureBuiltinJobs(orgID string) {
	s.mu.Lock()
	jobs := make([]BuiltinJob, 0, len(s.runtimeBuiltins))
	for _, j := range s.runtimeBuiltins {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()

	for _, j := range jobs {
		if _, err := s.EnsureJob(j.Name, j.Message, j.Schedule, j.SessionMode, j.AgentID, j.ExecScope, orgID); err != nil {
			s.log.Warn("failed to ensure builtin job", "name", j.Name, "org_id", orgID, "error", err)
		}
	}
}

// nameIsReservedBuiltin reports whether name matches any registered runtime
// builtin. Used to reject user- or plugin-owned jobs that would otherwise
// hijack a builtin's handler dispatch.
func (s *Service) nameIsReservedBuiltin(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runtimeBuiltins[name]
	return ok
}

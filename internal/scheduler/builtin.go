package scheduler

import "sync"

// BuiltinJob defines a job that is automatically seeded on scheduler startup.
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
}

var (
	builtinMu   sync.Mutex
	builtinJobs []BuiltinJob
)

// RegisterBuiltin registers a builtin job spec. Call from init().
func RegisterBuiltin(job BuiltinJob) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if job.ExecScope == "" {
		job.ExecScope = ExecScopeSystem
	}
	builtinJobs = append(builtinJobs, job)
}

// EnsureBuiltinJobs creates or updates all builtin jobs for the given org.
// For ExecScopeAllUsers jobs the scheduler fans out to all active users at execution time.
func (s *Service) EnsureBuiltinJobs(orgID string) {
	builtinMu.Lock()
	jobs := append([]BuiltinJob(nil), builtinJobs...)
	builtinMu.Unlock()

	for _, j := range jobs {
		if _, err := s.EnsureJob(j.Name, j.Message, j.Schedule, j.SessionMode, j.AgentID, j.ExecScope, orgID); err != nil {
			s.log.Warn("failed to ensure builtin job", "name", j.Name, "org_id", orgID, "error", err)
		}
	}
}

package scheduler

import "sync"

// BuiltinJob defines a job that is automatically seeded on scheduler startup.
type BuiltinJob struct {
	Name        string
	Message     string
	Schedule    Schedule
	SessionMode string
	AgentID     string
	// PerUser, when true, creates one job instance per active user rather than
	// a single system-level job. Use for jobs that operate on user-specific data.
	PerUser bool
}

var (
	builtinMu   sync.Mutex
	builtinJobs []BuiltinJob
)

// RegisterBuiltin registers a builtin job spec. Call from init().
func RegisterBuiltin(job BuiltinJob) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	builtinJobs = append(builtinJobs, job)
}

// EnsureBuiltinJobs creates or updates all system-level (non-PerUser) builtin jobs.
func (s *Service) EnsureBuiltinJobs() {
	builtinMu.Lock()
	jobs := append([]BuiltinJob(nil), builtinJobs...)
	builtinMu.Unlock()

	for _, j := range jobs {
		if j.PerUser {
			continue
		}
		if _, err := s.EnsureJob(j.Name, j.Message, j.Schedule, j.SessionMode, j.AgentID); err != nil {
			s.log.Warn("failed to ensure builtin job", "name", j.Name, "error", err)
		}
	}
}

// EnsureUserBuiltinJobs creates or updates all PerUser builtin jobs for the given user.
// Call this once per active user on startup and when a new user is provisioned.
func (s *Service) EnsureUserBuiltinJobs(userID int64, agentID string) {
	builtinMu.Lock()
	jobs := append([]BuiltinJob(nil), builtinJobs...)
	builtinMu.Unlock()

	for _, j := range jobs {
		if !j.PerUser {
			continue
		}
		aid := j.AgentID
		if aid == "" {
			aid = agentID
		}
		if _, err := s.ensureJobForUser(j.Name, j.Message, j.Schedule, j.SessionMode, aid, userID); err != nil {
			s.log.Warn("failed to ensure per-user builtin job", "name", j.Name, "user_id", userID, "error", err)
		}
	}
}

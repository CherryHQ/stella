package scheduler

import "sync"

// BuiltinJob defines a job that is automatically seeded on scheduler startup.
type BuiltinJob struct {
	Name        string
	Message     string
	Schedule    Schedule
	SessionMode string
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

// EnsureBuiltinJobs creates or updates all registered builtin jobs.
func (s *Service) EnsureBuiltinJobs() {
	builtinMu.Lock()
	jobs := append([]BuiltinJob(nil), builtinJobs...)
	builtinMu.Unlock()

	for _, j := range jobs {
		if _, err := s.EnsureJob(j.Name, j.Message, j.Schedule, j.SessionMode); err != nil {
			s.log.Warn("failed to ensure builtin job", "name", j.Name, "error", err)
		}
	}
}

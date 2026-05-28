package scheduler

import (
	"fmt"
	"sync"
)

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

var (
	builtinMu   sync.Mutex
	builtinJobs []BuiltinJob
)

// RegisterBuiltin registers a builtin job spec. Call from init().
//
// init-time misconfiguration is a programming error caught instantly in
// tests, so this panics on a bad spec. For runtime registration (e.g. from
// gateway wiring after deps are constructed), use (*Service).RegisterBuiltin
// which returns an error instead.
func RegisterBuiltin(job BuiltinJob) {
	if err := validateBuiltin(job); err != nil {
		panic(fmt.Sprintf("scheduler.RegisterBuiltin: %v", err))
	}
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if job.ExecScope == "" {
		job.ExecScope = ExecScopeSystem
	}
	builtinJobs = append(builtinJobs, job)
}

// RegisterBuiltin registers a runtime builtin spec on this Service instance.
//
// Use this from gateway wiring when the spec needs dependencies that aren't
// available at package init() time (e.g. a Handler that closes over a
// configured dispatcher). Returns an error on a malformed spec so the
// gateway can fail startup cleanly rather than panic.
func (s *Service) RegisterBuiltin(job BuiltinJob) error {
	if err := validateBuiltin(job); err != nil {
		return err
	}
	if job.ExecScope == "" {
		job.ExecScope = ExecScopeSystem
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.Handler != nil {
		if s.builtinHandlers == nil {
			s.builtinHandlers = make(map[string]OnJobFunc)
		}
		s.builtinHandlers[job.Name] = job.Handler
	}
	s.runtimeBuiltins = append(s.runtimeBuiltins, job)
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

// EnsureBuiltinJobs creates or updates all builtin jobs for the given org.
// For ExecScopeAllUsers jobs the scheduler fans out to all active users at execution time.
func (s *Service) EnsureBuiltinJobs(orgID string) {
	builtinMu.Lock()
	jobs := append([]BuiltinJob(nil), builtinJobs...)
	builtinMu.Unlock()

	s.mu.Lock()
	runtimeJobs := append([]BuiltinJob(nil), s.runtimeBuiltins...)
	s.mu.Unlock()

	seen := make(map[string]struct{}, len(jobs)+len(runtimeJobs))
	for _, j := range jobs {
		seen[j.Name] = struct{}{}
		s.ensureOneBuiltin(j, orgID)
	}
	for _, j := range runtimeJobs {
		if _, dup := seen[j.Name]; dup {
			s.log.Warn("runtime builtin shadowed by package-registered job", "name", j.Name, "org_id", orgID)
			continue
		}
		seen[j.Name] = struct{}{}
		s.ensureOneBuiltin(j, orgID)
	}
}

func (s *Service) ensureOneBuiltin(j BuiltinJob, orgID string) {
	if _, err := s.EnsureJob(j.Name, j.Message, j.Schedule, j.SessionMode, j.AgentID, j.ExecScope, orgID); err != nil {
		s.log.Warn("failed to ensure builtin job", "name", j.Name, "org_id", orgID, "error", err)
	}
}

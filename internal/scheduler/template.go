package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agentrun"
)

// ErrAlreadySubscribed is returned by Subscribe when the user already has an
// active subscription instance for the given template key.
var ErrAlreadySubscribed = errors.New("already subscribed to template")

// ErrTemplateNotFound is returned by Subscribe when the requested template key
// is not registered.
var ErrTemplateNotFound = errors.New("template not found")

// ErrSubscriptionMessageReadOnly is returned by UpdateUserJob when the caller
// attempts to change the message on a subscription instance (job_key non-empty).
// The message is owned by the template registry, not the row.
var ErrSubscriptionMessageReadOnly = errors.New("subscription message is read-only")

// JobTemplate defines a platform-managed job recipe that users can subscribe
// to. Unlike a BuiltinJob the template itself is never scheduled directly;
// instead each subscriber gets an ordinary user-owned sched_job row whose
// job_key field names the template. The message (prompt) lives here and is
// resolved at fire time so prompt improvements propagate automatically to all
// existing subscribers on the next upgrade.
type JobTemplate struct {
	// Key is a stable, unique identifier used as job_key on subscription rows.
	Key string
	// Name is the human-readable display name. Must not collide with any
	// registered builtin name — the two registries share a reserved-name check.
	Name string
	// Description is shown to users when browsing available templates.
	Description string
	// Message is the prompt dispatched when a subscription fires.
	Message string
	// DefaultSchedule is the schedule pre-filled when creating a subscription.
	DefaultSchedule Schedule
	// SessionMode is the default session_mode for new subscriptions.
	SessionMode string
}

// RegisterTemplate registers a job template before the scheduler is started.
//
// Rules:
//   - Key must be unique across all templates.
//   - Name must not collide with any registered builtin name (and vice-versa —
//     RegisterBuiltin likewise rejects names that match a template key/name).
//   - Must be called BEFORE (*Service).Start.
func (s *Service) RegisterTemplate(tmpl JobTemplate) error {
	if tmpl.Key == "" {
		return fmt.Errorf("template: Key is required")
	}
	if tmpl.Name == "" {
		return fmt.Errorf("template %q: Name is required", tmpl.Key)
	}
	if tmpl.Message == "" {
		return fmt.Errorf("template %q: Message is required", tmpl.Key)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return fmt.Errorf("scheduler: RegisterTemplate(%q) called after Start", tmpl.Key)
	}
	if s.templates == nil {
		s.templates = make(map[string]JobTemplate)
	}

	// Mutual exclusion with builtins — checked in both registration orders.
	if _, dup := s.templates[tmpl.Key]; dup {
		return fmt.Errorf("scheduler: template key %q already registered", tmpl.Key)
	}
	if reservedByBuiltin(s.runtimeBuiltins, tmpl.Key) || reservedByBuiltin(s.runtimeBuiltins, tmpl.Name) {
		return fmt.Errorf("scheduler: template key/name %q conflicts with a registered builtin", tmpl.Key)
	}

	s.templates[tmpl.Key] = tmpl
	return nil
}

// Templates returns a snapshot of all registered job templates.
func (s *Service) Templates() []JobTemplate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobTemplate, 0, len(s.templates))
	for _, t := range s.templates {
		out = append(out, t)
	}
	return out
}

// ResolveTemplateMessage returns the message for the given template key.
// Returns ("", false) when the key is not registered.
func (s *Service) ResolveTemplateMessage(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.templates[key]
	if !ok {
		return "", false
	}
	return t.Message, true
}

// Subscribe creates a user-owned subscription instance for the given template.
// A user may have at most one subscription per template; the uniqueness check
// and the insert + River registration happen inside a single critical section
// because there is no DB unique constraint to fall back on.
//
// schedOverride, when non-zero, replaces the template's DefaultSchedule.
func (s *Service) Subscribe(ctx context.Context, userID, agentID, key string, schedOverride Schedule) (Job, error) {
	if userID == "" {
		return Job{}, fmt.Errorf("subscribe: userID is required")
	}
	if key == "" {
		return Job{}, fmt.Errorf("subscribe: template key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tmpl, ok := s.templates[key]
	if !ok {
		return Job{}, fmt.Errorf("subscribe: template %q not found: %w", key, ErrTemplateNotFound)
	}

	// Dedup: scan in-memory jobs map — it is the authoritative mirror of the
	// DB write path, so this check is correct under the held lock.
	for _, j := range s.jobs {
		if j.OwnerKind == JobOwnerUser && j.UserID == userID && j.JobKey == key {
			return Job{}, fmt.Errorf("subscribe: user %q already subscribed to template %q (job %s): %w", userID, key, j.ID, ErrAlreadySubscribed)
		}
	}

	sched := tmpl.DefaultSchedule
	if schedOverride.Cron != "" || schedOverride.Every != "" || schedOverride.At != "" {
		sched = schedOverride
	}
	if err := validateSchedule(sched); err != nil {
		return Job{}, fmt.Errorf("subscribe schedule: %w", err)
	}

	sessionMode := tmpl.SessionMode
	if sessionMode == "" {
		sessionMode = SessionReuse
	}

	now := time.Now().UTC()
	job := Job{
		ID:        uuid.New().String()[:8],
		OwnerKind: JobOwnerUser,
		ExecScope: ExecScopeUser,
		JobKey:    key,
		Name:      tmpl.Name,
		Schedule:  sched,
		// Message is intentionally left empty on the subscription row. The
		// prompt is resolved from the template at fire time so every subscriber
		// automatically benefits from future prompt improvements.
		Message:     "",
		SessionMode: sessionMode,
		Enabled:     true,
		AgentID:     agentID,
		UserID:      userID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// addJobLocked: schedule + persist + update in-memory map — all under the
	// lock that is already held. This is the only path that must be atomic to
	// prevent concurrent Subscribe calls for the same (user, key) from both
	// passing the dedup check above.
	if err := s.addJobLocked(ctx, job); err != nil {
		return Job{}, err
	}

	s.log.Info("subscription created", "job_id", job.ID, "user_id", userID, "template", key)
	return job, nil
}

// addJobLocked schedules, persists, and registers a pre-built Job struct.
// Caller MUST hold s.mu. On error the River registration is rolled back.
//
// This is the shared insert+schedule body extracted from addJobInternal so that
// Subscribe can call it while already holding the lock — avoiding a
// re-entrant lock acquisition.
func (s *Service) addJobLocked(ctx context.Context, job Job) error {
	// Commit the guarded source row before changing River. A stale AgentRun can
	// therefore never create even a transient live registration. Once this
	// transaction wins, later lease loss does not undo the authorized durable
	// decision; startup reconstruction can repair a process crash in the gap.
	if err := s.insertJob(ctx, job); err != nil {
		return fmt.Errorf("persist job: %w", err)
	}
	if err := agentrun.Check(ctx); err != nil {
		return err
	}
	if job.Enabled {
		if err := s.scheduleJob(job); err != nil {
			// Best-effort guarded compensation. If ownership was lost after the
			// insert committed, retain the source row for startup reconstruction.
			_ = s.deleteJob(ctx, job.ID)
			return fmt.Errorf("schedule job: %w", err)
		}
	}

	s.jobs[job.ID] = job
	return nil
}

// UpdateUserJob merges the provided field overrides into the existing job,
// persists the result, and reschedules the live River registration — all inside
// a single critical section.
//
// Subscription instances (job_key non-empty) may not have their message
// changed; that field is owned by the template registry.
func (s *Service) UpdateUserJob(ctx context.Context, id string, update JobUpdate) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("job %q not found", id)
	}
	oldJob := job
	if job.OwnerKind != JobOwnerUser {
		return Job{}, fmt.Errorf("job %q is not a user job", id)
	}

	// Subscription instances: the prompt belongs to the template, not the row.
	if job.JobKey != "" && update.Message != nil {
		return Job{}, fmt.Errorf("job %q is a template subscription; message cannot be changed: %w", id, ErrSubscriptionMessageReadOnly)
	}

	// Apply optional field overrides.
	if update.Name != nil {
		job.Name = *update.Name
	}
	if update.Message != nil {
		job.Message = *update.Message
	}
	if update.DispatchKind != nil {
		job.DispatchKind = *update.DispatchKind
	}
	if update.Payload != nil {
		job.Payload = clonePayload(update.Payload)
	}
	if job.DispatchKind == "" {
		job.DispatchKind = DispatchKindChat
	}
	if err := s.validateDispatch(ctx, job.DispatchKind, job.Message, job.OwnerKind, job.UserID, job.AgentID, job.Payload); err != nil {
		return Job{}, err
	}
	if update.Schedule != nil {
		if err := validateSchedule(*update.Schedule); err != nil {
			return Job{}, fmt.Errorf("invalid schedule: %w", err)
		}
		job.Schedule = *update.Schedule
	}
	if update.SessionMode != nil {
		if *update.SessionMode != SessionReuse && *update.SessionMode != SessionNew {
			return Job{}, fmt.Errorf("invalid session_mode %q", *update.SessionMode)
		}
		job.SessionMode = *update.SessionMode
	}
	if update.Enabled != nil {
		job.Enabled = *update.Enabled
	}
	if update.AgentID != nil {
		job.AgentID = *update.AgentID
	}
	job.UpdatedAt = time.Now().UTC()

	// The guarded source update linearizes before any River change. This avoids
	// arming model-derived work from a stale executor; a crash after commit is
	// repaired from the durable row at startup.
	oldRef, hadOld := s.refs[id]
	if err := s.updateJob(ctx, job); err != nil {
		return Job{}, fmt.Errorf("persist job update: %w", err)
	}
	if err := agentrun.Check(ctx); err != nil {
		return Job{}, err
	}
	if job.Enabled {
		if err := s.scheduleJob(job); err != nil {
			_ = s.updateJob(ctx, oldJob)
			return Job{}, fmt.Errorf("reschedule job: %w", err)
		}
	}

	if hadOld {
		// Tear the old registration down only when it differs from the new one.
		// A one-time same-timestamp update dedups to the SAME River job
		// (newRef == oldRef via UniqueOpts ByArgs); cancelling it would kill the
		// only pending fire. When the job is disabled, scheduleJob was skipped so
		// s.refs[id] still holds oldRef — tear it down. Mirrors the rollback path.
		if newRef, ok := s.refs[id]; !job.Enabled || !ok || newRef != oldRef {
			s.unscheduleRef(oldRef)
		}
	}
	if !job.Enabled {
		delete(s.refs, id)
	}
	s.jobs[id] = job

	s.log.Info("user job updated", "id", id, "enabled", job.Enabled)
	return job, nil
}

// JobUpdate carries optional field overrides for UpdateUserJob. A nil pointer
// means "leave unchanged".
type JobUpdate struct {
	Name         *string
	Message      *string
	Schedule     *Schedule
	SessionMode  *string
	Enabled      *bool
	AgentID      *string
	DispatchKind *string
	Payload      map[string]any
}

// reservedByBuiltin reports whether name matches any key in the builtin map.
// Used for mutual-exclusion checks in both RegisterBuiltin and RegisterTemplate.
func reservedByBuiltin(builtins map[string]BuiltinJob, name string) bool {
	_, ok := builtins[name]
	return ok
}

// reservedName reports whether name is reserved by either the runtime builtin
// registry or the template registry. Caller must hold s.mu.
func (s *Service) reservedName(name string) bool {
	if _, ok := s.runtimeBuiltins[name]; ok {
		return true
	}
	for _, t := range s.templates {
		if t.Key == name || t.Name == name {
			return true
		}
	}
	return false
}

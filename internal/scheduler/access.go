package scheduler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Access captures one validated authority for a scheduler use case. Scheduler
// owns the direct rules for durable jobs; transports and tools pass a trusted
// Authority, never a scoped query. Platform jobs stay hidden from user-facing
// operations before these rules run.
type Access struct {
	svc       *Service
	authority authz.Authority
	userID    string
	// agentID confines a delegated AgentActor to its exact executor. It is empty
	// for a plain user actor.
	agentID string
}

// Begin captures validated authority for one scheduler use case.
func (s *Service) Begin(_ context.Context, authority authz.Authority) (*Access, error) {
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	if s.agents == nil {
		return nil, fmt.Errorf("scheduler authorization unavailable: agent access not configured")
	}
	return s.access(authority), nil
}

func (s *Service) access(authority authz.Authority) *Access {
	executor := ""
	if authority.Kind() == authz.ActorAgent {
		executor = string(authority.AgentID())
	}
	return &Access{svc: s, authority: authority, userID: string(authority.UserID()), agentID: executor}
}

// AuthorizeDurableFire reconstructs the sole authority shape permitted by a
// persisted job and rechecks the durable job and its current executor on every
// fire. System handler jobs may have no executor; user jobs may not. Plugin jobs
// never enter this path.
func (s *Service) AuthorizeDurableFire(ctx context.Context, job Job) (authz.Authority, error) {
	if s.agents == nil {
		return authz.Authority{}, fmt.Errorf("scheduler authorization unavailable: agent access not configured")
	}
	authority, err := durableFireAuthority(job)
	if err != nil {
		return authz.Authority{}, err
	}
	acc := s.access(authority)
	if err := acc.authorize(authz.ActionExecute, job); err != nil {
		return authz.Authority{}, err
	}
	if job.AgentID != "" {
		if err := acc.authorizeAgentAction(ctx, job.AgentID, authz.ActionExecute); err != nil {
			return authz.Authority{}, err
		}
	} else if job.OwnerKind != JobOwnerSystem {
		return authz.Authority{}, fmt.Errorf("scheduler: durable job %s has no agent to authorize", job.ID)
	}
	return authority, nil
}

// durableFireAuthority reconstructs the sole authority shape a persisted job may
// fire under, from its durable owner. A user job runs as its owner+executor
// worker; a system job runs under the named system authority. Plugin-owned jobs carry
// no user/agent authority and never reach the agent-dispatch fire path.
func durableFireAuthority(job Job) (authz.Authority, error) {
	switch job.OwnerKind {
	case JobOwnerUser:
		if job.UserID == "" || job.AgentID == "" {
			return authz.Authority{}, fmt.Errorf("scheduler: user job %s has no persisted owner or executor", job.ID)
		}
		return agentaccess.WorkerAgentAuthority(job.UserID, job.AgentID)
	case JobOwnerSystem:
		return agentaccess.SystemAgentAuthority("scheduler")
	default:
		return authz.Authority{}, fmt.Errorf("scheduler: unsupported durable owner kind %q for job %s", job.OwnerKind, job.ID)
	}
}

// CreateJob creates an enabled user-owned chat job.
func (a *Access) CreateJob(ctx context.Context, name, message string, sched Schedule, sessionMode, agentID, idempotencyKey string) (Job, error) {
	return a.createJob(ctx, name, message, sched, sessionMode, agentID, idempotencyKey, true)
}

// CreateJobWithEnabled creates a user-owned chat job with an explicit enabled bit.
func (a *Access) CreateJobWithEnabled(ctx context.Context, name, message string, sched Schedule, sessionMode, agentID, idempotencyKey string, enabled bool) (Job, error) {
	return a.createJob(ctx, name, message, sched, sessionMode, agentID, idempotencyKey, enabled)
}

func (a *Access) createJob(ctx context.Context, name, message string, sched Schedule, sessionMode, agentID, idempotencyKey string, enabled bool) (Job, error) {
	replay := func() (Job, error) {
		row, err := a.svc.q.GetSchedulerJobByIdempotencyKey(ctx, sqlc.GetSchedulerJobByIdempotencyKeyParams{UserID: pgnull.Text(a.userID), IdempotencyKey: pgnull.Text(idempotencyKey)})
		if err != nil {
			return Job{}, err
		}
		// Replay authorization is against the durable row and its durable target,
		// never a new request's agent field.
		existing, err := a.loadAndAuthorize(ctx, "", row.ID, authz.ActionRead)
		if err != nil {
			return Job{}, err
		}
		if !enabled {
			return a.SetJobEnabled(ctx, existing.AgentID, existing.ID, false)
		}
		return existing, nil
	}
	if idempotencyKey != "" {
		if existing, err := replay(); err == nil {
			return existing, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Job{}, err
		}
	}
	if err := a.authorizeCreate(ctx, agentID); err != nil {
		return Job{}, err
	}
	created, err := a.svc.addJobInternal(addJobSpec{Name: name, Message: message, Schedule: sched, SessionMode: sessionMode, AgentID: agentID, UserID: a.userID, OwnerKind: JobOwnerUser, ExecScope: ExecScopeUser, DispatchKind: DispatchKindChat, IdempotencyKey: idempotencyKey, Enabled: enabled})
	if err == nil || idempotencyKey == "" || !isSchedulerIdempotencyConflict(err) {
		return created, err
	}
	return replay()
}

// CreateWorkflowJob creates a user-owned workflow-dispatch job. Workflow owns
// its durable target decision and instantiation; Scheduler only binds the
// caller's user/executor and dispatches the persisted job at fire time.
func (a *Access) CreateWorkflowJob(ctx context.Context, name string, sched Schedule, sessionMode, agentID, workflowID string, inputs map[string]string, allowReplan bool) (Job, error) {
	if err := a.authorizeCreate(ctx, agentID); err != nil {
		return Job{}, err
	}
	runner := a.svc.workflowRunnerRef()
	if runner == nil {
		return Job{}, fmt.Errorf("workflow scheduler dispatch is not configured")
	}
	if err := runner.AuthorizeWorkflow(ctx, a.authority, workflowID, authz.ActionExecute); err != nil {
		return Job{}, err
	}
	return a.svc.AddWorkflowJobWithOwner(ctx, name, sched, sessionMode, agentID, a.userID, workflowID, inputs, allowReplan)
}

// Subscribe creates a user-owned template-subscription job.
func (a *Access) Subscribe(ctx context.Context, agentID, key string, schedOverride Schedule) (Job, error) {
	if err := a.authorizeCreate(ctx, agentID); err != nil {
		return Job{}, err
	}
	return a.svc.Subscribe(ctx, a.userID, agentID, key, schedOverride)
}

// ListJobs lists the caller's user-owned jobs for one agent. Query scoping stays
// owner-bound even for admins; every returned durable row also passes Scheduler's
// direct read rule.
func (a *Access) ListJobs(ctx context.Context, agentID string) ([]Job, error) {
	if a.userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	if a.agentID != "" && a.agentID != agentID {
		return nil, authz.ErrForbidden
	}
	if err := a.authorize(authz.ActionList, Job{}); err != nil {
		return nil, err
	}
	if agentID != "" {
		if err := a.authorizeAgent(ctx, agentID); err != nil {
			return nil, err
		}
	}
	rows, err := a.svc.q.ListSchedulerJobByOwner(ctx, sqlc.ListSchedulerJobByOwnerParams{
		AgentID: pgnull.Text(agentID),
		UserID:  pgnull.Text(a.userID),
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(rows))
	for _, row := range rows {
		job := dbRowToJob(row)
		if a.allowed(authz.ActionRead, job) {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// GetJob reads one user-owned job.
func (a *Access) GetJob(ctx context.Context, agentID, jobID string) (Job, error) {
	return a.loadAndAuthorize(ctx, agentID, jobID, authz.ActionRead)
}

// UpdateJob mutates a user-owned job after a write decision.
func (a *Access) UpdateJob(ctx context.Context, agentID, jobID string, update JobUpdate) (Job, error) {
	if _, err := a.loadAndAuthorize(ctx, agentID, jobID, authz.ActionWrite); err != nil {
		return Job{}, err
	}
	return a.svc.UpdateUserJob(ctx, jobID, update)
}

// DeleteJob removes a user-owned job after a delete decision.
func (a *Access) DeleteJob(ctx context.Context, agentID, jobID string) error {
	if _, err := a.loadAndAuthorize(ctx, agentID, jobID, authz.ActionDelete); err != nil {
		return err
	}
	return a.svc.RemoveJob(jobID)
}

// SetJobEnabled toggles a user-owned job after a write decision.
func (a *Access) SetJobEnabled(ctx context.Context, agentID, jobID string, enabled bool) (Job, error) {
	if _, err := a.loadAndAuthorize(ctx, agentID, jobID, authz.ActionWrite); err != nil {
		return Job{}, err
	}
	return a.svc.UpdateUserJob(ctx, jobID, JobUpdate{Enabled: &enabled})
}

// RunJobNow triggers an immediate (best-effort, non-durable) run after an execute
// decision.
func (a *Access) RunJobNow(ctx context.Context, agentID, jobID string) (string, error) {
	if _, err := a.loadAndAuthorize(ctx, agentID, jobID, authz.ActionExecute); err != nil {
		return "", err
	}
	return a.svc.RunJobNow(ctx, jobID)
}

// ListRuns authorizes reading a job, then returns up to `fetch` run records
// (caller passes pageSize+1 for cursor detection) enriched with the session's
// executing agent.
func (a *Access) ListRuns(ctx context.Context, agentID, jobID string, fetch, offset int) ([]JobRun, error) {
	if _, err := a.loadAndAuthorize(ctx, agentID, jobID, authz.ActionRead); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListSchedJobRuns(ctx, sqlc.ListSchedJobRunsParams{
		JobID:  jobID,
		UserID: pgnull.Text(""),
		Limit:  int32(fetch),
		Offset: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	out := make([]JobRun, 0, len(rows))
	for _, row := range rows {
		run := JobRun{
			ID: row.ID, JobID: row.JobID, SessionID: row.SessionID, Status: row.Status,
			Error: row.Error, Output: row.Output, StartedAt: row.StartedAt.UTC(),
		}
		if row.UserID.Valid {
			run.UserID = row.UserID.String
		}
		if row.RootGoalID.Valid {
			run.RootGoalID = row.RootGoalID.String
		}
		if row.FinishedAt.Valid {
			f := row.FinishedAt.Time.UTC()
			run.FinishedAt = &f
		}
		if run.SessionID != "" {
			agent, err := a.svc.q.GetConversationAgentBySessionID(ctx, sqlc.GetConversationAgentBySessionIDParams{
				SessionID: run.SessionID,
				UserID:    row.UserID,
			})
			switch {
			case err != nil:
				run.SessionID = ""
			case agent.Valid:
				run.SessionAgentID = agent.String
			}
		}
		out = append(out, run)
	}
	return out, nil
}

// authorizeCreate binds a new job to this Authority's durable user/executor,
// then asks Agent to validate the current requested target.
func (a *Access) authorizeCreate(ctx context.Context, agentID string) error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	if a.agentID != "" && a.agentID != agentID {
		return authz.ErrForbidden
	}
	if err := a.authorize(authz.ActionCreate, Job{UserID: a.userID, AgentID: agentID, OwnerKind: JobOwnerUser}); err != nil {
		return err
	}
	return a.authorizeAgent(ctx, agentID)
}

// loadAndAuthorize loads one job, hides system/plugin/foreign-agent jobs, gates
// the job's agent (read), and then applies Scheduler's direct resource rule.
func (a *Access) loadAndAuthorize(ctx context.Context, agentID, jobID string, action authz.Action) (Job, error) {
	if a.userID == "" {
		return Job{}, authz.ErrUnauthenticated
	}
	row, err := a.svc.q.GetSchedulerJob(ctx, jobID)
	if err != nil {
		return Job{}, authz.ErrNotFound
	}
	job := dbRowToJob(row)
	// System and plugin jobs are never reachable through a user/agent use case.
	if job.OwnerKind == JobOwnerPlugin || job.OwnerKind == JobOwnerSystem {
		return Job{}, authz.ErrNotFound
	}
	if agentID != "" && job.AgentID != agentID {
		return Job{}, authz.ErrNotFound
	}
	if err := a.authorizeAgent(ctx, job.AgentID); err != nil {
		return Job{}, err
	}
	if err := a.authorize(action, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// authorizeAgent requires Agent-domain read access to the job's agent.
func (a *Access) authorizeAgent(ctx context.Context, agentID string) error {
	return a.authorizeAgentAction(ctx, agentID, authz.ActionRead)
}

// authorizeAgentAction asks the Agent domain directly. Read gates user-facing
// use cases; durable fire requires Execute because it actually runs the agent.
func (a *Access) authorizeAgentAction(ctx context.Context, agentID string, action authz.Action) error {
	if agentID == "" {
		return authz.ErrNotFound
	}
	err := a.svc.agents.Authorize(ctx, a.authority, agentID, action)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentaccess.ErrNotFound), errors.Is(err, agentaccess.ErrForbidden):
		return authz.ErrNotFound
	default:
		return err
	}
}

// SubscribedTemplates returns the caller's own template-subscription jobs. A
// delegated agent sees only its exact executor's subscriptions.
func (a *Access) SubscribedTemplates(ctx context.Context) ([]Job, error) {
	if a.userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	if err := a.authorize(authz.ActionList, Job{}); err != nil {
		return nil, err
	}
	out := make([]Job, 0)
	for _, job := range a.svc.ListJobs() {
		if job.OwnerKind != JobOwnerUser || job.UserID != a.userID || job.JobKey == "" {
			continue
		}
		if a.agentID != "" && job.AgentID != a.agentID {
			continue
		}
		if job.AgentID != "" {
			if err := a.authorizeAgent(ctx, job.AgentID); err != nil {
				if errors.Is(err, authz.ErrNotFound) || errors.Is(err, authz.ErrForbidden) {
					continue
				}
				return nil, err
			}
		}
		if a.allowed(authz.ActionRead, job) {
			out = append(out, job)
		}
	}
	return out, nil
}

// authorize preserves Scheduler's resolved-but-denied contract: platform and
// wrong-path jobs are hidden before this point, while a loaded foreign job is
// forbidden.
func (a *Access) authorize(action authz.Action, job Job) error {
	if a.allowed(action, job) {
		return nil
	}
	return authz.ErrForbidden
}

func (a *Access) allowed(action authz.Action, job Job) bool {
	if !action.Valid() || !isSchedulerAction(action) {
		return false
	}
	if a.authority.IsAdmin() {
		return true
	}
	switch a.authority.Kind() {
	case authz.ActorUser:
		if action == authz.ActionList {
			return true
		}
		return job.OwnerKind == JobOwnerUser && a.userID != "" && a.userID == job.UserID
	case authz.ActorAgent:
		if action == authz.ActionList {
			return true
		}
		return job.OwnerKind == JobOwnerUser && a.userID != "" && a.userID == job.UserID && a.agentID != "" && a.agentID == job.AgentID
	case authz.ActorSystem:
		return job.OwnerKind == JobOwnerSystem && (action == authz.ActionRead || action == authz.ActionExecute)
	default:
		return false
	}
}

func isSchedulerAction(action authz.Action) bool {
	switch action {
	case authz.ActionList, authz.ActionCreate, authz.ActionRead, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute:
		return true
	default:
		return false
	}
}

func isSchedulerIdempotencyConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_sched_job_idem"
}

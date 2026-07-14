package scheduler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Access is one scheduler use case bound to exactly one Authorizer evaluation.
// The scheduler Service is the sole policy-enforcement point for job resources:
// transports and tools pass a trusted authz.Authority and never a scoped query.
// A denial is opaque (authz.ErrNotFound) so a foreign, system, or plugin job is
// indistinguishable from a missing one.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	authority authz.Authority
	userID    string
	// scopeAgentID is the executor confinement: empty for a plain user actor,
	// the bound agent for a delegated AgentActor (which may only touch its own).
	scopeAgentID string
}

// Begin opens exactly one evaluation for one scheduler use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s.authz == nil || s.agents == nil {
		return nil, fmt.Errorf("scheduler authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("scheduler authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, authority: authority, userID: string(actor.UserID()), scopeAgentID: agentID}, nil
}

// AuthorizeDurableFire re-authorizes a persisted job at fire time. It
// reconstructs the sole permitted authority from the job's durable owner (never a
// request), opens one evaluation, and decides — under that single revision — both
// the job's ActionExecute against its real facts and its bound agent's
// ActionExecute. It returns the reconstructed authority so the caller runs the
// turn under the same identity. Fails closed when authorization is unavailable,
// the job has no agent, the owner data is missing, or the owner kind is not a
// durable agent-dispatch owner. Plugin jobs never dispatch through here; passing
// one is rejected explicitly.
func (s *Service) AuthorizeDurableFire(ctx context.Context, job Job) (authz.Authority, error) {
	acc, err := s.beginDurableFire(ctx, job)
	if err != nil {
		return authz.Authority{}, err
	}
	return acc.authority, nil
}

// beginDurableFire returns the scheduler Access whose single evaluation protects
// the entire dispatch. System handler jobs may have no agent, but still require
// their Scheduler Execute decision. User and agent-dispatch system jobs also
// decide the actual bound Agent Execute under that same revision.
func (s *Service) beginDurableFire(ctx context.Context, job Job) (*Access, error) {
	if s.authz == nil || s.agents == nil {
		return nil, fmt.Errorf("scheduler authorization unavailable: authorizer not configured")
	}
	authority, err := durableFireAuthority(job)
	if err != nil {
		return nil, err
	}
	acc, err := s.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	if err := acc.decideJob(authz.ActionExecute, job); err != nil {
		return nil, err
	}
	if job.AgentID != "" {
		if err := acc.authorizeAgentAction(ctx, job.AgentID, authz.ActionExecute); err != nil {
			return nil, err
		}
	} else if job.OwnerKind != JobOwnerSystem {
		return nil, fmt.Errorf("scheduler: durable job %s has no agent to authorize", job.ID)
	}
	return acc, nil
}

// durableFireAuthority reconstructs the sole authority shape a persisted job may
// fire under, from its durable owner. A user job runs as its owner+executor
// worker; a system job runs under the named system grant. Plugin-owned jobs carry
// no user/agent authority and never reach the agent-dispatch fire path.
func durableFireAuthority(job Job) (authz.Authority, error) {
	switch job.OwnerKind {
	case JobOwnerUser:
		if job.UserID == "" {
			return authz.Authority{}, fmt.Errorf("scheduler: user job %s has no persisted owner", job.ID)
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
		existing, err := a.loadAndAuthorize(ctx, agentID, row.ID, authz.ActionRead)
		if err != nil {
			return Job{}, err
		}
		if !enabled {
			return a.SetJobEnabled(ctx, agentID, existing.ID, false)
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
	if err := a.authorizeCreate(ctx, agentID, enabled); err != nil {
		return Job{}, err
	}
	created, err := a.svc.addJobInternal(addJobSpec{Name: name, Message: message, Schedule: sched, SessionMode: sessionMode, AgentID: agentID, UserID: a.userID, OwnerKind: JobOwnerUser, ExecScope: ExecScopeUser, DispatchKind: DispatchKindChat, IdempotencyKey: idempotencyKey, Enabled: enabled})
	if err == nil || idempotencyKey == "" || !isSchedulerIdempotencyConflict(err) {
		return created, err
	}
	return replay()
}

// CreateWorkflowJob creates a user-owned workflow-dispatch job. The dispatch
// target workflow is decided through its own policy inside this scheduler
// evaluation (not a raw scoped Get): a scheduled job instantiates the workflow at
// fire time, so the caller must be able to execute it now. The actual fire
// re-authorizes again from the persisted job.
func (a *Access) CreateWorkflowJob(ctx context.Context, name string, sched Schedule, sessionMode, agentID, workflowID string, inputs map[string]string, allowReplan bool) (Job, error) {
	if err := a.authorizeCreate(ctx, agentID, true); err != nil {
		return Job{}, err
	}
	runner := a.svc.workflowRunnerRef()
	if runner == nil {
		return Job{}, fmt.Errorf("workflow scheduler dispatch is not configured")
	}
	if err := runner.AuthorizeWorkflowWithin(ctx, a.eval, a.authority, workflowID, authz.ActionExecute); err != nil {
		return Job{}, err
	}
	return a.svc.AddWorkflowJobWithOwner(ctx, name, sched, sessionMode, agentID, a.userID, workflowID, inputs, allowReplan)
}

// Subscribe creates a user-owned template-subscription job.
func (a *Access) Subscribe(ctx context.Context, agentID, key string, schedOverride Schedule) (Job, error) {
	if err := a.authorizeCreate(ctx, agentID, true); err != nil {
		return Job{}, err
	}
	return a.svc.Subscribe(ctx, a.userID, agentID, key, schedOverride)
}

// ListJobs lists the caller's user-owned jobs for one agent and filters every row
// through the same evaluation, so collection and per-row visibility cannot drift.
func (a *Access) ListJobs(ctx context.Context, agentID string) ([]Job, error) {
	if a.userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	// A delegated agent is confined to its own agent; it may not list another's.
	if a.scopeAgentID != "" && a.scopeAgentID != agentID {
		return nil, authz.ErrForbidden
	}
	resolvedAgentID := agentID
	req, err := policy.SchedulerListRequest()
	if err != nil {
		return nil, authz.ErrForbidden
	}
	if err := a.decideReq(req); err != nil {
		return nil, err
	}
	if resolvedAgentID != "" {
		if err := a.authorizeAgent(ctx, resolvedAgentID); err != nil {
			return nil, err
		}
	}
	rows, err := a.svc.q.ListSchedulerJobByOwner(ctx, sqlc.ListSchedulerJobByOwnerParams{
		AgentID: pgnull.Text(resolvedAgentID),
		UserID:  pgnull.Text(a.userID),
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(rows))
	for _, row := range rows {
		job := dbRowToJob(row)
		if err := a.decideJob(authz.ActionRead, job); err == nil {
			jobs = append(jobs, job)
		} else if !errors.Is(err, authz.ErrNotFound) && !errors.Is(err, authz.ErrForbidden) {
			return nil, err
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

// authorizeCreate gates a new user-owned job: authenticated, agent-confined for a
// delegated actor, agent-read on the target agent, and a scheduler create policy.
func (a *Access) authorizeCreate(ctx context.Context, agentID string, enabled bool) error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	if a.scopeAgentID != "" && a.scopeAgentID != agentID {
		return authz.ErrForbidden
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	facts := policy.SchedulerFacts{
		Owner: a.userID, Agent: agentID, Kind: JobOwnerUser, State: state, IsOwner: true,
		IsExecutor: a.scopeAgentID != "" && a.scopeAgentID == agentID,
	}
	req, err := policy.SchedulerRequest(authz.ActionCreate, a.userID, a.userID, facts)
	if err != nil {
		return authz.ErrForbidden
	}
	if err := a.decideReq(req); err != nil {
		return err
	}
	return a.authorizeAgent(ctx, agentID)
}

// loadAndAuthorize loads one job, hides system/plugin/foreign-agent jobs, gates
// the job's agent (read), and decides the resource action — all in this Access's
// single evaluation.
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
	if err := a.decideJob(action, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// authorizeAgent requires Agent-domain read access to the job's agent.
func (a *Access) authorizeAgent(ctx context.Context, agentID string) error {
	return a.authorizeAgentAction(ctx, agentID, authz.ActionRead)
}

// authorizeAgentAction asks the Agent domain directly. Read gates the common use
// cases; the durable fire path additionally requires ActionExecute on the bound
// agent, since a scheduled turn actually executes it.
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

// SubscribedTemplates returns the caller's own template-subscription jobs (those
// carrying a template job_key), each authorized through the same evaluation: the
// collection list decision once, then a per-row read plus the row's agent gate.
// It is how the template catalog resolves subscription metadata without a raw
// ListJobs bypass. A delegated agent sees only its own agent's subscriptions.
func (a *Access) SubscribedTemplates(ctx context.Context) ([]Job, error) {
	if a.userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	req, err := policy.SchedulerListRequest()
	if err != nil {
		return nil, authz.ErrForbidden
	}
	if err := a.decideReq(req); err != nil {
		return nil, err
	}
	out := make([]Job, 0)
	for _, job := range a.svc.ListJobs() {
		if job.OwnerKind != JobOwnerUser || job.UserID != a.userID || job.JobKey == "" {
			continue
		}
		if a.scopeAgentID != "" && job.AgentID != a.scopeAgentID {
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
		if err := a.decideJob(authz.ActionRead, job); err == nil {
			out = append(out, job)
		} else if !errors.Is(err, authz.ErrNotFound) && !errors.Is(err, authz.ErrForbidden) {
			return nil, err
		}
	}
	return out, nil
}

func (a *Access) decideJob(action authz.Action, job Job) error {
	facts := policy.SchedulerFacts{
		Owner: job.UserID, Agent: job.AgentID, Kind: job.OwnerKind, State: jobState(job),
		IsOwner:    a.userID != "" && a.userID == job.UserID,
		IsExecutor: a.scopeAgentID != "" && a.scopeAgentID == job.AgentID,
	}
	req, err := policy.SchedulerRequest(action, job.ID, job.UserID, facts)
	if err != nil {
		return authz.ErrForbidden
	}
	return a.decideReq(req)
}

func isSchedulerIdempotencyConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_sched_job_idem"
}

func (a *Access) decideReq(req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("scheduler decide: %w", err)
	}
	if !dec.Allowed() {
		// A resolved-but-denied job is forbidden (403), preserving the pre-cutover
		// contract. System/plugin/foreign-agent jobs are hidden earlier as 404.
		return authz.ErrForbidden
	}
	return nil
}

func jobState(job Job) string {
	if job.Enabled {
		return "enabled"
	}
	return "disabled"
}

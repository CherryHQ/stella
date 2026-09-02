package goal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// CreatedGoal is the minimal result returned to trusted cross-domain writers.
type CreatedGoal struct {
	ID string
}

// Access captures one validated authority for a Goal use case. The goal Service
// owns the direct rules; transports and the agent tool pass trusted authority,
// never a scoped query handle. A goal denial is opaque (authz.ErrNotFound) so a
// foreign goal cannot be told from a missing one.
type Access struct {
	svc       *Service
	authority authz.Authority
	userID    string
	// agentID is the executor confinement: empty for a plain user actor, the
	// bound agent for a delegated AgentActor.
	agentID string
}

// Begin captures validated authority for one Goal use case.
func (s *Service) Begin(_ context.Context, authority authz.Authority) (*Access, error) {
	if s.agents == nil {
		return nil, fmt.Errorf("goal authorization unavailable: agent access not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	executor := ""
	if authority.Kind() == authz.ActorAgent {
		executor = string(authority.AgentID())
	}
	return &Access{svc: s, authority: authority, userID: string(authority.UserID()), agentID: executor}, nil
}

// workerAuthorizer is the durable-attempt authorization boundary. It needs only
// the Agent domain port: on every dequeue it reconstructs authority from the
// durable goal owner and actual persisted executor, then checks both before a
// model turn. Missing owner, executor, or Agent PEP fails closed.
type workerAuthorizer struct {
	agents *agentaccess.Service
}

func newWorkerAuthorizer(agents *agentaccess.Service) *workerAuthorizer {
	return &workerAuthorizer{agents: agents}
}

func (wa *workerAuthorizer) authorize(ctx context.Context, goal sqlc.AgentGoal, executorAgentID string) error {
	if wa == nil || wa.agents == nil {
		return fmt.Errorf("goal worker authorization unavailable: agent access not configured")
	}
	authority, err := agentaccess.WorkerAgentAuthority(goal.UserID, executorAgentID)
	if err != nil {
		return fmt.Errorf("goal worker authority invalid: %w", err)
	}
	// The attempt's executor is durable authority-bearing state. It may be a
	// dispatch override, so it must be checked directly rather than replaced by
	// the goal's default AgentID.
	if goal.UserID == "" || executorAgentID == "" || string(authority.UserID()) != goal.UserID || string(authority.AgentID()) != executorAgentID {
		return authz.ErrNotFound
	}
	return authorizeAgentWithin(ctx, wa.agents, authority, executorAgentID)
}

// GoalFilter narrows a root-goal list. The zero value lists active
// (non-archived) roots across all agents; populated fields AND together.
// Terminal is tri-state: nil = both, false = active only, true = history only.
type GoalFilter struct {
	AgentID    string
	Lifecycle  string
	ProjectID  string
	WorkflowID string
	Terminal   *bool
	Q          string
	Archived   bool
}

func (f GoalFilter) terminalArg() pgtype.Bool {
	if f.Terminal == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *f.Terminal, Valid: true}
}

// Get resolves a goal and authorizes reading it.
func (a *Access) Get(ctx context.Context, id string) (Goal, error) {
	d, err := a.getRow(ctx, id)
	if err != nil {
		return Goal{}, err
	}
	return goalFromRow(d), nil
}

// getRow is the private read path: it resolves and read-authorizes a goal,
// returning the raw persistence row for internal consumers that still work on
// sqlc (readiness computation). The public Get converts it to a domain value.
func (a *Access) getRow(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	d, err := a.load(ctx, id)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.decide(authz.ActionRead, d); err != nil {
		return sqlc.AgentGoal{}, err
	}
	return d, nil
}

// Use resolves a goal, authorizes a state change on it, and confirms the caller
// may still execute its persisted agent — the pre-cutover loadGoalForUse gate.
func (a *Access) Use(ctx context.Context, id string) (Goal, error) {
	d, err := a.useRow(ctx, id)
	if err != nil {
		return Goal{}, err
	}
	return goalFromRow(d), nil
}

// useRow is the private execute-authorization path returning the raw row, so the
// internal lifecycle actions (Cancel/Archive/Abandon) can authorize once and keep
// working from the durable row.
func (a *Access) useRow(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	d, err := a.load(ctx, id)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.decide(authz.ActionExecute, d); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.authorizeAgent(ctx, d.AgentID); err != nil {
		return sqlc.AgentGoal{}, err
	}
	return d, nil
}

// CreateGoal authorizes creating a goal (owner + agent execute) and mints it.
func (a *Access) CreateGoal(ctx context.Context, in CreateInput) (Goal, error) {
	if a.userID == "" {
		return Goal{}, authz.ErrUnauthenticated
	}
	if a.agentID != "" && a.agentID != in.AgentID {
		return Goal{}, authz.ErrForbidden
	}
	in.UserID = a.userID
	// Idempotency replay is authorized against the existing row's durable facts,
	// never the requested route. The same reload path also handles a concurrent
	// creator winning the unique key between this lookup and the insert.
	replay := func() (sqlc.AgentGoal, error) {
		existing, err := a.svc.q.GetGoalByIdempotencyKey(ctx, sqlc.GetGoalByIdempotencyKeyParams{UserID: a.userID, IdempotencyKey: pgnull.Text(in.IdempotencyKey)})
		if err != nil {
			return sqlc.AgentGoal{}, err
		}
		if err := a.decide(authz.ActionRead, existing); err != nil {
			return sqlc.AgentGoal{}, err
		}
		if err := a.authorizeAgent(ctx, existing.AgentID); err != nil {
			return sqlc.AgentGoal{}, err
		}
		return existing, nil
	}
	if in.IdempotencyKey != "" {
		if existing, err := replay(); err == nil {
			return goalFromRow(existing), nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Goal{}, err
		}
	}
	if err := a.authorize(authz.ActionCreate, sqlc.AgentGoal{UserID: a.userID, AgentID: in.AgentID}); err != nil {
		return Goal{}, err
	}
	if err := a.authorizeAgent(ctx, in.AgentID); err != nil {
		return Goal{}, err
	}
	created, err := a.svc.goal.CreateRoot(ctx, in)
	if err == nil || in.IdempotencyKey == "" || !isGoalIdempotencyConflict(err) {
		if err != nil {
			return Goal{}, err
		}
		return goalFromRow(created), nil
	}
	replayed, err := replay()
	if err != nil {
		return Goal{}, err
	}
	return goalFromRow(replayed), nil
}

func isGoalIdempotencyConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_agent_goal_idem"
}

// Cancel authorizes a state change on a goal and cancels it (cascade handled by
// the transition). Cancellation is an execute-authorized lifecycle action, so it
// goes through Use (goal-execute + agent-execute) rather than a bare read.
func (a *Access) Cancel(ctx context.Context, id, reason string) error {
	d, err := a.Use(ctx, id)
	if err != nil {
		return err
	}
	return a.svc.goal.Cancel(ctx, id, reason, UserActor(d.UserID))
}

// Archive authorizes a state change on a goal and archives it (audit-safe delete).
func (a *Access) Archive(ctx context.Context, id string) error {
	if _, err := a.Use(ctx, id); err != nil {
		return err
	}
	return a.svc.goal.Archive(ctx, id)
}

// Unarchive authorizes a state change on a goal and restores it to default lists.
func (a *Access) Unarchive(ctx context.Context, id string) error {
	if _, err := a.Use(ctx, id); err != nil {
		return err
	}
	return a.svc.goal.Unarchive(ctx, id)
}

// Abandon authorizes a state change on a goal and records the human give-up on a
// budget-exhausted block.
func (a *Access) Abandon(ctx context.Context, id, reason string) error {
	d, err := a.Use(ctx, id)
	if err != nil {
		return err
	}
	return a.svc.goal.Abandon(ctx, id, reason, UserActor(d.UserID))
}

// AddEdge authorizes mutation of the downstream and reading the caller-supplied
// upstream goal, then inserts the dependency edge (cycle-checked). Referencing an
// upstream requires visibility of its output, not execute authority over its
// agent; requiring Execute on both resources would reject valid cross-agent
// dependencies without adding protection.
func (a *Access) AddEdge(ctx context.Context, downstreamID, upstreamID, kind, onFailure string) (Edge, error) {
	if _, err := a.useRow(ctx, downstreamID); err != nil {
		return Edge{}, err
	}
	if _, err := a.getRow(ctx, upstreamID); err != nil {
		return Edge{}, err
	}
	e, err := a.svc.goal.AddEdge(ctx, downstreamID, upstreamID, kind, onFailure)
	if err != nil {
		return Edge{}, err
	}
	return edgeFromRow(e), nil
}

// UpdateMetadata authorizes a lifecycle mutation before applying the edit.
func (a *Access) UpdateMetadata(ctx context.Context, id string, in UpdateInput) (Goal, error) {
	if _, err := a.useRow(ctx, id); err != nil {
		return Goal{}, err
	}
	in.By = UserActor(a.userID)
	d, err := a.svc.goal.UpdateMetadata(ctx, id, in)
	if err != nil {
		return Goal{}, err
	}
	return d, nil
}

func (a *Access) Activate(ctx context.Context, id string) (Goal, error) {
	if _, err := a.useRow(ctx, id); err != nil {
		return Goal{}, err
	}
	d, err := a.svc.goal.Activate(ctx, id)
	if err != nil {
		return Goal{}, err
	}
	return d, nil
}

func (a *Access) Reattempt(ctx context.Context, id string) error {
	if _, err := a.useRow(ctx, id); err != nil {
		return err
	}
	return a.svc.goal.Reattempt(ctx, id, UserActor(a.userID))
}

func (a *Access) AddHumanMessage(ctx context.Context, in HumanMessageInput) (TimelineEvent, error) {
	if _, err := a.useRow(ctx, in.GoalID); err != nil {
		return TimelineEvent{}, err
	}
	in.ResponderUserID = a.userID
	e, err := a.svc.goal.AddHumanMessage(ctx, in)
	if err != nil {
		return TimelineEvent{}, err
	}
	return e, nil
}

func (a *Access) SubmitVerdict(ctx context.Context, in VerdictInput) error {
	if _, err := a.useRow(ctx, in.GoalID); err != nil {
		return err
	}
	in.ReviewerUserID = a.userID
	return a.svc.goal.SubmitVerdict(ctx, in)
}

func (a *Access) WaiveEdge(ctx context.Context, id, upstreamID, reason string) error {
	if _, err := a.useRow(ctx, id); err != nil {
		return err
	}
	return a.svc.goal.WaiveEdge(ctx, id, upstreamID, reason, UserActor(a.userID))
}

func (a *Access) ApprovePlan(ctx context.Context, id string) error {
	if _, err := a.useRow(ctx, id); err != nil {
		return err
	}
	return a.svc.goal.ApprovePlan(ctx, id, UserActor(a.userID))
}

func (a *Access) RejectPlan(ctx context.Context, id, reason string) error {
	if _, err := a.useRow(ctx, id); err != nil {
		return err
	}
	return a.svc.goal.RejectPlan(ctx, id, reason, UserActor(a.userID))
}

func validateAccessPage(limit, offset, maxLimit int64) error {
	if limit < 1 || limit > maxLimit || offset < 0 || offset > math.MaxInt32-limit {
		return ErrInvalidPage
	}
	return nil
}

// listRootParams builds the durable candidate query for a scanned page.
func (a *Access) listRootParams(filter GoalFilter, limit, offset int64) sqlc.ListRootGoalParams {
	return sqlc.ListRootGoalParams{
		UserID:          pgnull.Text(a.listUserID()),
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		WorkflowID:      pgnull.Text(filter.WorkflowID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.Archived,
		Limit:           int32(limit),
		Offset:          int32(offset),
	}
}

// listUserID is the durable owner scope of every goal collection. It stays
// owner-bound even for an admin: goal lists back per-user workspace surfaces
// (an agent's goal board, its counts), so an admin must see their own goals
// there, not every user's. This mirrors Scheduler's ListJobs and Session's
// ListPage, which are owner-bound for admins too. Admin superuser reach is
// unchanged for a resolved row (Get/Write) and for the explicitly user-scoped
// HealthReport.
func (a *Access) listUserID() string {
	return a.userID
}

// ListGoals fills a page of up to `limit` root goals the caller may read. It scans
// durable candidates from `offset` and applies Goal's direct rules to each,
// skipping denied rows without shrinking the page or dropping a visible row that
// sits behind one. It returns the page, the candidate offset to resume from, and
// whether more candidates remain — so the opaque offset token still advances by
// candidates consumed. Storage failures fail the whole page closed.
func (a *Access) ListGoals(ctx context.Context, filter GoalFilter, limit, offset int64) (page []Goal, nextOffset int64, hasMore bool, err error) {
	if err := validateAccessPage(limit+1, offset, math.MaxInt32); err != nil {
		return nil, 0, false, err
	}
	if err := a.decideList(); err != nil {
		return nil, 0, false, err
	}
	agentID, err := a.scopeAgent(filter.AgentID)
	if err != nil {
		return nil, 0, false, err
	}
	filter.AgentID = agentID
	// Fetch one more than the page target so a page that fills exactly still gets
	// a chance to observe a further candidate in the same batch.
	batch := limit + 1
	rows := make([]sqlc.AgentGoal, 0, limit)
	cursor := offset
	for {
		if cursor > math.MaxInt32 {
			return nil, 0, false, ErrInvalidPage
		}
		batchRows, err := a.svc.q.ListRootGoal(ctx, a.listRootParams(filter, batch, cursor))
		if err != nil {
			return nil, 0, false, err
		}
		for _, d := range batchRows {
			ok, derr := a.readable(d)
			if derr != nil {
				return nil, 0, false, derr
			}
			cursor++
			if !ok {
				continue
			}
			if int64(len(rows)) == limit {
				// A further visible row exists: resume the next page at it.
				nextOffset := cursor - 1
				if nextOffset+limit+1 > math.MaxInt32 {
					return nil, 0, false, ErrInvalidPage
				}
				return goalsFromRows(rows), nextOffset, true, nil
			}
			rows = append(rows, d)
		}
		if int64(len(batchRows)) < batch {
			// Candidate stream exhausted; no more visible rows past the page.
			return goalsFromRows(rows), cursor, false, nil
		}
	}
}

// CountGoals counts the root goals in the caller's scope that the caller may read.
// It scans candidates and applies the same direct read rule to every row, so
// the reported total cannot leak a hidden goal. Storage failures fail the count
// closed.
func (a *Access) CountGoals(ctx context.Context, filter GoalFilter) (int64, error) {
	if err := a.decideList(); err != nil {
		return 0, err
	}
	agentID, err := a.scopeAgent(filter.AgentID)
	if err != nil {
		return 0, err
	}
	filter.AgentID = agentID
	const batch = int64(200)
	var total, cursor int64
	for {
		rows, err := a.svc.q.ListRootGoal(ctx, a.listRootParams(filter, batch, cursor))
		if err != nil {
			return 0, err
		}
		for _, d := range rows {
			ok, derr := a.readable(d)
			if derr != nil {
				return 0, derr
			}
			if ok {
				total++
			}
		}
		cursor += int64(len(rows))
		if int64(len(rows)) < batch {
			return total, nil
		}
	}
}

// ListChildren authorizes the parent goal, then lists the direct children the
// caller may read, applying the same direct read rule to each.
func (a *Access) ListChildren(ctx context.Context, parentID string) ([]Goal, error) {
	if _, err := a.getRow(ctx, parentID); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListGoalChildren(ctx, pgnull.Text(parentID))
	if err != nil {
		return nil, err
	}
	filtered, err := a.filterReadable(rows)
	if err != nil {
		return nil, err
	}
	return goalsFromRows(filtered), nil
}

// ListSubtree authorizes the root goal, then lists the tree rows the caller may
// read, applying the same direct read rule to each.
func (a *Access) ListSubtree(ctx context.Context, rootID string) ([]Goal, error) {
	if _, err := a.getRow(ctx, rootID); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListGoalByRoot(ctx, rootID)
	if err != nil {
		return nil, err
	}
	filtered, err := a.filterReadable(rows)
	if err != nil {
		return nil, err
	}
	return goalsFromRows(filtered), nil
}

// HealthReport gates the aggregated execution-health view on the collection list
// decision, then computes it only over goals the caller may read. It scans the
// window's candidate goals, applies the direct read rule to each, and passes the
// visible id set to the aggregation so hidden goals' attempts/events never leak
// into totals. Storage failures fail the report closed.
func (a *Access) HealthReport(ctx context.Context, filter HealthFilter) (HealthReport, error) {
	if err := a.decideList(); err != nil {
		return HealthReport{}, err
	}
	agentID, err := a.scopeAgent(filter.AgentID)
	if err != nil {
		return HealthReport{}, err
	}
	filter.AgentID = agentID
	since, _ := a.svc.goal.healthWindow(filter)
	candidates, err := a.svc.q.ListGoalForHealthScope(ctx, sqlc.ListGoalForHealthScopeParams{
		SinceAt: since,
		UserID:  pgnull.Text(filter.UserID),
		AgentID: pgnull.Text(filter.AgentID),
	})
	if err != nil {
		return HealthReport{}, err
	}
	ids := make([]string, 0, len(candidates))
	for _, d := range candidates {
		ok, derr := a.readable(d)
		if derr != nil {
			return HealthReport{}, derr
		}
		if ok {
			ids = append(ids, d.ID)
		}
	}
	return a.svc.goal.HealthReport(ctx, filter, ids)
}

// ListAttempts authorizes the goal, then lists its attempts.
func (a *Access) ListAttempts(ctx context.Context, id string) ([]Attempt, error) {
	if _, err := a.getRow(ctx, id); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: id})
	if err != nil {
		return nil, err
	}
	return attemptsFromRows(rows), nil
}

// ListAttemptSummaries authorizes the goal, then returns at most limit
// lightweight attempt rows for read-only status projection.
func (a *Access) ListAttemptSummaries(ctx context.Context, id string, limit int32) ([]AttemptSummary, error) {
	if _, err := a.getRow(ctx, id); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListAttemptSummaryByGoal(ctx, sqlc.ListAttemptSummaryByGoalParams{GoalID: id, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]AttemptSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, AttemptSummary{
			ID: row.ID, Purpose: row.Purpose, AttemptNo: row.AttemptNo, Status: row.Status,
			SessionID: row.SessionID, Error: row.Error, FailureClass: row.FailureClass,
			StartedAt: timePtr(row.StartedAt), FinishedAt: timePtr(row.FinishedAt), UpdatedAt: row.UpdatedAt.UTC(),
		})
	}
	return out, nil
}

// GetAttempt authorizes the goal, then returns one attempt scoped to it.
func (a *Access) GetAttempt(ctx context.Context, id, attemptID string) (Attempt, error) {
	if _, err := a.getRow(ctx, id); err != nil {
		return Attempt{}, err
	}
	att, err := a.svc.q.GetAttempt(ctx, attemptID)
	if err != nil || att.GoalID != id {
		return Attempt{}, authz.ErrNotFound
	}
	return attemptFromRow(att), nil
}

// ListAcceptanceEvents authorizes the goal, then lists its acceptance ledger.
func (a *Access) ListAcceptanceEvents(ctx context.Context, id string) ([]AcceptanceEvent, error) {
	if _, err := a.getRow(ctx, id); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListAcceptanceEventByGoal(ctx, id)
	if err != nil {
		return nil, err
	}
	return acceptanceEventsFromRows(rows), nil
}

// ListTimeline authorizes the goal, then returns a page of its L3 timeline.
func (a *Access) ListTimeline(ctx context.Context, id string, limit, offset int) ([]TimelineEvent, error) {
	if err := validateAccessPage(int64(limit), int64(offset), math.MaxInt32); err != nil {
		return nil, err
	}
	if _, err := a.getRow(ctx, id); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListGoalEventByGoal(ctx, sqlc.ListGoalEventByGoalParams{GoalID: id, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	return timelineEventsFromRows(rows), nil
}

// ListEdges authorizes the goal, then lists its upstream edges.
func (a *Access) ListEdges(ctx context.Context, id string) ([]Edge, error) {
	if _, err := a.getRow(ctx, id); err != nil {
		return nil, err
	}
	rows, err := a.svc.q.ListEdgeByGoal(ctx, id)
	if err != nil {
		return nil, err
	}
	return edgesFromRows(rows), nil
}

// Readiness authorizes the goal, then computes its dispatchability view.
func (a *Access) Readiness(ctx context.Context, id string, now time.Time) (Readiness, error) {
	d, err := a.getRow(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	edges, err := a.svc.q.ListEdgeWithUpstreamState(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	return Compute(d, edges, now), nil
}

func (a *Access) load(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	d, err := getGoal(ctx, a.svc.q, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AgentGoal{}, authz.ErrNotFound
		}
		return sqlc.AgentGoal{}, err
	}
	return d, nil
}

func (a *Access) decide(action authz.Action, d sqlc.AgentGoal) error {
	return a.authorize(action, d)
}

// authorize applies Goal's fixed rules only to a loaded durable row. Goal
// denials are opaque, including collection actions, so callers cannot use this
// boundary to distinguish a forbidden goal from a missing one.
func (a *Access) authorize(action authz.Action, d sqlc.AgentGoal) error {
	if a.allowed(action, d) {
		return nil
	}
	return authz.ErrNotFound
}

func (a *Access) allowed(action authz.Action, d sqlc.AgentGoal) bool {
	if a.authority.IsAdmin() {
		return true
	}
	switch a.authority.Kind() {
	case authz.ActorUser:
		switch action {
		case authz.ActionList:
			return true
		case authz.ActionCreate, authz.ActionRead, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute:
			return a.userID != "" && a.userID == d.UserID
		default:
			return false
		}
	case authz.ActorAgent:
		switch action {
		case authz.ActionList:
			return true
		case authz.ActionCreate, authz.ActionRead, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute:
			return a.userID != "" && a.userID == d.UserID && a.agentID != "" && a.agentID == d.AgentID
		default:
			return false
		}
	default:
		return false
	}
}

// readable reports whether the caller may read a goal. A clean deny drops the
// row from a collection; storage errors remain visible to the caller.
func (a *Access) readable(d sqlc.AgentGoal) (bool, error) {
	return a.allowed(authz.ActionRead, d), nil
}

func (a *Access) decideList() error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	return a.authorize(authz.ActionList, sqlc.AgentGoal{})
}

// authorizeAgent requires the caller may still execute the goal's persisted
// agent through the Agent domain's direct authorization port.
func (a *Access) authorizeAgent(ctx context.Context, agentID string) error {
	return authorizeAgentWithin(ctx, a.svc.agents, a.authority, agentID)
}

// authorizeAgentWithin maps the Agent domain's direct execute decision back to
// the goal domain's opaque errors. It is shared by Access and the durable-worker
// PEP.
func authorizeAgentWithin(ctx context.Context, agents *agentaccess.Service, authority authz.Authority, agentID string) error {
	err := agents.Authorize(ctx, authority, agentID, authz.ActionExecute)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentaccess.ErrNotFound):
		return authz.ErrNotFound
	case errors.Is(err, agentaccess.ErrForbidden):
		return authz.ErrForbidden
	default:
		return err
	}
}

// scopeAgent confines a delegated agent's list to its own agent; a user actor
// may filter by any agent it owns goals for.
func (a *Access) scopeAgent(requested string) (string, error) {
	if a.agentID == "" {
		return requested, nil
	}
	if requested != "" && requested != a.agentID {
		return "", authz.ErrForbidden
	}
	return a.agentID, nil
}

// filterReadable keeps only the rows the caller may read. Storage failures are
// returned by their query before this point, so filtering cannot hide one.
func (a *Access) filterReadable(rows []sqlc.AgentGoal) ([]sqlc.AgentGoal, error) {
	out := rows[:0]
	for _, d := range rows {
		ok, err := a.readable(d)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// Authorize is Goal's narrow direct port for another domain. It loads the
// durable row before applying the same fixed Goal rules as Access; no caller can
// supply ownership or executor facts. A missing or denied goal is opaque. It
// returns the narrow AuthorizedGoal (owner + bound agent), never the sqlc row.
func (s *GoalService) Authorize(ctx context.Context, authority authz.Authority, goalID string, action authz.Action) (AuthorizedGoal, error) {
	if !authority.Valid() {
		return AuthorizedGoal{}, authz.ErrForbidden
	}
	d, err := getGoal(ctx, s.q, goalID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return AuthorizedGoal{}, authz.ErrNotFound
		}
		return AuthorizedGoal{}, err
	}
	access := Access{authority: authority, userID: string(authority.UserID())}
	if authority.Kind() == authz.ActorAgent {
		access.agentID = string(authority.AgentID())
	}
	if err := access.authorize(action, d); err != nil {
		return AuthorizedGoal{}, err
	}
	return AuthorizedGoal{ID: d.ID, UserID: d.UserID, AgentID: d.AgentID}, nil
}

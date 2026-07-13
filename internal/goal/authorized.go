package goal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Access is one Goal use case bound to exactly one Authorizer evaluation. The
// goal Service is the sole policy-enforcement point: transports and the agent
// tool pass a trusted authz.Authority and never a scoped query handle. A goal
// denial is opaque (authz.ErrNotFound) so a foreign goal cannot be told from a
// missing one; an agent-execute denial keeps its 403/404 visibility.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	authority authz.Authority
	userID    string
	// agentID is the executor confinement: empty for a plain user actor, the
	// bound agent for a delegated AgentActor.
	agentID string
}

// Begin opens exactly one evaluation for one Goal use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s.authz == nil || s.agents == nil {
		return nil, fmt.Errorf("goal authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("goal authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, authority: authority, userID: string(actor.UserID()), agentID: agentID}, nil
}

// workerAuthorizer is the durable-attempt policy-enforcement point. On every
// dequeue the worker calls it to fresh-authorize one attempt's execution: the
// persisted goal's ActionExecute and the executor agent, folded into ONE
// evaluation bound to a worker authority minted from the goal's durable owner.
// Facts come only from durable goal/attempt rows — a triggering request is never
// trusted — and any missing dependency fails closed rather than running the model.
type workerAuthorizer struct {
	authz  authz.Authorizer
	agents *agentaccess.Service
}

func newWorkerAuthorizer(az authz.Authorizer, agents *agentaccess.Service) *workerAuthorizer {
	return &workerAuthorizer{authz: az, agents: agents}
}

// authorize denies execution unless the goal's own durable facts pass a fresh
// Goal-execute decision and its bound agent passes an agent-execute decision, both
// under the single evaluation opened here. The authority is minted from the goal's
// durable owner and bound agent, mirroring the transport's execute path
// (Access.Use authorizes d.AgentID); a runtime executor override is a dispatch
// detail, never an authority source. A nil PEP (unconfigured boot) or an unusable
// owner/agent fails closed.
func (wa *workerAuthorizer) authorize(ctx context.Context, goal sqlc.AgentGoal) error {
	if wa == nil || wa.authz == nil || wa.agents == nil {
		return fmt.Errorf("goal worker authorization unavailable: PEP not configured")
	}
	authority, err := agentaccess.WorkerAgentAuthority(goal.UserID, goal.AgentID)
	if err != nil {
		return fmt.Errorf("goal worker authority invalid: %w", err)
	}
	eval, err := wa.authz.Begin(ctx, authority)
	if err != nil {
		return fmt.Errorf("goal worker authorization begin: %w", err)
	}
	if err := decideGoal(eval, authz.ActionExecute, goal.UserID, goal.AgentID, goal); err != nil {
		return err
	}
	return authorizeAgentWithin(ctx, wa.agents, eval, authority, goal.AgentID)
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
func (a *Access) Get(ctx context.Context, id string) (sqlc.AgentGoal, error) {
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
func (a *Access) Use(ctx context.Context, id string) (sqlc.AgentGoal, error) {
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
func (a *Access) CreateGoal(ctx context.Context, in CreateInput) (sqlc.AgentGoal, error) {
	if a.userID == "" {
		return sqlc.AgentGoal{}, authz.ErrUnauthenticated
	}
	if a.agentID != "" && a.agentID != in.AgentID {
		return sqlc.AgentGoal{}, authz.ErrForbidden
	}
	in.UserID = a.userID
	// Idempotency replay: a goal already minted under this key is authorized
	// against ITS OWN durable facts and persisted agent in this same evaluation,
	// never the requested route. A reused key that names a different agent (or a
	// goal since custom-denied) therefore fails closed instead of handing back a
	// differently bound goal the caller may no longer touch.
	if in.IdempotencyKey != "" {
		existing, err := a.svc.Queries.GetGoalByIdempotencyKey(ctx, sqlc.GetGoalByIdempotencyKeyParams{UserID: a.userID, IdempotencyKey: pgnull.Text(in.IdempotencyKey)})
		switch {
		case err == nil:
			if derr := a.decide(authz.ActionRead, existing); derr != nil {
				return sqlc.AgentGoal{}, derr
			}
			if derr := a.authorizeAgent(ctx, existing.AgentID); derr != nil {
				return sqlc.AgentGoal{}, derr
			}
			return existing, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return sqlc.AgentGoal{}, err
		}
	}
	facts := policy.GoalFacts{
		Owner: a.userID, Agent: in.AgentID, State: "draft", IsOwner: true,
		IsExecutor: a.agentID != "" && a.agentID == in.AgentID,
	}
	req, err := policy.GoalRequest(authz.ActionCreate, a.userID, a.userID, facts)
	if err != nil {
		return sqlc.AgentGoal{}, authz.ErrForbidden
	}
	if err := a.decideReq(req); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if err := a.authorizeAgent(ctx, in.AgentID); err != nil {
		return sqlc.AgentGoal{}, err
	}
	return a.svc.Goal.CreateRoot(ctx, in)
}

// Cancel authorizes a state change on a goal and cancels it (cascade handled by
// the transition). Cancellation is an execute-authorized lifecycle action, so it
// goes through Use (goal-execute + agent-execute) rather than a bare read.
func (a *Access) Cancel(ctx context.Context, id, reason string) error {
	d, err := a.Use(ctx, id)
	if err != nil {
		return err
	}
	return a.svc.Goal.Cancel(ctx, id, reason, UserActor(d.UserID))
}

// Archive authorizes a state change on a goal and archives it (audit-safe delete).
func (a *Access) Archive(ctx context.Context, id string) error {
	if _, err := a.Use(ctx, id); err != nil {
		return err
	}
	return a.svc.Goal.Archive(ctx, id)
}

// Unarchive authorizes a state change on a goal and restores it to default lists.
func (a *Access) Unarchive(ctx context.Context, id string) error {
	if _, err := a.Use(ctx, id); err != nil {
		return err
	}
	return a.svc.Goal.Unarchive(ctx, id)
}

// Abandon authorizes a state change on a goal and records the human give-up on a
// budget-exhausted block.
func (a *Access) Abandon(ctx context.Context, id, reason string) error {
	d, err := a.Use(ctx, id)
	if err != nil {
		return err
	}
	return a.svc.Goal.Abandon(ctx, id, reason, UserActor(d.UserID))
}

// AddEdge authorizes a state change on BOTH the downstream and the caller-supplied
// upstream goal, then inserts the upstream dependency edge (cycle-checked). The
// upstream is gated under the same execute decision as the downstream so a caller
// cannot wire in another tenant's goal and pull its frozen accepted_output into
// their own attempt's input context.
func (a *Access) AddEdge(ctx context.Context, downstreamID, upstreamID, kind, onFailure string) (sqlc.AgentGoalEdge, error) {
	if _, err := a.Use(ctx, downstreamID); err != nil {
		return sqlc.AgentGoalEdge{}, err
	}
	if _, err := a.Use(ctx, upstreamID); err != nil {
		return sqlc.AgentGoalEdge{}, err
	}
	return a.svc.Goal.AddEdge(ctx, downstreamID, upstreamID, kind, onFailure)
}

// listRootParams builds the durable candidate query for a scanned page.
func (a *Access) listRootParams(filter GoalFilter, limit, offset int64) sqlc.ListRootGoalParams {
	return sqlc.ListRootGoalParams{
		UserID:          a.userID,
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

// ListGoals fills a page of up to `limit` root goals the caller may read. It scans
// durable candidates from `offset` and authorizes each under this evaluation,
// skipping denied rows without shrinking the page or dropping a visible row that
// sits behind one. It returns the page, the candidate offset to resume from, and
// whether more candidates remain — so the opaque offset token still advances by
// candidates consumed. An unexpected PDP error fails the whole page closed.
func (a *Access) ListGoals(ctx context.Context, filter GoalFilter, limit, offset int64) (page []sqlc.AgentGoal, nextOffset int64, hasMore bool, err error) {
	if err := a.decideList(); err != nil {
		return nil, 0, false, err
	}
	agentID, err := a.scopeAgent(filter.AgentID)
	if err != nil {
		return nil, 0, false, err
	}
	filter.AgentID = agentID
	if limit <= 0 {
		limit = 50
	}
	// Fetch one more than the page target so a page that fills exactly still gets
	// a chance to observe a further candidate in the same batch.
	batch := limit + 1
	page = make([]sqlc.AgentGoal, 0, limit)
	cursor := offset
	for {
		rows, err := a.svc.Queries.ListRootGoal(ctx, a.listRootParams(filter, batch, cursor))
		if err != nil {
			return nil, 0, false, err
		}
		for _, d := range rows {
			ok, derr := a.readable(d)
			if derr != nil {
				return nil, 0, false, derr
			}
			cursor++
			if !ok {
				continue
			}
			if int64(len(page)) == limit {
				// A further visible row exists: resume the next page at it.
				return page, cursor - 1, true, nil
			}
			page = append(page, d)
		}
		if int64(len(rows)) < batch {
			// Candidate stream exhausted; no more visible rows past the page.
			return page, cursor, false, nil
		}
	}
}

// CountGoals counts the root goals in the caller's scope that the caller may read.
// It scans candidates and authorizes every row under this evaluation so a
// per-resource custom deny never leaks a hidden goal into the reported total. An
// unexpected PDP error fails the count closed.
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
		rows, err := a.svc.Queries.ListRootGoal(ctx, a.listRootParams(filter, batch, cursor))
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
// caller may read, authorizing each under this evaluation.
func (a *Access) ListChildren(ctx context.Context, parentID string) ([]sqlc.AgentGoal, error) {
	if _, err := a.Get(ctx, parentID); err != nil {
		return nil, err
	}
	rows, err := a.svc.Queries.ListGoalChildren(ctx, pgnull.Text(parentID))
	if err != nil {
		return nil, err
	}
	return a.filterReadable(rows)
}

// ListSubtree authorizes the root goal, then lists the tree rows the caller may
// read, authorizing each under this evaluation.
func (a *Access) ListSubtree(ctx context.Context, rootID string) ([]sqlc.AgentGoal, error) {
	if _, err := a.Get(ctx, rootID); err != nil {
		return nil, err
	}
	rows, err := a.svc.Queries.ListGoalByRoot(ctx, rootID)
	if err != nil {
		return nil, err
	}
	return a.filterReadable(rows)
}

// HealthReport gates the aggregated execution-health view on the collection list
// decision, then computes it only over goals the caller may read. It scans the
// window's candidate goals, authorizes each under this evaluation, and passes the
// authorized id set to the aggregation so a per-resource custom deny never leaks a
// hidden goal's attempts/events into the totals. An unexpected PDP error fails the
// report closed.
func (a *Access) HealthReport(ctx context.Context, filter HealthFilter) (HealthReport, error) {
	if err := a.decideList(); err != nil {
		return HealthReport{}, err
	}
	agentID, err := a.scopeAgent(filter.AgentID)
	if err != nil {
		return HealthReport{}, err
	}
	filter.AgentID = agentID
	since, _ := a.svc.Goal.healthWindow(filter)
	candidates, err := a.svc.Queries.ListGoalForHealthScope(ctx, sqlc.ListGoalForHealthScopeParams{
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
	return a.svc.Goal.HealthReport(ctx, filter, ids)
}

// ListAttempts authorizes the goal, then lists its attempts.
func (a *Access) ListAttempts(ctx context.Context, id string) ([]sqlc.AgentGoalAttempt, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: id})
}

// GetAttempt authorizes the goal, then returns one attempt scoped to it.
func (a *Access) GetAttempt(ctx context.Context, id, attemptID string) (sqlc.AgentGoalAttempt, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}
	att, err := a.svc.Queries.GetAttempt(ctx, attemptID)
	if err != nil || att.GoalID != id {
		return sqlc.AgentGoalAttempt{}, authz.ErrNotFound
	}
	return att, nil
}

// ListAcceptanceEvents authorizes the goal, then lists its acceptance ledger.
func (a *Access) ListAcceptanceEvents(ctx context.Context, id string) ([]sqlc.AgentGoalAcceptanceEvent, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListAcceptanceEventByGoal(ctx, id)
}

// ListTimeline authorizes the goal, then returns a page of its L3 timeline.
func (a *Access) ListTimeline(ctx context.Context, id string, limit, offset int) ([]sqlc.AgentGoalEvent, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListGoalEventByGoal(ctx, sqlc.ListGoalEventByGoalParams{GoalID: id, Limit: int32(limit), Offset: int32(offset)})
}

// ListEdges authorizes the goal, then lists its upstream edges.
func (a *Access) ListEdges(ctx context.Context, id string) ([]sqlc.AgentGoalEdge, error) {
	if _, err := a.Get(ctx, id); err != nil {
		return nil, err
	}
	return a.svc.Queries.ListEdgeByGoal(ctx, id)
}

// Readiness authorizes the goal, then computes its dispatchability view.
func (a *Access) Readiness(ctx context.Context, id string, now time.Time) (Readiness, error) {
	d, err := a.Get(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	edges, err := a.svc.Queries.ListEdgeWithUpstreamState(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	return Compute(d, edges, now), nil
}

func (a *Access) load(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	d, err := getGoal(ctx, a.svc.Queries, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AgentGoal{}, authz.ErrNotFound
		}
		return sqlc.AgentGoal{}, err
	}
	return d, nil
}

func (a *Access) decide(action authz.Action, d sqlc.AgentGoal) error {
	return decideGoal(a.eval, action, a.userID, a.agentID, d)
}

// readable reports whether the caller may read a goal under this evaluation. A
// clean deny (foreign/hidden goal) drops the row from a collection; an unexpected
// PDP error propagates so a decision-backend failure fails the whole list closed
// instead of silently shrinking it.
func (a *Access) readable(d sqlc.AgentGoal) (bool, error) {
	err := a.decide(authz.ActionRead, d)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, authz.ErrNotFound), errors.Is(err, authz.ErrForbidden):
		return false, nil
	default:
		return false, err
	}
}

// decideGoal evaluates one Goal action against durable facts within an already
// open evaluation. It is the single Goal decision seam shared by the transport
// PEP (Access) and the durable-worker PEP (workerAuthorizer): both derive
// IsOwner/IsExecutor from the caller identity and the loaded row, never from a
// request. A denial is opaque (ErrNotFound) so it never reveals a goal's
// existence; an unexpected PDP error surfaces so callers can fail closed.
func decideGoal(eval authz.Evaluation, action authz.Action, actorUserID, actorAgentID string, d sqlc.AgentGoal) error {
	facts := policy.GoalFacts{
		Owner: d.UserID, Agent: d.AgentID, State: d.Lifecycle,
		IsOwner:    actorUserID != "" && actorUserID == d.UserID,
		IsExecutor: actorAgentID != "" && actorAgentID == d.AgentID,
	}
	req, err := policy.GoalRequest(action, d.ID, d.UserID, facts)
	if err != nil {
		return authz.ErrForbidden
	}
	dec, err := eval.Decide(req)
	if err != nil {
		return fmt.Errorf("goal decide: %w", err)
	}
	if !dec.Allowed() {
		return authz.ErrNotFound
	}
	return nil
}

func (a *Access) decideList() error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	req, err := policy.GoalListRequest()
	if err != nil {
		return authz.ErrForbidden
	}
	return a.decideReq(req)
}

func (a *Access) decideReq(req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("goal decide: %w", err)
	}
	if !dec.Allowed() {
		// Goals are opaque: a denial never reveals existence.
		return authz.ErrNotFound
	}
	return nil
}

// authorizeAgent folds the former requireAgentUse gate into this evaluation: a
// state change requires the caller may still execute the goal's persisted agent.
func (a *Access) authorizeAgent(ctx context.Context, agentID string) error {
	return authorizeAgentWithin(ctx, a.svc.agents, a.eval, a.authority, agentID)
}

// authorizeAgentWithin folds an agent execute decision into an already open
// evaluation and maps the agent PEP's typed denials back to the goal domain's
// opaque errors. Shared by Access and the durable-worker PEP so the agent gate is
// evaluated under the same revision as the goal decision, never a second one.
func authorizeAgentWithin(ctx context.Context, agents *agentaccess.Service, eval authz.Evaluation, authority authz.Authority, agentID string) error {
	err := agents.AuthorizeWithin(ctx, eval, authority, agentID, authz.ActionExecute)
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

// filterReadable keeps only the rows the caller may read, authorizing each under
// this evaluation. It never swallows a decision-backend error: an unexpected PDP
// failure propagates so the collection fails closed instead of silently omitting
// rows it could not decide.
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

// AuthorizeWithin decides a goal action against a caller's already-open
// evaluation and returns the loaded row, so another domain (workflow's
// SaveGoalAsWorkflow) can gate a source goal under its single policy revision
// instead of opening a second evaluation. It is a narrow Goal-owned port,
// mirroring agentaccess.AuthorizeWithin; facts come only from the durable row and
// the passed Authority, so it never widens access. A missing or denied goal is
// opaque (authz.ErrNotFound).
func (s *GoalService) AuthorizeWithin(ctx context.Context, eval authz.Evaluation, authority authz.Authority, goalID string, action authz.Action) (sqlc.AgentGoal, error) {
	d, err := getGoal(ctx, s.q, goalID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return sqlc.AgentGoal{}, authz.ErrNotFound
		}
		return sqlc.AgentGoal{}, err
	}
	actor := authority.Actor()
	userID := string(actor.UserID())
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	facts := policy.GoalFacts{
		Owner:      d.UserID,
		Agent:      d.AgentID,
		State:      d.Lifecycle,
		IsOwner:    userID != "" && userID == d.UserID,
		IsExecutor: agentID != "" && agentID == d.AgentID,
	}
	req, err := policy.GoalRequest(action, d.ID, d.UserID, facts)
	if err != nil {
		return sqlc.AgentGoal{}, authz.ErrForbidden
	}
	dec, err := eval.Decide(req)
	if err != nil {
		return sqlc.AgentGoal{}, fmt.Errorf("goal decide: %w", err)
	}
	if !dec.Allowed() {
		return sqlc.AgentGoal{}, authz.ErrNotFound
	}
	return d, nil
}

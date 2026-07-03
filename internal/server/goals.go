package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SetGoalService wires the goal system into the admin server.
// When unset, every /api/goals route returns 503.
func (s *Server) SetGoalService(svc *goal.Service) {
	if svc == nil {
		s.goalSvc = nil
		s.goalQueries = nil
		return
	}
	s.goalSvc = svc.Goal
	s.goalQueries = svc.Queries
}

func (s *Server) goalsReady() bool { return s.goalSvc != nil && s.goalQueries != nil }

// goalAuth gates a handler on the goal system being wired and an
// authenticated caller, returning the caller's user id.
func (s *Server) goalAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.goalsReady() {
		writeError(w, http.StatusServiceUnavailable, "goals unavailable")
		return "", false
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	return info.UserID, true
}

// goalError maps the package's sentinel errors to HTTP status codes:
// not-found → 404, validation → 400, lifecycle/guard → 409, else 500.
type goalFilter struct {
	AgentID    string
	Lifecycle  string
	ProjectID  string
	WorkflowID string
	Terminal   *bool
	Q          string
	Archived   bool
}

func goalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, goal.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, goal.ErrDeterministicChecksUnsupported):
		writeErrorDetails(w, http.StatusBadRequest, "required deterministic acceptance checks need a sandbox-capable backend; enable a sandbox backend or change those checks to judgment items", map[string]any{
			"code": "deterministic_checks_unsupported",
			"fix":  "enable a sandbox backend or remove required deterministic acceptance items",
		})
	case errors.Is(err, goal.ErrInvalidContract),
		errors.Is(err, goal.ErrCompositeDeterministicContract),
		errors.Is(err, goal.ErrInvalidDecomposition),
		errors.Is(err, goal.ErrDepthExceeded),
		errors.Is(err, goal.ErrCycle),
		errors.Is(err, goal.ErrInvalidEvidence),
		errors.Is(err, goal.ErrInvalidVerdict):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, goal.ErrInvalidTransition),
		errors.Is(err, goal.ErrPlanGate),
		errors.Is(err, goal.ErrBudgetExhausted),
		errors.Is(err, goal.ErrConcurrencyCap),
		errors.Is(err, goal.ErrStaleProjection):
		writeError(w, http.StatusConflict, err.Error())
	default:
		// Unmapped errors collapse to a generic 500, so log the cause here —
		// it is the only place it survives.
		slog.Error("goal handler internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// loadGoal fetches a goal by id, enforcing ownership. A row owned
// by another user is reported as 404 to avoid leaking its existence; a scoped
// token whose agent differs is 403. Returns false (after writing the error) on
// any miss.
func (s *Server) loadGoal(ctx context.Context, w http.ResponseWriter, userID, id string) (sqlc.AgentGoal, bool) {
	d, err := s.goalQueries.GetGoal(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, goal.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return sqlc.AgentGoal{}, false
		}
		goalError(w, err)
		return sqlc.AgentGoal{}, false
	}
	if d.UserID != userID {
		writeError(w, http.StatusNotFound, "not_found")
		return sqlc.AgentGoal{}, false
	}
	if agentID, _, ok := UserFromContext(ctx).scopedBoundary(); ok && d.AgentID != agentID {
		writeError(w, http.StatusForbidden, "permission denied")
		return sqlc.AgentGoal{}, false
	}
	return d, true
}

// ── List / CRUD ──────────────────────────────────────────────────────────────

// ListGoals lists root goals (goals) by default; `?parent={id}`
// lists a composite's children and `?root={id}` lists a whole tree.
func (s *Server) ListGoals(w http.ResponseWriter, r *http.Request, params apiserver.ListGoalsParams) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	if params.Parent != nil {
		if _, ok := s.loadGoal(ctx, w, userID, *params.Parent); !ok {
			return
		}
		rows, err := s.goalQueries.ListGoalChildren(ctx, pgnull.Text(*params.Parent))
		if err != nil {
			goalError(w, err)
			return
		}
		writeData(w, http.StatusOK, goalListAPI(rows, "", nil))
		return
	}
	if params.Root != nil {
		if _, ok := s.loadGoal(ctx, w, userID, *params.Root); !ok {
			return
		}
		rows, err := s.goalQueries.ListGoalByRoot(ctx, *params.Root)
		if err != nil {
			goalError(w, err)
			return
		}
		writeData(w, http.StatusOK, goalListAPI(rows, "", nil))
		return
	}

	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter := goalFilter{}
	if params.AgentId != nil {
		filter.AgentID = *params.AgentId
	}
	if params.Lifecycle != nil {
		filter.Lifecycle = *params.Lifecycle
	}
	if params.ProjectId != nil {
		filter.ProjectID = *params.ProjectId
	}
	if params.WorkflowId != nil {
		filter.WorkflowID = *params.WorkflowId
	}
	if params.Terminal != nil {
		filter.Terminal = params.Terminal
	}
	if params.Q != nil {
		filter.Q = *params.Q
	}
	if params.Archived != nil {
		filter.Archived = *params.Archived
	}
	rows, err := s.goalQueries.ListRootGoal(ctx, sqlc.ListRootGoalParams{
		UserID:          userID,
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		WorkflowID:      pgnull.Text(filter.WorkflowID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        goalTerminalArg(filter.Terminal),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.Archived,
		Limit:           int32(limit + 1),
		Offset:          int32(offset),
	})
	if err != nil {
		goalError(w, err)
		return
	}
	page, next := nextPageTokenForRows(rows, limit, offset)
	var total *int
	if n, err := s.goalQueries.CountRootGoal(ctx, sqlc.CountRootGoalParams{
		UserID:          userID,
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		WorkflowID:      pgnull.Text(filter.WorkflowID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        goalTerminalArg(filter.Terminal),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.Archived,
	}); err == nil {
		v := int(n)
		total = &v
	}
	writeData(w, http.StatusOK, goalListAPI(page, next, total))
}

// GetGoalHealth returns the aggregated execution health report for a time window.
func (s *Server) GetGoalHealth(w http.ResponseWriter, r *http.Request, params apiserver.GetGoalHealthParams) {
	if !s.goalsReady() {
		writeError(w, http.StatusServiceUnavailable, "goals unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	userID := info.UserID
	if info.IsAdmin && params.UserId == nil {
		userID = ""
	}
	if params.UserId != nil {
		if *params.UserId != info.UserID && !info.IsAdmin {
			writeError(w, http.StatusForbidden, "admin access required")
			return
		}
		userID = *params.UserId
	}
	agentID := derefStr(params.AgentId)
	if boundAgent, _, ok := info.scopedBoundary(); ok {
		if agentID != "" && agentID != boundAgent {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		agentID = boundAgent
	}
	report, err := s.goalSvc.HealthReport(r.Context(), goal.HealthFilter{
		SinceAt: params.Since,
		UserID:  userID,
		AgentID: agentID,
	})
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, healthReportToAPI(report))
}

// CreateGoal mints a root goal (goal). With activate=true a leaf is
// activated immediately (direct run); the flag is ignored for a composite, which
// is decomposed and materialized by the dispatcher first.
func (s *Server) CreateGoal(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var body apitypes.CreateGoalRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Title == "" || body.AgentId == "" {
		writeError(w, http.StatusBadRequest, "title and agent_id are required")
		return
	}
	// Every user-created goal is a planned composite: it must go through plan +
	// decomposition into verifiable sub-tasks before any work runs. There is no
	// top-level direct-leaf execution — leaves exist only as planner-produced
	// children. The request's kind is ignored.
	in := goal.CreateInput{UserID: userID, AgentID: body.AgentId, Title: body.Title, Kind: goal.KindComposite}
	if body.Intent != nil {
		in.Intent = *body.Intent
	}
	if body.ProjectId != nil {
		in.ProjectID = *body.ProjectId
	}
	if body.Priority != nil {
		in.Priority = string(*body.Priority)
	}
	if body.ReviewPolicy != nil {
		in.ReviewPolicy = string(*body.ReviewPolicy)
	}
	if body.AcceptanceContract != nil {
		in.Contract = toContract(*body.AcceptanceContract)
	}
	if body.ConvergencePolicy != nil {
		in.Convergence = toConvergence(*body.ConvergencePolicy)
	}
	created, err := s.goalSvc.CreateRoot(ctx, in)
	if err != nil {
		goalError(w, err)
		return
	}
	// No activation here: a review_policy=none composite is autonomously planned by
	// the dispatcher (scanAndDecompose) once it lands in draft; a review_policy=human
	// composite is planned interactively. The plan gate runs after decomposition.
	writeData(w, http.StatusCreated, goalToAPI(created))
}

// GetGoal returns one goal.
func (s *Server) GetGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	d, ok := s.loadGoal(r.Context(), w, userID, id)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, goalToAPI(d))
}

// UpdateGoal applies a partial metadata edit (PATCH).
func (s *Server) UpdateGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.UpdateGoalRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in := goal.UpdateInput{Title: body.Title, Intent: body.Intent, By: goal.UserActor(userID)}
	if body.Priority != nil {
		v := string(*body.Priority)
		in.Priority = &v
	}
	if body.ReviewPolicy != nil {
		v := string(*body.ReviewPolicy)
		in.ReviewPolicy = &v
	}
	if body.AcceptanceContract != nil {
		c := toContract(*body.AcceptanceContract)
		in.Contract = &c
	}
	if body.ConvergencePolicy != nil {
		c := toConvergence(*body.ConvergencePolicy)
		in.Convergence = &c
	}
	updated, err := s.goalSvc.UpdateMetadata(ctx, id, in)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(updated))
}

// DeleteGoal archives a goal (audit-safe delete).
func (s *Server) DeleteGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	if err := s.goalSvc.Archive(ctx, id); err != nil {
		goalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Lifecycle commands ───────────────────────────────────────────────────────

// ActivateGoal runs the plan gate (draft → ready).
func (s *Server) ActivateGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	d, err := s.goalSvc.Activate(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(d))
}

// CancelGoal cancels a goal, cascading over its non-terminal subtree.
func (s *Server) CancelGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.CancelRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.goalSvc.Cancel(ctx, id, derefStr(body.Reason), goal.UserActor(userID)); err != nil {
		goalError(w, err)
		return
	}
	s.respondGoal(ctx, w, id)
}

// AbandonGoal is the human give-up on a budget-exhausted block.
func (s *Server) AbandonGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.AbandonRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.goalSvc.Abandon(ctx, id, derefStr(body.Reason), goal.UserActor(userID)); err != nil {
		goalError(w, err)
		return
	}
	s.respondGoal(ctx, w, id)
}

// ReattemptGoal raises the budget on a blocked goal and resumes it.
func (s *Server) ReattemptGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	if err := s.goalSvc.Reattempt(ctx, id, goal.UserActor(userID)); err != nil {
		goalError(w, err)
		return
	}
	s.respondGoal(ctx, w, id)
}

// UnarchiveGoal restores an archived goal to default lists.
func (s *Server) UnarchiveGoal(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	if err := s.goalSvc.Unarchive(ctx, id); err != nil {
		goalError(w, err)
		return
	}
	s.respondGoal(ctx, w, id)
}

// GetGoalReadiness returns the computed dispatchability view.
func (s *Server) GetGoalReadiness(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	rd, err := s.goalReadiness(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, readinessToAPI(rd))
}

// ── Sub-resource reads ───────────────────────────────────────────────────────

// ListGoalChildren lists a composite's direct children.
func (s *Server) ListGoalChildren(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.goalQueries.ListGoalChildren(ctx, pgnull.Text(id))
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalListAPI(rows, "", nil))
}

// ListAttempts lists a goal's attempts (newest first).
func (s *Server) ListAttempts(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.goalQueries.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: id})
	if err != nil {
		goalError(w, err)
		return
	}
	out := make([]apitypes.Attempt, 0, len(rows))
	for _, a := range rows {
		out = append(out, attemptToAPI(a))
	}
	writeData(w, http.StatusOK, apitypes.AttemptList{Attempts: out})
}

// GetAttempt returns one attempt, scoped to its goal.
func (s *Server) GetAttempt(w http.ResponseWriter, r *http.Request, id string, attemptId string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	a, err := s.goalQueries.GetAttempt(ctx, attemptId)
	if err != nil || a.GoalID != id {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeData(w, http.StatusOK, attemptToAPI(a))
}

// ListAcceptanceEvents lists the acceptance ledger (audit trail, in fold order).
func (s *Server) ListAcceptanceEvents(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.goalQueries.ListAcceptanceEventByGoal(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, acceptanceEventListAPI(rows))
}

// ListGoalTimeline lists a goal's L3 timeline in chronological order.
func (s *Server) ListGoalTimeline(w http.ResponseWriter, r *http.Request, id string, params apiserver.ListGoalTimelineParams) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.goalQueries.ListGoalEventByGoal(ctx, sqlc.ListGoalEventByGoalParams{GoalID: id, Limit: int32(limit + 1), Offset: int32(offset)})
	if err != nil {
		goalError(w, err)
		return
	}
	page, next := nextPageTokenForRows(rows, limit, offset)
	writeData(w, http.StatusOK, goalTimelineAPI(page, next))
}

// CreateGoalTimelineEvent appends a human message and reattempts non-dep blocks.
func (s *Server) CreateGoalTimelineEvent(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.GoalTimelineMessageRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	event, err := s.goalSvc.AddHumanMessage(ctx, goal.HumanMessageInput{GoalID: id, Text: body.Text, ResponderUserID: userID})
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, goalTimelineEventToAPI(event))
}

// ── Verdict + edges ──────────────────────────────────────────────────────────

// SubmitVerdict appends a human verdict against a contract item and re-folds.
func (s *Server) SubmitVerdict(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.VerdictRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in := goal.VerdictInput{
		GoalID:         id,
		ItemID:         body.ItemId,
		Result:         string(body.Result),
		Rationale:      derefStr(body.Rationale),
		Scope:          derefStr(body.Scope),
		ScopeHash:      derefStr(body.ScopeHash),
		ReviewerUserID: userID,
	}
	if err := s.goalSvc.SubmitVerdict(ctx, in); err != nil {
		goalError(w, err)
		return
	}
	// The verdict is the highest-seq event after the append; surface it.
	events, err := s.goalQueries.ListAcceptanceEventByGoal(ctx, id)
	if err != nil || len(events) == 0 {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, acceptanceEventToAPI(events[len(events)-1]))
}

// ListEdges lists a goal's upstream dependency edges.
func (s *Server) ListEdges(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.goalQueries.ListEdgeByGoal(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, edgeListAPI(rows))
}

// AddEdge inserts an upstream dependency edge (cycle-checked).
func (s *Server) AddEdge(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.AddEdgeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.UpstreamId == "" {
		writeError(w, http.StatusBadRequest, "upstream_id is required")
		return
	}
	// The upstream is caller-supplied — gate it through the same ownership check
	// as the downstream, or a caller could wire in another tenant's goal
	// and pull its frozen accepted_output into their own attempt's input context.
	if _, ok := s.loadGoal(ctx, w, userID, body.UpstreamId); !ok {
		return
	}
	kind := goal.EdgeHard
	if body.Kind != nil {
		kind = string(*body.Kind)
	}
	onFailure := goal.OnFailureBlock
	if body.OnFailure != nil {
		onFailure = string(*body.OnFailure)
	}
	edge, err := s.goalSvc.AddEdge(ctx, id, body.UpstreamId, kind, onFailure)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, edgeToAPI(edge))
}

// WaiveEdge waives a hard edge so a blocked(dep) downstream can proceed.
func (s *Server) WaiveEdge(w http.ResponseWriter, r *http.Request, id string, upstreamId string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.WaiveRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.goalSvc.WaiveEdge(ctx, id, upstreamId, derefStr(body.Reason), goal.UserActor(userID)); err != nil {
		goalError(w, err)
		return
	}
	edges, err := s.goalQueries.ListEdgeByGoal(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	for _, e := range edges {
		if e.UpstreamID == upstreamId {
			writeData(w, http.StatusOK, edgeToAPI(e))
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found")
}

// ── Plan approval (composite decomposition gate) ─────────────────────────────

// ApprovePlan approves a composite's proposed plan (blocked(needs_plan_approval)),
// materializing its children and resuming the tree.
func (s *Server) ApprovePlan(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	if err := s.goalSvc.ApprovePlan(ctx, id, goal.UserActor(userID)); err != nil {
		goalError(w, err)
		return
	}
	d, err := s.goalQueries.GetGoal(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(d))
}

// RejectPlan rejects a composite's proposed plan, returning it to draft so the
// dispatcher re-decomposes it.
func (s *Server) RejectPlan(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.goalAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadGoal(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.DecisionRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.goalSvc.RejectPlan(ctx, id, derefStr(body.Reason), goal.UserActor(userID)); err != nil {
		goalError(w, err)
		return
	}
	d, err := s.goalQueries.GetGoal(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(d))
}

// ── Shared helpers ───────────────────────────────────────────────────────────

// respondGoal re-fetches a goal and writes it; used by the
// command handlers whose service method returns only an error.
func (s *Server) respondGoal(ctx context.Context, w http.ResponseWriter, id string) {
	d, err := s.goalQueries.GetGoal(ctx, id)
	if err != nil {
		goalError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(d))
}

// decodeOptionalBody decodes an optional request body, tolerating an empty body
// (EOF) but rejecting malformed JSON with a 400. Returns false (after writing the
// error) only on malformed input.
func decodeOptionalBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeJSON(r, dst); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// ── Type conversions: request → package domain ───────────────────────────────

func toContract(c apitypes.AcceptanceContract) goal.AcceptanceContract {
	var out goal.AcceptanceContract
	jsonRoundTrip(c, &out)
	return out
}

func toConvergence(c apitypes.ConvergencePolicy) goal.ConvergencePolicy {
	var out goal.ConvergencePolicy
	jsonRoundTrip(c, &out)
	return out
}

// jsonRoundTrip copies between two JSON-tag-compatible shapes (api ⇄ domain).
func jsonRoundTrip(src, dst any) {
	b, err := json.Marshal(src)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, dst)
}

// ── Row → API mappers ────────────────────────────────────────────────────────

func goalListAPI(rows []sqlc.AgentGoal, nextToken string, total *int) apitypes.GoalList {
	items := make([]apitypes.Goal, 0, len(rows))
	for _, d := range rows {
		items = append(items, goalToAPI(d))
	}
	out := apitypes.GoalList{Goals: items, Total: total}
	if nextToken != "" {
		out.NextPageToken = &nextToken
	}
	return out
}

func goalToAPI(d sqlc.AgentGoal) apitypes.Goal {
	out := apitypes.Goal{
		Id:                 d.ID,
		UserId:             d.UserID,
		AgentId:            d.AgentID,
		RootId:             d.RootID,
		Depth:              int(d.Depth),
		Position:           int(d.Position),
		Title:              d.Title,
		Kind:               apitypes.GoalKind(d.Kind),
		Priority:           apitypes.GoalPriority(d.Priority),
		Required:           d.Required,
		Lifecycle:          apitypes.GoalLifecycle(d.Lifecycle),
		DoneReason:         apitypes.GoalDoneReason(d.DoneReason),
		AcceptanceState:    apitypes.GoalAcceptanceState(d.AcceptanceState),
		NeedsAttention:     goal.NeedsAttention(d.Lifecycle, d.BlockReason),
		CreatedAt:          d.CreatedAt.UTC(),
		UpdatedAt:          d.UpdatedAt.UTC(),
		Intent:             optStr(d.Intent),
		AcceptanceSeq:      iptr(d.AcceptanceSeq),
		AttemptCount:       iptr(d.AttemptCount),
		BudgetBonus:        int(d.BudgetBonus),
		FlakyCount:         iptr(d.FlakyCount),
		AcceptanceContract: parseAcceptanceContract(d.AcceptanceContract),
		ConvergencePolicy:  parseConvergencePolicy(d.ConvergencePolicy),
		Context:            jsonObject(d.Context),
		DispatchHint:       jsonObject(d.DispatchHint),
		ProjectId:          nullToPtr(d.ProjectID),
		ParentId:           nullToPtr(d.ParentID),
		ActiveAttemptId:    nullToPtr(d.ActiveAttemptID),
		Plan:               jsonObject(d.Plan),
		PlannedAt:          parseTimePtr(d.PlannedAt),
		AcceptedAt:         parseTimePtr(d.AcceptedAt),
		CancelledAt:        parseTimePtr(d.CancelledAt),
		ArchivedAt:         parseTimePtr(d.ArchivedAt),
		WorkflowId:         nullToPtr(d.WorkflowID),
		WorkflowVersion:    nullInt4ToPtr(d.WorkflowVersion),
	}
	if d.ReviewPolicy != "" {
		rp := apitypes.GoalReviewPolicy(d.ReviewPolicy)
		out.ReviewPolicy = &rp
	}
	if d.BlockReason != "" {
		br := apitypes.GoalBlockReason(d.BlockReason)
		out.BlockReason = &br
	}
	if d.AcceptedOutput.Valid {
		out.AcceptedOutput = jsonObject(json.RawMessage(d.AcceptedOutput.String))
	}
	return out
}

func attemptToAPI(a sqlc.AgentGoalAttempt) apitypes.Attempt {
	out := apitypes.Attempt{
		Id:              a.ID,
		GoalId:          a.GoalID,
		SessionId:       a.SessionID,
		Purpose:         apitypes.AttemptPurpose(a.Purpose),
		AttemptNo:       int(a.AttemptNo),
		Status:          apitypes.AttemptStatus(a.Status),
		CreatedAt:       a.CreatedAt.UTC(),
		UpdatedAt:       a.UpdatedAt.UTC(),
		UserId:          optStr(a.UserID),
		Error:           optStr(a.Error),
		WorkerId:        optStr(a.WorkerID),
		AgentId:         nullToPtr(a.AgentID),
		ExecutorAgentId: nullToPtr(a.ExecutorAgentID),
		InputContext:    jsonObject(a.InputContext),
		Evidence:        jsonObject(a.Evidence),
		Output:          jsonObject(a.Output),
		Gaps:            jsonObject(a.Gaps),
		HeartbeatAt:     parseTimePtr(a.HeartbeatAt),
		LeaseExpiresAt:  parseTimePtr(a.LeaseExpiresAt),
		StartedAt:       parseTimePtr(a.StartedAt),
		FinishedAt:      parseTimePtr(a.FinishedAt),
		RepairRounds:    iptr(int64(a.RepairRounds)),
	}
	if a.FailureClass != "" {
		fc := apitypes.AttemptFailureClass(a.FailureClass)
		out.FailureClass = &fc
	}
	return out
}

func acceptanceEventListAPI(rows []sqlc.AgentGoalAcceptanceEvent) apitypes.AcceptanceEventList {
	out := make([]apitypes.AcceptanceEvent, 0, len(rows))
	for _, e := range rows {
		out = append(out, acceptanceEventToAPI(e))
	}
	return apitypes.AcceptanceEventList{AcceptanceEvents: out}
}

func goalTimelineAPI(rows []sqlc.AgentGoalEvent, nextToken string) apitypes.GoalTimeline {
	items := make([]apitypes.GoalTimelineEvent, 0, len(rows))
	for _, e := range rows {
		items = append(items, goalTimelineEventToAPI(e))
	}
	out := apitypes.GoalTimeline{Events: items}
	if nextToken != "" {
		out.NextPageToken = &nextToken
	}
	return out
}

func goalTimelineEventToAPI(e sqlc.AgentGoalEvent) apitypes.GoalTimelineEvent {
	return apitypes.GoalTimelineEvent{
		Id:        e.ID,
		GoalId:    e.GoalID,
		AttemptId: nullToPtr(e.AttemptID),
		EventType: apitypes.GoalTimelineEventEventType(e.EventType),
		Payload:   jsonMap(e.Payload),
		CreatedAt: e.CreatedAt.UTC(),
	}
}

func acceptanceEventToAPI(e sqlc.AgentGoalAcceptanceEvent) apitypes.AcceptanceEvent {
	out := apitypes.AcceptanceEvent{
		Id:                e.ID,
		GoalId:            e.GoalID,
		Seq:               int(e.Seq),
		ItemId:            e.ItemID,
		ItemKind:          apitypes.AcceptanceEventItemKind(e.ItemKind),
		Result:            apitypes.AcceptanceEventResult(e.Result),
		Authority:         apitypes.AcceptanceEventAuthority(e.Authority),
		CreatedAt:         e.CreatedAt.UTC(),
		AttemptId:         nullToPtr(e.AttemptID),
		Command:           optStr(e.Command),
		CacheKey:          optStr(e.CacheKey),
		ReviewerUserId:    nullToPtr(e.ReviewerUserID),
		ReviewerAttemptId: nullToPtr(e.ReviewerAttemptID),
		Rationale:         optStr(e.Rationale),
		Scope:             optStr(e.Scope),
		ScopeHash:         optStr(e.ScopeHash),
		Detail:            optStr(string(e.Detail)),
	}
	if e.ExitCode.Valid {
		x := int(e.ExitCode.Int64)
		out.ExitCode = &x
	}
	return out
}

func edgeListAPI(rows []sqlc.AgentGoalEdge) apitypes.EdgeList {
	out := make([]apitypes.Edge, 0, len(rows))
	for _, e := range rows {
		out = append(out, edgeToAPI(e))
	}
	return apitypes.EdgeList{Edges: out}
}

func edgeToAPI(e sqlc.AgentGoalEdge) apitypes.Edge {
	return apitypes.Edge{
		GoalId:       e.GoalID,
		UpstreamId:   e.UpstreamID,
		EdgeKind:     apitypes.EdgeEdgeKind(e.EdgeKind),
		OnFailure:    apitypes.EdgeOnFailure(e.OnFailure),
		CreatedAt:    e.CreatedAt.UTC(),
		WaivedAt:     parseTimePtr(e.WaivedAt),
		WaivedByUser: nullToPtr(e.WaivedByUser),
		WaiverReason: optStr(e.WaiverReason),
	}
}

func healthReportToAPI(r goal.HealthReport) apitypes.GoalHealthReport {
	var out apitypes.GoalHealthReport
	jsonRoundTrip(r, &out)
	return out
}

func readinessToAPI(r goal.Readiness) apitypes.Readiness {
	out := apitypes.Readiness{
		State:        apitypes.ReadinessState(r.State),
		Dispatchable: r.Dispatchable,
	}
	if len(r.Reasons) > 0 {
		reasons := make([]apitypes.ReadinessReason, 0, len(r.Reasons))
		for _, rs := range r.Reasons {
			reasons = append(reasons, apitypes.ReadinessReason{
				Type:       optStr(rs.Type),
				UpstreamId: optStr(rs.UpstreamID),
				Detail:     optStr(rs.Detail),
			})
		}
		out.Reasons = &reasons
	}
	return out
}

// parseAcceptanceContract / parseConvergencePolicy decode a stored TEXT JSON
// column into the typed API shape, returning nil for an empty/trivial value so
// the field is omitted.
func parseAcceptanceContract(s json.RawMessage) *apitypes.AcceptanceContract {
	if len(s) == 0 || string(s) == "{}" {
		return nil
	}
	var c apitypes.AcceptanceContract
	if err := json.Unmarshal(s, &c); err != nil {
		return nil
	}
	if c.Policy == nil && (c.Items == nil || len(*c.Items) == 0) {
		return nil
	}
	return &c
}

func parseConvergencePolicy(s json.RawMessage) *apitypes.ConvergencePolicy {
	if len(s) == 0 || string(s) == "{}" {
		return nil
	}
	var c apitypes.ConvergencePolicy
	if err := json.Unmarshal(s, &c); err != nil {
		return nil
	}
	return &c
}

// ── Small pointer/value helpers ──────────────────────────────────────────────

func optStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func iptr(v int64) *int {
	x := int(v)
	return &x
}

func nullToPtr(ns pgtype.Text) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	v := ns.String
	return &v
}

func nullInt4ToPtr(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int32)
	return &v
}

func goalTerminalArg(v *bool) pgtype.Bool {
	if v == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *v, Valid: true}
}

func (s *Server) goalReadiness(ctx context.Context, id string) (goal.Readiness, error) {
	d, err := s.goalQueries.GetGoal(ctx, id)
	if err != nil {
		return goal.Readiness{}, err
	}
	edges, err := s.goalQueries.ListEdgeWithUpstreamState(ctx, id)
	if err != nil {
		return goal.Readiness{}, err
	}
	return goal.Compute(d, edges, time.Now().UTC()), nil
}

func jsonMap(s json.RawMessage) map[string]any {
	if len(s) == 0 || string(s) == "{}" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(s, &m); err != nil {
		return map[string]any{}
	}
	return m
}

func jsonObject(s json.RawMessage) *map[string]any {
	if len(s) == 0 || string(s) == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(s, &m); err != nil {
		return nil
	}
	return &m
}

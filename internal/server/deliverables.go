package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/deliverable"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SetDeliverableService wires the deliverable system into the admin server.
// When unset, every /api/deliverables route returns 503.
func (s *Server) SetDeliverableService(svc *deliverable.Service) {
	s.deliverableSvc = svc
}

func (s *Server) deliverablesReady() bool { return s.deliverableSvc != nil }

// deliverableAuth gates a handler on the deliverable system being wired and an
// authenticated caller, returning the caller's user id.
func (s *Server) deliverableAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.deliverablesReady() {
		writeError(w, http.StatusServiceUnavailable, "deliverables unavailable")
		return "", false
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	return info.UserID, true
}

// deliverableError maps the package's sentinel errors to HTTP status codes:
// not-found → 404, validation → 400, lifecycle/guard → 409, else 500.
func deliverableError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, deliverable.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, deliverable.ErrInvalidContract),
		errors.Is(err, deliverable.ErrInvalidDecomposition),
		errors.Is(err, deliverable.ErrDepthExceeded),
		errors.Is(err, deliverable.ErrCycle),
		errors.Is(err, deliverable.ErrInvalidEvidence),
		errors.Is(err, deliverable.ErrInvalidVerdict):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, deliverable.ErrInvalidTransition),
		errors.Is(err, deliverable.ErrPlanGate),
		errors.Is(err, deliverable.ErrBudgetExhausted),
		errors.Is(err, deliverable.ErrConcurrencyCap),
		errors.Is(err, deliverable.ErrStaleProjection):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// loadDeliverable fetches a deliverable by id, enforcing ownership. A row owned
// by another user is reported as 404 to avoid leaking its existence; a scoped
// token whose agent differs is 403. Returns false (after writing the error) on
// any miss.
func (s *Server) loadDeliverable(ctx context.Context, w http.ResponseWriter, userID, id string) (sqlc.AgentDlvDeliverable, bool) {
	d, err := s.deliverableSvc.GetDeliverable(ctx, id)
	if err != nil {
		if errors.Is(err, deliverable.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return sqlc.AgentDlvDeliverable{}, false
		}
		deliverableError(w, err)
		return sqlc.AgentDlvDeliverable{}, false
	}
	if d.UserID != userID {
		writeError(w, http.StatusNotFound, "not_found")
		return sqlc.AgentDlvDeliverable{}, false
	}
	if info := UserFromContext(ctx); info != nil && info.Scoped != nil && d.AgentID != info.Scoped.AgentID {
		writeError(w, http.StatusForbidden, "permission denied")
		return sqlc.AgentDlvDeliverable{}, false
	}
	return d, true
}

// loadRevision fetches a revision and enforces that it belongs to the given
// deliverable (path parentage), reporting a mismatch/miss as 404.
func (s *Server) loadRevision(ctx context.Context, w http.ResponseWriter, d sqlc.AgentDlvDeliverable, revID string) (sqlc.AgentDlvRevision, bool) {
	rev, err := s.deliverableSvc.GetRevision(ctx, revID)
	if err != nil || rev.DeliverableID != d.ID {
		writeError(w, http.StatusNotFound, "not_found")
		return sqlc.AgentDlvRevision{}, false
	}
	return rev, true
}

// ── List / CRUD ──────────────────────────────────────────────────────────────

// ListDeliverables lists root deliverables (goals) by default; `?parent={id}`
// lists a composite's children and `?root={id}` lists a whole tree.
func (s *Server) ListDeliverables(w http.ResponseWriter, r *http.Request, params apiserver.ListDeliverablesParams) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	if params.Parent != nil {
		if _, ok := s.loadDeliverable(ctx, w, userID, *params.Parent); !ok {
			return
		}
		rows, err := s.deliverableSvc.ListChildren(ctx, *params.Parent)
		if err != nil {
			deliverableError(w, err)
			return
		}
		writeData(w, http.StatusOK, deliverableListAPI(rows, "", nil))
		return
	}
	if params.Root != nil {
		if _, ok := s.loadDeliverable(ctx, w, userID, *params.Root); !ok {
			return
		}
		rows, err := s.deliverableSvc.ListSubtree(ctx, *params.Root)
		if err != nil {
			deliverableError(w, err)
			return
		}
		writeData(w, http.StatusOK, deliverableListAPI(rows, "", nil))
		return
	}

	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter := deliverable.DeliverableFilter{}
	if params.AgentId != nil {
		filter.AgentID = *params.AgentId
	}
	if params.Lifecycle != nil {
		filter.Lifecycle = *params.Lifecycle
	}
	if params.ProjectId != nil {
		filter.ProjectID = *params.ProjectId
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
	rows, err := s.deliverableSvc.ListDeliverables(ctx, userID, filter, int64(limit+1), int64(offset))
	if err != nil {
		deliverableError(w, err)
		return
	}
	page, next := nextPageTokenForRows(rows, limit, offset)
	var total *int
	if n, err := s.deliverableSvc.CountDeliverables(ctx, userID, filter); err == nil {
		v := int(n)
		total = &v
	}
	writeData(w, http.StatusOK, deliverableListAPI(page, next, total))
}

// CreateDeliverable mints a root deliverable (goal). With activate=true a leaf is
// activated immediately (direct run); the flag is ignored for a composite, which
// must be planned via the revisions endpoints first.
func (s *Server) CreateDeliverable(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var body apitypes.CreateDeliverableRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Title == "" || body.AgentId == "" {
		writeError(w, http.StatusBadRequest, "title and agent_id are required")
		return
	}
	in := deliverable.CreateInput{UserID: userID, AgentID: body.AgentId, Title: body.Title}
	if body.Intent != nil {
		in.Intent = *body.Intent
	}
	if body.ProjectId != nil {
		in.ProjectID = *body.ProjectId
	}
	if body.Kind != nil {
		in.Kind = string(*body.Kind)
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
	created, err := s.deliverableSvc.CreateDeliverable(ctx, in)
	if err != nil {
		deliverableError(w, err)
		return
	}
	if body.Activate != nil && *body.Activate && created.Kind == deliverable.KindLeaf {
		activated, err := s.deliverableSvc.Activate(ctx, created.ID)
		if err != nil {
			deliverableError(w, err)
			return
		}
		created = activated
	}
	writeData(w, http.StatusCreated, deliverableToAPI(created))
}

// GetDeliverable returns one deliverable.
func (s *Server) GetDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	d, ok := s.loadDeliverable(r.Context(), w, userID, id)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, deliverableToAPI(d))
}

// UpdateDeliverable applies a partial metadata edit (PATCH).
func (s *Server) UpdateDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.UpdateDeliverableRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in := deliverable.UpdateInput{Title: body.Title, Intent: body.Intent}
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
	updated, err := s.deliverableSvc.UpdateDeliverable(ctx, id, in)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, deliverableToAPI(updated))
}

// DeleteDeliverable archives a deliverable (audit-safe delete).
func (s *Server) DeleteDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	if err := s.deliverableSvc.Archive(ctx, id); err != nil {
		deliverableError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Lifecycle commands ───────────────────────────────────────────────────────

// ActivateDeliverable runs the plan gate (draft → ready).
func (s *Server) ActivateDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	d, err := s.deliverableSvc.Activate(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, deliverableToAPI(d))
}

// CancelDeliverable cancels a deliverable, cascading over its non-terminal subtree.
func (s *Server) CancelDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.CancelRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.deliverableSvc.Cancel(ctx, id, derefStr(body.Reason), deliverable.UserActor(userID)); err != nil {
		deliverableError(w, err)
		return
	}
	s.respondDeliverable(ctx, w, id)
}

// AbandonDeliverable is the human give-up on a budget-exhausted block.
func (s *Server) AbandonDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.AbandonRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.deliverableSvc.Abandon(ctx, id, derefStr(body.Reason), deliverable.UserActor(userID)); err != nil {
		deliverableError(w, err)
		return
	}
	s.respondDeliverable(ctx, w, id)
}

// ReattemptDeliverable raises the budget on a blocked deliverable and resumes it.
func (s *Server) ReattemptDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	if err := s.deliverableSvc.Reattempt(ctx, id, deliverable.UserActor(userID)); err != nil {
		deliverableError(w, err)
		return
	}
	s.respondDeliverable(ctx, w, id)
}

// UnarchiveDeliverable restores an archived deliverable to default lists.
func (s *Server) UnarchiveDeliverable(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	if err := s.deliverableSvc.Unarchive(ctx, id); err != nil {
		deliverableError(w, err)
		return
	}
	s.respondDeliverable(ctx, w, id)
}

// GetDeliverableReadiness returns the computed dispatchability view.
func (s *Server) GetDeliverableReadiness(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	rd, err := s.deliverableSvc.GetReadiness(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, readinessToAPI(rd))
}

// ── Sub-resource reads ───────────────────────────────────────────────────────

// ListDeliverableChildren lists a composite's direct children.
func (s *Server) ListDeliverableChildren(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.deliverableSvc.ListChildren(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, deliverableListAPI(rows, "", nil))
}

// ListAttempts lists a deliverable's attempts (newest first).
func (s *Server) ListAttempts(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.deliverableSvc.ListAttempts(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	out := make([]apitypes.Attempt, 0, len(rows))
	for _, a := range rows {
		out = append(out, attemptToAPI(a))
	}
	writeData(w, http.StatusOK, apitypes.AttemptList{Attempts: out})
}

// GetAttempt returns one attempt, scoped to its deliverable.
func (s *Server) GetAttempt(w http.ResponseWriter, r *http.Request, id string, attemptId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	a, err := s.deliverableSvc.GetAttempt(ctx, attemptId)
	if err != nil || a.DeliverableID != id {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeData(w, http.StatusOK, attemptToAPI(a))
}

// ListAcceptanceEvents lists the acceptance ledger (audit trail, in fold order).
func (s *Server) ListAcceptanceEvents(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.deliverableSvc.ListAcceptanceEvents(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, acceptanceEventListAPI(rows))
}

// ── Verdict + edges ──────────────────────────────────────────────────────────

// SubmitVerdict appends a human verdict against a contract item and re-folds.
func (s *Server) SubmitVerdict(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.VerdictRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in := deliverable.VerdictInput{
		DeliverableID:  id,
		ItemID:         body.ItemId,
		Result:         string(body.Result),
		Rationale:      derefStr(body.Rationale),
		Scope:          derefStr(body.Scope),
		ScopeHash:      derefStr(body.ScopeHash),
		ReviewerUserID: userID,
	}
	if err := s.deliverableSvc.SubmitVerdict(ctx, in); err != nil {
		deliverableError(w, err)
		return
	}
	// The verdict is the highest-seq event after the append; surface it.
	events, err := s.deliverableSvc.ListAcceptanceEvents(ctx, id)
	if err != nil || len(events) == 0 {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusCreated, acceptanceEventToAPI(events[len(events)-1]))
}

// ListEdges lists a deliverable's upstream dependency edges.
func (s *Server) ListEdges(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.deliverableSvc.ListEdges(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, edgeListAPI(rows))
}

// AddEdge inserts an upstream dependency edge (cycle-checked).
func (s *Server) AddEdge(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
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
	// as the downstream, or a caller could wire in another tenant's deliverable
	// and pull its frozen accepted_output into their own attempt's input context.
	if _, ok := s.loadDeliverable(ctx, w, userID, body.UpstreamId); !ok {
		return
	}
	kind := deliverable.EdgeHard
	if body.Kind != nil {
		kind = string(*body.Kind)
	}
	onFailure := deliverable.OnFailureBlock
	if body.OnFailure != nil {
		onFailure = string(*body.OnFailure)
	}
	edge, err := s.deliverableSvc.AddEdge(ctx, id, body.UpstreamId, kind, onFailure)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusCreated, edgeToAPI(edge))
}

// WaiveEdge waives a hard edge so a blocked(dep) downstream can proceed.
func (s *Server) WaiveEdge(w http.ResponseWriter, r *http.Request, id string, upstreamId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.WaiveRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if err := s.deliverableSvc.WaiveEdge(ctx, id, upstreamId, derefStr(body.Reason), deliverable.UserActor(userID)); err != nil {
		deliverableError(w, err)
		return
	}
	edges, err := s.deliverableSvc.ListEdges(ctx, id)
	if err != nil {
		deliverableError(w, err)
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

// ── Revisions (decomposition) ────────────────────────────────────────────────

// ListRevisions lists a composite's decomposition revisions (newest first).
func (s *Server) ListRevisions(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	rows, err := s.deliverableSvc.ListRevisions(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, revisionListAPI(rows))
}

// PutRevision authors/stages a decomposition edit as a new draft revision.
func (s *Server) PutRevision(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	var body apitypes.DecompositionContent
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rev, err := s.deliverableSvc.PutRevision(ctx, id, toDecomposition(body), "")
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, revisionToAPI(rev))
}

// StartDecomposition begins a composite's decomposition in a planning session.
func (s *Server) StartDecomposition(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, ok := s.loadDeliverable(ctx, w, userID, id); !ok {
		return
	}
	att, err := s.deliverableSvc.StartDecomposition(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, apitypes.DecompositionSession{PlanningSessionId: att.SessionID})
}

// AcceptRevision auto-accepts a draft revision (review_policy=none).
func (s *Server) AcceptRevision(w http.ResponseWriter, r *http.Request, id string, revId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d, ok := s.loadDeliverable(ctx, w, userID, id)
	if !ok {
		return
	}
	if _, ok := s.loadRevision(ctx, w, d, revId); !ok {
		return
	}
	rev, err := s.deliverableSvc.AcceptRevision(ctx, revId, deliverable.UserActor(userID))
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, revisionToAPI(rev))
}

// SubmitRevisionReview moves a draft revision into human review.
func (s *Server) SubmitRevisionReview(w http.ResponseWriter, r *http.Request, id string, revId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d, ok := s.loadDeliverable(ctx, w, userID, id)
	if !ok {
		return
	}
	if _, ok := s.loadRevision(ctx, w, d, revId); !ok {
		return
	}
	rev, err := s.deliverableSvc.SubmitRevisionReview(ctx, revId)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, revisionToAPI(rev))
}

// ApproveRevision accepts an in_review revision (human approval).
func (s *Server) ApproveRevision(w http.ResponseWriter, r *http.Request, id string, revId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d, ok := s.loadDeliverable(ctx, w, userID, id)
	if !ok {
		return
	}
	if _, ok := s.loadRevision(ctx, w, d, revId); !ok {
		return
	}
	var body apitypes.DecisionRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	rev, err := s.deliverableSvc.ApproveRevision(ctx, revId, deliverable.UserActor(userID))
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, revisionToAPI(rev))
}

// RejectRevision rejects an in_review revision (composite stays active for rework).
func (s *Server) RejectRevision(w http.ResponseWriter, r *http.Request, id string, revId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d, ok := s.loadDeliverable(ctx, w, userID, id)
	if !ok {
		return
	}
	if _, ok := s.loadRevision(ctx, w, d, revId); !ok {
		return
	}
	var body apitypes.DecisionRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	rev, err := s.deliverableSvc.RejectRevision(ctx, revId, derefStr(body.Reason), deliverable.UserActor(userID))
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, revisionToAPI(rev))
}

// RequestChangesRevision sends an in_review revision back to draft for edits.
func (s *Server) RequestChangesRevision(w http.ResponseWriter, r *http.Request, id string, revId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d, ok := s.loadDeliverable(ctx, w, userID, id)
	if !ok {
		return
	}
	if _, ok := s.loadRevision(ctx, w, d, revId); !ok {
		return
	}
	var body apitypes.DecisionRequest
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	rev, err := s.deliverableSvc.RequestChangesRevision(ctx, revId, derefStr(body.Reason), deliverable.UserActor(userID))
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, revisionToAPI(rev))
}

// MaterializeRevision creates the revision's children + edges and lists them.
func (s *Server) MaterializeRevision(w http.ResponseWriter, r *http.Request, id string, revId string) {
	userID, ok := s.deliverableAuth(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d, ok := s.loadDeliverable(ctx, w, userID, id)
	if !ok {
		return
	}
	if _, ok := s.loadRevision(ctx, w, d, revId); !ok {
		return
	}
	children, err := s.deliverableSvc.MaterializeRevision(ctx, revId)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, deliverableListAPI(children, "", nil))
}

// ── Shared helpers ───────────────────────────────────────────────────────────

// respondDeliverable re-fetches a deliverable and writes it; used by the
// command handlers whose service method returns only an error.
func (s *Server) respondDeliverable(ctx context.Context, w http.ResponseWriter, id string) {
	d, err := s.deliverableSvc.GetDeliverable(ctx, id)
	if err != nil {
		deliverableError(w, err)
		return
	}
	writeData(w, http.StatusOK, deliverableToAPI(d))
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

func toContract(c apitypes.AcceptanceContract) deliverable.AcceptanceContract {
	var out deliverable.AcceptanceContract
	jsonRoundTrip(c, &out)
	return out
}

func toConvergence(c apitypes.ConvergencePolicy) deliverable.ConvergencePolicy {
	var out deliverable.ConvergencePolicy
	jsonRoundTrip(c, &out)
	return out
}

func toDecomposition(c apitypes.DecompositionContent) deliverable.DecompositionContent {
	var out deliverable.DecompositionContent
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

func deliverableListAPI(rows []sqlc.AgentDlvDeliverable, nextToken string, total *int) apitypes.DeliverableList {
	items := make([]apitypes.Deliverable, 0, len(rows))
	for _, d := range rows {
		items = append(items, deliverableToAPI(d))
	}
	out := apitypes.DeliverableList{Deliverables: items, Total: total}
	if nextToken != "" {
		out.NextPageToken = &nextToken
	}
	return out
}

func deliverableToAPI(d sqlc.AgentDlvDeliverable) apitypes.Deliverable {
	out := apitypes.Deliverable{
		Id:                 d.ID,
		UserId:             d.UserID,
		AgentId:            d.AgentID,
		RootId:             d.RootID,
		Depth:              int(d.Depth),
		Position:           int(d.Position),
		SessionId:          d.SessionID,
		Title:              d.Title,
		Kind:               apitypes.DeliverableKind(d.Kind),
		Priority:           apitypes.DeliverablePriority(d.Priority),
		Required:           d.Required == 1,
		Lifecycle:          apitypes.DeliverableLifecycle(d.Lifecycle),
		AcceptanceState:    apitypes.DeliverableAcceptanceState(d.AcceptanceState),
		CreatedAt:          parseTime(d.CreatedAt),
		UpdatedAt:          parseTime(d.UpdatedAt),
		Intent:             optStr(d.Intent),
		AcceptanceSeq:      iptr(d.AcceptanceSeq),
		AttemptCount:       iptr(d.AttemptCount),
		RequiredTotal:      iptr(d.RequiredTotal),
		RequiredAccepted:   iptr(d.RequiredAccepted),
		RequiredFailed:     iptr(d.RequiredFailed),
		RequiredBlocked:    iptr(d.RequiredBlocked),
		AcceptanceContract: parseAcceptanceContract(d.AcceptanceContract),
		ConvergencePolicy:  parseConvergencePolicy(d.ConvergencePolicy),
		Context:            jsonObject(d.Context),
		DispatchHint:       jsonObject(d.DispatchHint),
		ProjectId:          nullToPtr(d.ProjectID),
		ParentId:           nullToPtr(d.ParentID),
		ActiveAttemptId:    nullToPtr(d.ActiveAttemptID),
		AcceptedRevisionId: nullToPtr(d.AcceptedRevisionID),
		AcceptedAt:         parseTimePtr(d.AcceptedAt),
		CancelledAt:        parseTimePtr(d.CancelledAt),
		ArchivedAt:         parseTimePtr(d.ArchivedAt),
	}
	if d.ReviewPolicy != "" {
		rp := apitypes.DeliverableReviewPolicy(d.ReviewPolicy)
		out.ReviewPolicy = &rp
	}
	if d.BlockReason != "" {
		br := apitypes.DeliverableBlockReason(d.BlockReason)
		out.BlockReason = &br
	}
	if d.AcceptedOutput.Valid {
		out.AcceptedOutput = jsonObject(d.AcceptedOutput.String)
	}
	return out
}

func attemptToAPI(a sqlc.AgentDlvAttempt) apitypes.Attempt {
	return apitypes.Attempt{
		Id:              a.ID,
		DeliverableId:   a.DeliverableID,
		SessionId:       a.SessionID,
		Purpose:         apitypes.AttemptPurpose(a.Purpose),
		AttemptNo:       int(a.AttemptNo),
		Status:          apitypes.AttemptStatus(a.Status),
		CreatedAt:       parseTime(a.CreatedAt),
		UpdatedAt:       parseTime(a.UpdatedAt),
		UserId:          optStr(a.UserID),
		Error:           optStr(a.Error),
		WorkerId:        optStr(a.WorkerID),
		AgentId:         nullToPtr(a.AgentID),
		ExecutorAgentId: nullToPtr(a.ExecutorAgentID),
		RevisionId:      nullToPtr(a.RevisionID),
		InputContext:    jsonObject(a.InputContext),
		Evidence:        jsonObject(a.Evidence),
		Output:          jsonObject(a.Output),
		Gaps:            jsonObject(a.Gaps),
		HeartbeatAt:     parseTimePtr(a.HeartbeatAt),
		LeaseExpiresAt:  parseTimePtr(a.LeaseExpiresAt),
		StartedAt:       parseTimePtr(a.StartedAt),
		FinishedAt:      parseTimePtr(a.FinishedAt),
	}
}

func acceptanceEventListAPI(rows []sqlc.AgentDlvAcceptanceEvent) apitypes.AcceptanceEventList {
	out := make([]apitypes.AcceptanceEvent, 0, len(rows))
	for _, e := range rows {
		out = append(out, acceptanceEventToAPI(e))
	}
	return apitypes.AcceptanceEventList{AcceptanceEvents: out}
}

func acceptanceEventToAPI(e sqlc.AgentDlvAcceptanceEvent) apitypes.AcceptanceEvent {
	out := apitypes.AcceptanceEvent{
		Id:                e.ID,
		DeliverableId:     e.DeliverableID,
		Seq:               int(e.Seq),
		ItemId:            e.ItemID,
		ItemKind:          apitypes.AcceptanceEventItemKind(e.ItemKind),
		Result:            apitypes.AcceptanceEventResult(e.Result),
		Authority:         apitypes.AcceptanceEventAuthority(e.Authority),
		CreatedAt:         parseTime(e.CreatedAt),
		AttemptId:         nullToPtr(e.AttemptID),
		Command:           optStr(e.Command),
		CacheKey:          optStr(e.CacheKey),
		ReviewerUserId:    nullToPtr(e.ReviewerUserID),
		ReviewerAttemptId: nullToPtr(e.ReviewerAttemptID),
		Rationale:         optStr(e.Rationale),
		Scope:             optStr(e.Scope),
		ScopeHash:         optStr(e.ScopeHash),
		Detail:            optStr(e.Detail),
	}
	if e.ExitCode.Valid {
		x := int(e.ExitCode.Int64)
		out.ExitCode = &x
	}
	return out
}

func edgeListAPI(rows []sqlc.AgentDlvEdge) apitypes.EdgeList {
	out := make([]apitypes.Edge, 0, len(rows))
	for _, e := range rows {
		out = append(out, edgeToAPI(e))
	}
	return apitypes.EdgeList{Edges: out}
}

func edgeToAPI(e sqlc.AgentDlvEdge) apitypes.Edge {
	return apitypes.Edge{
		DeliverableId: e.DeliverableID,
		UpstreamId:    e.UpstreamID,
		EdgeKind:      apitypes.EdgeEdgeKind(e.EdgeKind),
		OnFailure:     apitypes.EdgeOnFailure(e.OnFailure),
		CreatedAt:     parseTime(e.CreatedAt),
		WaivedAt:      parseTimePtr(e.WaivedAt),
		WaivedByUser:  nullToPtr(e.WaivedByUser),
		WaiverReason:  optStr(e.WaiverReason),
	}
}

func revisionListAPI(rows []sqlc.AgentDlvRevision) apitypes.RevisionList {
	out := make([]apitypes.Revision, 0, len(rows))
	for _, rev := range rows {
		out = append(out, revisionToAPI(rev))
	}
	return apitypes.RevisionList{Revisions: out}
}

func revisionToAPI(r sqlc.AgentDlvRevision) apitypes.Revision {
	return apitypes.Revision{
		Id:                r.ID,
		DeliverableId:     r.DeliverableID,
		RevisionNo:        int(r.RevisionNo),
		Status:            apitypes.RevisionStatus(r.Status),
		ReviewPolicy:      apitypes.RevisionReviewPolicy(r.ReviewPolicy),
		CreatedAt:         parseTime(r.CreatedAt),
		UpdatedAt:         parseTime(r.UpdatedAt),
		Content:           parseDecompositionContent(r.Content),
		SourceAttemptId:   nullToPtr(r.SourceAttemptID),
		PlanningSessionId: nullToPtr(r.PlanningSessionID),
		AcceptedAt:        parseTimePtr(r.AcceptedAt),
		MaterializedAt:    parseTimePtr(r.MaterializedAt),
	}
}

func readinessToAPI(r deliverable.Readiness) apitypes.Readiness {
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

// parseAcceptanceContract / parseConvergencePolicy / parseDecompositionContent
// decode a stored TEXT JSON column into the typed API shape, returning nil for an
// empty/trivial value so the field is omitted.
func parseAcceptanceContract(s string) *apitypes.AcceptanceContract {
	if s == "" || s == "{}" {
		return nil
	}
	var c apitypes.AcceptanceContract
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil
	}
	if c.Policy == nil && (c.Items == nil || len(*c.Items) == 0) {
		return nil
	}
	return &c
}

func parseConvergencePolicy(s string) *apitypes.ConvergencePolicy {
	if s == "" || s == "{}" {
		return nil
	}
	var c apitypes.ConvergencePolicy
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil
	}
	return &c
}

func parseDecompositionContent(s string) *apitypes.DecompositionContent {
	if s == "" || s == "{}" {
		return nil
	}
	var c apitypes.DecompositionContent
	if err := json.Unmarshal([]byte(s), &c); err != nil {
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

func nullToPtr(ns sql.NullString) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	v := ns.String
	return &v
}

func jsonObject(s string) *map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return &m
}

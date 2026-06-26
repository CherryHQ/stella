package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/workflow"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SetWorkflowService wires the workflow system into the admin server. When
// unset, every /api/workflows route (and save-as-workflow) returns 503.
func (s *Server) SetWorkflowService(svc *workflow.Service) {
	s.workflowSvc = svc
}

// workflowAuth gates a handler on the workflow system being wired and an
// authenticated caller, returning the caller's user id.
func (s *Server) workflowAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.workflowSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "workflows unavailable")
		return "", false
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	return info.UserID, true
}

// workflowError maps the package's sentinels to HTTP status codes: not-found →
// 404, ineligible source → 409, validation → 400, else 500. The workflow service
// translates goal-subsystem errors into these sentinels at its boundary, so this
// handler never reasons about the goal package's error taxonomy.
func workflowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflow.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, workflow.ErrSourceNotEligible):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, workflow.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("workflow handler internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// ListWorkflows lists the caller's workflows.
func (s *Server) ListWorkflows(w http.ResponseWriter, r *http.Request, params apiserver.ListWorkflowsParams) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter := workflow.Filter{}
	if params.AgentId != nil {
		filter.AgentID = *params.AgentId
	}
	if params.Q != nil {
		filter.Q = *params.Q
	}
	rows, err := s.workflowSvc.List(r.Context(), userID, filter, int64(limit), int64(offset))
	if err != nil {
		workflowError(w, err)
		return
	}
	total, err := s.workflowSvc.Count(r.Context(), userID, filter)
	if err != nil {
		workflowError(w, err)
		return
	}
	next := ""
	if len(rows) == limit && int64(offset+limit) < total {
		next = encodeOffsetToken(offset + limit)
	}
	writeData(w, http.StatusOK, workflowListAPI(rows, next, int(total)))
}

// CreateWorkflow stores a hand-authored workflow.
func (s *Server) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	var body apitypes.WorkflowCreateInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// The caller must be able to use the managing agent: at instantiate time a
	// workflow runs goal trees under this agent's identity, so an unchecked
	// agent_id would let a user execute under another tenant's agent.
	if _, code, msg := s.requireAgentAccess(r.Context(), body.AgentId); code != 0 {
		writeError(w, code, msg)
		return
	}
	plan, err := decodeFrozenPlan(body.Plan)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid plan: "+err.Error())
		return
	}
	in := workflow.CreateInput{Name: body.Name, AgentID: body.AgentId, Plan: plan}
	if body.Intent != nil {
		in.Intent = *body.Intent
	}
	if body.AcceptanceContract != nil {
		c, err := decodeContract(*body.AcceptanceContract)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid acceptance_contract: "+err.Error())
			return
		}
		in.Contract = c
	}
	if body.ConvergencePolicy != nil {
		p, err := decodePolicy(*body.ConvergencePolicy)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid convergence_policy: "+err.Error())
			return
		}
		in.Convergence = p
	}
	wf, err := s.workflowSvc.Create(r.Context(), userID, in)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeData(w, http.StatusCreated, workflowToAPI(wf))
}

// GetWorkflow returns one workflow owned by the caller.
func (s *Server) GetWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	wf, err := s.workflowSvc.Get(r.Context(), userID, id)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeData(w, http.StatusOK, workflowToAPI(wf))
}

// UpdateWorkflow edits a workflow's name/intent.
func (s *Server) UpdateWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	var body apitypes.WorkflowUpdateInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, intent := "", ""
	if body.Name != nil {
		name = *body.Name
	}
	if body.Intent != nil {
		intent = *body.Intent
	}
	wf, err := s.workflowSvc.UpdateMeta(r.Context(), userID, id, name, intent)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeData(w, http.StatusOK, workflowToAPI(wf))
}

// DeleteWorkflow removes a workflow owned by the caller.
func (s *Server) DeleteWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	if err := s.workflowSvc.Delete(r.Context(), userID, id); err != nil {
		workflowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// InstantiateWorkflow materializes a workflow into a live goal tree and returns
// the new root goal.
func (s *Server) InstantiateWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	var body apitypes.InstantiateWorkflowInput
	_ = decodeJSON(r, &body) // optional body; empty is fine
	projectID := ""
	if body.ProjectId != nil {
		projectID = *body.ProjectId
	}
	if !s.validateWorkflowProject(w, r, userID, projectID) {
		return
	}
	root, err := s.workflowSvc.Instantiate(r.Context(), userID, id, projectID)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeData(w, http.StatusCreated, goalToAPI(root))
}

// SaveGoalAsWorkflow freezes an accepted composite goal into a workflow.
func (s *Server) SaveGoalAsWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	var body apitypes.SaveAsWorkflowInput
	_ = decodeJSON(r, &body) // optional body; empty is fine
	in := workflow.SaveAsInput{SourceGoalID: id}
	if body.Name != nil {
		in.Name = *body.Name
	}
	wf, err := s.workflowSvc.SaveGoalAsWorkflow(r.Context(), userID, in)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeData(w, http.StatusCreated, workflowToAPI(wf))
}

// ScheduleWorkflow creates a scheduler job that instantiates the workflow on a
// recurring or one-time schedule. The managing agent is taken from the workflow
// itself, so the dispatcher routes runs to the same agent that owns the plan.
func (s *Server) ScheduleWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	userID, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	if s.schedulerSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler unavailable")
		return
	}
	wf, err := s.workflowSvc.Get(r.Context(), userID, id)
	if err != nil {
		workflowError(w, err)
		return
	}
	var body apitypes.ScheduleWorkflowInput
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	projectID := derefStr(body.ProjectId)
	if !s.validateWorkflowProject(w, r, userID, projectID) {
		return
	}
	sched := scheduler.Schedule{Cron: derefStr(body.Cron), Every: derefStr(body.Every), At: derefStr(body.At)}
	agentID := ""
	if wf.AgentID.Valid {
		agentID = wf.AgentID.String
	}
	job, err := s.schedulerSvc.AddWorkflowJob(body.Name, wf.ID, projectID, sched, agentID, userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, s.schedulerJobToAPI(job))
}

// validateWorkflowProject confirms that a non-empty projectID belongs to the
// caller, writing a 404 and returning false otherwise. An empty projectID (no
// project scoping) always passes.
func (s *Server) validateWorkflowProject(w http.ResponseWriter, r *http.Request, userID, projectID string) bool {
	if projectID == "" {
		return true
	}
	if _, err := s.q.GetProject(r.Context(), sqlc.GetProjectParams{ID: projectID, UserID: userID}); err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return false
	}
	return true
}

// ── mappers ──────────────────────────────────────────────────────────────────

func workflowListAPI(rows []sqlc.AgentWorkflow, nextToken string, total int) apitypes.WorkflowList {
	items := make([]apitypes.Workflow, 0, len(rows))
	for _, r := range rows {
		items = append(items, workflowToAPI(r))
	}
	out := apitypes.WorkflowList{Workflows: items, Total: &total}
	if nextToken != "" {
		out.NextPageToken = &nextToken
	}
	return out
}

func workflowToAPI(wf sqlc.AgentWorkflow) apitypes.Workflow {
	out := apitypes.Workflow{
		Id:                 wf.ID,
		OwnerKind:          wf.OwnerKind,
		Name:               wf.Name,
		Version:            wf.Version,
		Intent:             optStr(wf.Intent),
		AcceptanceContract: jsonObject(wf.AcceptanceContract),
		ConvergencePolicy:  jsonObject(wf.ConvergencePolicy),
		Plan:               jsonObject(wf.Plan),
		CreatedAt:          wf.CreatedAt.UTC(),
		UpdatedAt:          wf.UpdatedAt.UTC(),
	}
	if wf.AgentID.Valid {
		out.AgentId = &wf.AgentID.String
	}
	if wf.SourceGoalID.Valid {
		out.SourceGoalId = &wf.SourceGoalID.String
	}
	return out
}

// decodeFrozenPlan / decodeContract / decodePolicy round-trip the opaque API
// objects through JSON into the typed goal structures. They return the decode
// error rather than swallowing it: a type-mismatched contract/policy would
// otherwise silently become a zero-value (trivial, auto-accept) struct instead
// of a clear 400.
func decodeFrozenPlan(m map[string]any) (goal.FrozenPlan, error) {
	var p goal.FrozenPlan
	err := remarshal(m, &p)
	return p, err
}

func decodeContract(m map[string]any) (goal.AcceptanceContract, error) {
	var c goal.AcceptanceContract
	err := remarshal(m, &c)
	return c, err
}

func decodePolicy(m map[string]any) (goal.ConvergencePolicy, error) {
	var p goal.ConvergencePolicy
	err := remarshal(m, &p)
	return p, err
}

func remarshal(src any, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

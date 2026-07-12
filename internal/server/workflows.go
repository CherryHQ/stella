package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/goal"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *Server) workflowsReady() bool { return s.workflowSvc != nil }

func (s *Server) workflowAuth(w http.ResponseWriter, r *http.Request) (*AuthInfo, bool) {
	if !s.workflowsReady() {
		writeCapabilityUnavailable(w, capWorkflow)
		return nil, false
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	return info, true
}

func workflowError(w http.ResponseWriter, err error) {
	switch {
	case isNotFound(err):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, workflowpkg.ErrInvalidWorkflowInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, workflowpkg.ErrWorkflowHasRuns), errors.Is(err, workflowpkg.ErrWorkflowHasSchedulerJob), errors.Is(err, workflowpkg.ErrRunAlreadyFailed), errors.Is(err, workflowpkg.ErrWorkflowVersionConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, goal.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "goal is not an accepted root composite")
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found")
	default:
		slog.Error("workflow handler internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) SaveGoalAsWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	info, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	goalRow, ok := s.loadGoal(r.Context(), w, toolIdentity(info), id)
	if !ok {
		return
	}
	// Saving captures an agent-bound executable workflow; authorize the
	// initiator before persisting it rather than relying on a future worker.
	if _, code, msg := s.requireAgentUse(r.Context(), goalRow.AgentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	var body apitypes.SaveGoalAsWorkflowRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	wf, err := s.workflowSvc.SaveGoalAsWorkflow(r.Context(), workflowpkg.SaveInput{UserID: info.UserID, AgentID: goalRow.AgentID, GoalID: id, Name: body.Name, Inputs: workflowInputSpecs(body.Inputs)})
	if err != nil {
		workflowError(w, err)
		return
	}
	writeData(w, http.StatusCreated, workflowToAPI(wf))
}

func (s *Server) ListWorkflows(w http.ResponseWriter, r *http.Request, params apiserver.ListWorkflowsParams) {
	info, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	agentID := derefStr(params.AgentId)
	rows, err := s.workflowSvc.List(r.Context(), info.UserID, agentID)
	if err != nil {
		workflowError(w, err)
		return
	}
	workflows := make([]apitypes.Workflow, 0, len(rows))
	for _, row := range rows {
		workflows = append(workflows, workflowToAPI(row))
	}
	writeData(w, http.StatusOK, apitypes.WorkflowList{Workflows: workflows})
}

func (s *Server) GetWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	info, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	agentID := workflowAgentScope(info)
	wf, err := s.workflowSvc.Get(r.Context(), info.UserID, agentID, id)
	if err != nil {
		workflowError(w, err)
		return
	}
	writeData(w, http.StatusOK, workflowToAPI(wf))
}

func (s *Server) ListWorkflowRuns(w http.ResponseWriter, r *http.Request, id string, params apiserver.ListWorkflowRunsParams) {
	info, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, total, err := s.workflowSvc.ListRuns(r.Context(), info.UserID, workflowAgentScope(info), id, int32(limit+1), int32(offset))
	if err != nil {
		workflowError(w, err)
		return
	}
	page, next := nextPageTokenForRows(rows, limit, offset)
	runs := make([]apitypes.WorkflowRun, 0, len(page))
	for _, row := range page {
		run := workflowRunToAPI(sqlc.AgentWorkflowRun{ID: row.ID, WorkflowID: row.WorkflowID, WorkflowVersion: row.WorkflowVersion, IdempotencyKey: row.IdempotencyKey, RootGoalID: row.RootGoalID, Status: row.Status, Inputs: row.Inputs, PlanHash: row.PlanHash, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
		run.RootLifecycle = nullToPtr(row.RootLifecycle)
		run.RootBlockReason = nullToPtr(row.RootBlockReason)
		run.RootDoneReason = nullToPtr(row.RootDoneReason)
		runs = append(runs, run)
	}
	out := apitypes.WorkflowRunList{Runs: runs, Total: int(total)}
	if next != "" {
		out.NextPageToken = &next
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) InstantiateWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	info, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	var body apitypes.InstantiateWorkflowRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	key := uuid.NewString()
	if body.IdempotencyKey != nil && *body.IdempotencyKey != "" {
		key = *body.IdempotencyKey
	}
	inputs := map[string]string{}
	if body.Inputs != nil {
		inputs = *body.Inputs
	}
	// The workflow's AgentID is persisted authority-bearing state. Load it under
	// the initiator's ownership scope and authorize use before claiming a run or
	// materializing its root goal; worker executor authority is only confinement.
	wf, err := s.workflowSvc.Get(r.Context(), info.UserID, workflowAgentScope(info), id)
	if err != nil {
		workflowError(w, err)
		return
	}
	if !wf.AgentID.Valid || wf.AgentID.String == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if _, code, msg := s.requireAgentUse(r.Context(), wf.AgentID.String); code != 0 {
		writeError(w, code, msg)
		return
	}
	run, created, err := s.workflowSvc.Instantiate(r.Context(), workflowpkg.InstantiateInput{UserID: info.UserID, AgentID: workflowAgentScope(info), WorkflowID: id, Inputs: inputs, IdempotencyKey: key})
	if err != nil {
		workflowError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeData(w, status, workflowRunToAPI(run))
}

func (s *Server) DeleteWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	info, ok := s.workflowAuth(w, r)
	if !ok {
		return
	}
	if err := s.workflowSvc.Delete(r.Context(), info.UserID, workflowAgentScope(info), id); err != nil {
		workflowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func workflowAgentScope(_ *AuthInfo) string {
	return ""
}

func workflowInputSpecs(in *[]apitypes.WorkflowInputSpec) []workflowpkg.InputSpec {
	if in == nil {
		return nil
	}
	out := make([]workflowpkg.InputSpec, 0, len(*in))
	for _, spec := range *in {
		out = append(out, workflowpkg.InputSpec{Name: spec.Name, Description: derefStr(spec.Description), Required: derefBool(spec.Required), Default: derefStr(spec.Default)})
	}
	return out
}

func workflowToAPI(wf sqlc.AgentWorkflow) apitypes.Workflow {
	return apitypes.Workflow{
		Id:                 wf.ID,
		OwnerKind:          apitypes.WorkflowOwnerKind(wf.OwnerKind),
		UserId:             nullToPtr(wf.UserID),
		AgentId:            nullToPtr(wf.AgentID),
		Name:               wf.Name,
		Version:            int(wf.Version),
		Intent:             wf.Intent,
		AcceptanceContract: jsonObject(wf.AcceptanceContract),
		ConvergencePolicy:  jsonObject(wf.ConvergencePolicy),
		Inputs:             workflowInputsToAPI(wf.Inputs),
		PayloadFormat:      apitypes.WorkflowPayloadFormat(wf.PayloadFormat),
		Payload:            jsonMap(wf.Payload),
		FullyFrozen:        wf.FullyFrozen,
		SourceGoalId:       nullToPtr(wf.SourceGoalID),
		CreatedAt:          wf.CreatedAt.UTC(),
		UpdatedAt:          wf.UpdatedAt.UTC(),
	}
}

func workflowRunToAPI(run sqlc.AgentWorkflowRun) apitypes.WorkflowRun {
	inputs := map[string]string{}
	_ = json.Unmarshal(run.Inputs, &inputs)
	return apitypes.WorkflowRun{Id: run.ID, WorkflowId: run.WorkflowID, WorkflowVersion: int(run.WorkflowVersion), IdempotencyKey: run.IdempotencyKey, RootGoalId: nullToPtr(run.RootGoalID), Status: apitypes.WorkflowRunStatus(run.Status), Inputs: inputs, PlanHash: run.PlanHash, CreatedAt: run.CreatedAt.UTC(), UpdatedAt: run.UpdatedAt.UTC()}
}

func workflowInputsToAPI(raw json.RawMessage) []apitypes.WorkflowInputSpec {
	var specs []workflowpkg.InputSpec
	_ = json.Unmarshal(raw, &specs)
	out := make([]apitypes.WorkflowInputSpec, 0, len(specs))
	for _, spec := range specs {
		item := apitypes.WorkflowInputSpec{Name: spec.Name, Required: &spec.Required}
		if spec.Description != "" {
			item.Description = &spec.Description
		}
		if spec.Default != "" {
			item.Default = &spec.Default
		}
		out = append(out, item)
	}
	return out
}

func derefBool(v *bool) bool {
	return v != nil && *v
}

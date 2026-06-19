package server

import (
	"encoding/json"
	"net/http"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// goal_plan.go exposes the structured-plan lifecycle (#525): stage a plan as the
// goal's pending edit, accept it directly or route it through a human plan review,
// then materialize it into the goal's task graph. Plan reviews are their own
// lifecycle (subject='plan'); their decisions go through the dedicated
// *GoalPlanReview facade methods, never the generic review API.

func (s *Server) GetGoalPlan(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, info.UserID, goalID)
	if !ok {
		return
	}
	plan, err := s.tasksSvc.Facade.GetGoalPlan(r.Context(), g.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	out, err := planToAPI(plan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "plan_decode_failed")
		return
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) PutGoalPlan(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, info.UserID, goalID)
	if !ok {
		return
	}
	var req apitypes.PutGoalPlanRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	content, err := apiPlanToTasks(req.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plan")
		return
	}
	reviewPolicy := ""
	if req.ReviewPolicy != nil {
		reviewPolicy = string(*req.ReviewPolicy)
	}
	if err := s.tasksSvc.Facade.CreateGoalPlan(r.Context(), g.ID, content, reviewPolicy, authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	s.writeFreshPlan(w, r, g.ID)
}

func (s *Server) AcceptGoalPlan(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, info.UserID, goalID)
	if !ok {
		return
	}
	if err := s.tasksSvc.Facade.AcceptGoalPlan(r.Context(), g.ID, authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	s.writeFreshPlan(w, r, g.ID)
}

// StartGoalPlanning opens (or creates) the goal's dedicated planning session and
// returns its id so the UI can mount a chat to author/refine the plan. Idempotent:
// a goal that already has a planning session gets the same one back.
func (s *Server) StartGoalPlanning(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, info.UserID, goalID)
	if !ok {
		return
	}
	sid, err := s.tasksSvc.Facade.StartGoalPlanning(r.Context(), g.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, apitypes.GoalPlanningSession{SessionId: sid})
}

func (s *Server) MaterializeGoalPlan(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, info.UserID, goalID)
	if !ok {
		return
	}
	if err := s.tasksSvc.Facade.MaterializeGoalPlan(r.Context(), g.ID, authActor(r)); err != nil {
		s.taskError(w, err)
		return
	}
	fresh, err := s.tasksSvc.Facade.GetGoal(r.Context(), g.ID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, goalToAPI(fresh))
}

func (s *Server) SubmitGoalPlanReview(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, info.UserID, goalID)
	if !ok {
		return
	}
	reviewID, err := s.tasksSvc.Facade.SubmitGoalPlanForReview(r.Context(), g.ID, authActor(r))
	if err != nil {
		s.taskError(w, err)
		return
	}
	rev, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	writeData(w, http.StatusOK, reviewToAPI(rev))
}

func (s *Server) ApproveGoalPlanReview(w http.ResponseWriter, r *http.Request, goalID string, reviewID string) {
	s.decideGoalPlanReview(w, r, goalID, reviewID, "approve")
}

func (s *Server) RejectGoalPlanReview(w http.ResponseWriter, r *http.Request, goalID string, reviewID string) {
	s.decideGoalPlanReview(w, r, goalID, reviewID, "reject")
}

func (s *Server) RequestChangesGoalPlanReview(w http.ResponseWriter, r *http.Request, goalID string, reviewID string) {
	s.decideGoalPlanReview(w, r, goalID, reviewID, "request_changes")
}

func (s *Server) decideGoalPlanReview(w http.ResponseWriter, r *http.Request, goalID, reviewID, kind string) {
	if !s.tasksReady() {
		writeError(w, http.StatusServiceUnavailable, "task_service_unavailable")
		return
	}
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	g, ok := s.loadGoal(r.Context(), w, info.UserID, goalID)
	if !ok {
		return
	}
	rev, err := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if err != nil || !rev.GoalID.Valid || rev.GoalID.String != g.ID {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var req apitypes.ReviewDecisionRequest
	_ = decodeJSON(r, &req)
	actor := authActor(r)
	switch kind {
	case "approve":
		err = s.tasksSvc.Facade.ApproveGoalPlanReview(r.Context(), reviewID, strPtr(req.Summary), actor)
	case "reject":
		err = s.tasksSvc.Facade.RejectGoalPlanReview(r.Context(), reviewID, strPtr(req.Summary), strPtr(req.Feedback), actor)
	case "request_changes":
		err = s.tasksSvc.Facade.RequestChangesGoalPlanReview(r.Context(), reviewID, strPtr(req.Summary), strPtr(req.Feedback), actor)
	}
	if err != nil {
		s.taskError(w, err)
		return
	}
	fresh, gerr := s.tasksSvc.Facade.GetReview(r.Context(), reviewID)
	if gerr != nil {
		writeData(w, http.StatusOK, reviewToAPI(rev))
		return
	}
	writeData(w, http.StatusOK, reviewToAPI(fresh))
}

// writeFreshPlan re-reads and returns the goal's plan after a mutation.
func (s *Server) writeFreshPlan(w http.ResponseWriter, r *http.Request, goalID string) {
	plan, err := s.tasksSvc.Facade.GetGoalPlan(r.Context(), goalID)
	if err != nil {
		s.taskError(w, err)
		return
	}
	out, err := planToAPI(plan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "plan_decode_failed")
		return
	}
	writeData(w, http.StatusOK, out)
}

// planToAPI converts a sqlc.AgentGoalPlan to its API shape, decoding the stored
// plan JSON (which shares the PlanContent wire format) into typed content. It
// returns an error on corrupt stored JSON so callers surface 500 instead of
// silently emitting an empty plan.
func planToAPI(p sqlc.AgentGoalPlan) (apitypes.GoalPlan, error) {
	content, err := planContentToAPI(p.ContentJson)
	if err != nil {
		return apitypes.GoalPlan{}, err
	}
	out := apitypes.GoalPlan{
		Id:           p.ID,
		GoalId:       p.GoalID,
		Status:       apitypes.GoalPlanStatus(p.Status),
		ReviewPolicy: apitypes.GoalPlanReviewPolicy(p.ReviewPolicy),
		Content:      content,
		CreatedAt:    parseTS(p.CreatedAt),
		UpdatedAt:    parseTS(p.UpdatedAt),
	}
	if p.PendingContentJson.Valid {
		pc, err := planContentToAPI(p.PendingContentJson.String)
		if err != nil {
			return apitypes.GoalPlan{}, err
		}
		out.PendingContent = &pc
	}
	if p.ApprovedReviewID.Valid {
		v := p.ApprovedReviewID.String
		out.ApprovedReviewId = &v
	}
	if p.PlanningSessionID.Valid {
		v := p.PlanningSessionID.String
		out.PlanningSessionId = &v
	}
	if p.AcceptedAt.Valid {
		v := parseTS(p.AcceptedAt.String)
		out.AcceptedAt = &v
	}
	if p.MaterializedAt.Valid {
		v := parseTS(p.MaterializedAt.String)
		out.MaterializedAt = &v
	}
	return out, nil
}

func planContentToAPI(raw string) (apitypes.PlanContent, error) {
	pc := apitypes.PlanContent{Items: []apitypes.PlanItem{}}
	if raw == "" {
		return pc, nil
	}
	if err := json.Unmarshal([]byte(raw), &pc); err != nil {
		return apitypes.PlanContent{}, err
	}
	if pc.Items == nil {
		pc.Items = []apitypes.PlanItem{}
	}
	return pc, nil
}

// apiPlanToTasks converts request PlanContent into the tasks-package shape via the
// shared wire format, so validation lives in one place (tasks.validatePlan).
func apiPlanToTasks(c apitypes.PlanContent) (tasks.PlanContent, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return tasks.PlanContent{}, err
	}
	var out tasks.PlanContent
	if err := json.Unmarshal(raw, &out); err != nil {
		return tasks.PlanContent{}, err
	}
	return out, nil
}

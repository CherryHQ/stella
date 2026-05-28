package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/tasks"
)

// PHASE 1 STUB — handlers temporarily return 503 while the flat /api/tasks
// surface is being wired. Phase 2 of plan
// ~/.agents/sessions/stella/2026-05-28-task-system-v2-flat-api/plan.md
// replaces these with real handlers backed by tasks.ServiceFacade.

// SetTasksService wires the tasks service into the admin server.
func (s *Server) SetTasksService(svc *tasks.Service) {
	s.tasksSvc = svc
}

func tasksUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "task system v2 flat API not yet wired")
}

func (s *Server) ListTasks(w http.ResponseWriter, _ *http.Request, _ apiserver.ListTasksParams) {
	tasksUnavailable(w)
}

func (s *Server) CreateTask(w http.ResponseWriter, _ *http.Request) {
	tasksUnavailable(w)
}

func (s *Server) GetTask(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) CancelTask(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ReopenTask(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) GetTaskReadiness(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ListTaskEvents(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ListTaskRuns(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ListTaskDeps(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) AddTaskDep(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) WaiveTaskDep(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ResolveTaskBlocker(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ListTaskReviews(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ApproveTaskReview(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) RejectTaskReview(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) RequestChangesTaskReview(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) EscalateTaskReview(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

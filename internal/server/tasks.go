package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/tasks"
)

// PHASE 1 STUB — every handler returns 503 while the v2 task system is being
// ported. See plan.md (D14 / MP1). Phase 5/6 will rewrite these against the
// regenerated flat /api/tasks routes.

// SetTasksService wires the tasks service into the admin server.
func (s *Server) SetTasksService(svc *tasks.Service) {
	s.tasksSvc = svc
}

func tasksUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "task system v2 not yet initialized")
}

func (s *Server) ListAgentTasks(w http.ResponseWriter, _ *http.Request, _ string, _ apiserver.ListAgentTasksParams) {
	tasksUnavailable(w)
}

func (s *Server) CreateAgentTask(w http.ResponseWriter, _ *http.Request, _ string) {
	tasksUnavailable(w)
}

func (s *Server) GetAgentTask(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) UpdateAgentTask(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) DeleteAgentTask(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) AgentTaskAction(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

func (s *Server) ListAgentTaskEvents(w http.ResponseWriter, _ *http.Request, _ string, _ string) {
	tasksUnavailable(w)
}

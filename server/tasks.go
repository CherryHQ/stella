package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
)

func (s *Server) ListAgentTasks(w http.ResponseWriter, r *http.Request, params apiserver.ListAgentTasksParams) {
	writeData(w, http.StatusOK, apiserver.AgentTaskList{Items: []apiserver.AgentTask{}})
}

func (s *Server) CreateAgentTask(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not implemented")
}

func (s *Server) GetAgentTask(w http.ResponseWriter, r *http.Request, id string) {
	writeError(w, http.StatusNotImplemented, "not implemented")
}

func (s *Server) UpdateAgentTask(w http.ResponseWriter, r *http.Request, id string) {
	writeError(w, http.StatusNotImplemented, "not implemented")
}

func (s *Server) DeleteAgentTask(w http.ResponseWriter, r *http.Request, id string) {
	writeError(w, http.StatusNotImplemented, "not implemented")
}

func (s *Server) AgentTaskAction(w http.ResponseWriter, r *http.Request, id string) {
	writeError(w, http.StatusNotImplemented, "not implemented")
}

func (s *Server) ListAgentTaskEvents(w http.ResponseWriter, r *http.Request, id string) {
	writeData(w, http.StatusOK, apiserver.AgentTaskEventList{Items: []apiserver.AgentTaskEvent{}})
}

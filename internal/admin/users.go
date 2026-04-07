package admin

import (
	"net/http"
	"strconv"

	"github.com/vaayne/anna/pkg/db/sqlc"
)

func (s *Server) updateUserDefaultAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		DefaultAgentID string `json:"default_agent_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.authStore.UpdateUserDefaultAgent(r.Context(), id, body.DefaultAgentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) updateUserNotifyIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		NotifyIdentityID *int64 `json:"notify_identity_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.authStore.UpdateUserNotifyIdentity(r.Context(), id, body.NotifyIdentityID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) listUserMemories(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	memories, err := s.q.ListUserAgentMemoriesByUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, memories)
}

func (s *Server) setUserMemory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	agentID := r.PathValue("agentId")
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.q.UpsertUserAgentMemory(r.Context(), sqlc.UpsertUserAgentMemoryParams{
		UserID:  id,
		AgentID: agentID,
		Content: body.Content,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) deleteUserMemory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	agentID := r.PathValue("agentId")
	if err := s.q.DeleteUserAgentMemory(r.Context(), sqlc.DeleteUserAgentMemoryParams{
		UserID:  id,
		AgentID: agentID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

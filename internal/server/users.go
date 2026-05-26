package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
)

func (s *Server) UpdateUserDefaultAgent(w http.ResponseWriter, r *http.Request, id string) {
	// TODO: handler uses PUT semantics, update to PATCH partial-update; change to return 204 with empty body
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		DefaultAgentID string `json:"default_agent_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.users.UpdateUserDefaultAgent(r.Context(), id, body.DefaultAgentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) UpdateUserNotifyIdentity(w http.ResponseWriter, r *http.Request, id string) {
	// TODO: handler uses PUT semantics, update to PATCH partial-update; change to return 204 with empty body
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		NotifyIdentityID *string `json:"notify_identity_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.users.UpdateUserNotifyIdentity(r.Context(), id, body.NotifyIdentityID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) ListUserMemories(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	memories, err := s.q.ListUserAgentMemoriesByUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, memories)
}

func (s *Server) SetUserMemory(w http.ResponseWriter, r *http.Request, id string, agentID string) {
	// TODO: handler uses PUT semantics, update to PATCH partial-update; change to return 204 with empty body
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	ctx := memory.WithChangeSource(r.Context(), memory.SourceSystem)
	if err := memorywrite.SetProfile(ctx, s.db, s.q, id, agentID, body.Content); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) DeleteUserMemory(w http.ResponseWriter, r *http.Request, id string, agentID string) {
	// TODO: change to return 204 with empty body
	if !requireAdmin(w, r) {
		return
	}
	ctx := memory.WithChangeSource(r.Context(), memory.SourceSystem)
	if err := memorywrite.DeleteProfile(ctx, s.db, s.q, id, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

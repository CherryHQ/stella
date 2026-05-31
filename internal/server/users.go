package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
)

func resolveTargetUserID(info *AuthInfo, id string) string {
	if id == "me" {
		return info.UserID
	}
	return id
}

// requireUserTarget resolves the users/me alias and permits access only to the
// current user or admins. Cross-user IDs return 404 to avoid leaking existence.
func (s *Server) requireUserTarget(w http.ResponseWriter, r *http.Request, id string) (*AuthInfo, string, bool) {
	info := requireAuth(w, r)
	if info == nil {
		return nil, "", false
	}
	targetUserID := resolveTargetUserID(info, id)
	if targetUserID == info.UserID || info.IsAdmin {
		return info, targetUserID, true
	}
	writeError(w, http.StatusNotFound, "user not found")
	return nil, "", false
}

func (s *Server) UpdateUserDefaultAgent(w http.ResponseWriter, r *http.Request, id string) {
	info, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	var body struct {
		DefaultAgentID string `json:"default_agent_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if !info.IsAdmin {
		if _, code, msg := s.requireAgentAccess(r.Context(), body.DefaultAgentID); code != 0 {
			writeError(w, code, msg)
			return
		}
	}
	if err := s.users.UpdateUserDefaultAgent(r.Context(), targetUserID, body.DefaultAgentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeAuthUser(w, r, targetUserID)
}

func (s *Server) UpdateUserNotifyIdentity(w http.ResponseWriter, r *http.Request, id string) {
	_, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	var body struct {
		NotifyIdentityID *string `json:"notify_identity_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.NotifyIdentityID != nil {
		identity, err := s.users.GetChannelIdentity(r.Context(), *body.NotifyIdentityID)
		if err != nil {
			writeError(w, http.StatusNotFound, "identity not found")
			return
		}
		if identity.UserID != targetUserID {
			writeError(w, http.StatusBadRequest, "identity does not belong to this user")
			return
		}
	}
	if err := s.users.UpdateUserNotifyIdentity(r.Context(), targetUserID, body.NotifyIdentityID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeAuthUser(w, r, targetUserID)
}

func (s *Server) ListUserMemories(w http.ResponseWriter, r *http.Request, id string) {
	_, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	memories, err := s.q.ListUserAgentMemoriesByUser(r.Context(), targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defaultSoul := prompt.DefaultAgentSoul()
	for i := range memories {
		if memories[i].Soul == "" {
			memories[i].Soul = defaultSoul
		}
	}
	writeData(w, http.StatusOK, map[string]any{"memories": memories})
}

func (s *Server) SetUserMemory(w http.ResponseWriter, r *http.Request, id string, agentID string) {
	info, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	source := memory.SourceSystem
	if !info.IsAdmin || targetUserID == info.UserID {
		source = memory.SourceUser
	}
	ctx := memory.WithChangeSource(r.Context(), source)
	if err := memorywrite.SetProfile(ctx, s.db, s.q, targetUserID, agentID, body.Content); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeProfileMemory(w, r, targetUserID, agentID)
}

func (s *Server) DeleteUserMemory(w http.ResponseWriter, r *http.Request, id string, agentID string) {
	info, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	source := memory.SourceSystem
	if !info.IsAdmin || targetUserID == info.UserID {
		source = memory.SourceUser
	}
	ctx := memory.WithChangeSource(r.Context(), source)
	if err := memorywrite.DeleteProfile(ctx, s.db, s.q, targetUserID, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeNoContent(w)
}

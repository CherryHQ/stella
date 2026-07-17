package server

import (
	"net/http"

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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		DefaultAgentID string `json:"default_agent_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// A non-admin may only set a default agent they can use; that cross-domain
	// decision stays with the Agent PEP, before the account write.
	if !info.IsAdmin {
		if _, code, msg := s.requireAgentUse(r.Context(), body.DefaultAgentID); code != 0 {
			writeError(w, code, msg)
			return
		}
	}
	view, err := s.account.SetDefaultAgent(r.Context(), authority, targetUserID, body.DefaultAgentID)
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeData(w, http.StatusOK, authUserResponseFromView(view))
}

func (s *Server) UpdateUserNotifyIdentity(w http.ResponseWriter, r *http.Request, id string) {
	info, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		NotifyIdentityID *string `json:"notify_identity_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	view, err := s.account.SetNotifyIdentity(r.Context(), authority, targetUserID, body.NotifyIdentityID)
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeData(w, http.StatusOK, authUserResponseFromView(view))
}

func (s *Server) ListUserMemories(w http.ResponseWriter, r *http.Request, id string) {
	_, targetUserID, ok := s.requireUserTarget(w, r, id)
	if !ok {
		return
	}
	memories, err := s.q.ListUserAgentMemoriesByUser(r.Context(), targetUserID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	for i := range memories {
		if err := s.applyProfileFacts(r.Context(), &memories[i]); err != nil {
			s.writeInternalError(w, err)
			return
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
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	source := memory.SourceSystem
	if !info.IsAdmin || targetUserID == info.UserID {
		source = memory.SourceUser
	}
	ctx := memory.WithChangeSource(r.Context(), source)
	profiles, ok := s.mem.(memory.ProfileStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "profile memory store not configured")
		return
	}
	if err := profiles.SetProfile(ctx, targetUserID, agentID, body.Content); err != nil {
		s.writeInternalError(w, err)
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
	if err := memorywrite.ResetUserAgentMemory(ctx, s.db, s.q, targetUserID, agentID); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

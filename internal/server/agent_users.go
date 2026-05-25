package server

import (
	"net/http"
)

// --- Agent user assignment API (admin-only) ---

func (s *Server) ListAgentUsers(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	agentID := id
	ctx := r.Context()

	userIDs, err := s.authStore.ListAgentUserIDs(ctx, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent users: "+err.Error())
		return
	}

	type agentUser struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	users := make([]agentUser, 0, len(userIDs))
	for _, uid := range userIDs {
		u, err := s.users.GetUser(ctx, uid)
		if err != nil {
			continue
		}
		users = append(users, agentUser{ID: u.ID, Username: u.Email})
	}

	writeData(w, http.StatusOK, users)
}

func (s *Server) AssignAgentUser(w http.ResponseWriter, r *http.Request, id string) {
	if !requireAdmin(w, r) {
		return
	}
	agentID := id
	ctx := r.Context()

	var body struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	if _, err := s.store.GetAgent(ctx, agentID); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if _, err := s.users.GetUser(ctx, body.UserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if err := s.authStore.AssignAgent(ctx, body.UserID, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to assign user: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "assigned"})
}

func (s *Server) RemoveAgentUser(w http.ResponseWriter, r *http.Request, id string, userId string) {
	if !requireAdmin(w, r) {
		return
	}
	agentID := id
	ctx := r.Context()

	if err := s.authStore.RemoveAgent(ctx, userId, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove user: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "removed"})
}

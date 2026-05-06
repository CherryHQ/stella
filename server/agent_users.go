package server

import (
	"net/http"
	"strconv"
)

// --- Agent user assignment API (admin-only) ---

func (s *Server) listAgentUsers(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	ctx := r.Context()

	userIDs, err := s.authStore.ListAgentUserIDs(ctx, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent users: "+err.Error())
		return
	}

	type agentUser struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	users := make([]agentUser, 0, len(userIDs))
	for _, uid := range userIDs {
		u, err := s.authStore.GetUser(ctx, uid)
		if err != nil {
			continue
		}
		users = append(users, agentUser{ID: u.ID, Username: u.Username})
	}

	writeData(w, http.StatusOK, users)
}

func (s *Server) assignAgentUser(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	ctx := r.Context()

	var body struct {
		UserID int64 `json:"user_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.UserID == 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	if _, err := s.store.GetAgent(ctx, agentID); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if _, err := s.authStore.GetUser(ctx, body.UserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if err := s.authStore.AssignAgent(ctx, body.UserID, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to assign user: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "assigned"})
}

func (s *Server) removeAgentUser(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	userIDStr := r.PathValue("userId")
	ctx := r.Context()

	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user ID")
		return
	}

	if err := s.authStore.RemoveAgent(ctx, userID, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove user: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "removed"})
}

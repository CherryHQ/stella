package server

import (
	"net/http"
)

// --- Agent user assignment API (admin-only) ---

func (s *Server) ListAgentUsers(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	agentID := id
	ctx := r.Context()

	userIDs, err := s.authStore.ListAgentUserIDs(ctx, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	type agentUser struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	rows, err := s.q.ListAuthUsersByIDs(ctx, userIDs)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	byID := make(map[string]string, len(rows))
	for _, u := range rows {
		byID[u.ID] = u.Email
	}

	// Stale auth_user_agent links should not break the admin list; real query
	// failures are surfaced above, while missing users are intentionally skipped.
	users := make([]agentUser, 0, len(userIDs))
	for _, uid := range userIDs {
		if email, ok := byID[uid]; ok {
			users = append(users, agentUser{ID: uid, Username: email})
		}
	}

	writeData(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) AssignAgentUser(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	agentID := id
	ctx := r.Context()

	var body struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
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
	u, err := s.users.GetUser(ctx, body.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if err := s.authStore.AssignAgent(ctx, body.UserID, agentID); err != nil {
		s.writeInternalError(w, err)
		return
	}

	writeData(w, http.StatusCreated, map[string]string{"id": u.ID, "username": u.Email})
}

func (s *Server) RemoveAgentUser(w http.ResponseWriter, r *http.Request, id string, userId string) {
	if requireAdmin(w, r) == nil {
		return
	}
	agentID := id
	ctx := r.Context()

	if err := s.authStore.RemoveAgent(ctx, userId, agentID); err != nil {
		s.writeInternalError(w, err)
		return
	}

	writeNoContent(w)
}

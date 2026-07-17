package server

import (
	"net/http"
)

// --- Agent user assignment API (admin-only) ---

func (s *Server) ListAgentUsers(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	users, err := s.agentManagement.ListAssignedUsers(r.Context(), authority, id)
	if err != nil {
		code, msg := agentManagementError(err)
		if code == http.StatusInternalServerError {
			s.writeInternalError(w, err)
			return
		}
		writeError(w, code, msg)
		return
	}

	type agentUser struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	out := make([]agentUser, 0, len(users))
	for _, u := range users {
		out = append(out, agentUser{ID: u.ID, Username: u.Email})
	}
	writeData(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) AssignAgentUser(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

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

	u, err := s.agentManagement.AssignUser(r.Context(), authority, id, body.UserID)
	if err != nil {
		code, msg := agentManagementError(err)
		if code == http.StatusInternalServerError {
			s.writeInternalError(w, err)
			return
		}
		writeError(w, code, msg)
		return
	}

	writeData(w, http.StatusCreated, map[string]string{"id": u.ID, "username": u.Email})
}

func (s *Server) RemoveAgentUser(w http.ResponseWriter, r *http.Request, id string, userId string) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := s.agentManagement.RemoveUser(r.Context(), authority, id, userId); err != nil {
		code, msg := agentManagementError(err)
		if code == http.StatusInternalServerError {
			s.writeInternalError(w, err)
			return
		}
		writeError(w, code, msg)
		return
	}

	writeNoContent(w)
}

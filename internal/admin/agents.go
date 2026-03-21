package admin

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
)

// slugify converts a name to a URL-safe agent ID.
// "My Cool Agent" -> "my-cool-agent"
var nonAlphaNum = regexp.MustCompile(`[^a-z0-9-]+`)

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "agent"
	}
	return s
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Admin sees all agents. Non-admin users see only accessible agents.
	if info != nil && !info.IsAdmin {
		agents, err = s.filterAccessibleAgents(ctx, info, agents)
		if err != nil {
			s.log.Error("filter accessible agents", "user_id", info.UserID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to filter agents")
			return
		}
	}

	writeData(w, http.StatusOK, agents)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := UserFromContext(ctx)

	var a config.Agent
	if err := decodeJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if a.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Auto-generate ID from name.
	a.ID = slugify(a.Name)

	// Deduplicate: if the ID already exists, append a suffix.
	if _, err := s.store.GetAgent(ctx, a.ID); err == nil {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", a.ID, i)
			if _, err := s.store.GetAgent(ctx, candidate); err != nil {
				a.ID = candidate
				break
			}
		}
	}

	// Workspace is always the default path — never user-supplied.
	a.Workspace = ""

	// Set creator.
	if info != nil {
		a.CreatorID = info.UserID
	}

	// Non-admin users always get restricted scope, auto-assigned.
	if info != nil && !info.IsAdmin {
		a.Scope = config.AgentScopeRestricted
	} else {
		if a.Scope == "" {
			a.Scope = config.AgentScopeSystem
		}
		if a.Scope != config.AgentScopeSystem && a.Scope != config.AgentScopeRestricted {
			writeError(w, http.StatusBadRequest, "scope must be 'system' or 'restricted'")
			return
		}
	}

	if err := s.store.CreateAgent(ctx, a); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Auto-assign the creator if scope is restricted and user is non-admin.
	if info != nil && !info.IsAdmin && a.Scope == config.AgentScopeRestricted {
		if err := s.authStore.AssignAgent(ctx, info.UserID, a.ID); err != nil {
			s.log.Error("auto-assign agent to creator", "user_id", info.UserID, "agent_id", a.ID, "error", err)
		}
	}

	writeData(w, http.StatusCreated, a)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := UserFromContext(ctx)
	id := r.PathValue("id")

	a, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	// Non-admin users can only access system or assigned agents.
	if info != nil && !info.IsAdmin {
		if !s.canAccessAgent(ctx, info, a) {
			writeError(w, http.StatusForbidden, "you don't have access to this agent")
			return
		}
	}

	writeData(w, http.StatusOK, a)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := UserFromContext(ctx)
	id := r.PathValue("id")

	// Check access: admin or creator.
	existing, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if info != nil && !info.IsAdmin && existing.CreatorID != info.UserID {
		writeError(w, http.StatusForbidden, "only the creator or an admin can edit this agent")
		return
	}

	var a config.Agent
	if err := decodeJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	a.ID = id
	if a.Name == "" {
		a.Name = id
	}

	// Non-admin: keep scope as-is, don't allow changing it.
	if info != nil && !info.IsAdmin {
		a.Scope = existing.Scope
	} else {
		if a.Scope == "" {
			a.Scope = config.AgentScopeSystem
		}
		if a.Scope != config.AgentScopeSystem && a.Scope != config.AgentScopeRestricted {
			writeError(w, http.StatusBadRequest, "scope must be 'system' or 'restricted'")
			return
		}
	}

	if err := s.store.UpdateAgent(ctx, a); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, a)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := UserFromContext(ctx)
	id := r.PathValue("id")

	// Check access: admin or creator.
	existing, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if info != nil && !info.IsAdmin && existing.CreatorID != info.UserID {
		writeError(w, http.StatusForbidden, "only the creator or an admin can delete this agent")
		return
	}

	if err := s.store.DeleteAgent(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Agent user assignment API (admin-only) ---

func (s *Server) listAgentUsers(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	ctx := r.Context()

	userIDs, err := s.authStore.ListAgentUserIDs(ctx, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent users: "+err.Error())
		return
	}

	// Resolve user details for each user ID.
	type agentUser struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	users := make([]agentUser, 0, len(userIDs))
	for _, uid := range userIDs {
		u, err := s.authStore.GetUser(ctx, uid)
		if err != nil {
			continue // skip users that no longer exist
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

	// Verify agent exists.
	if _, err := s.store.GetAgent(ctx, agentID); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	// Verify user exists.
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

// --- Policy engine helpers ---

// filterAccessibleAgents returns only agents the user can access (system scope + assigned).
func (s *Server) filterAccessibleAgents(ctx context.Context, info *AuthInfo, agents []config.Agent) ([]config.Agent, error) {
	assignedIDs, err := s.authStore.ListUserAgentIDs(ctx, info.UserID)
	if err != nil {
		return nil, fmt.Errorf("list user agent IDs: %w", err)
	}

	subject := auth.Subject{
		UserID:   info.UserID,
		Roles:    []string{info.Role},
		AgentIDs: assignedIDs,
	}

	var filtered []config.Agent
	for _, a := range agents {
		req := auth.AccessRequest{
			Subject: subject,
			Action:  auth.ActionRead,
			Resource: auth.Resource{
				Type:  auth.ResourceAgent,
				ID:    a.ID,
				Attrs: map[string]any{"scope": a.Scope},
			},
		}
		if s.engine.Can(ctx, req) {
			filtered = append(filtered, a)
		}
	}
	return filtered, nil
}

// canAccessAgent checks if the user can access a specific agent.
func (s *Server) canAccessAgent(ctx context.Context, info *AuthInfo, a config.Agent) bool {
	assignedIDs, err := s.authStore.ListUserAgentIDs(ctx, info.UserID)
	if err != nil {
		s.log.Error("list user agent IDs for access check", "user_id", info.UserID, "error", err)
		return false
	}

	req := auth.AccessRequest{
		Subject: auth.Subject{
			UserID:   info.UserID,
			Roles:    []string{info.Role},
			AgentIDs: assignedIDs,
		},
		Action: auth.ActionRead,
		Resource: auth.Resource{
			Type:  auth.ResourceAgent,
			ID:    a.ID,
			Attrs: map[string]any{"scope": a.Scope},
		},
	}
	return s.engine.Can(ctx, req)
}

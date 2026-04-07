package admin

import (
	"net/http"
	"strconv"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/pkg/db/sqlc"
)

// listProfileIdentities handles GET /api/auth/profile/identities.
func (s *Server) listProfileIdentities(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	identities, err := s.authStore.ListIdentitiesByUser(r.Context(), info.UserID)
	if err != nil {
		s.log.Error("list identities", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeData(w, http.StatusOK, identities)
}

// changePassword handles PUT /api/auth/profile/password.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if body.CurrentPassword == "" {
		writeError(w, http.StatusBadRequest, "current password is required")
		return
	}
	if len(body.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	if len(body.NewPassword) > 72 {
		writeError(w, http.StatusBadRequest, "new password must be at most 72 characters")
		return
	}

	ctx := r.Context()

	// Verify current password.
	user, err := s.authStore.GetUser(ctx, info.UserID)
	if err != nil {
		s.log.Error("get user for password change", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, body.CurrentPassword); err != nil {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	// Hash and save new password.
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		s.log.Error("hash new password", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	user.PasswordHash = hash
	if err := s.authStore.UpdateUser(ctx, user); err != nil {
		s.log.Error("update password", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "password changed"})
}

// generateLinkCode handles POST /api/auth/profile/link-code.
func (s *Server) generateLinkCode(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body struct {
		Platform string `json:"platform"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	switch body.Platform {
	case channel.PlatformTelegram, channel.PlatformQQ, channel.PlatformFeishu:
		// valid
	default:
		writeError(w, http.StatusBadRequest, "platform must be telegram, qq, or feishu")
		return
	}

	code := s.linkCodes.Generate(info.UserID, body.Platform)

	writeData(w, http.StatusOK, map[string]string{
		"code":     code,
		"platform": body.Platform,
	})
}

// unlinkIdentity handles DELETE /api/auth/profile/identities/{id}.
func (s *Server) unlinkIdentity(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid identity ID")
		return
	}

	ctx := r.Context()

	// Verify the identity belongs to the current user.
	identity, err := s.authStore.GetIdentity(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "identity not found")
		return
	}

	if identity.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "identity does not belong to you")
		return
	}

	if err := s.authStore.DeleteIdentity(ctx, id); err != nil {
		s.log.Error("delete identity", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeData(w, http.StatusOK, map[string]string{"status": "unlinked"})
}

// listProfileMemories handles GET /api/auth/profile/memories.
func (s *Server) listProfileMemories(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	memories, err := s.q.ListUserAgentMemoriesByUser(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defaultSoul := runner.DefaultAgentSoul()
	for i := range memories {
		if memories[i].Soul == "" {
			memories[i].Soul = defaultSoul
		}
	}
	writeData(w, http.StatusOK, memories)
}

// setProfileMemory handles PUT /api/auth/profile/memories/{agentId}.
func (s *Server) setProfileMemory(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
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
		UserID:  info.UserID,
		AgentID: agentID,
		Content: body.Content,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "saved"})
}

// setProfileSoul handles PUT /api/auth/profile/soul/{agentId}.
func (s *Server) setProfileSoul(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	agentID := r.PathValue("agentId")
	var body struct {
		Soul string `json:"soul"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.q.UpsertAgentSoul(r.Context(), sqlc.UpsertAgentSoulParams{
		UserID:  info.UserID,
		AgentID: agentID,
		Soul:    body.Soul,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "saved"})
}

// deleteProfileMemory handles DELETE /api/auth/profile/memories/{agentId}.
func (s *Server) deleteProfileMemory(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	agentID := r.PathValue("agentId")
	if err := s.q.DeleteUserAgentMemory(r.Context(), sqlc.DeleteUserAgentMemoryParams{
		UserID:  info.UserID,
		AgentID: agentID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

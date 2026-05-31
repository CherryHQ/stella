package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// ListProfileIdentities handles GET /api/auth/profile/identities.
func (s *Server) ListProfileIdentities(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.users == nil {
		writeData(w, http.StatusOK, map[string]any{"identities": []auth.ChannelIdentity{}})
		return
	}
	identities, err := s.users.ListChannelIdentitiesByUser(r.Context(), info.UserID)
	if err != nil {
		s.log.Error("list identities", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeData(w, http.StatusOK, map[string]any{"identities": identities})
}

// ChangePassword handles PATCH /api/auth/profile/password.
func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
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

	if s.credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credential store not configured")
		return
	}

	// Verify current password.
	cred, err := s.credentials.GetCredentialByUserID(ctx, info.UserID)
	if err != nil {
		s.log.Error("get credential for password change", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := auth.CheckPassword(cred.PasswordHash, body.CurrentPassword); err != nil {
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

	if err := s.credentials.UpdateCredentialHash(ctx, info.UserID, hash); err != nil {
		s.log.Error("update password", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeNoContent(w)
}

// GenerateLinkCode handles POST /api/auth/profile/link-code.
func (s *Server) GenerateLinkCode(w http.ResponseWriter, r *http.Request) {
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
	case pkgchannel.PlatformTelegram, pkgchannel.PlatformQQ, pkgchannel.PlatformFeishu:
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

// UnlinkProfileIdentity handles DELETE /api/auth/profile/identities/{id}.
func (s *Server) UnlinkProfileIdentity(w http.ResponseWriter, r *http.Request, id string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	ctx := r.Context()

	if s.users == nil {
		writeError(w, http.StatusServiceUnavailable, "channel identity store not configured")
		return
	}

	// Verify the identity belongs to the current user.
	identity, err := s.users.GetChannelIdentity(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "identity not found")
		return
	}

	if identity.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "identity does not belong to you")
		return
	}

	if err := s.users.DeleteChannelIdentity(ctx, id); err != nil {
		s.log.Error("delete identity", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeNoContent(w)
}

// ListProfileMemories handles GET /api/auth/profile/memories.
func (s *Server) ListProfileMemories(w http.ResponseWriter, r *http.Request) {
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
	defaultSoul := prompt.DefaultAgentSoul()
	for i := range memories {
		if memories[i].Soul == "" {
			memories[i].Soul = defaultSoul
		}
	}
	writeData(w, http.StatusOK, map[string]any{"memories": memories})
}

// SetProfileMemory handles PATCH /api/auth/profile/memories/{agentID}.
func (s *Server) SetProfileMemory(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	ctx := memory.WithChangeSource(r.Context(), memory.SourceUser)
	if err := memorywrite.SetProfile(ctx, s.db, s.q, info.UserID, agentID, body.Content); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeNoContent(w)
}

// SetProfileSoul handles PATCH /api/auth/profile/soul/{agentID}.
func (s *Server) SetProfileSoul(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body struct {
		Soul string `json:"soul"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	ctx := memory.WithChangeSource(r.Context(), memory.SourceUser)
	if err := memorywrite.SetAgentSoul(ctx, s.db, s.q, info.UserID, agentID, body.Soul); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeNoContent(w)
}

// DeleteProfileMemory handles DELETE /api/auth/profile/memories/{agentID}.
func (s *Server) DeleteProfileMemory(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	ctx := memory.WithChangeSource(r.Context(), memory.SourceUser)
	if err := memorywrite.DeleteProfile(ctx, s.db, s.q, info.UserID, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeNoContent(w)
}

// OauthCallback handles GET /api/auth/profile/oauth/{provider}/callback.
// This is intentionally a pass-through to the unexported helper so the
// generated interface signature is satisfied.
func (s *Server) OauthCallback(w http.ResponseWriter, r *http.Request, provider string, params apiserver.OauthCallbackParams) {
	if s.vaultSvc == nil {
		http.Error(w, "vault not configured", http.StatusServiceUnavailable)
		return
	}

	code := params.Code
	flowID := params.State
	if code == "" || flowID == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	flow, ok := s.credSvc.GetFlowForCallback(flowID)
	if !ok {
		http.Error(w, "unknown or expired flow", http.StatusBadRequest)
		return
	}

	if err := s.credSvc.CompleteAuthCodeFlowWithOrigin(r.Context(), provider, flowID, code, requestOrigin(r)); err != nil {
		s.log.Error("oauth callback complete", "provider", provider, "user_id", flow.UserID, "flow_id", flowID, "error", err)
		http.Error(w, "failed to complete authorization: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/credentials", http.StatusFound)
}

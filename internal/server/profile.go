package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ListProfileIdentities handles GET /api/users/me/identities.
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

// ChangePassword handles PATCH /api/users/me/password.
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
		writeError(w, http.StatusBadRequest, "invalid JSON")
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

// GenerateLinkCode handles POST /api/users/me/link-code.
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
		writeError(w, http.StatusBadRequest, "invalid JSON")
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

// UnlinkProfileIdentity handles DELETE /api/users/me/identities/{id}.
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

// ListProfileMemories handles GET /api/users/me/memories.
func (s *Server) ListProfileMemories(w http.ResponseWriter, r *http.Request) {
	s.ListUserMemories(w, r, "me")
}

// GetProfileMemory handles GET /api/users/me/memories/{agentID}.
func (s *Server) GetProfileMemory(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	mem, err := s.loadProfileMemory(r.Context(), info.UserID, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, mem)
}

// SetProfileMemory handles PATCH /api/users/me/memories/{agentID}.
func (s *Server) SetProfileMemory(w http.ResponseWriter, r *http.Request, agentID string) {
	s.SetUserMemory(w, r, "me", agentID)
}

// SetProfileSoul handles PATCH /api/users/me/soul/{agentID}.
func (s *Server) SetProfileSoul(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body struct {
		Soul string `json:"soul"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := memory.WithChangeSource(r.Context(), memory.SourceUser)
	profiles, ok := s.mem.(memory.ProfileStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "profile memory store not configured")
		return
	}
	if err := profiles.SetAgentSoul(ctx, info.UserID, agentID, body.Soul); err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.writeProfileMemory(w, r, info.UserID, agentID)
}

// writeProfileMemory loads the user/agent memory and writes the full resource,
// applying the default soul when none is stored.
func (s *Server) writeProfileMemory(w http.ResponseWriter, r *http.Request, userID, agentID string) {
	mem, err := s.loadProfileMemory(r.Context(), userID, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, mem)
}

func (s *Server) loadProfileMemory(ctx context.Context, userID string, agentID string) (sqlc.CtxAgentMemory, error) {
	mem, err := s.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if isNotFound(err) {
		mem = sqlc.CtxAgentMemory{
			UserID:         userID,
			AgentID:        agentID,
			Constraints:    json.RawMessage(`[]`),
			ProfileEntries: json.RawMessage(`[]`),
		}
	} else if err != nil {
		return sqlc.CtxAgentMemory{}, err
	}
	if err := s.applyProfileFacts(ctx, &mem); err != nil {
		return sqlc.CtxAgentMemory{}, err
	}
	return mem, nil
}

func (s *Server) applyProfileFacts(ctx context.Context, mem *sqlc.CtxAgentMemory) error {
	profiles, ok := s.mem.(memory.ProfileStore)
	if !ok {
		return errors.New("profile memory store not configured")
	}
	content, err := profiles.GetProfile(ctx, mem.UserID, mem.AgentID)
	if err != nil {
		return err
	}
	soul, err := profiles.GetAgentSoul(ctx, mem.UserID, mem.AgentID)
	if err != nil {
		return err
	}
	mem.Content = content
	mem.Soul = soul
	if mem.Soul == "" {
		mem.Soul = prompt.DefaultAgentSoul()
	}
	return nil
}

// DeleteProfileMemory handles DELETE /api/users/me/memories/{agentID}.
func (s *Server) DeleteProfileMemory(w http.ResponseWriter, r *http.Request, agentID string) {
	s.DeleteUserMemory(w, r, "me", agentID)
}

// ListProfileConstraints handles GET /api/users/me/memories/{agentID}/constraints.
func (s *Server) ListProfileConstraints(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	constraints, err := memorywrite.GetConstraints(r.Context(), s.q, info.UserID, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileConstraintListToAPI(constraints))
}

// AddProfileConstraint handles POST /api/users/me/memories/{agentID}/constraints.
func (s *Server) AddProfileConstraint(w http.ResponseWriter, r *http.Request, agentID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}

	ctx := memory.WithChangeSource(r.Context(), memory.SourceManual)
	constraints, err := memorywrite.AddConstraint(ctx, s.db, s.q, info.UserID, agentID, text)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileConstraintListToAPI(constraints))
}

// DeleteProfileConstraint handles DELETE /api/users/me/memories/{agentID}/constraints/{constraintID}.
func (s *Server) DeleteProfileConstraint(w http.ResponseWriter, r *http.Request, agentID string, constraintID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	constraints, err := memorywrite.GetConstraints(r.Context(), s.q, info.UserID, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	found := false
	for _, constraint := range constraints {
		if constraint.ID == constraintID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "constraint not found")
		return
	}

	ctx := memory.WithChangeSource(r.Context(), memory.SourceManual)
	updated, err := memorywrite.RemoveConstraint(ctx, s.db, s.q, info.UserID, agentID, constraintID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileConstraintListToAPI(updated))
}

// ListProfileChangelog handles GET /api/users/me/memories/{agentID}/changelog.
func (s *Server) ListProfileChangelog(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListProfileChangelogParams) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	pageSize := defaultPageSize
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	if pageSize < 1 || pageSize > maxKnowledgePageSize {
		writeError(w, http.StatusBadRequest, "page_size must be between 1 and 100")
		return
	}

	scopeKey := "all"
	scopes := []string{"profile", "soul", "knowledge", "constraint"}
	if params.Scope != nil && *params.Scope != "" {
		scopeKey = string(*params.Scope)
		if scopeKey != "profile" && scopeKey != "soul" && scopeKey != "knowledge" && scopeKey != "constraint" {
			writeError(w, http.StatusBadRequest, "scope must be profile, soul, knowledge, or constraint")
			return
		}
		scopes = []string{scopeKey}
	}

	var cursor *memory.ChangelogCursor
	if params.PageToken != nil {
		var err error
		cursor, err = decodeChangelogPageToken(*params.PageToken, scopeKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	entries := make([]apiserver.ChangelogEntry, 0, pageSize+1)
	for _, scope := range scopes {
		rows, err := s.readProfileChangelogScope(r.Context(), info.UserID, agentID, scope, cursor, pageSize+1)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		entries = append(entries, rows...)
	}

	sortChangelogEntries(entries)
	response := apiserver.ChangelogList{}
	if len(entries) > pageSize {
		entries = entries[:pageSize]
		last := entries[len(entries)-1]
		token, err := encodeChangelogPageToken(scopeKey, memory.ChangelogCursor{CreatedAt: last.CreatedAt, ID: last.Id})
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		response.NextPageToken = &token
	}
	response.Entries = entries
	writeData(w, http.StatusOK, response)
}

func profileConstraintListToAPI(constraints []memory.ConstraintEntry) apiserver.ConstraintList {
	out := make([]apiserver.ConstraintEntry, len(constraints))
	for i, constraint := range constraints {
		out[i] = apiserver.ConstraintEntry{
			Id:        constraint.ID,
			Text:      constraint.Text,
			CreatedAt: parseTime(constraint.CreatedAt),
		}
	}
	return apiserver.ConstraintList{Constraints: out}
}

// memoryChangelogEntryToAPI preserves Provider-projected changelog entries,
// including fact-backed profile/soul history, for the HTTP changelog endpoint.
func memoryChangelogEntryToAPI(entry memory.ChangeEntry) apiserver.ChangelogEntry {
	return apiserver.ChangelogEntry{
		Id:                  entry.ID,
		Scope:               entry.Scope,
		Action:              entry.Action,
		Source:              string(entry.Source),
		MemoryVersionBefore: int64PtrToIntPtr(entry.MemoryVersionBefore),
		MemoryVersionAfter:  int64PtrToIntPtr(entry.MemoryVersionAfter),
		BeforeText:          stringPtrIfNotEmpty(entry.BeforeText),
		AfterText:           stringPtrIfNotEmpty(entry.AfterText),
		CreatedAt:           parseTime(entry.CreatedAt),
	}
}

func int64PtrToIntPtr(value *int64) *int {
	if value == nil {
		return nil
	}
	v := int(*value)
	return &v
}

func stringPtrIfNotEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// OauthCallback handles GET /api/auth/oauth/{provider}/callback.
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
		s.writeInternalError(w, err)
		return
	}

	http.Redirect(w, r, "/settings/credentials", http.StatusFound)
}

// OauthCallbackLegacy handles the deprecated /api/auth/profile/oauth/{provider}/callback alias.
func (s *Server) OauthCallbackLegacy(w http.ResponseWriter, r *http.Request, provider string, params apiserver.OauthCallbackLegacyParams) {
	s.OauthCallback(w, r, provider, apiserver.OauthCallbackParams(params))
}

package server

import (
	"net/http"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// ListProfileIdentities handles GET /api/users/me/identities.
func (s *Server) ListProfileIdentities(w http.ResponseWriter, r *http.Request) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	identities, err := s.account.SelfChannelIdentities(r.Context(), authority)
	if err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
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

	if err := s.account.ChangePassword(r.Context(), authority, body.CurrentPassword, body.NewPassword); err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := s.account.UnlinkSelfChannelIdentity(r.Context(), authority, id); err != nil {
		s.writeAccountError(w, err)
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	mem, err := s.profileSvc.Memory(r.Context(), authority, agentID)
	if err != nil {
		s.writeProfileError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileMemoryResponseFrom(mem))
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Soul string `json:"soul"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	mem, err := s.profileSvc.SetSoul(r.Context(), authority, agentID, body.Soul)
	if err != nil {
		s.writeProfileError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileMemoryResponseFrom(mem))
}

// DeleteProfileMemory handles DELETE /api/users/me/memories/{agentID}.
func (s *Server) DeleteProfileMemory(w http.ResponseWriter, r *http.Request, agentID string) {
	s.DeleteUserMemory(w, r, "me", agentID)
}

// ListProfileConstraints handles GET /api/users/me/memories/{agentID}/constraints.
func (s *Server) ListProfileConstraints(w http.ResponseWriter, r *http.Request, agentID string) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
		return
	}
	constraints, err := s.profileSvc.ListConstraints(r.Context(), authority, agentID)
	if err != nil {
		s.writeProfileError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileConstraintListToAPI(constraints))
}

// AddProfileConstraint handles POST /api/users/me/memories/{agentID}/constraints.
func (s *Server) AddProfileConstraint(w http.ResponseWriter, r *http.Request, agentID string) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
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
	constraints, err := s.profileSvc.AddConstraint(r.Context(), authority, agentID, text)
	if err != nil {
		s.writeProfileError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileConstraintListToAPI(constraints))
}

// DeleteProfileConstraint handles DELETE /api/users/me/memories/{agentID}/constraints/{constraintID}.
func (s *Server) DeleteProfileConstraint(w http.ResponseWriter, r *http.Request, agentID string, constraintID string) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
		return
	}
	updated, err := s.profileSvc.RemoveConstraint(r.Context(), authority, agentID, constraintID)
	if err != nil {
		s.writeProfileError(w, err)
		return
	}
	writeData(w, http.StatusOK, profileConstraintListToAPI(updated))
}

// ListProfileChangelog handles GET /api/users/me/memories/{agentID}/changelog.
func (s *Server) ListProfileChangelog(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListProfileChangelogParams) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
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
		rows, err := s.readProfileChangelogScope(r.Context(), authority, agentID, scope, cursor, pageSize+1)
		if err != nil {
			s.writeKnowledgeError(w, err)
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

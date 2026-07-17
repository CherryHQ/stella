package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/agent/prompt"
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

	limit := 20
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit <= 0 {
		writeError(w, http.StatusBadRequest, "limit must be positive")
		return
	}
	if limit > 100 {
		limit = 100
	}

	scopes := []string{"profile", "soul", "constraint"}
	if params.Scope != nil && *params.Scope != "" {
		scope := *params.Scope
		if scope != "profile" && scope != "soul" && scope != "constraint" {
			writeError(w, http.StatusBadRequest, "scope must be profile, soul, or constraint")
			return
		}
		scopes = []string{scope}
	}

	entries := make([]apiserver.ChangelogEntry, 0, limit)
	for _, scope := range scopes {
		if scope == "profile" || scope == "soul" {
			reader, ok := s.mem.(memory.ChangelogReader)
			if !ok {
				writeError(w, http.StatusServiceUnavailable, "memory changelog reader not configured")
				return
			}
			rows, err := reader.ReadChangelog(r.Context(), info.UserID, agentID, scope, limit)
			if err != nil {
				s.writeInternalError(w, err)
				return
			}
			for _, row := range rows {
				entries = append(entries, memoryChangelogEntryToAPI(row))
			}
			continue
		}

		rows, err := s.q.ListMemoryChangelog(r.Context(), sqlc.ListMemoryChangelogParams{
			UserID:  info.UserID,
			AgentID: agentID,
			Scope:   scope,
			Limit:   int32(limit),
		})
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		for _, row := range rows {
			entries = append(entries, profileChangelogEntryToAPI(row))
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	writeData(w, http.StatusOK, apiserver.ChangelogList{Entries: entries})
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

func profileChangelogEntryToAPI(row sqlc.CtxAgentMemoryChangelog) apiserver.ChangelogEntry {
	return apiserver.ChangelogEntry{
		Id:                  row.ID,
		Scope:               row.Scope,
		Action:              row.Action,
		Source:              row.Source,
		MemoryVersionBefore: nullIntToPtr(row.MemoryVersionBefore),
		MemoryVersionAfter:  nullIntToPtr(row.MemoryVersionAfter),
		BeforeText:          nullStringToPtr(row.BeforeText),
		AfterText:           nullStringToPtr(row.AfterText),
		CreatedAt:           row.CreatedAt.UTC(),
	}
}

func int64PtrToIntPtr(value *int64) *int {
	if value == nil {
		return nil
	}
	v := int(*value)
	return &v
}

func nullIntToPtr(value pgtype.Int8) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int64)
	return &v
}

func nullStringToPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
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

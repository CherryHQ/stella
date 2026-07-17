package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/memory"
	memprofile "github.com/CherryHQ/stella/internal/memory/profile"
)

const maxKnowledgePageSize = 100

// ListProfileKnowledge handles GET /api/users/me/memories/{agentId}/knowledge.
func (s *Server) ListProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListProfileKnowledgeParams) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
		return
	}

	state := memprofile.KnowledgeStateActive
	if params.State != nil {
		state = memprofile.KnowledgeState(*params.State)
	}
	if state != memprofile.KnowledgeStateActive && state != memprofile.KnowledgeStateRemoved {
		writeError(w, http.StatusBadRequest, "state must be active or removed")
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

	var cursor *memprofile.KnowledgeCursor
	if params.PageToken != nil {
		var err error
		cursor, err = decodeKnowledgePageToken(*params.PageToken, state)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	page, err := s.profileSvc.ListKnowledge(r.Context(), authority, agentID, state, pageSize, cursor)
	if err != nil {
		s.writeKnowledgeError(w, err)
		return
	}

	response := apitypes.KnowledgeList{
		Knowledge: knowledgeItemsToAPI(page.Items),
		TotalSize: int(page.Total),
	}
	if page.HasMore && page.NextCursor != nil {
		token, err := encodeKnowledgePageToken(state, *page.NextCursor)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		response.NextPageToken = &token
	}
	writeData(w, http.StatusOK, response)
}

// CreateProfileKnowledge handles POST /api/users/me/memories/{agentId}/knowledge.
func (s *Server) CreateProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
		return
	}
	var body apitypes.CreateKnowledgeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	fact, err := s.profileSvc.CreateKnowledge(r.Context(), authority, agentID, body.Content)
	if err != nil {
		s.writeKnowledgeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, activeKnowledgeToAPI(fact))
}

// UpdateProfileKnowledge handles PATCH /api/users/me/memories/{agentId}/knowledge/{factId}.
func (s *Server) UpdateProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, factID string) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
		return
	}
	if !validKnowledgeFactID(factID) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	var body apitypes.UpdateKnowledgeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	fact, err := s.profileSvc.ReplaceKnowledge(r.Context(), authority, agentID, factID, body.Content)
	if errors.Is(err, memprofile.ErrKnowledgeNotRestorable) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	if err != nil {
		s.writeKnowledgeError(w, err)
		return
	}
	writeData(w, http.StatusOK, activeKnowledgeToAPI(fact))
}

// DeleteProfileKnowledge handles DELETE /api/users/me/memories/{agentId}/knowledge/{factId}.
func (s *Server) DeleteProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, factID string) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
		return
	}
	if !validKnowledgeFactID(factID) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	err := s.profileSvc.DeprecateKnowledge(r.Context(), authority, agentID, factID)
	if errors.Is(err, memprofile.ErrKnowledgeNotRestorable) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	if err != nil {
		s.writeKnowledgeError(w, err)
		return
	}
	writeNoContent(w)
}

// RestoreProfileKnowledge handles POST /api/users/me/memories/{agentId}/knowledge/{factId}/restore.
func (s *Server) RestoreProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, factID string) {
	authority, ok := s.profileAuthority(w, r)
	if !ok {
		return
	}
	if !validKnowledgeFactID(factID) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	fact, err := s.profileSvc.RestoreKnowledge(r.Context(), authority, agentID, factID)
	switch {
	case errors.Is(err, memprofile.ErrKnowledgeNotFound):
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	case errors.Is(err, memprofile.ErrKnowledgeDuplicateContent):
		writeError(w, http.StatusConflict, "active knowledge already has this content")
		return
	case errors.Is(err, memprofile.ErrKnowledgeRestoreExpired):
		writeError(w, http.StatusGone, "knowledge restore window expired")
		return
	case errors.Is(err, memprofile.ErrKnowledgeNotRestorable):
		writeError(w, http.StatusConflict, "knowledge is not restorable")
		return
	case err != nil:
		s.writeKnowledgeError(w, err)
		return
	}
	writeData(w, http.StatusOK, activeKnowledgeToAPI(fact))
}

// writeKnowledgeError maps the shared knowledge boundary errors (agent-gate
// denials via writeProfileError, unavailable backend, not-found, and a logged
// 500) to HTTP. Per-handler lifecycle codes (404/409/410) are decided at the call
// site before this fallback.
func (s *Server) writeKnowledgeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memprofile.ErrKnowledgeNotFound):
		writeError(w, http.StatusNotFound, "knowledge not found")
	case errors.Is(err, memprofile.ErrKnowledgeUnavailable):
		writeError(w, http.StatusServiceUnavailable, "knowledge management not configured")
	default:
		s.writeProfileError(w, err)
	}
}

func knowledgeItemsToAPI(items []memprofile.KnowledgeItem) []apitypes.KnowledgeItem {
	result := make([]apitypes.KnowledgeItem, len(items))
	for i, item := range items {
		result[i] = activeKnowledgeToAPI(item.Fact)
		result[i].IsRestorable = item.IsRestorable
		result[i].DeprecatedAt = item.DeprecatedAt
		result[i].RestoreDeadline = item.RestoreDeadline
		if item.RemovalSource != "" {
			source := apitypes.RemovalSource(item.RemovalSource)
			result[i].RemovalSource = &source
		}
	}
	return result
}

func activeKnowledgeToAPI(fact memory.Fact) apitypes.KnowledgeItem {
	return apitypes.KnowledgeItem{
		Id: fact.ID, Content: fact.Content, Source: apitypes.KnowledgeItemSource(fact.Source),
		CreatedAt: fact.CreatedAt.UTC(), UpdatedAt: fact.UpdatedAt.UTC(), IsRestorable: false,
	}
}

func validKnowledgeFactID(factID string) bool {
	_, err := uuid.Parse(factID)
	return err == nil
}

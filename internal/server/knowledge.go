package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
)

const maxKnowledgePageSize = 100

// ListProfileKnowledge handles GET /api/users/me/memories/{agentId}/knowledge.
func (s *Server) ListProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListProfileKnowledgeParams) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	state := memorywrite.KnowledgeStateActive
	if params.State != nil {
		state = memorywrite.KnowledgeState(*params.State)
	}
	if state != memorywrite.KnowledgeStateActive && state != memorywrite.KnowledgeStateRemoved {
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

	var cursor *memorywrite.KnowledgeCursor
	if params.PageToken != nil {
		var err error
		cursor, err = decodeKnowledgePageToken(*params.PageToken, state)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	page, err := s.memoryManagement.ListKnowledge(r.Context(), memorywrite.KnowledgeListQuery{
		UserID: info.UserID, AgentID: agentID, State: state, Limit: int32(pageSize), Now: time.Now().UTC(), Cursor: cursor,
	})
	if err != nil {
		s.writeInternalError(w, err)
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
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
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

	fact, err := s.memoryManagement.CreateKnowledge(r.Context(), memorywrite.KnowledgeCreateInput{
		UserID: info.UserID, AgentID: agentID, Content: body.Content,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, activeKnowledgeToAPI(fact))
}

// UpdateProfileKnowledge handles PATCH /api/users/me/memories/{agentId}/knowledge/{factId}.
func (s *Server) UpdateProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, factID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
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

	fact, err := s.memoryManagement.ReplaceKnowledge(r.Context(), memorywrite.KnowledgeReplaceInput{
		FactID: factID, UserID: info.UserID, AgentID: agentID, Content: body.Content,
	})
	if errors.Is(err, memorywrite.ErrFactNotRestorable) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, activeKnowledgeToAPI(fact))
}

// DeleteProfileKnowledge handles DELETE /api/users/me/memories/{agentId}/knowledge/{factId}.
func (s *Server) DeleteProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, factID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if !validKnowledgeFactID(factID) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	_, err := s.memoryManagement.DeprecateKnowledge(r.Context(), memorywrite.KnowledgeDeprecateInput{
		FactID: factID, UserID: info.UserID, AgentID: agentID, DeprecatedBy: info.UserID,
	})
	if errors.Is(err, memorywrite.ErrFactNotRestorable) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

// RestoreProfileKnowledge handles POST /api/users/me/memories/{agentId}/knowledge/{factId}/restore.
func (s *Server) RestoreProfileKnowledge(w http.ResponseWriter, r *http.Request, agentID string, factID string) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if !validKnowledgeFactID(factID) {
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	}
	result, err := s.memoryManagement.RestoreKnowledge(r.Context(), memorywrite.KnowledgeRestoreInput{
		FactID: factID, UserID: info.UserID, AgentID: agentID, RestoredBy: info.UserID, Now: time.Now().UTC(),
	})
	switch {
	case errors.Is(err, memorywrite.ErrFactNotFound):
		writeError(w, http.StatusNotFound, "knowledge not found")
		return
	case errors.Is(err, memorywrite.ErrFactDuplicateContent):
		writeError(w, http.StatusConflict, "active knowledge already has this content")
		return
	case errors.Is(err, memorywrite.ErrFactRestoreExpired):
		writeError(w, http.StatusGone, "knowledge restore window expired")
		return
	case errors.Is(err, memorywrite.ErrFactNotRestorable):
		writeError(w, http.StatusConflict, "knowledge is not restorable")
		return
	case err != nil:
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, activeKnowledgeToAPI(result.Fact))
}

func knowledgeItemsToAPI(items []memorywrite.KnowledgeItem) []apitypes.KnowledgeItem {
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

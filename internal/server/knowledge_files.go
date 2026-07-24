package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/knowledge"
)

const (
	knowledgeMultipartOverhead = 1 << 20
	maxKnowledgePageTokenBytes = 4 << 10
)

var errInvalidKnowledgeMultipart = errors.New("invalid knowledge multipart body")

// ListKnowledgeFiles handles GET /api/knowledge-files.
func (s *Server) ListKnowledgeFiles(
	w http.ResponseWriter,
	r *http.Request,
	params apiserver.ListKnowledgeFilesParams,
) {
	owner, ok := s.resolveKnowledgeOwner(w, r, params.Scope, params.AgentId)
	if !ok {
		return
	}
	if s.knowledgeSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
		return
	}

	query := ""
	if params.Q != nil {
		query = strings.TrimSpace(*params.Q)
	}
	if utf8.RuneCountInString(query) > 200 {
		writeErrorDetails(w, http.StatusBadRequest, "q must not exceed 200 characters", map[string]any{
			"reason": "invalid_query",
		})
		return
	}
	pageSize := 0
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	var cursor *knowledge.ListCursor
	if params.PageToken != nil && *params.PageToken != "" {
		decoded, err := decodeKnowledgeFilePageToken(*params.PageToken, owner, query)
		if err != nil {
			writeErrorDetails(w, http.StatusBadRequest, "invalid page token", map[string]any{
				"reason": "invalid_page_token",
			})
			return
		}
		cursor = &decoded
	}

	result, err := s.knowledgeSvc.List(r.Context(), knowledge.ListInput{
		Owner: owner, Query: query, PageSize: pageSize, Cursor: cursor,
	})
	if err != nil {
		if errors.Is(err, knowledge.ErrServiceUnavailable) || errors.Is(err, knowledge.ErrInvalidOwner) {
			writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
			return
		}
		if strings.Contains(err.Error(), "page size") || strings.Contains(err.Error(), "query") ||
			strings.Contains(err.Error(), "cursor") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.writeInternalError(w, err)
		return
	}

	files := make([]apitypes.KnowledgeFile, 0, len(result.Files))
	for _, file := range result.Files {
		view, err := knowledgeFileView(file)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		files = append(files, view)
	}
	response := apitypes.KnowledgeFileList{
		KnowledgeFiles: files,
		Quota:          knowledgeQuotaView(result.Quota),
	}
	if result.HasMore && len(result.Files) > 0 {
		last := result.Files[len(result.Files)-1]
		token, err := encodeKnowledgeFilePageToken(owner, query, knowledge.ListCursor{
			CreatedAt: last.CreatedAt,
			ID:        last.ID,
		})
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		response.NextPageToken = &token
	}
	writeData(w, http.StatusOK, response)
}

// CreateKnowledgeFile handles POST /api/knowledge-files.
func (s *Server) CreateKnowledgeFile(
	w http.ResponseWriter,
	r *http.Request,
	params apiserver.CreateKnowledgeFileParams,
) {
	// Resolve authority before constructing the multipart reader. An
	// unauthorized request is rejected without consuming a potentially large
	// upload body.
	owner, ok := s.resolveKnowledgeOwner(w, r, params.Scope, params.AgentId)
	if !ok {
		return
	}
	if s.knowledgeSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
		return
	}

	fileName, content, err := readKnowledgeMultipart(w, r)
	if err != nil {
		reason := "invalid_multipart"
		message := "multipart body must contain exactly one file field"
		if errors.Is(err, knowledge.ErrFileTooLarge) {
			reason = "file_too_large"
			message = fmt.Sprintf("file must not exceed %d bytes", knowledge.MaxFileBytes)
		}
		writeErrorDetails(w, http.StatusBadRequest, message, map[string]any{"reason": reason})
		return
	}

	file, err := s.knowledgeSvc.Create(r.Context(), owner, fileName, content)
	if err != nil {
		switch {
		case errors.Is(err, knowledge.ErrFileTooLarge):
			writeErrorDetails(w, http.StatusBadRequest, "file is too large", map[string]any{
				"reason": "file_too_large",
			})
		case errors.Is(err, knowledge.ErrUnsupportedFileType):
			writeErrorDetails(w, http.StatusBadRequest, "unsupported file type", map[string]any{
				"reason": "unsupported_file_type",
			})
		case errors.Is(err, knowledge.ErrInvalidFile):
			writeErrorDetails(w, http.StatusBadRequest, "invalid file content", map[string]any{
				"reason": "invalid_file",
			})
		case errors.Is(err, knowledge.ErrQuotaExceeded):
			var quotaErr *knowledge.QuotaExceededError
			if !errors.As(err, &quotaErr) {
				s.writeInternalError(w, err)
				return
			}
			writeErrorDetails(w, http.StatusTooManyRequests, "knowledge quota exceeded", map[string]any{
				"reason": "quota_exceeded",
				"quota":  knowledgeQuotaView(quotaErr.Quota),
			})
		case errors.Is(err, knowledge.ErrServiceUnavailable):
			writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
		default:
			s.writeInternalError(w, err)
		}
		return
	}

	view, err := knowledgeFileView(file)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}

// GetKnowledgeFile handles GET /api/knowledge-files/{id}.
func (s *Server) GetKnowledgeFile(w http.ResponseWriter, r *http.Request, id string) {
	authority, ok := s.knowledgeAuthority(w, r)
	if !ok {
		return
	}
	if s.knowledgeSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusNotFound, "knowledge file not found")
		return
	}
	file, err := s.knowledgeSvc.GetManaged(r.Context(), authority, id)
	if err != nil {
		s.writeKnowledgeManagementError(w, err)
		return
	}
	view, err := knowledgeFileView(file)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

// DeleteKnowledgeFile handles DELETE /api/knowledge-files/{id}.
func (s *Server) DeleteKnowledgeFile(w http.ResponseWriter, r *http.Request, id string) {
	authority, ok := s.knowledgeAuthority(w, r)
	if !ok {
		return
	}
	if s.knowledgeSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusNotFound, "knowledge file not found")
		return
	}
	_, err := s.knowledgeSvc.GetManaged(r.Context(), authority, id)
	if err != nil {
		s.writeKnowledgeManagementError(w, err)
		return
	}
	if _, err := s.knowledgeSvc.Delete(r.Context(), id); errors.Is(err, knowledge.ErrNotFound) {
		writeError(w, http.StatusNotFound, "knowledge file not found")
		return
	} else if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) resolveKnowledgeOwner(
	w http.ResponseWriter,
	r *http.Request,
	scopeValue apitypes.KnowledgeFileScope,
	agentIDValue *string,
) (knowledge.Owner, bool) {
	if !scopeValue.Valid() {
		writeError(w, http.StatusBadRequest, "invalid scope")
		return knowledge.Owner{}, false
	}
	authority, ok := s.knowledgeAuthority(w, r)
	if !ok {
		return knowledge.Owner{}, false
	}
	if s.knowledgeSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
		return knowledge.Owner{}, false
	}
	scope := string(scopeValue)
	agentID := ""
	if agentIDValue != nil {
		agentID = strings.TrimSpace(*agentIDValue)
	}
	if (scope == string(knowledge.ScopeSystem) || scope == string(knowledge.ScopeUser)) && agentID != "" {
		writeError(w, http.StatusBadRequest, "agent_id is not allowed for this scope")
		return knowledge.Owner{}, false
	}
	if (scope == string(knowledge.ScopeUserAgent) || scope == string(knowledge.ScopeSystemAgent)) &&
		agentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required for this scope")
		return knowledge.Owner{}, false
	}
	owner, err := s.knowledgeSvc.ResolveManageOwner(
		r.Context(),
		authority,
		knowledge.Scope(scope),
		agentID,
	)
	if err != nil {
		s.writeKnowledgeManagementError(w, err)
		return knowledge.Owner{}, false
	}
	return owner, true
}

// knowledgeAuthority mints the trusted HTTP caller authority. Resource policy
// remains inside Knowledge Base; request parameters never participate here.
func (s *Server) knowledgeAuthority(
	w http.ResponseWriter,
	r *http.Request,
) (authz.Authority, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return authz.Authority{}, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return authz.Authority{}, false
	}
	return authority, true
}

func (s *Server) writeKnowledgeManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, knowledge.ErrInvalidOwner):
		writeError(w, http.StatusBadRequest, "invalid knowledge owner")
	case errors.Is(err, knowledge.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, knowledge.ErrNotFound):
		writeError(w, http.StatusNotFound, "knowledge file not found")
	case errors.Is(err, knowledge.ErrServiceUnavailable):
		writeError(w, http.StatusServiceUnavailable, "knowledge service is unavailable")
	default:
		s.writeInternalError(w, err)
	}
}

func readKnowledgeMultipart(
	w http.ResponseWriter,
	r *http.Request,
) (string, []byte, error) {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return "", nil, errInvalidKnowledgeMultipart
	}
	r.Body = http.MaxBytesReader(w, r.Body, knowledge.MaxFileBytes+knowledgeMultipartOverhead)
	reader := multipart.NewReader(r.Body, parameters["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		return "", nil, fmt.Errorf("%w: missing file part", errInvalidKnowledgeMultipart)
	}
	defer func() { _ = part.Close() }()
	if part.FormName() != "file" || part.FileName() == "" {
		return "", nil, fmt.Errorf("%w: first part must be file", errInvalidKnowledgeMultipart)
	}

	content, err := io.ReadAll(io.LimitReader(part, knowledge.MaxFileBytes+1))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return "", nil, knowledge.ErrFileTooLarge
		}
		return "", nil, fmt.Errorf("%w: read file: %w", errInvalidKnowledgeMultipart, err)
	}
	if len(content) > knowledge.MaxFileBytes {
		return "", nil, knowledge.ErrFileTooLarge
	}
	next, err := reader.NextPart()
	if err == nil {
		_ = next.Close()
		return "", nil, fmt.Errorf("%w: extra multipart field", errInvalidKnowledgeMultipart)
	}
	if !errors.Is(err, io.EOF) {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return "", nil, knowledge.ErrFileTooLarge
		}
		return "", nil, fmt.Errorf("%w: read trailer: %w", errInvalidKnowledgeMultipart, err)
	}
	return part.FileName(), content, nil
}

func knowledgeFileView(file knowledge.File) (apitypes.KnowledgeFile, error) {
	id, err := uuid.Parse(file.ID)
	if err != nil {
		return apitypes.KnowledgeFile{}, fmt.Errorf("parse knowledge file id: %w", err)
	}
	view := apitypes.KnowledgeFile{
		Id:        id,
		Scope:     apitypes.KnowledgeFileScope(file.Owner.Scope),
		FileName:  file.FileName,
		MediaType: apitypes.KnowledgeFileMediaType(file.MediaType),
		SizeBytes: file.SizeBytes,
		Status:    apitypes.KnowledgeFileStatus(file.Status),
		CreatedAt: file.CreatedAt.UTC(),
		UpdatedAt: file.UpdatedAt.UTC(),
	}
	if file.ErrorMessage != "" {
		message := file.ErrorMessage
		view.ErrorMessage = &message
	}
	return view, nil
}

func knowledgeQuotaView(quota knowledge.Quota) apitypes.KnowledgeFileQuota {
	return apitypes.KnowledgeFileQuota{
		UsedFiles: quota.UsedFiles,
		MaxFiles:  quota.MaxFiles,
		UsedBytes: quota.UsedBytes,
		MaxBytes:  quota.MaxBytes,
	}
}

type knowledgeFilePageToken struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Scope     string `json:"scope"`
	AgentID   string `json:"agent_id,omitempty"`
	Query     string `json:"q,omitempty"`
}

func encodeKnowledgeFilePageToken(
	owner knowledge.Owner,
	query string,
	cursor knowledge.ListCursor,
) (string, error) {
	payload, err := json.Marshal(knowledgeFilePageToken{
		Version: 1, CreatedAt: cursor.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID: cursor.ID, Scope: string(owner.Scope), AgentID: owner.AgentID, Query: query,
	})
	if err != nil {
		return "", fmt.Errorf("encode knowledge page token: %w", err)
	}
	// Pagination tokens are opaque cursors, not authorization credentials.
	// Scope and Agent access are resolved from the authenticated request before
	// this filter-bound cursor is decoded.
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeKnowledgeFilePageToken(
	token string,
	owner knowledge.Owner,
	query string,
) (knowledge.ListCursor, error) {
	if len(token) > maxKnowledgePageTokenBytes {
		return knowledge.ListCursor{}, fmt.Errorf("page token is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return knowledge.ListCursor{}, fmt.Errorf("decode page token payload: %w", err)
	}
	var decoded knowledgeFilePageToken
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return knowledge.ListCursor{}, fmt.Errorf("decode page token JSON: %w", err)
	}
	if decoded.Version != 1 || decoded.Scope != string(owner.Scope) ||
		decoded.AgentID != owner.AgentID || decoded.Query != query {
		return knowledge.ListCursor{}, fmt.Errorf("page token does not match filters")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
	if err != nil || createdAt.IsZero() {
		return knowledge.ListCursor{}, fmt.Errorf("invalid page token timestamp")
	}
	if _, err := uuid.Parse(decoded.ID); err != nil {
		return knowledge.ListCursor{}, fmt.Errorf("invalid page token id")
	}
	return knowledge.ListCursor{CreatedAt: createdAt.UTC(), ID: decoded.ID}, nil
}

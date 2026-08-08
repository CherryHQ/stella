package server

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/library"
)

const (
	defaultLibraryFilePageSize = 20
	maxLibraryFilePageSize     = 100
	maxLibraryFileSearchRunes  = 200
	// Multipart framing and headers are bounded separately from the 25 MiB file
	// body enforced by the Library acquisition service.
	maxLibraryMultipartBytes = library.MaxFileBytes + (1 << 20)
)

// ListLibraryFiles handles GET /api/library-files.
func (s *Server) ListLibraryFiles(w http.ResponseWriter, r *http.Request, params apiserver.ListLibraryFilesParams) {
	authority, svc, ok := s.libraryFileAccess(w, r)
	if !ok {
		return
	}
	scope := library.Scope(params.Scope)
	agentID, validAgentID := libraryAgentIDParam(params.AgentId)
	if !validAgentID {
		writeError(w, http.StatusBadRequest, "agent_id must not be empty")
		return
	}
	query := strings.TrimSpace(libraryParamString(params.Q))
	if utf8.RuneCountInString(query) > maxLibraryFileSearchRunes {
		writeError(w, http.StatusBadRequest, "q must be at most 200 characters")
		return
	}
	pageSize := defaultLibraryFilePageSize
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	if pageSize < 1 || pageSize > maxLibraryFilePageSize {
		writeError(w, http.StatusBadRequest, "page_size must be between 1 and 100")
		return
	}

	pageQuery := libraryFilePageQuery{
		UserID: string(authority.UserID()), Scope: string(scope), AgentID: agentID, Query: query,
	}
	var cursor *library.ListCursor
	if params.PageToken != nil {
		var err error
		cursor, err = decodeLibraryFilePageToken(*params.PageToken, pageQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	files, quota, err := svc.ListManaged(
		r.Context(), authority, scope, agentID, query, int32(pageSize+1), cursor,
	)
	if err != nil {
		s.writeLibraryFileError(w, err)
		return
	}
	var nextToken *string
	if len(files) > pageSize {
		files = files[:pageSize]
		last := files[len(files)-1]
		token, err := encodeLibraryFilePageToken(
			library.ListCursor{CreatedAt: last.CreatedAt, ID: last.ID}, pageQuery,
		)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		nextToken = &token
	}

	views := make([]apitypes.LibraryFile, len(files))
	for i := range files {
		views[i] = libraryFileView(files[i])
	}
	writeData(w, http.StatusOK, apitypes.LibraryFileList{
		LibraryFiles:  views,
		NextPageToken: nextToken,
		Quota:         libraryQuotaView(quota),
	})
}

// CreateLibraryFile handles POST /api/library-files. The collection gate is
// evaluated before MultipartReader advances the request body; CreateManagedUpload
// repeats the authoritative domain check immediately before reading file bytes.
func (s *Server) CreateLibraryFile(w http.ResponseWriter, r *http.Request, params apiserver.CreateLibraryFileParams) {
	authority, svc, ok := s.libraryFileAccess(w, r)
	if !ok {
		return
	}
	scope := library.Scope(params.Scope)
	agentID, validAgentID := libraryAgentIDParam(params.AgentId)
	if !validAgentID {
		writeError(w, http.StatusBadRequest, "agent_id must not be empty")
		return
	}
	if _, err := svc.ResolveManageOwner(r.Context(), authority, scope, agentID); err != nil {
		s.writeLibraryFileError(w, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLibraryMultipartBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			s.writeLibraryFileError(w, err)
		} else {
			writeError(w, http.StatusBadRequest, "invalid multipart form")
		}
		return
	}
	if part.FormName() != "file" || part.FileName() == "" {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer func() { _ = part.Close() }()

	file, err := svc.CreateManagedUpload(
		r.Context(), authority, scope, agentID, part.FileName(), part,
	)
	if err != nil {
		s.writeLibraryFileError(w, err)
		return
	}
	writeData(w, http.StatusCreated, libraryFileView(file))
}

// GetLibraryFile handles GET /api/library-files/{id}.
func (s *Server) GetLibraryFile(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	authority, svc, ok := s.libraryFileAccess(w, r)
	if !ok {
		return
	}
	file, err := svc.GetManaged(r.Context(), authority, id.String())
	if err != nil {
		s.writeLibraryFileError(w, err)
		return
	}
	writeData(w, http.StatusOK, libraryFileView(file))
}

// DeleteLibraryFile handles DELETE /api/library-files/{id}.
func (s *Server) DeleteLibraryFile(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	authority, svc, ok := s.libraryFileAccess(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteManaged(r.Context(), authority, id.String()); err != nil {
		s.writeLibraryFileError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) libraryFileAccess(w http.ResponseWriter, r *http.Request) (authz.Authority, *library.Service, bool) {
	if s.librarySvc == nil {
		writeError(w, http.StatusServiceUnavailable, "library management is unavailable")
		return authz.Authority{}, nil, false
	}
	info := requireAuth(w, r)
	if info == nil {
		return authz.Authority{}, nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return authz.Authority{}, nil, false
	}
	return authority, s.librarySvc, true
}

func (s *Server) writeLibraryFileError(w http.ResponseWriter, err error) {
	var quotaErr *library.QuotaExceededError
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.Is(err, library.ErrNotFound):
		writeError(w, http.StatusNotFound, "library file not found")
	case errors.Is(err, library.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, library.ErrInvalidOwner):
		writeError(w, http.StatusBadRequest, "invalid scope or agent_id")
	case errors.Is(err, library.ErrUnsupportedFileType):
		writeError(w, http.StatusBadRequest, "supported file types are Markdown and plain text")
	case errors.Is(err, library.ErrInvalidFile):
		writeError(w, http.StatusBadRequest, "invalid library file")
	case errors.Is(err, library.ErrFileTooLarge):
		writeErrorDetails(w, http.StatusRequestEntityTooLarge, "library file exceeds 25 MiB", map[string]any{
			"code": "file_too_large", "max_bytes": library.MaxFileBytes,
		})
	case errors.As(err, &maxBytesErr):
		writeErrorDetails(w, http.StatusRequestEntityTooLarge, "library upload exceeds the request size limit", map[string]any{
			"code": "request_too_large", "max_bytes": maxBytesErr.Limit,
		})
	case errors.As(err, &quotaErr):
		writeErrorDetails(w, http.StatusTooManyRequests, "library quota exceeded", map[string]any{
			"code":       "quota_exceeded",
			"used_files": quotaErr.Quota.UsedFiles,
			"max_files":  quotaErr.Quota.MaxFiles,
			"used_bytes": quotaErr.Quota.UsedBytes,
			"max_bytes":  quotaErr.Quota.MaxBytes,
		})
	case errors.Is(err, library.ErrSpoolCapacity),
		errors.Is(err, library.ErrRawStorageDegraded),
		errors.Is(err, library.ErrServiceUnavailable):
		writeError(w, http.StatusServiceUnavailable, "library management is temporarily unavailable")
	case errors.Is(err, io.ErrUnexpectedEOF):
		writeError(w, http.StatusBadRequest, "incomplete library file upload")
	default:
		s.writeInternalError(w, err)
	}
}

func libraryFileView(file library.LibraryFile) apitypes.LibraryFile {
	id := uuid.MustParse(file.ID)
	fileName := file.FileName
	mediaType := file.MediaType
	sizeBytes := file.SizeBytes
	createdAt := file.CreatedAt.UTC()
	updatedAt := file.UpdatedAt.UTC()
	view := apitypes.LibraryFile{
		Id: &id, Scope: apitypes.LibraryFileScope(file.Owner.Scope),
		FileName: &fileName, MediaType: &mediaType, SizeBytes: &sizeBytes,
		Status: apitypes.LibraryFileStatus(file.Status), CreatedAt: &createdAt, UpdatedAt: &updatedAt,
	}
	if file.Owner.AgentID != "" {
		view.AgentId = stringPtr(file.Owner.AgentID)
	}
	if file.ErrorMessage != "" {
		view.ErrorMessage = stringPtr(file.ErrorMessage)
	}
	return view
}

func libraryQuotaView(quota library.Quota) apitypes.LibraryQuota {
	usedFiles, maxFiles := quota.UsedFiles, quota.MaxFiles
	usedBytes, maxBytes := quota.UsedBytes, quota.MaxBytes
	return apitypes.LibraryQuota{
		UsedFiles: &usedFiles, MaxFiles: &maxFiles, UsedBytes: &usedBytes, MaxBytes: &maxBytes,
	}
}

func libraryParamString[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

// libraryAgentIDParam preserves the difference between an omitted optional
// query parameter and an explicitly empty value rejected by the API contract.
func libraryAgentIDParam[T ~string](value *T) (string, bool) {
	if value == nil {
		return "", true
	}
	agentID := string(*value)
	return agentID, agentID != ""
}

package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

const libraryManagementListSibling = "settings_library_file_list"

var libraryManagementDescriptions = map[string]string{
	"list":   "List authorized Library files in one scope. Results include an opaque continuation token when more files exist.",
	"get":    "Read safe metadata and a version for one authorized Library file. Raw document bytes are never returned.",
	"upload": "Upload one Library file from a sandbox path after authorizing the current scope. The source file is never returned.",
	"delete": "Delete one Library file using the version from settings_library_file_get. Raw cleanup happens after the tombstone commits.",
}

// ManagementTool is one exact, Stella-only Library action. The direct-human
// authority is extracted at execution time, never taken from its arguments.
type ManagementTool struct {
	spec    SettingsLibraryActionTool
	service *Service
	runtime pkgsandbox.Session
}

func NewRuntimeManagementTool(service *Service, runtime pkgsandbox.Session, spec SettingsLibraryActionTool) *ManagementTool {
	return &ManagementTool{spec: spec, service: service, runtime: runtime}
}

func (t *ManagementTool) Definition() tools.Definition {
	return t.spec.Definition(libraryManagementDescriptions[t.spec.Action])
}

func (t *ManagementTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.service == nil {
		return "", ErrServiceUnavailable
	}
	authority, err := authz.DirectAuthority(ctx, authz.UserIDFromContext(ctx))
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, libraryManagementListSibling, err)
	}
	out, err := SettingsLibraryDispatch(ctx, libraryManagementHandler{service: t.service, authority: authority, runtime: t.runtime}, t.spec.Action, args)
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, libraryManagementListSibling, err)
	}
	return tools.MarshalResult(out)
}

type libraryManagementHandler struct {
	service   *Service
	authority authz.Authority
	runtime   pkgsandbox.Session
}

type libraryToolFile struct {
	ID        string     `json:"id"`
	Scope     Scope      `json:"scope"`
	AgentID   string     `json:"agent_id,omitempty"`
	FileName  string     `json:"file_name"`
	MediaType string     `json:"media_type"`
	SizeBytes int64      `json:"size_bytes"`
	Status    FileStatus `json:"status"`
	Version   string     `json:"version"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

func libraryToolView(file LibraryFile) libraryToolFile {
	return libraryToolFile{
		ID: file.ID, Scope: file.Owner.Scope, AgentID: file.Owner.AgentID,
		FileName: file.FileName, MediaType: file.MediaType, SizeBytes: file.SizeBytes,
		Status: file.Status, Version: file.UpdatedAt.UTC().Format(time.RFC3339Nano),
		CreatedAt: file.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: file.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (h libraryManagementHandler) List(ctx context.Context, in SettingsLibraryListInput) (any, error) {
	limit := in.PageSize
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("page_size must be between 1 and 100")
	}
	query := strings.TrimSpace(in.Q)
	cursor, err := decodeLibraryToolCursor(in.PageToken, Scope(in.Scope), in.TargetAgentId, query)
	if err != nil {
		return nil, err
	}
	files, quota, err := h.service.ListManaged(ctx, h.authority, Scope(in.Scope), in.TargetAgentId, query, int32(limit+1), cursor)
	if err != nil {
		return nil, err
	}
	var nextPageToken *string
	if len(files) > limit {
		files = files[:limit]
		token, err := encodeLibraryToolCursor(ListCursor{CreatedAt: files[len(files)-1].CreatedAt, ID: files[len(files)-1].ID}, Scope(in.Scope), in.TargetAgentId, query)
		if err != nil {
			return nil, err
		}
		nextPageToken = &token
	}
	out := make([]libraryToolFile, 0, len(files))
	for _, file := range files {
		out = append(out, libraryToolView(file))
	}
	return map[string]any{"library_files": out, "next_page_token": nextPageToken, "quota": quota}, nil
}

func (h libraryManagementHandler) Get(ctx context.Context, in SettingsLibraryGetInput) (any, error) {
	file, err := h.service.GetManaged(ctx, h.authority, in.Id)
	if err != nil {
		return nil, err
	}
	return libraryToolView(file), nil
}

func (h libraryManagementHandler) Upload(ctx context.Context, in SettingsLibraryUploadInput) (any, error) {
	content, err := h.readContentPath(ctx, in.ContentPath, MaxFileBytes)
	if err != nil {
		return nil, fmt.Errorf("content_path: %w", err)
	}
	file, err := h.service.CreateManagedUpload(ctx, h.authority, Scope(in.Scope), in.TargetAgentId, in.Name, bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	return libraryToolView(file), nil
}

func (h libraryManagementHandler) Delete(ctx context.Context, in SettingsLibraryDeleteInput) (any, error) {
	if err := h.service.DeleteManagedIfVersion(ctx, h.authority, in.Id, in.ExpectedVersion); err != nil {
		return nil, err
	}
	return map[string]string{"id": in.Id, "status": "deleted"}, nil
}

// Search is unreachable from this adapter. A crafted dispatch must not widen
// settings management into the ordinary Agent retrieval surface.
func (libraryManagementHandler) Search(context.Context, LibrarySearchInput) (any, error) {
	return nil, fmt.Errorf("library_search is not a settings management action")
}

func (h libraryManagementHandler) readContentPath(ctx context.Context, filePath string, maxBytes int64) ([]byte, error) {
	if h.runtime == nil {
		return nil, fmt.Errorf("sandbox file access is unavailable")
	}
	view, err := pkgsandbox.SelectFileView(ctx, h.runtime)
	if err != nil {
		return nil, err
	}
	resolved := filePath
	if strings.HasPrefix(resolved, "$") {
		resolved, err = pkgsandbox.ExpandPathVariables(resolved, view.Policy.Env)
		if err != nil {
			return nil, err
		}
	}
	resolved, err = tools.ResolvePath(view.WorkingDir, resolved)
	if err != nil {
		return nil, err
	}
	info, err := view.Files.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, fmt.Errorf("path is a directory")
	}
	if info.Size < 0 || info.Size > maxBytes {
		return nil, fmt.Errorf("file is %d bytes, over the %d-byte limit", info.Size, maxBytes)
	}
	content, err := view.Files.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("file is %d bytes, over the %d-byte limit", len(content), maxBytes)
	}
	return content, nil
}

type libraryToolCursor struct {
	CreatedAt   time.Time `json:"created_at"`
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
}

func libraryToolFingerprint(scope Scope, agentID, query string) string {
	sum := sha256.Sum256([]byte(string(scope) + "\x00" + agentID + "\x00" + query))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encodeLibraryToolCursor(cursor ListCursor, scope Scope, agentID, query string) (string, error) {
	payload, err := json.Marshal(libraryToolCursor{CreatedAt: cursor.CreatedAt.UTC(), ID: cursor.ID, Fingerprint: libraryToolFingerprint(scope, agentID, query)})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeLibraryToolCursor(token string, scope Scope, agentID, query string) (*ListCursor, error) {
	if token == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("page_token is malformed")
	}
	var cursor libraryToolCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.CreatedAt.IsZero() || cursor.ID == "" || cursor.Fingerprint != libraryToolFingerprint(scope, agentID, query) {
		return nil, fmt.Errorf("page_token does not match the library file query")
	}
	return &ListCursor{CreatedAt: cursor.CreatedAt.UTC(), ID: cursor.ID}, nil
}

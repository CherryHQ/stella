package access

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

var (
	ErrInvalid       = errors.New("invalid workspace request")
	ErrIsDir         = errors.New("workspace path is a directory")
	ErrBinary        = errors.New("workspace file appears to be binary")
	ErrTooLarge      = errors.New("workspace file exceeds read limit")
	ErrAlreadyExists = errors.New("workspace destination already exists")
)

const workspaceReadMaxBytes int64 = 32 << 20

type WorkspaceScope string

const (
	WorkspaceScopeAgent WorkspaceScope = "agent"
	WorkspaceScopeUser  WorkspaceScope = "user"
)

type WorkspaceInfo struct {
	Paths      []string `json:"paths"`
	TotalFiles int      `json:"total_files"`
	TotalDirs  int      `json:"total_dirs"`
	TotalBytes int64    `json:"total_bytes"`
}

type WorkspaceListInput struct {
	AgentID    string
	SessionID  string
	Scope      WorkspaceScope
	ShowHidden bool
	Path       string
	Depth      int
}

type WorkspacePathInput struct {
	AgentID   string
	SessionID string
	Scope     WorkspaceScope
	Path      string
}

type WorkspaceCreateInput struct {
	AgentID   string
	SessionID string
	Scope     WorkspaceScope
	Path      string
	Content   string
	IsDir     bool
}

type WorkspaceMoveInput struct {
	AgentID   string
	SessionID string
	Scope     WorkspaceScope
	Path      string
	NewPath   string
}

type WorkspaceReadInput struct {
	AgentID   string
	SessionID string
	Scope     WorkspaceScope
	Path      string
	Raw       bool
	// MaxBytes bounds allocation at the provider Filesystem boundary. Zero uses
	// the workspace API ceiling; callers needing a smaller bound may set it.
	MaxBytes int64
}

type WorkspaceReadResult struct {
	Path         string    `json:"path"`
	Content      string    `json:"content,omitempty"`
	Language     string    `json:"language,omitempty"`
	Raw          bool      `json:"-"`
	RawName      string    `json:"-"`
	RawMediaType string    `json:"-"`
	RawContent   []byte    `json:"-"`
	RawModTime   time.Time `json:"-"`
}

type WorkspaceWriteInput struct {
	AgentID   string
	SessionID string
	Scope     WorkspaceScope
	Path      string
	Content   string
}

type WorkspaceUploadInput struct {
	AgentID       string
	SessionID     string
	Filename      string
	Reader        io.Reader
	ContentLength *int64
	Now           time.Time
}

type WorkspaceUploadResult struct {
	// Path is the portable agent path expression the agent reads (for example,
	// $STELLA_ASSETS_DIR/202608/file.png), never a provider or host coordinate.
	Path string `json:"path"`
	// RelativePath is the upload location relative to the /user workspace root.
	RelativePath string         `json:"relative_path"`
	Scope        WorkspaceScope `json:"scope"`
}

func (a *Access) ListWorkspace(ctx context.Context, in WorkspaceListInput) (WorkspaceInfo, error) {
	scope, root, listPath, err := workspacePath(in.Scope, in.Path, true)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 2
	}
	var result WorkspaceInfo
	err = a.useWorkspaceFilesystem(ctx, in.AgentID, in.SessionID, scope, authz.ActionList, func(filesystem pkgsandbox.Filesystem) error {
		result, err = collectWorkspaceInfo(ctx, filesystem, root, listPath, in.ShowHidden, depth)
		return err
	})
	return result, err
}

func (a *Access) CreateWorkspacePath(ctx context.Context, in WorkspaceCreateInput) (WorkspaceInfo, error) {
	scope, root, name, err := workspacePath(in.Scope, in.Path, false)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	var result WorkspaceInfo
	err = a.useWorkspaceFilesystem(ctx, in.AgentID, in.SessionID, scope, authz.ActionCreate, func(filesystem pkgsandbox.Filesystem) error {
		if in.IsDir {
			if err := filesystem.Mkdir(ctx, name, 0o755); err != nil {
				return err
			}
		} else {
			if err := mkdirWorkspaceParent(ctx, filesystem, name); err != nil {
				return err
			}
			content := []byte(in.Content)
			length := int64(len(content))
			if err := filesystem.Write(ctx, name, bytes.NewReader(content), pkgsandbox.WriteOptions{Perm: 0o644, ContentLength: &length}); err != nil {
				return err
			}
		}
		result, err = collectWorkspaceInfo(ctx, filesystem, root, root, false, 0)
		return err
	})
	return result, err
}

func (a *Access) DeleteWorkspacePath(ctx context.Context, in WorkspacePathInput) error {
	scope, _, name, err := workspacePath(in.Scope, in.Path, false)
	if err != nil {
		return err
	}
	return a.useWorkspaceFilesystem(ctx, in.AgentID, in.SessionID, scope, authz.ActionDelete, func(filesystem pkgsandbox.Filesystem) error {
		return filesystem.Remove(ctx, name, true)
	})
}

func (a *Access) MoveWorkspacePath(ctx context.Context, in WorkspaceMoveInput) (WorkspaceInfo, error) {
	scope, root, source, err := workspacePath(in.Scope, in.Path, false)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	destinationScope, _, destination, err := workspacePath(in.Scope, in.NewPath, false)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if destinationScope != scope {
		return WorkspaceInfo{}, ErrInvalid
	}
	var result WorkspaceInfo
	err = a.useWorkspaceFilesystem(ctx, in.AgentID, in.SessionID, scope, authz.ActionWrite, func(filesystem pkgsandbox.Filesystem) error {
		if _, err := filesystem.Stat(ctx, destination); err == nil {
			return ErrAlreadyExists
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := mkdirWorkspaceParent(ctx, filesystem, destination); err != nil {
			return err
		}
		if err := filesystem.Rename(ctx, source, destination); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return ErrAlreadyExists
			}
			return err
		}
		result, err = collectWorkspaceInfo(ctx, filesystem, root, root, false, 0)
		return err
	})
	return result, err
}

func (a *Access) ReadWorkspacePath(ctx context.Context, in WorkspaceReadInput) (WorkspaceReadResult, error) {
	scope, _, name, err := workspacePath(in.Scope, in.Path, false)
	if err != nil {
		return WorkspaceReadResult{}, err
	}
	maxBytes := in.MaxBytes
	if maxBytes <= 0 {
		maxBytes = workspaceReadMaxBytes
	}
	var result WorkspaceReadResult
	err = a.useWorkspaceFilesystem(ctx, in.AgentID, in.SessionID, scope, authz.ActionRead, func(filesystem pkgsandbox.Filesystem) error {
		reader, readInfo, err := filesystem.Read(ctx, name, pkgsandbox.ReadOptions{MaxBytes: maxBytes})
		if err != nil {
			if reader != nil {
				err = errors.Join(err, reader.Close())
			}
			return workspaceFilesystemError(err)
		}
		if reader == nil {
			return ErrInvalid
		}
		// A provider normally enforces MaxBytes too, but retain a local limit so
		// a faulty remote implementation cannot turn this into an unbounded
		// allocation. The extra byte distinguishes an exact-size file from an
		// over-limit stream without buffering either whole oversized payload.
		data, readErr := io.ReadAll(io.LimitReader(reader, maxBytes+1))
		closeErr := reader.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return workspaceFilesystemError(err)
		}
		if readInfo.IsDir {
			return ErrIsDir
		}
		if !readInfo.Mode.IsRegular() || readInfo.Size < 0 {
			return ErrInvalid
		}
		if readInfo.Size > maxBytes || int64(len(data)) > maxBytes {
			return ErrTooLarge
		}
		if int64(len(data)) != readInfo.Size {
			return ErrInvalid
		}
		if in.Raw {
			mediaType := mime.TypeByExtension(path.Ext(name))
			if mediaType == "" {
				mediaType = "application/octet-stream"
			}
			return assignWorkspaceRawResult(&result, workspaceRelativePath(scope, name), path.Base(name), mediaType, data, readInfo.ModTime)
		}
		probe := data
		if len(probe) > 512 {
			probe = probe[:512]
		}
		if slices.Contains(probe, 0) {
			return ErrBinary
		}
		result = WorkspaceReadResult{Path: workspaceRelativePath(scope, name), Content: string(data), Language: DetectLanguage(name)}
		return nil
	})
	return result, err
}

func assignWorkspaceRawResult(result *WorkspaceReadResult, relative, name, mediaType string, data []byte, modTime time.Time) error {
	*result = WorkspaceReadResult{Path: relative, Raw: true, RawName: name, RawMediaType: mediaType, RawContent: data, RawModTime: modTime}
	return nil
}

func (a *Access) WriteWorkspacePath(ctx context.Context, in WorkspaceWriteInput) (WorkspaceReadResult, error) {
	scope, _, name, err := workspacePath(in.Scope, in.Path, false)
	if err != nil {
		return WorkspaceReadResult{}, err
	}
	var result WorkspaceReadResult
	err = a.useWorkspaceFilesystem(ctx, in.AgentID, in.SessionID, scope, authz.ActionWrite, func(filesystem pkgsandbox.Filesystem) error {
		if err := mkdirWorkspaceParent(ctx, filesystem, name); err != nil {
			return err
		}
		content := []byte(in.Content)
		length := int64(len(content))
		if err := filesystem.Write(ctx, name, bytes.NewReader(content), pkgsandbox.WriteOptions{Perm: 0o644, ContentLength: &length}); err != nil {
			return err
		}
		result = WorkspaceReadResult{Path: workspaceRelativePath(scope, name), Content: in.Content, Language: DetectLanguage(name)}
		return nil
	})
	return result, err
}

func (a *Access) UploadWorkspacePath(ctx context.Context, in WorkspaceUploadInput) (WorkspaceUploadResult, error) {
	if in.Reader == nil {
		return WorkspaceUploadResult{}, ErrInvalid
	}
	filename := path.Base(strings.ReplaceAll(in.Filename, "\\", "/"))
	if filename == "" || filename == "." || filename == "/" {
		return WorkspaceUploadResult{}, ErrInvalid
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	uploadID, err := uuid.NewV7()
	if err != nil {
		return WorkspaceUploadResult{}, fmt.Errorf("%w: generate upload ID: %w", ErrUnavailable, err)
	}
	relative := path.Join("assets", now.Format("200601"), fmt.Sprintf("%s-%s-%s", now.Format("20060102"), uploadID, filename))
	name := path.Join(pkgsandbox.PathUser, relative)
	var result WorkspaceUploadResult
	err = a.useWorkspaceFilesystem(ctx, in.AgentID, in.SessionID, WorkspaceScopeUser, authz.ActionCreate, func(filesystem pkgsandbox.Filesystem) error {
		if err := filesystem.Mkdir(ctx, path.Dir(name), 0o755); err != nil {
			return err
		}
		if err := filesystem.Upload(ctx, name, in.Reader, pkgsandbox.WriteOptions{Perm: 0o644, ContentLength: in.ContentLength}); err != nil {
			return err
		}
		result = WorkspaceUploadResult{
			Path:         "$" + pkgsandbox.EnvStellaAssetsDir + "/" + strings.TrimPrefix(relative, "assets/"),
			RelativePath: relative,
			Scope:        WorkspaceScopeUser,
		}
		return nil
	})
	return result, err
}

// useWorkspaceFilesystem authorizes the exact durable Session before obtaining
// its exact runtime and making one callback-only Filesystem lease. The callback
// owns all filesystem work for one HTTP workspace operation; it receives no
// host path and cannot escape the provider's canonical mount contract.
func (a *Access) useWorkspaceFilesystem(ctx context.Context, agentID, sessionID string, scope WorkspaceScope, action authz.Action, use func(pkgsandbox.Filesystem) error) error {
	if !validWorkspaceScope(scope) || use == nil {
		return ErrInvalid
	}
	info, err := a.Workspace(ctx, agentID, sessionID, action)
	if err != nil {
		return err
	}
	if info.AgentID == "" || (info.UserID == "" && info.GroupID == "") {
		return ErrNotFound
	}
	runtime, err := a.svc.runtimeFor(info.AgentID)
	if err != nil {
		return err
	}
	filesystemRuntime, ok := runtime.(FilesystemRuntimeService)
	if !ok {
		return fmt.Errorf("%w: runtime lacks filesystem capability", ErrUnavailable)
	}
	return filesystemRuntime.UseFilesystem(ctx, info, use)
}

func workspacePath(scope WorkspaceScope, input string, allowRoot bool) (WorkspaceScope, string, string, error) {
	if !validWorkspaceScope(scope) {
		return "", "", "", ErrInvalid
	}
	root := workspaceCanonicalRoot(scope)
	if input == "" {
		if !allowRoot {
			return "", "", "", ErrInvalid
		}
		return scope, root, root, nil
	}
	if strings.HasPrefix(input, "/") {
		for _, mount := range []struct {
			scope WorkspaceScope
			root  string
		}{
			{WorkspaceScopeAgent, pkgsandbox.PathWorkspace},
			{WorkspaceScopeUser, pkgsandbox.PathUser},
		} {
			if input == mount.root && allowRoot {
				return mount.scope, mount.root, mount.root, nil
			}
			if relative, ok := strings.CutPrefix(input, mount.root+"/"); ok && strictWorkspaceRelative(relative) {
				return mount.scope, mount.root, path.Join(mount.root, relative), nil
			}
		}
		return "", "", "", ErrInvalid
	}
	name, suffix, hasVariable, err := pkgsandbox.SplitLeadingPathVariable(input)
	if err != nil {
		return "", "", "", ErrInvalid
	}
	if hasVariable {
		switch name {
		case pkgsandbox.EnvHome:
			scope, root = WorkspaceScopeAgent, pkgsandbox.PathWorkspace
		case pkgsandbox.EnvStellaAssetsDir:
			scope, root = WorkspaceScopeUser, path.Join(pkgsandbox.PathUser, "assets")
		default:
			return "", "", "", ErrInvalid
		}
		input = strings.TrimPrefix(suffix, "/")
	}
	if input == "" {
		if !allowRoot {
			return "", "", "", ErrInvalid
		}
		return scope, workspaceCanonicalRoot(scope), root, nil
	}
	if !strictWorkspaceRelative(input) {
		return "", "", "", ErrInvalid
	}
	return scope, workspaceCanonicalRoot(scope), path.Join(root, input), nil
}

func validWorkspaceScope(scope WorkspaceScope) bool {
	return scope == WorkspaceScopeAgent || scope == WorkspaceScopeUser
}

func workspaceCanonicalRoot(scope WorkspaceScope) string {
	if scope == WorkspaceScopeUser {
		return pkgsandbox.PathUser
	}
	return pkgsandbox.PathWorkspace
}

func strictWorkspaceRelative(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) || windowsAbsolutePath(value) {
		return false
	}
	for segment := range strings.SplitSeq(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return path.Clean(value) == value
}

func windowsAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && value[2] == '/'
}

func workspaceRelativePath(scope WorkspaceScope, name string) string {
	root := workspaceCanonicalRoot(scope)
	relative, ok := strings.CutPrefix(name, root+"/")
	if ok {
		return relative
	}
	return ""
}

func mkdirWorkspaceParent(ctx context.Context, filesystem pkgsandbox.Filesystem, name string) error {
	parent := path.Dir(name)
	if parent == workspaceCanonicalRoot(WorkspaceScopeAgent) || parent == workspaceCanonicalRoot(WorkspaceScopeUser) {
		return nil
	}
	return filesystem.Mkdir(ctx, parent, 0o755)
}

func workspaceFilesystemError(err error) error {
	switch {
	case errors.Is(err, pkgsandbox.ErrReadLimit):
		return ErrTooLarge
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	default:
		return err
	}
}

func collectWorkspaceInfo(ctx context.Context, filesystem pkgsandbox.Filesystem, root, listPath string, showHidden bool, depth int) (WorkspaceInfo, error) {
	info, err := filesystem.Stat(ctx, listPath)
	if err != nil {
		return WorkspaceInfo{}, workspaceFilesystemError(err)
	}
	if !info.IsDir {
		return WorkspaceInfo{}, ErrInvalid
	}
	result := WorkspaceInfo{Paths: []string{}}
	var walk func(string, int) error
	walk = func(directory string, level int) error {
		entries, err := filesystem.List(ctx, directory)
		if err != nil {
			return workspaceFilesystemError(err)
		}
		entries = slices.Clone(entries)
		slices.SortFunc(entries, func(left, right pkgsandbox.DirEntry) int { return strings.Compare(left.Name, right.Name) })
		for _, entry := range entries {
			if entry.Name == "" || !strictWorkspaceRelative(entry.Name) {
				return ErrInvalid
			}
			entryPath := path.Join(directory, entry.Name)
			relative := workspaceRelativePathForRoot(root, entryPath)
			if relative == "" {
				return ErrInvalid
			}
			if !showHidden && hiddenWorkspacePath(relative) {
				continue
			}
			if depth > 0 && level > depth {
				continue
			}
			if entry.IsDir {
				result.TotalDirs++
				result.Paths = append(result.Paths, relative+"/")
				if depth <= 0 || level < depth {
					if err := walk(entryPath, level+1); err != nil {
						return err
					}
				}
				continue
			}
			entryInfo, err := filesystem.Stat(ctx, entryPath)
			if err != nil {
				return workspaceFilesystemError(err)
			}
			result.TotalFiles++
			if entryInfo.Mode.IsRegular() {
				result.TotalBytes += entryInfo.Size
			}
			result.Paths = append(result.Paths, relative)
		}
		return nil
	}
	if err := walk(listPath, 1); err != nil {
		return WorkspaceInfo{}, err
	}
	return result, nil
}

func workspaceRelativePathForRoot(root, name string) string {
	if name == root {
		return ""
	}
	relative, _ := strings.CutPrefix(name, root+"/")
	return relative
}

func hiddenWorkspacePath(relative string) bool {
	for segment := range strings.SplitSeq(relative, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func DetectLanguage(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md", ".mdx":
		return "markdown"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".sh", ".bash":
		return "shell"
	case ".sql":
		return "sql"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".txt":
		return "text"
	default:
		return ""
	}
}

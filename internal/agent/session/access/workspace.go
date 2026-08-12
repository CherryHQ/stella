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
	"github.com/CherryHQ/stella/internal/home"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

var (
	ErrInvalid  = errors.New("invalid workspace request")
	ErrIsDir    = errors.New("workspace path is a directory")
	ErrBinary   = errors.New("workspace file appears to be binary")
	ErrTooLarge = errors.New("workspace file exceeds read limit")
)

const (
	workspaceReadMaxBytes   = int64(32 << 20)
	workspaceUploadMaxBytes = int64(32 << 20)
	workspaceListLimit      = 10_000
)

type WorkspaceScope string

const (
	WorkspaceScopeAgent WorkspaceScope = "agent"
	WorkspaceScopeUser  WorkspaceScope = "user"
)

type WorkspaceInfo struct {
	Root        string   `json:"root"`
	SandboxRoot string   `json:"sandbox_root"`
	Paths       []string `json:"paths"`
	TotalFiles  int      `json:"total_files"`
	TotalDirs   int      `json:"total_dirs"`
	TotalBytes  int64    `json:"total_bytes"`
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
	// MaxBytes bounds allocation at the authorized file boundary. Zero leaves
	// existing workspace read behavior unchanged.
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
	// Path is a portable logical path expression, never a host coordinate.
	Path string `json:"path"`
	// RelativePath is the upload location relative to its workspace root.
	// Combine with Scope to build a workspace file-content read URL.
	RelativePath string `json:"relative_path"`
	// Scope is the workspace root the file was written to.
	Scope WorkspaceScope `json:"scope"`
}

func (a *Access) ListWorkspace(ctx context.Context, in WorkspaceListInput) (WorkspaceInfo, error) {
	scope, name, err := canonicalWorkspacePath(in.Scope, in.Path, true)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 2
	}
	var result WorkspaceInfo
	err = a.withWorkspaceRoot(ctx, in.AgentID, in.SessionID, scope, authz.ActionList, home.RootReadOnly, func(root home.RootOperations, _ home.WorkspaceRequest) error {
		result, err = collectWorkspaceInfo(ctx, root, scope, name, in.ShowHidden, depth)
		return err
	})
	if errors.Is(err, home.ErrListLimit) {
		return WorkspaceInfo{}, ErrTooLarge
	}
	return result, err
}

func (a *Access) CreateWorkspacePath(ctx context.Context, in WorkspaceCreateInput) (WorkspaceInfo, error) {
	if in.Path == "" {
		return WorkspaceInfo{}, ErrInvalid
	}
	scope, name, err := canonicalWorkspacePath(in.Scope, in.Path, false)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	var result WorkspaceInfo
	err = a.withWorkspaceRoot(ctx, in.AgentID, in.SessionID, scope, authz.ActionCreate, home.RootReadWrite, func(root home.RootOperations, _ home.WorkspaceRequest) error {
		if in.IsDir {
			if err := root.Mkdir(ctx, name, 0o755, home.MkdirOptions{Parents: true}); err != nil {
				return err
			}
		} else {
			if err := mkdirWorkspaceParent(ctx, root, name); err != nil {
				return err
			}
			if err := root.Write(ctx, name, strings.NewReader(in.Content), home.WriteOptions{Mode: 0o644}); err != nil {
				return err
			}
		}
		result, err = collectWorkspaceInfo(ctx, root, scope, ".", false, 0)
		if err != nil {
			return fmt.Errorf("%w: collect response after create: %w", home.ErrOutcomeUnknown, err)
		}
		return nil
	})
	return result, err
}

func (a *Access) DeleteWorkspacePath(ctx context.Context, in WorkspacePathInput) error {
	if in.Path == "" {
		return ErrInvalid
	}
	scope, name, err := canonicalWorkspacePath(in.Scope, in.Path, false)
	if err != nil {
		return err
	}
	return a.withWorkspaceRoot(ctx, in.AgentID, in.SessionID, scope, authz.ActionDelete, home.RootReadWrite, func(root home.RootOperations, _ home.WorkspaceRequest) error {
		return root.Remove(ctx, name, home.RemoveOptions{Recursive: true})
	})
}

func (a *Access) MoveWorkspacePath(ctx context.Context, in WorkspaceMoveInput) (WorkspaceInfo, error) {
	if in.Path == "" || in.NewPath == "" {
		return WorkspaceInfo{}, ErrInvalid
	}
	scope, source, err := canonicalWorkspacePath(in.Scope, in.Path, false)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	destinationScope, destination, err := canonicalWorkspacePath(in.Scope, in.NewPath, false)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if destinationScope != scope {
		return WorkspaceInfo{}, ErrInvalid
	}
	var result WorkspaceInfo
	err = a.withWorkspaceRoot(ctx, in.AgentID, in.SessionID, scope, authz.ActionWrite, home.RootReadWrite, func(root home.RootOperations, _ home.WorkspaceRequest) error {
		if err := mkdirWorkspaceParent(ctx, root, destination); err != nil {
			return err
		}
		if err := root.Rename(ctx, source, destination, home.RenameOptions{}); err != nil {
			return err
		}
		result, err = collectWorkspaceInfo(ctx, root, scope, ".", false, 0)
		if err != nil {
			return fmt.Errorf("%w: collect response after move: %w", home.ErrOutcomeUnknown, err)
		}
		return nil
	})
	return result, err
}

func (a *Access) ReadWorkspacePath(ctx context.Context, in WorkspaceReadInput) (WorkspaceReadResult, error) {
	if in.Path == "" {
		return WorkspaceReadResult{}, ErrInvalid
	}
	scope, name, err := canonicalWorkspacePath(in.Scope, in.Path, false)
	if resolver, ok := a.svc.homes.(home.CoordinateResolver); ok {
		info, accessErr := a.Workspace(ctx, in.AgentID, in.SessionID, authz.ActionRead)
		if accessErr != nil {
			return WorkspaceReadResult{}, accessErr
		}
		rootScope := home.RootAgentWorkspace
		if in.Scope == WorkspaceScopeUser {
			rootScope = home.RootPrincipalData
		}
		resolvedScope, resolvedName, resolveErr := resolver.ResolveCoordinate(home.Coordinate{
			Request: home.WorkspaceRequest{UserID: info.UserID, GroupID: info.GroupID, AgentID: info.AgentID},
			Scope:   rootScope,
			Value:   in.Path,
		})
		if resolveErr != nil {
			return WorkspaceReadResult{}, ErrNotFound
		}
		err = nil
		scope, name = WorkspaceScopeAgent, resolvedName
		if resolvedScope == home.RootPrincipalData {
			scope = WorkspaceScopeUser
		}
	}
	if err != nil {
		return WorkspaceReadResult{}, err
	}
	maxBytes := in.MaxBytes
	if maxBytes <= 0 {
		maxBytes = workspaceReadMaxBytes
	}
	var result WorkspaceReadResult
	err = a.withWorkspaceRoot(ctx, in.AgentID, in.SessionID, scope, authz.ActionRead, home.RootReadOnly, func(root home.RootOperations, _ home.WorkspaceRequest) error {
		info, err := root.Stat(ctx, name)
		if err != nil {
			// Missing files and containment failures are intentionally opaque.
			return ErrNotFound
		}
		if info.IsDir() {
			return ErrIsDir
		}
		if info.Size() > maxBytes {
			return ErrTooLarge
		}
		var data bytes.Buffer
		if err := root.Read(ctx, name, &data, home.ReadOptions{MaxBytes: maxBytes}); err != nil {
			if errors.Is(err, home.ErrReadLimit) {
				return ErrTooLarge
			}
			return err
		}
		payload := data.Bytes()
		if in.Raw {
			mediaType := mime.TypeByExtension(path.Ext(name))
			if mediaType == "" {
				mediaType = "application/octet-stream"
			}
			result = WorkspaceReadResult{Path: name, Raw: true, RawName: path.Base(name), RawMediaType: mediaType, RawContent: payload, RawModTime: info.ModTime()}
			return nil
		}
		probe := payload
		if len(probe) > 512 {
			probe = probe[:512]
		}
		if slices.Contains(probe, 0) {
			return ErrBinary
		}
		result = WorkspaceReadResult{Path: name, Content: data.String(), Language: DetectLanguage(name)}
		return nil
	})
	return result, err
}

func (a *Access) WriteWorkspacePath(ctx context.Context, in WorkspaceWriteInput) (WorkspaceReadResult, error) {
	if in.Path == "" {
		return WorkspaceReadResult{}, ErrInvalid
	}
	scope, name, err := canonicalWorkspacePath(in.Scope, in.Path, false)
	if err != nil {
		return WorkspaceReadResult{}, err
	}
	var result WorkspaceReadResult
	err = a.withWorkspaceRoot(ctx, in.AgentID, in.SessionID, scope, authz.ActionWrite, home.RootReadWrite, func(root home.RootOperations, _ home.WorkspaceRequest) error {
		if err := mkdirWorkspaceParent(ctx, root, name); err != nil {
			return err
		}
		if err := root.Write(ctx, name, strings.NewReader(in.Content), home.WriteOptions{Mode: 0o644}); err != nil {
			return err
		}
		result = WorkspaceReadResult{Path: name, Content: in.Content, Language: DetectLanguage(name)}
		return nil
	})
	return result, err
}

func (a *Access) UploadWorkspacePath(ctx context.Context, in WorkspaceUploadInput) (WorkspaceUploadResult, error) {
	if in.Filename == "" || in.Reader == nil {
		return WorkspaceUploadResult{}, ErrInvalid
	}
	if in.ContentLength != nil && *in.ContentLength > workspaceUploadMaxBytes {
		return WorkspaceUploadResult{}, ErrTooLarge
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	filename := path.Base(strings.ReplaceAll(in.Filename, `\`, "/"))
	if filename == "" || filename == "." || filename == "/" {
		return WorkspaceUploadResult{}, ErrInvalid
	}
	var staged bytes.Buffer
	if _, err := io.Copy(&staged, &workspaceUploadReader{reader: in.Reader, remaining: workspaceUploadMaxBytes}); err != nil {
		return WorkspaceUploadResult{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return WorkspaceUploadResult{}, err
	}
	relative := path.Join("assets", now.Format("200601"), fmt.Sprintf("%s-%s-%s", now.Format("20060102"), id, filename))
	var result WorkspaceUploadResult
	err = a.withWorkspaceRoot(ctx, in.AgentID, in.SessionID, WorkspaceScopeUser, authz.ActionCreate, home.RootReadWrite, func(root home.RootOperations, _ home.WorkspaceRequest) error {
		if err := root.Mkdir(ctx, path.Dir(relative), 0o755, home.MkdirOptions{Parents: true}); err != nil {
			return err
		}
		if err := root.Upload(ctx, relative, bytes.NewReader(staged.Bytes()), home.WriteOptions{Mode: 0o644, MaxBytes: workspaceUploadMaxBytes}); err != nil {
			if errors.Is(err, home.ErrUploadLimit) {
				return ErrTooLarge
			}
			return err
		}
		result = WorkspaceUploadResult{Path: "$" + pkgsandbox.EnvStellaAssetsDir + "/" + strings.TrimPrefix(relative, "assets/"), RelativePath: relative, Scope: WorkspaceScopeUser}
		return nil
	})
	return result, err
}

type workspaceUploadReader struct {
	reader    io.Reader
	remaining int64
}

func (r *workspaceUploadReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		if count, err := r.reader.Read(extra[:]); count != 0 {
			return 0, ErrTooLarge
		} else if err != nil {
			return 0, err
		}
		return 0, ErrTooLarge
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

func (a *Access) withWorkspaceRoot(ctx context.Context, agentID, sessionID string, scope WorkspaceScope, action authz.Action, access home.RootAccess, use func(home.RootOperations, home.WorkspaceRequest) error) (resultErr error) {
	if use == nil || (scope != WorkspaceScopeAgent && scope != WorkspaceScopeUser) {
		return ErrInvalid
	}
	info, err := a.Workspace(ctx, agentID, sessionID, action)
	if err != nil {
		return err
	}
	if info.AgentID == "" || (info.UserID == "" && info.GroupID == "") {
		return ErrNotFound
	}
	if a.svc.homes == nil {
		return fmt.Errorf("%w: workspace root opener not configured", ErrUnavailable)
	}
	rootScope := home.RootAgentWorkspace
	if scope == WorkspaceScopeUser {
		rootScope = home.RootPrincipalData
	}
	req := home.WorkspaceRequest{UserID: info.UserID, GroupID: info.GroupID, AgentID: info.AgentID}
	root, err := a.svc.homes.OpenRoot(ctx, req, rootScope, access)
	if err != nil {
		return fmt.Errorf("%w: open workspace root: %w", ErrUnavailable, err)
	}
	defer func() {
		resultErr = classifyWorkspaceRootClose(resultErr, root.Close(), access)
	}()
	return use(root, req)
}

func classifyWorkspaceRootClose(operationErr, closeErr error, access home.RootAccess) error {
	if closeErr == nil {
		return operationErr
	}
	if access == home.RootReadWrite && operationErr == nil {
		return fmt.Errorf("%w: close workspace root: %w", home.ErrOutcomeUnknown, closeErr)
	}
	return errors.Join(operationErr, closeErr)
}

// AdmitWorkspaceUpload performs lightweight authorization before transport
// reads the request body. UploadWorkspacePath repeats the same decision at the
// publication boundary rather than treating this as a durable grant.
func (a *Access) AdmitWorkspaceUpload(ctx context.Context, agentID, sessionID string) error {
	_, err := a.Workspace(ctx, agentID, sessionID, authz.ActionCreate)
	return err
}

func mkdirWorkspaceParent(ctx context.Context, root home.RootOperations, name string) error {
	if parent := path.Dir(name); parent != "." {
		return root.Mkdir(ctx, parent, 0o755, home.MkdirOptions{Parents: true})
	}
	return nil
}

func canonicalWorkspacePath(scope WorkspaceScope, value string, allowRoot bool) (WorkspaceScope, string, error) {
	rootScope := home.RootAgentWorkspace
	if scope == WorkspaceScopeUser {
		rootScope = home.RootPrincipalData
	} else if scope != WorkspaceScopeAgent {
		return "", "", ErrInvalid
	}
	resolved, name, err := home.ResolveLogicalCoordinate(rootScope, value, allowRoot)
	if err != nil {
		return "", "", ErrInvalid
	}
	if resolved == home.RootPrincipalData {
		return WorkspaceScopeUser, name, nil
	}
	return WorkspaceScopeAgent, name, nil
}

func collectWorkspaceInfo(ctx context.Context, root home.RootOperations, scope WorkspaceScope, listName string, showHidden bool, depth int) (WorkspaceInfo, error) {
	stat, err := root.Stat(ctx, listName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return WorkspaceInfo{}, ErrNotFound
		}
		return WorkspaceInfo{}, err
	}
	if !stat.IsDir() {
		return WorkspaceInfo{}, ErrInvalid
	}
	logicalRoot := pkgsandbox.MountWorkspace
	if scope == WorkspaceScopeUser {
		logicalRoot = pkgsandbox.MountUserData
	}
	result := WorkspaceInfo{Root: logicalRoot, SandboxRoot: logicalRoot, Paths: []string{}}
	var walk func(string, int) error
	walk = func(directory string, level int) error {
		entries, err := root.List(ctx, directory, home.ListOptions{Limit: workspaceListLimit})
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, entry := range entries {
			if len(result.Paths) >= workspaceListLimit {
				return home.ErrListLimit
			}
			name := path.Join(directory, entry.Name())
			rel := strings.TrimPrefix(name, "./")
			if !showHidden && hiddenWorkspacePath(rel) {
				continue
			}
			if entry.IsDir() {
				result.TotalDirs++
				result.Paths = append(result.Paths, rel+"/")
				if depth <= 0 || level < depth {
					if err := walk(name, level+1); err != nil {
						return err
					}
				}
				continue
			}
			info, err := root.Stat(ctx, name)
			if err != nil {
				continue
			}
			result.TotalFiles++
			if info.Mode().IsRegular() {
				result.TotalBytes += info.Size()
			}
			result.Paths = append(result.Paths, rel)
		}
		return nil
	}
	if err := walk(listName, 1); err != nil {
		return WorkspaceInfo{}, err
	}
	return result, nil
}

func hiddenWorkspacePath(name string) bool {
	for segment := range strings.SplitSeq(name, "/") {
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

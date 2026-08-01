package access

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

var (
	ErrInvalid  = errors.New("invalid workspace request")
	ErrIsDir    = errors.New("workspace path is a directory")
	ErrBinary   = errors.New("workspace file appears to be binary")
	ErrTooLarge = errors.New("workspace file exceeds read limit")
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
	AgentID   string
	SessionID string
	Filename  string
	Reader    io.Reader
	Now       time.Time
}

type WorkspaceUploadResult struct {
	// Path is the sandbox-view path the agent reads (e.g. /user/assets/... on
	// isolating backends, the absolute host path otherwise).
	Path string `json:"path"`
	// RelativePath is the upload location relative to its workspace root.
	// Combine with Scope to build a workspace file-content read URL.
	RelativePath string `json:"relative_path"`
	// Scope is the workspace root the file was written to.
	Scope WorkspaceScope `json:"scope"`
}

func (a *Access) ListWorkspace(ctx context.Context, in WorkspaceListInput) (WorkspaceInfo, error) {
	root, err := a.workspaceRoot(ctx, in.AgentID, in.SessionID, in.Scope, authz.ActionList)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 2
	}
	info, err := collectWorkspaceInfo(root, in.ShowHidden, in.Path, depth)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	info.SandboxRoot = scopeSandboxView(a.svc.sandboxBackend(ctx), root, in.Scope)
	return info, nil
}

func (a *Access) CreateWorkspacePath(ctx context.Context, in WorkspaceCreateInput) (WorkspaceInfo, error) {
	if in.Path == "" {
		return WorkspaceInfo{}, ErrInvalid
	}
	root, err := a.workspaceRoot(ctx, in.AgentID, in.SessionID, in.Scope, authz.ActionCreate)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	abs, err := sharepkg.SafePath(root, in.Path)
	if err != nil {
		return WorkspaceInfo{}, ErrInvalid
	}
	if in.IsDir {
		rootFS, name, err := sharepkg.OpenSafeRoot(root, in.Path)
		if err != nil {
			return WorkspaceInfo{}, ErrInvalid
		}
		defer func() { _ = rootFS.Close() }()
		if err := rootFS.MkdirAll(name, 0o755); err != nil {
			return WorkspaceInfo{}, err
		}
	} else if err := a.svc.assets.CreateFile(ctx, abs, []byte(in.Content), 0o644); err != nil {
		return WorkspaceInfo{}, err
	}
	return collectWorkspaceInfo(root, false, "", 0)
}

func (a *Access) DeleteWorkspacePath(ctx context.Context, in WorkspacePathInput) error {
	if in.Path == "" {
		return ErrInvalid
	}
	root, err := a.workspaceRoot(ctx, in.AgentID, in.SessionID, in.Scope, authz.ActionDelete)
	if err != nil {
		return err
	}
	abs, err := sharepkg.SafePath(root, in.Path)
	if err != nil {
		return ErrInvalid
	}
	return a.svc.assets.RemoveFile(ctx, abs)
}

func (a *Access) MoveWorkspacePath(ctx context.Context, in WorkspaceMoveInput) (WorkspaceInfo, error) {
	if in.Path == "" || in.NewPath == "" {
		return WorkspaceInfo{}, ErrInvalid
	}
	root, err := a.workspaceRoot(ctx, in.AgentID, in.SessionID, in.Scope, authz.ActionWrite)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	src, err := sharepkg.SafePath(root, in.Path)
	if err != nil {
		return WorkspaceInfo{}, ErrInvalid
	}
	dst, err := sharepkg.SafePath(root, in.NewPath)
	if err != nil {
		return WorkspaceInfo{}, ErrInvalid
	}
	if err := a.svc.assets.MoveFile(ctx, src, dst); err != nil {
		return WorkspaceInfo{}, err
	}
	return collectWorkspaceInfo(root, false, "", 0)
}

func (a *Access) ReadWorkspacePath(ctx context.Context, in WorkspaceReadInput) (WorkspaceReadResult, error) {
	if in.Path == "" {
		return WorkspaceReadResult{}, ErrInvalid
	}
	scope, path := in.Scope, in.Path
	// An absolute path is self-describing (e.g. a host path embedded in a chat
	// message by a non-isolating backend, or a sandbox-view mount path). Resolve
	// which authorized workspace root contains it and read under that root's
	// scope, ignoring the requested scope. This never widens authority: the file
	// must already be reachable via one of the two roots the caller may read.
	if filepath.IsAbs(filepath.FromSlash(path)) {
		resolvedScope, rel, err := a.canonicalizeAbsPath(ctx, in.AgentID, in.SessionID, path)
		if err != nil {
			return WorkspaceReadResult{}, err
		}
		scope, path = resolvedScope, rel
	}
	root, err := a.workspaceRoot(ctx, in.AgentID, in.SessionID, scope, authz.ActionRead)
	if err != nil {
		return WorkspaceReadResult{}, err
	}
	rootFS, name, err := sharepkg.OpenSafeRoot(root, path)
	if err != nil {
		return WorkspaceReadResult{}, ErrInvalid
	}
	defer func() { _ = rootFS.Close() }()
	abs := filepath.Join(root, name)
	info, err := rootFS.Stat(name)
	if err != nil {
		if os.IsNotExist(err) && a.svc.assets.Restore(ctx, abs) == nil {
			info, err = rootFS.Stat(name)
		}
		if err != nil {
			return WorkspaceReadResult{}, ErrNotFound
		}
	}
	if info.IsDir() {
		return WorkspaceReadResult{}, ErrIsDir
	}
	if in.MaxBytes > 0 && info.Size() > in.MaxBytes {
		return WorkspaceReadResult{}, ErrTooLarge
	}
	data, err := readRootFile(rootFS, name, in.MaxBytes)
	if err != nil {
		return WorkspaceReadResult{}, err
	}
	if in.Raw {
		mediaType := mime.TypeByExtension(filepath.Ext(path))
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		return WorkspaceReadResult{
			Path: path, Raw: true, RawName: filepath.Base(path),
			RawMediaType: mediaType, RawContent: data, RawModTime: info.ModTime(),
		}, nil
	}
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if slices.Contains(probe, 0) {
		return WorkspaceReadResult{}, ErrBinary
	}
	return WorkspaceReadResult{Path: path, Content: string(data), Language: DetectLanguage(path)}, nil
}

// readRootFile enforces MaxBytes while reading as well as before it. The second
// check closes the stat/read race if a concurrently writable file grows after
// ReadWorkspacePath inspected it.
func readRootFile(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return root.ReadFile(name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// canonicalizeAbsPath maps an absolute request path to the (scope, relative
// path) pair under whichever authorized workspace root the endpoint may read.
// Both roots are resolved through the same ActionRead authorization the endpoint
// applies for an explicit scope, so an absolute path can only ever resolve to
// content already reachable via (scope, relative path) — no new roots, no
// widening.
//
// Resolution order is deliberate:
//  1. Host-root containment. A path that physically lives inside a workspace
//     root is served from that root. This wins so a genuine host path that
//     happens to sit under a /workspace-like STELLA_HOME is served from the root
//     that actually contains it, never mistaken for a sandbox mount view.
//  2. Sandbox mount mapping. A path under neither host root may still be a
//     sandbox-view mount path (/user, /workspace) that an isolating backend
//     embedded in a chat message. It maps to the owning scope; the mapped
//     relative path flows through the same ActionRead authz and OpenSafeRoot
//     containment as any relative request, so a ".."-escaping mount path is
//     rejected and mount mapping can only reach content (scope, rel) already
//     could.
//
// A path matched by neither step is ErrNotFound, identical to a missing relative
// file. The two host roots are disjoint siblings (users/<id>/data vs
// users/<id>/agents/<id>), so the containment check order is immaterial.
func (a *Access) canonicalizeAbsPath(ctx context.Context, agentID, sessionID, absPath string) (WorkspaceScope, string, error) {
	for _, scope := range []WorkspaceScope{WorkspaceScopeUser, WorkspaceScopeAgent} {
		root, err := a.workspaceRoot(ctx, agentID, sessionID, scope, authz.ActionRead)
		if err != nil {
			return "", "", err
		}
		if rel, ok := containedRel(root, absPath); ok {
			return scope, rel, nil
		}
	}
	if scope, rel, ok := mountScopeRel(absPath); ok {
		return scope, rel, nil
	}
	return "", "", ErrNotFound
}

// mountScopeRel maps a sandbox mount-view absolute path to the workspace scope
// that mount belongs to and the path relative to the mount. It matches only on a
// full path-segment boundary — exactly the mount, or the mount followed by "/" —
// so /userdata never matches the /user mount. The remainder is returned
// uncleaned; the caller runs it through OpenSafeRoot, which rejects any
// non-local (".."-escaping) result. That boundary means /workspace/../user/...
// maps to the agent scope with a "../user/..." remainder and is rejected, never
// crossing into the user root.
func mountScopeRel(absPath string) (WorkspaceScope, string, bool) {
	p := filepath.ToSlash(absPath)
	for _, m := range []struct {
		mount string
		scope WorkspaceScope
	}{
		{pkgsandbox.MountUserData, WorkspaceScopeUser},
		{pkgsandbox.MountWorkspace, WorkspaceScopeAgent},
	} {
		if p == m.mount {
			return m.scope, ".", true
		}
		if rel, ok := strings.CutPrefix(p, m.mount+"/"); ok {
			return m.scope, rel, true
		}
	}
	return "", "", false
}

// containedRel reports whether absPath resolves to a location strictly inside
// root and, if so, returns the cleaned root-relative path. It resolves symlinks
// on both sides — matching macOS realities like /var → /private/var — the same
// way agent.ValidateProjectDir does, then requires the relative result to be
// local (filepath.IsLocal rejects any ".." escape). A path equal to or outside
// root yields ok=false, so authority is never widened.
func containedRel(root, absPath string) (string, bool) {
	cleanRoot := resolveExistingSymlinks(filepath.Clean(root))
	cleanPath := resolveExistingSymlinks(filepath.Clean(filepath.FromSlash(absPath)))
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return "", false
	}
	if rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return rel, true
}

// resolveExistingSymlinks returns path with symlinks resolved, falling back to
// the longest existing ancestor so a symlinked parent cannot mask an escape
// even when the leaf does not exist yet. Mirrors agent.ValidateProjectDir.
func resolveExistingSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := path
	for dir != string(filepath.Separator) && dir != "." {
		parent := filepath.Dir(dir)
		if parent == dir {
			// Fixed point (e.g. a Windows drive root that itself fails to
			// resolve); the request path is attacker-controlled, so never spin.
			break
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			tail, _ := filepath.Rel(parent, path)
			return filepath.Join(resolved, tail)
		}
		dir = parent
	}
	return path
}

func (a *Access) WriteWorkspacePath(ctx context.Context, in WorkspaceWriteInput) (WorkspaceReadResult, error) {
	if in.Path == "" {
		return WorkspaceReadResult{}, ErrInvalid
	}
	root, err := a.workspaceRoot(ctx, in.AgentID, in.SessionID, in.Scope, authz.ActionWrite)
	if err != nil {
		return WorkspaceReadResult{}, err
	}
	abs, err := sharepkg.SafePath(root, in.Path)
	if err != nil {
		return WorkspaceReadResult{}, ErrInvalid
	}
	if err := a.svc.assets.WriteFile(ctx, abs, []byte(in.Content), 0o644); err != nil {
		return WorkspaceReadResult{}, err
	}
	return WorkspaceReadResult{Path: in.Path, Content: in.Content, Language: DetectLanguage(in.Path)}, nil
}

func (a *Access) UploadWorkspacePath(ctx context.Context, in WorkspaceUploadInput) (WorkspaceUploadResult, error) {
	if in.Filename == "" || in.Reader == nil {
		return WorkspaceUploadResult{}, ErrInvalid
	}
	root, err := a.workspaceRoot(ctx, in.AgentID, in.SessionID, WorkspaceScopeUser, authz.ActionCreate)
	if err != nil {
		return WorkspaceUploadResult{}, err
	}
	data, err := io.ReadAll(in.Reader)
	if err != nil {
		return WorkspaceUploadResult{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	hash := fmt.Sprintf("%06x", now.UnixNano()&0xFFFFFF)
	dir := filepath.Join(root, "assets", now.Format("200601"))
	name := fmt.Sprintf("%s-%s-%s", now.Format("20060102"), hash, filepath.Base(in.Filename))
	abs := filepath.Join(dir, name)
	if err := a.svc.assets.WriteFile(ctx, abs, data, 0o644); err != nil {
		return WorkspaceUploadResult{}, err
	}
	rel, _ := filepath.Rel(root, abs)
	relSlash := filepath.ToSlash(rel)
	sandboxRoot := sandbox.UserDataViewFor(a.svc.sandboxBackend(ctx), root)
	return WorkspaceUploadResult{
		Path:         filepath.ToSlash(filepath.Join(sandboxRoot, rel)),
		RelativePath: relSlash,
		Scope:        WorkspaceScopeUser,
	}, nil
}

func (a *Access) workspaceRoot(ctx context.Context, agentID, sessionID string, scope WorkspaceScope, action authz.Action) (string, error) {
	info, err := a.Workspace(ctx, agentID, sessionID, action)
	if err != nil {
		return "", err
	}
	if info.UserID == "" || info.AgentID == "" {
		return "", ErrNotFound
	}
	if _, err := agent.SetupUserWorkspace(config.StellaHome(), info.UserID, info.AgentID); err != nil {
		return "", err
	}
	return workspaceRootForScope(info.UserID, info.AgentID, scope), nil
}

func workspaceRootForScope(userID, agentID string, scope WorkspaceScope) string {
	if scope == WorkspaceScopeUser {
		return agent.UserDataDir(agent.UserHomeDir(config.StellaHome(), userID))
	}
	return agent.UserAgentDir(config.StellaHome(), userID, agentID)
}

func (s *Service) sandboxBackend(ctx context.Context) string {
	plugins, _ := s.store.ListPlugins(ctx)
	return config.ActiveSandboxBackend(plugins)
}

func scopeSandboxView(backend, root string, scope WorkspaceScope) string {
	if scope == WorkspaceScopeUser {
		return sandbox.UserDataViewFor(backend, root)
	}
	return sandbox.WorkspaceViewFor(backend, root)
}

func pathDepth(path string) int {
	return len(strings.Split(filepath.ToSlash(path), "/"))
}

func collectWorkspaceInfo(root string, showHidden bool, listPath string, depth int) (WorkspaceInfo, error) {
	info := WorkspaceInfo{Root: root, Paths: []string{}}
	rootFS, listName, err := sharepkg.OpenSafeRoot(root, strings.TrimSuffix(listPath, "/"))
	if err != nil {
		return WorkspaceInfo{}, ErrInvalid
	}
	defer func() { _ = rootFS.Close() }()
	if stat, statErr := rootFS.Stat(listName); statErr != nil {
		// A listed subdirectory can vanish between renders (deleted by the
		// agent or a peer session); that is a 404, not a server fault.
		if os.IsNotExist(statErr) {
			return WorkspaceInfo{}, ErrNotFound
		}
		return WorkspaceInfo{}, statErr
	} else if !stat.IsDir() {
		return WorkspaceInfo{}, ErrInvalid
	}
	err = fs.WalkDir(rootFS.FS(), filepath.ToSlash(listName), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr
		}
		scopeRel, scopeRelErr := filepath.Rel(listName, filepath.FromSlash(path))
		if scopeRelErr != nil || scopeRel == "." {
			return nil //nolint:nilerr
		}
		if depth > 0 && pathDepth(scopeRel) > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel := filepath.FromSlash(path)
		if rel == "." {
			return nil
		}
		name := d.Name()
		isDot := strings.HasPrefix(name, ".") || strings.Contains(rel, string(filepath.Separator)+".")
		if isDot && !showHidden {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			info.TotalDirs++
			info.Paths = append(info.Paths, filepath.ToSlash(rel)+"/")
			return nil
		}
		entryInfo, statErr := d.Info()
		if statErr != nil {
			return nil //nolint:nilerr
		}
		info.TotalFiles++
		if entryInfo.Mode().IsRegular() {
			info.TotalBytes += entryInfo.Size()
		}
		info.Paths = append(info.Paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return WorkspaceInfo{}, err
	}
	return info, nil
}

func DetectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
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

package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
)

// boxshFactory creates boxsh-backed sandbox sessions.
// This factory is only available on platforms that support boxsh.
type boxshFactory struct{}

func (f *boxshFactory) Name() string { return "boxsh" }

func (f *boxshFactory) Available() bool {
	return PlatformRequiresBoxsh()
}

func (f *boxshFactory) Supported(policy Policy) error {
	if !f.Available() {
		return &PolicyCompatibilityError{
			Backend:          f.Name(),
			Policy:           policy,
			Reason:           "boxsh is not available on this platform",
			RelaxedWouldHelp: false,
		}
	}

	// Check network whitelist support
	// boxsh 2.0.1 doesn't support whitelist mode
	if policy.RequiresWhitelist() && !policy.Relaxed {
		return &PolicyCompatibilityError{
			Backend:          f.Name(),
			Policy:           policy,
			Reason:           "boxsh does not support network whitelist mode",
			RelaxedWouldHelp: true,
		}
	}

	// All other policies are supported by boxsh
	return nil
}

func (f *boxshFactory) CreateSession(ctx context.Context, policy Policy) (Session, error) {
	if err := f.Supported(policy); err != nil {
		return nil, err
	}

	annaHome := config.AnnaHome()
	binaryPath, err := boxshclient.ResolveManagedBoxshPath(annaHome)
	if err != nil {
		return nil, fmt.Errorf("boxsh session: %w", err)
	}

	backendCfg := boxshclient.BackendConfig{
		AnnaHome:     annaHome,
		BinaryPath:   binaryPath,
		Workspace:    policy.WorkspaceRootOrDefault(),
		WorkDir:      policy.Filesystem.WorkingDir,
		ReadOnlyDirs: policy.Filesystem.ReadOnlyPaths,
		Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{
			Mode:      string(policy.NetworkModeOrDefault()),
			Allowlist: policy.Network.Allowlist,
		}},
	}
	if policy.RequiresWhitelist() && policy.Relaxed {
		backendCfg.Sandbox.Network.Mode = config.SandboxNetworkAllowAll
	}

	backend, err := boxshclient.NewSharedBackend(backendCfg)
	if err != nil {
		return nil, fmt.Errorf("boxsh session: %w", err)
	}
	if err := backend.Start(ctx, backendCfg); err != nil {
		return nil, fmt.Errorf("boxsh session: start backend: %w", err)
	}

	session := &boxshSession{
		policy:  policy,
		backend: backend,
		client:  backend.Client(),
		done:    make(chan struct{}),
	}
	go session.watchBackend()
	return session, nil
}

// boxshSession is a boxsh-backed sandbox session.
type boxshSession struct {
	policy   Policy
	backend  *boxshclient.SharedBackend
	client   *boxshclient.Client
	host     *boxshHost
	done     chan struct{}
	doneOnce sync.Once
	closed   bool
	closeErr error
	mu       sync.RWMutex
}

func (s *boxshSession) Host() Host {
	if s.host == nil {
		s.host = &boxshHost{session: s}
	}
	return s.host
}

func (s *boxshSession) Policy() Policy { return s.policy }

func (s *boxshSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || s.client == nil {
		return false
	}

	return s.client.Alive()
}

func (s *boxshSession) Done() <-chan struct{} { return s.done }

func (s *boxshSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *boxshSession) watchBackend() {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		closed := s.closed
		backend := s.backend
		s.mu.RUnlock()
		if closed {
			s.closeDone()
			return
		}
		if backend == nil || !backend.Alive() {
			s.closeDone()
			return
		}
	}
}

func (s *boxshSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return s.closeErr
	}

	s.closed = true

	if s.backend != nil {
		s.closeErr = s.backend.Close()
	} else if s.client != nil {
		s.closeErr = s.client.Close()
	}

	s.closeDone()
	return s.closeErr
}

// boxshHost implements Host interface using boxsh RPC calls.
type boxshHost struct {
	session *boxshSession
}

func (h *boxshHost) ReadFile(ctx context.Context, path string, offset, limit int) (ReadResult, error) {
	client := h.session.client
	if client == nil {
		return ReadResult{}, fmt.Errorf("boxsh host: session not available")
	}

	// Use boxsh client for file read
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return ReadResult{}, err
	}
	result, err := client.Read(ctx, boxshclient.ReadParams{FilePath: resolved, Offset: offset, Limit: limit})
	if err != nil {
		return ReadResult{}, err
	}

	nextOffset := offset + len(result.Content)
	if result.Truncated && limit > 0 {
		nextOffset = offset + limit
	}

	return ReadResult{
		Content:    []byte(result.Content),
		Truncated:  result.Truncated,
		NextOffset: nextOffset,
	}, nil
}

func (h *boxshHost) WriteFile(ctx context.Context, path string, content []byte) (WriteResult, error) {
	client := h.session.client
	if client == nil {
		return WriteResult{}, fmt.Errorf("boxsh host: session not available")
	}

	if err := h.ensureWritable(path); err != nil {
		return WriteResult{}, err
	}

	// Use boxsh client for file write
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return WriteResult{}, err
	}
	result, err := client.Write(ctx, boxshclient.WriteParams{FilePath: resolved, Content: string(content)})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{BytesWritten: result.BytesWritten}, nil
}

func (h *boxshHost) EditFile(ctx context.Context, path string, edits []Edit) (EditResult, error) {
	if err := h.ensureWritable(path); err != nil {
		return EditResult{}, err
	}
	// Read current content
	readResult, err := h.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return EditResult{}, err
	}

	content := string(readResult.Content)
	applied := 0

	for _, edit := range edits {
		if strings.Contains(content, edit.OldText) {
			content = strings.Replace(content, edit.OldText, edit.NewText, 1)
			applied++
		}
	}

	// Write back
	_, err = h.WriteFile(ctx, path, []byte(content))
	if err != nil {
		return EditResult{}, err
	}

	return EditResult{AppliedEdits: applied}, nil
}

func (h *boxshHost) Stat(ctx context.Context, path string) (StatResult, error) {
	// Use local filesystem stat since boxsh client may not have direct stat RPC
	// In a full implementation, this would use boxsh RPC
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return StatResult{}, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return StatResult{Exists: false}, nil
		}
		return StatResult{}, err
	}

	return StatResult{
		Exists:  true,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    uint32(info.Mode()),
		ModTime: info.ModTime(),
	}, nil
}

func (h *boxshHost) ListDir(ctx context.Context, path string) ([]DirEntry, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}

	result := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		result = append(result, DirEntry{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}

	return result, nil
}

func (h *boxshHost) MkdirAll(ctx context.Context, path string, perm uint32) error {
	if err := h.ensureWritable(path); err != nil {
		return err
	}
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}

	return os.MkdirAll(resolved, os.FileMode(perm))
}

func (h *boxshHost) Remove(ctx context.Context, path string, recursive bool) error {
	if err := h.ensureWritable(path); err != nil {
		return err
	}
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}

	if recursive {
		return os.RemoveAll(resolved)
	}
	return os.Remove(resolved)
}

func (h *boxshHost) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := h.ensureWritable(oldPath); err != nil {
		return err
	}
	if err := h.ensureWritable(newPath); err != nil {
		return err
	}
	resolvedOld, err := h.ResolvePath(oldPath)
	if err != nil {
		return err
	}

	resolvedNew, err := h.ResolvePath(newPath)
	if err != nil {
		return err
	}

	return os.Rename(resolvedOld, resolvedNew)
}

func (h *boxshHost) CreateTemp(ctx context.Context, dir, pattern string) (TempFile, error) {
	policy := h.session.policy.Filesystem

	resolvedDir := dir
	if resolvedDir == "" {
		resolvedDir = policy.WorkingDir
	}
	if err := h.ensureWritable(resolvedDir); err != nil {
		return nil, err
	}
	resolvedDir, err := h.ResolvePath(resolvedDir)
	if err != nil {
		return nil, err
	}

	// Ensure the directory exists
	if err := os.MkdirAll(resolvedDir, 0o755); err != nil {
		return nil, err
	}

	f, err := os.CreateTemp(resolvedDir, pattern)
	if err != nil {
		return nil, err
	}

	return &boxshTempFile{file: f}, nil
}

func (h *boxshHost) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	policy := h.session.policy.Process

	// Determine working directory
	cwd := opts.Cwd
	if cwd == "" {
		cwd = h.session.policy.Filesystem.WorkingDir
	}
	cwd, err := h.ResolvePath(cwd)
	if err != nil {
		return ExecResult{}, err
	}

	// Merge environment
	env := os.Environ()
	if !policy.InheritEnv {
		env = nil
	}
	for k, v := range policy.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	for k, v := range opts.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Determine timeout
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}

	// Apply timeout to context
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if h.hasReadOnlyOverlap() {
		return ExecResult{}, fmt.Errorf("sandbox: boxsh Host.Exec is fail-closed when ReadOnlyPaths overlap WorkspaceRoot in Phase 2")
	}

	// Use boxsh client for sandboxed execution
	client := h.session.client
	if client == nil {
		return ExecResult{}, fmt.Errorf("boxsh host: session not available")
	}

	if len(env) > 0 {
		// Current boxsh RPC only accepts a shell command, so env/cwd are encoded in the command.
		prefix := strings.Join(func() []string {
			parts := make([]string, 0, len(env))
			for _, kv := range env {
				parts = append(parts, fmt.Sprintf("export %s;", kv))
			}
			return parts
		}(), " ")
		command = prefix + " cd '" + strings.ReplaceAll(cwd, "'", "'\"'\"'") + "' && " + command
	} else {
		command = "cd '" + strings.ReplaceAll(cwd, "'", "'\"'\"'") + "' && " + command
	}

	result, err := client.Exec(ctx, boxshclient.ExecParams{Command: command, Timeout: int(timeout.Seconds())})
	if err != nil {
		return ExecResult{}, err
	}

	return ExecResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode}, nil
}

func (h *boxshHost) StartProcess(context.Context, ProcessRequest) (ProcessHandle, error) {
	return nil, fmt.Errorf("sandbox: boxsh Host.StartProcess is not implemented in Phase 2; fail closed until transport mediation is wired")
}

func (h *boxshHost) HTTPRequest(context.Context, HTTPOptions) (HTTPResult, error) {
	return HTTPResult{}, fmt.Errorf("sandbox: boxsh Host.HTTPRequest is not implemented in Phase 2; fail closed until transport mediation is wired")
}

func (h *boxshHost) OpenHTTPStream(context.Context, HTTPOptions) (HTTPStream, error) {
	return nil, fmt.Errorf("sandbox: boxsh Host.OpenHTTPStream is not implemented in Phase 2; fail closed until transport mediation is wired")
}

func (h *boxshHost) ResolvePath(path string) (string, error) {
	root, err := h.session.backend.SandboxRoot(context.Background())
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(h.session.policy.Filesystem.WorkingDir, path)
	}

	srcRoot := h.session.policy.WorkspaceRootOrDefault()
	if isWithinRoot(root, path) {
		return path, nil
	}
	if isWithinRoot(srcRoot, path) {
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return "", err
		}
		return filepath.Join(root, rel), nil
	}
	for _, ro := range h.session.policy.Filesystem.ReadOnlyPaths {
		if isWithinRoot(ro, path) {
			return path, nil
		}
	}
	if sandboxRelativeAbsolute(path) {
		return filepath.Join(root, strings.TrimPrefix(path, string(filepath.Separator))), nil
	}
	if err := boxshclient.ValidateSandboxPath(root, path); err != nil {
		return "", err
	}
	return path, nil
}

func isWithinRoot(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (h *boxshHost) ensureWritable(path string) error {
	logicalPath := path
	if !filepath.IsAbs(logicalPath) {
		logicalPath = filepath.Join(h.session.policy.Filesystem.WorkingDir, logicalPath)
	}
	for _, ro := range h.session.policy.Filesystem.ReadOnlyPaths {
		if isWithinRoot(ro, logicalPath) {
			return fmt.Errorf("sandbox: path %q is read-only in boxsh session", path)
		}
	}
	return nil
}

func (h *boxshHost) hasReadOnlyOverlap() bool {
	workspaceRoot := h.session.policy.WorkspaceRootOrDefault()
	for _, ro := range h.session.policy.Filesystem.ReadOnlyPaths {
		if isWithinRoot(workspaceRoot, ro) || isWithinRoot(ro, workspaceRoot) {
			return true
		}
	}
	return false
}

func sandboxRelativeAbsolute(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	trimmed := strings.Trim(path, string(filepath.Separator))
	if trimmed == "" {
		return true
	}
	return len(strings.Split(trimmed, string(filepath.Separator))) <= 2
}

func (h *boxshHost) WorkingDir() string {
	return h.session.policy.Filesystem.WorkingDir
}

// boxshTempFile implements TempFile for boxsh sessions.
type boxshTempFile struct {
	file *os.File
}

func (f *boxshTempFile) Path() string { return f.file.Name() }
func (f *boxshTempFile) Write(p []byte) (int, error) {
	return f.file.Write(p)
}

func (f *boxshTempFile) Close() error {
	path := f.file.Name()
	if err := f.file.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}

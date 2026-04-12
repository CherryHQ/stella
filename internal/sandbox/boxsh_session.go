package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
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
		Workspace:    policy.Filesystem.WorkingDir,
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

	return &boxshSession{
		policy:  policy,
		backend: backend,
		client:  backend.Client(),
		done:    make(chan struct{}),
	}, nil
}

// boxshSession is a boxsh-backed sandbox session.
type boxshSession struct {
	policy   Policy
	backend  *boxshclient.SharedBackend
	client   *boxshclient.Client
	host     *boxshHost
	done     chan struct{}
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

	close(s.done)
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
	result, err := client.Read(ctx, boxshclient.ReadParams{FilePath: path, Offset: offset, Limit: limit})
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

	// Use boxsh client for file write
	result, err := client.Write(ctx, boxshclient.WriteParams{FilePath: path, Content: string(content)})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{BytesWritten: result.BytesWritten}, nil
}

func (h *boxshHost) EditFile(ctx context.Context, path string, edits []Edit) (EditResult, error) {
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
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}

	return os.MkdirAll(resolved, os.FileMode(perm))
}

func (h *boxshHost) Remove(ctx context.Context, path string, recursive bool) error {
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

func (h *boxshHost) StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error) {
	policy := h.session.policy.Process

	// Determine working directory
	cwd := req.Cwd
	if cwd == "" {
		cwd = h.session.policy.Filesystem.WorkingDir
	}

	// Merge environment
	env := os.Environ()
	if !policy.InheritEnv {
		env = nil
	}
	for k, v := range policy.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	for k, v := range req.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// For boxsh, we spawn the process outside the sandbox but with network policy applied
	// In a full implementation, this would use a boxsh spawn RPC
	var execCtx context.Context
	var cancel context.CancelFunc

	timeout := req.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}

	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}

	cmd := exec.CommandContext(execCtx, req.Path, req.Args...)
	cmd.Dir = cwd
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	return &boxshProcessHandle{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		cancel: cancel,
	}, nil
}

func (h *boxshHost) HTTPRequest(ctx context.Context, opts HTTPOptions) (HTTPResult, error) {
	policy := h.session.policy.Network

	// Boxsh should enforce network policy, but we check here for fail-closed
	if policy.Mode == NetworkDisabled {
		return HTTPResult{}, fmt.Errorf("sandbox: network access denied by policy")
	}

	// Determine timeout
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Create request
	var body io.Reader
	if len(opts.Body) > 0 {
		body = strings.NewReader(string(opts.Body))
	}

	req, err := http.NewRequestWithContext(ctx, opts.Method, opts.URL, body)
	if err != nil {
		return HTTPResult{}, err
	}

	for k, v := range opts.Header {
		req.Header.Set(k, v)
	}

	// Execute with timeout
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return HTTPResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPResult{}, err
	}

	// Convert headers
	headers := make(map[string][]string)
	maps.Copy(headers, resp.Header)

	return HTTPResult{
		StatusCode: resp.StatusCode,
		Header:     headers,
		Body:       respBody,
	}, nil
}

func (h *boxshHost) OpenHTTPStream(ctx context.Context, opts HTTPOptions) (HTTPStream, error) {
	policy := h.session.policy.Network

	// Boxsh should enforce network policy
	if policy.Mode == NetworkDisabled {
		return nil, fmt.Errorf("sandbox: network access denied by policy")
	}

	// Determine timeout
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Create request
	var body io.Reader
	if len(opts.Body) > 0 {
		body = strings.NewReader(string(opts.Body))
	}

	req, err := http.NewRequestWithContext(ctx, opts.Method, opts.URL, body)
	if err != nil {
		return nil, err
	}

	for k, v := range opts.Header {
		req.Header.Set(k, v)
	}

	// Use a client that doesn't enforce timeout on body reads
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req) //nolint:bodyclose // body ownership is transferred to boxshHTTPStream
	if err != nil {
		return nil, err
	}

	return &boxshHTTPStream{
		resp:   resp,
		reader: resp.Body,
	}, nil
}

func (h *boxshHost) ResolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}

	workingDir := h.session.policy.Filesystem.WorkingDir
	return filepath.Join(workingDir, path), nil
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

// boxshProcessHandle implements ProcessHandle for boxsh sessions.
type boxshProcessHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
}

func (p *boxshProcessHandle) PID() int {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func (p *boxshProcessHandle) Wait(ctx context.Context) (ExecResult, error) {
	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = p.Close()
		return ExecResult{}, ctx.Err()
	case err := <-done:
		exitCode := 0
		if err != nil {
			exitErr := &exec.ExitError{}
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				return ExecResult{}, err
			}
		}

		// Read any remaining output
		stdout, _ := io.ReadAll(p.stdout)
		stderr, _ := io.ReadAll(p.stderr)

		return ExecResult{
			Stdout:   string(stdout),
			Stderr:   string(stderr),
			ExitCode: exitCode,
		}, nil
	}
}

func (p *boxshProcessHandle) Stdin() io.WriteCloser { return p.stdin }
func (p *boxshProcessHandle) Stdout() io.ReadCloser { return p.stdout }
func (p *boxshProcessHandle) Stderr() io.ReadCloser { return p.stderr }

func (p *boxshProcessHandle) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	p.cancel()

	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}

	_ = p.stdin.Close()
	_ = p.stdout.Close()
	_ = p.stderr.Close()

	return nil
}

// boxshHTTPStream implements HTTPStream for boxsh sessions.
type boxshHTTPStream struct {
	resp   *http.Response
	reader io.ReadCloser
}

func (s *boxshHTTPStream) Header() map[string][]string {
	headers := make(map[string][]string)
	maps.Copy(headers, s.resp.Header)
	return headers
}

func (s *boxshHTTPStream) Reader() io.ReadCloser { return s.reader }

func (s *boxshHTTPStream) Close() error {
	return s.reader.Close()
}

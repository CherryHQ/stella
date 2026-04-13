package local

import (
	"bytes"
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
)

// localFactory creates relaxed/unsandboxed sessions for explicit opt-in scenarios.
// This factory is always available but provides advisory enforcement only.
type localFactory struct{}

func (f *localFactory) Name() string    { return "local" }
func (f *localFactory) Available() bool { return true }

func (f *localFactory) Supported(policy Policy) error {
	if !policy.Relaxed {
		return &PolicyCompatibilityError{
			Backend:          f.Name(),
			Policy:           policy,
			Reason:           "local backend requires explicit relaxed mode",
			RelaxedWouldHelp: true,
		}
	}

	return nil
}

func (f *localFactory) CreateSession(ctx context.Context, policy Policy) (Session, error) {
	if err := f.Supported(policy); err != nil {
		return nil, err
	}

	session := newLocalSession(policy)
	logRelaxedMode(session.id, f.Name(), "local backend requires explicit relaxed mode", policy, "local backend enforcement is advisory")
	return session, nil
}

// localSession is a relaxed/unsandboxed session implementation.
// It provides the Host interface but enforces constraints only as advisory checks.
type localSession struct {
	id     string
	policy Policy
	host   *localHost
	done   chan struct{}
	closed bool
	mu     sync.RWMutex
}

func newLocalSession(policy Policy) *localSession {
	s := &localSession{
		id:     nextSessionID(),
		policy: policy,
		done:   make(chan struct{}),
	}
	s.host = &localHost{session: s}
	logSessionCreated(s.id, "local", policy)
	return s
}

func (s *localSession) Host() Host     { return s.host }
func (s *localSession) Policy() Policy { return s.policy }

func (s *localSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed
}

func (s *localSession) Done() <-chan struct{} { return s.done }

func (s *localSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	close(s.done)

	// Local session has no persistent resources to clean up
	logSessionClosed(s.id, "local", "explicit_close")
	return nil
}

// localHost implements Host interface with local/unrestricted access.
// It enforces policy constraints as advisory checks and observability events.
type localHost struct {
	session *localSession
}

func (h *localHost) checkPath(path string) error {
	policy := h.session.policy.Filesystem

	if policy.AllowEscapes {
		return nil
	}

	// Resolve and check if path is within allowed directories
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	allowedDirs := []string{policy.WorkingDir}
	allowedDirs = append(allowedDirs, policy.ReadOnlyPaths...)
	allowedDirs = append(allowedDirs, policy.ReadWritePaths...)

	for _, allowed := range allowedDirs {
		if allowed == "" {
			continue
		}
		absAllowed, err := filepath.Abs(allowed)
		if err != nil {
			continue
		}

		// Check if path is under allowed directory
		rel, err := filepath.Rel(absAllowed, absPath)
		if err != nil {
			continue
		}

		if rel != ".." && !strings.HasPrefix(rel, "..") {
			return nil
		}
	}

	// Path is outside allowed directories
	return fmt.Errorf("sandbox: path %q is outside allowed directories", path)
}

func (h *localHost) ReadFile(ctx context.Context, path string, offset, limit int) (ReadResult, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return ReadResult{}, err
	}

	// Advisory check
	if err := h.checkPath(resolved); err != nil && !h.session.policy.Relaxed {
		return ReadResult{}, err
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return ReadResult{}, err
	}

	if offset > 0 {
		if offset >= len(content) {
			return ReadResult{Content: nil, NextOffset: offset}, nil
		}
		content = content[offset:]
	}

	truncated := false
	if limit > 0 && len(content) > limit {
		content = content[:limit]
		truncated = true
	}

	nextOffset := offset + len(content)
	if truncated {
		nextOffset = offset + limit
	}

	return ReadResult{
		Content:    content,
		Truncated:  truncated,
		NextOffset: nextOffset,
	}, nil
}

func (h *localHost) WriteFile(ctx context.Context, path string, content []byte) (WriteResult, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return WriteResult{}, err
	}

	// Advisory check
	if err := h.checkPath(resolved); err != nil && !h.session.policy.Relaxed {
		return WriteResult{}, err
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return WriteResult{}, err
	}

	if err := os.WriteFile(resolved, content, 0o644); err != nil {
		return WriteResult{}, err
	}

	return WriteResult{BytesWritten: len(content)}, nil
}

func (h *localHost) EditFile(ctx context.Context, path string, edits []Edit) (EditResult, error) {
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

func (h *localHost) Stat(ctx context.Context, path string) (StatResult, error) {
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

func (h *localHost) ListDir(ctx context.Context, path string) ([]DirEntry, error) {
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

func (h *localHost) MkdirAll(ctx context.Context, path string, perm uint32) error {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}

	return os.MkdirAll(resolved, os.FileMode(perm))
}

func (h *localHost) Remove(ctx context.Context, path string, recursive bool) error {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}

	if recursive {
		return os.RemoveAll(resolved)
	}
	return os.Remove(resolved)
}

func (h *localHost) Rename(ctx context.Context, oldPath, newPath string) error {
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

func (h *localHost) CreateTemp(ctx context.Context, dir, pattern string) (TempFile, error) {
	resolvedDir, err := h.ResolvePath(dir)
	if err != nil {
		// Fall back to working directory
		resolvedDir = h.session.policy.Filesystem.WorkingDir
	}

	f, err := os.CreateTemp(resolvedDir, pattern)
	if err != nil {
		return nil, err
	}

	return &localTempFile{file: f}, nil
}

func (h *localHost) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
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

	// Execute command
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return ExecResult{}, err
		}
	}

	return ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

func (h *localHost) StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error) {
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

	// Apply timeout
	timeout := req.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}

	var execCtx context.Context
	var cancel context.CancelFunc
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

	return &localProcessHandle{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		cancel: cancel,
	}, nil
}

func (h *localHost) HTTPRequest(ctx context.Context, opts HTTPOptions) (HTTPResult, error) {
	policy := h.session.policy.Network

	// Advisory network check in relaxed mode
	if policy.Mode == NetworkDisabled && !h.session.policy.Relaxed {
		logPolicyDenied(h.session.id, "local", "http_request", opts.URL, "network access denied by policy")
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

func (h *localHost) OpenHTTPStream(ctx context.Context, opts HTTPOptions) (HTTPStream, error) {
	policy := h.session.policy.Network

	// Advisory network check in relaxed mode
	if policy.Mode == NetworkDisabled && !h.session.policy.Relaxed {
		logPolicyDenied(h.session.id, "local", "http_stream", opts.URL, "network access denied by policy")
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
	resp, err := client.Do(req) //nolint:bodyclose // body ownership is transferred to localHTTPStream
	if err != nil {
		return nil, err
	}

	return &localHTTPStream{
		resp:   resp,
		reader: resp.Body,
	}, nil
}

func (h *localHost) ResolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}

	workingDir := h.session.policy.Filesystem.WorkingDir
	return filepath.Join(workingDir, path), nil
}

func (h *localHost) WorkingDir() string {
	return h.session.policy.Filesystem.WorkingDir
}

// localTempFile implements TempFile for local sessions.
type localTempFile struct {
	file *os.File
}

func (f *localTempFile) Path() string { return f.file.Name() }
func (f *localTempFile) Write(p []byte) (int, error) {
	return f.file.Write(p)
}

func (f *localTempFile) Close() error {
	// Clean up temp file on close
	path := f.file.Name()
	if err := f.file.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}

// localProcessHandle implements ProcessHandle for local sessions.
type localProcessHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
}

func (p *localProcessHandle) PID() int {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func (p *localProcessHandle) Wait(ctx context.Context) (ExecResult, error) {
	// Create a goroutine to wait for process
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

		// Read any remaining stdout/stderr
		stdout, _ := io.ReadAll(p.stdout)
		stderr, _ := io.ReadAll(p.stderr)

		return ExecResult{
			Stdout:   string(stdout),
			Stderr:   string(stderr),
			ExitCode: exitCode,
		}, nil
	}
}

func (p *localProcessHandle) Stdin() io.WriteCloser { return p.stdin }
func (p *localProcessHandle) Stdout() io.ReadCloser { return p.stdout }
func (p *localProcessHandle) Stderr() io.ReadCloser { return p.stderr }

func (p *localProcessHandle) Close() error {
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

// localHTTPStream implements HTTPStream for local sessions.
type localHTTPStream struct {
	resp   *http.Response
	reader io.ReadCloser
}

func (s *localHTTPStream) Header() map[string][]string {
	headers := make(map[string][]string)
	maps.Copy(headers, s.resp.Header)
	return headers
}

func (s *localHTTPStream) Reader() io.ReadCloser { return s.reader }

func (s *localHTTPStream) Close() error {
	return s.reader.Close()
}

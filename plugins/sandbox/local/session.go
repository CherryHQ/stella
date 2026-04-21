// WARNING: commands run directly on the host OS with OS-level hardening only.
// Hardening layers applied: process-group isolation, rlimits (Linux),
// Seatbelt sandbox-exec profile (macOS), bwrap/unshare network isolation (Linux).
// Use the docker backend when full container isolation is required.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// Factory creates local sandbox sessions that run directly on the host OS.
type Factory struct{}

// NewFactory returns a Factory for the local backend.
func NewFactory() sandboxpkg.Factory { return &Factory{} }

// Name returns the backend name.
func (f *Factory) Name() string { return "local" }

// Available always returns true — the local backend has no external dependencies.
func (f *Factory) Available() bool { return true }

// Supported always returns nil — the local backend can satisfy any policy.
func (f *Factory) Supported(_ sandboxpkg.Policy) error { return nil }

// CreateSession creates a new localSession.
func (f *Factory) CreateSession(_ context.Context, policy sandboxpkg.Policy) (sandboxpkg.Session, error) {
	sessionID := sandboxpkg.NewSessionID()
	s := &localSession{
		id:     sessionID,
		policy: policy,
		done:   make(chan struct{}),
	}
	sandboxpkg.LogSessionCreated(sessionID, "local", policy)
	return s, nil
}

// ─────────────────────────── localSession ─────────────────────────────

// localSession implements sandboxpkg.Session by running commands directly on
// the host OS with no container isolation.
type localSession struct {
	id       string
	policy   sandboxpkg.Policy
	done     chan struct{}
	doneOnce sync.Once
	mu       sync.RWMutex
	closed   bool
	procs    []*localProcess
}

func (s *localSession) Policy() sandboxpkg.Policy { return s.policy }

func (s *localSession) WorkspaceRoot() string {
	return s.policy.WorkspaceRootOrDefault()
}

func (s *localSession) WorkingDir() string {
	if s.policy.Filesystem.WorkingDir != "" {
		return s.policy.Filesystem.WorkingDir
	}
	return s.WorkspaceRoot()
}

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

	// Snapshot and clear the process list, then close each.
	// localProcess.Close() is idempotent so double-close from natural exit is safe.
	procs := s.procs
	s.procs = nil
	for _, p := range procs {
		p.Close() //nolint:errcheck
	}

	s.doneOnce.Do(func() { close(s.done) })
	sandboxpkg.LogSessionClosed(s.id, "local", "explicit_close")
	return nil
}

// deregisterProcess removes a process handle from the session's tracked list.
// Called by localProcess after natural exit so stale PIDs are not killed.
func (s *localSession) deregisterProcess(p *localProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, proc := range s.procs {
		if proc == p {
			s.procs = append(s.procs[:i], s.procs[i+1:]...)
			return
		}
	}
}

// ResolvePath resolves a path and rejects anything outside WorkspaceRoot.
// Uses filepath.EvalSymlinks so symlink traversals cannot escape the workspace.
func (s *localSession) ResolvePath(agentPath string) (string, error) {
	root := s.WorkspaceRoot()

	abs := agentPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.WorkingDir(), agentPath)
	}

	// EvalSymlinks requires the path to exist; fall back to Clean for
	// non-existent paths so that tools creating new files still work.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("local: resolve path %q: %w", agentPath, err)
		}
		resolved = filepath.Clean(abs)
	}

	// Ensure the resolved root prefix is absolute and cleaned.
	cleanRoot := filepath.Clean(root)
	if resolved != cleanRoot && !strings.HasPrefix(resolved, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("local: path %q resolves to %q which is outside workspace root %q", agentPath, resolved, root)
	}
	return resolved, nil
}

// Exec runs a shell command via sh -c on the host.
func (s *localSession) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	// Finding 5: check closed before starting.
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return sandboxpkg.ExecResult{}, fmt.Errorf("local: session is closed")
	}

	cwd := opts.Cwd
	if cwd == "" {
		cwd = s.WorkingDir()
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = s.policy.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	execPath, execArgs, err := wrapCommand(s.policy, "sh", []string{"-c", command})
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("local exec: wrap: %w", err)
	}

	// Finding 2: do NOT use exec.CommandContext — it only kills the leader PID,
	// leaving process-group children alive. We manage cancellation manually.
	cmd := exec.Command(execPath, execArgs...)
	cmd.Dir = cwd
	cmd.Env = buildEnv(s.policy, opts.Env)
	setSysProcAttr(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if startErr := cmd.Start(); startErr != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("local exec: start: %w", startErr)
	}

	// Finding 3: reap zombie if rlimits fail.
	if rlErr := applyRlimits(cmd); rlErr != nil {
		killProcessGroup(cmd)
		_ = cmd.Wait()
		return sandboxpkg.ExecResult{}, fmt.Errorf("local exec: rlimits: %w", rlErr)
	}

	// Finding 2: watch ctx cancellation manually so the whole process group dies.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		killProcessGroup(cmd)
		<-done // reap
		return sandboxpkg.ExecResult{}, ctx.Err()
	case waitErr := <-done:
		exitCode := 0
		if waitErr != nil {
			exitErr := &exec.ExitError{}
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				return sandboxpkg.ExecResult{}, fmt.Errorf("local exec: %w", waitErr)
			}
		}
		return sandboxpkg.ExecResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode,
		}, nil
	}
}

// StartProcess starts a long-running process on the host and returns a handle.
func (s *localSession) StartProcess(ctx context.Context, req sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = s.WorkingDir()
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = s.policy.Timeout
	}

	var (
		execCtx context.Context
		cancel  context.CancelFunc
	)
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}

	args := make([]string, 0, len(req.Args))
	args = append(args, req.Args...)

	execPath, execArgs, err := wrapCommand(s.policy, req.Path, args)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("local start_process: wrap: %w", err)
	}

	// Finding 2: do NOT use exec.CommandContext — kill the process group instead.
	cmd := exec.Command(execPath, execArgs...)
	cmd.Dir = cwd
	cmd.Env = buildEnv(s.policy, req.Env)
	setSysProcAttr(cmd)

	// Finding 7: close previously opened pipes on error.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("local start_process: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, fmt.Errorf("local start_process: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return nil, fmt.Errorf("local start_process: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		return nil, fmt.Errorf("local start_process: start: %w", err)
	}

	// Finding 3: reap zombie if rlimits fail.
	if rlErr := applyRlimits(cmd); rlErr != nil {
		killProcessGroup(cmd)
		_ = cmd.Wait()
		cancel()
		return nil, fmt.Errorf("local start_process: rlimits: %w", rlErr)
	}

	// Finding 5: check closed and register atomically under write lock.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		killProcessGroup(cmd)
		_ = cmd.Wait()
		cancel()
		return nil, fmt.Errorf("local: session is closed")
	}
	proc := &localProcess{
		session: s,
		cmd:     cmd,
		cancel:  cancel,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		exitCh:  make(chan struct{}),
	}
	// Watch context cancellation so the process group is killed on timeout/cancel.
	go func() {
		select {
		case <-execCtx.Done():
			proc.Close() //nolint:errcheck
		case <-proc.exitCh:
		}
	}()
	s.procs = append(s.procs, proc)
	s.mu.Unlock()

	return proc, nil
}

// ─────────────────────────── helpers ─────────────────────────────

// buildEnv constructs the environment slice for a subprocess.
// If policy.InheritEnv is true, the host environment is included as a base.
// Policy env vars are applied on top, then per-call overrides.
func buildEnv(policy sandboxpkg.Policy, overrides map[string]string) []string {
	merged := make(map[string]string)

	if policy.InheritEnv {
		for _, kv := range os.Environ() {
			if before, after, ok := strings.Cut(kv, "="); ok {
				merged[before] = after
			}
		}
	}

	maps.Copy(merged, policy.Env)
	maps.Copy(merged, overrides)

	env := make([]string, 0, len(merged))
	for k, v := range merged {
		env = append(env, k+"="+v)
	}
	return env
}

// ─────────────────────────── localProcess ─────────────────────────────

// localProcess implements sandboxpkg.ProcessHandle for a host os/exec process.
type localProcess struct {
	session *localSession
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	mu      sync.Mutex
	closed  bool
	exitCh  chan struct{} // closed when the process exits naturally
}

func (p *localProcess) PID() int {
	if p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func (p *localProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *localProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *localProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *localProcess) Wait(ctx context.Context) (sandboxpkg.ExecResult, error) {
	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		err := p.cmd.Wait()
		code := 0
		if err != nil {
			exitErr := &exec.ExitError{}
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
				err = nil
			}
		}
		// Finding 1: deregister on natural exit so Close() doesn't kill a stale PID.
		p.mu.Lock()
		if !p.closed {
			p.closed = true
			if p.exitCh != nil {
				close(p.exitCh)
			}
		}
		p.mu.Unlock()
		if p.session != nil {
			p.session.deregisterProcess(p)
		}
		done <- struct {
			code int
			err  error
		}{code, err}
	}()

	select {
	case <-ctx.Done():
		_ = p.Close()
		return sandboxpkg.ExecResult{}, ctx.Err()
	case r := <-done:
		return sandboxpkg.ExecResult{ExitCode: r.code}, r.err
	}
}

func (p *localProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true
	if p.exitCh != nil {
		close(p.exitCh)
	}
	p.cancel()
	killProcessGroup(p.cmd)
	return nil
}

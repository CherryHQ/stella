// Package none provides a no-op sandbox backend that runs commands directly on
// the host with the same permissions as the current user and no isolation.
// Use only when agent workloads are fully trusted.
package none

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/fsops"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

// Config configures the none factory.
type Config struct {
	// StellaHome is the host path to the stella home directory, used for
	// building a PATH that includes $STELLA_HOME/bin.
	StellaHome string
	// Layout is the complete physical host filesystem authority for this factory.
	Layout hostlayout.Layout
}

// Factory creates sessions that execute directly on the host with no sandboxing.
type Factory struct {
	cfg Config
}

var (
	_ sandboxpkg.FilesystemSession                   = (*noneSession)(nil)
	_ sandboxpkg.FilesystemPathProjector             = (*noneSession)(nil)
	_ sandboxpkg.FilesystemWorkingDirectoryProjector = (*noneSession)(nil)
)

// NewFactory returns a Factory for the none backend.
func NewFactory(cfg ...Config) sandboxpkg.Factory {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}
	c.Layout = c.Layout.Clone()
	return &Factory{cfg: c}
}

// Name returns the backend name.
func (f *Factory) Name() string { return "none" }

// Available returns true on all platforms except Windows.
func (f *Factory) Available() bool { return platformAvailable() }

// Supported accepts any policy; the none backend imposes no restrictions.
func (f *Factory) Supported(_ sandboxpkg.Policy) error { return f.cfg.Layout.Validate() }

// CreateSession creates a new noneSession.
// If a StellaHome was provided via Config, the factory adjusts the policy env
// with a sandboxed PATH. Network mode is always overridden to AllowAll since
// the none backend cannot enforce network restrictions.
func (f *Factory) CreateSession(_ context.Context, policy sandboxpkg.Policy) (sandboxpkg.Session, error) {
	if err := f.Supported(policy); err != nil {
		return nil, err
	}
	tmpDir, err := hostlayout.CreateSessionTempDir(f.cfg.Layout, "none-tmp-*")
	if err != nil {
		return nil, fmt.Errorf("none: create session temp: %w", err)
	}
	transferredTempOwnership := false
	defer func() {
		if !transferredTempOwnership {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	policy, err = f.adjustPolicy(policy, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("none: apply filesystem environment: %w", err)
	}
	id := sandboxpkg.NewSessionID()
	policy.Filesystem.WorkingDir = f.cfg.Layout.WorkingDirSource
	s := &noneSession{
		id:           id,
		policy:       policy,
		layout:       f.cfg.Layout,
		ownedTempDir: tmpDir,
		done:         make(chan struct{}),
	}
	transferredTempOwnership = true
	sandboxpkg.LogSessionCreated(id, "none", policy)
	return s, nil
}

// adjustPolicy applies none-backend-specific policy adjustments.
func (f *Factory) adjustPolicy(policy sandboxpkg.Policy, tmpDir string) (sandboxpkg.Policy, error) {
	policy.Network.Mode = sandboxpkg.NetworkAllowAll
	env := maps.Clone(policy.Env)
	if env == nil {
		env = make(map[string]string)
	}

	// The none backend has no path remapping or confinement: all filesystem
	// roots are real host paths. A user-less session falls back to its workspace.
	workspace := f.cfg.Layout.WorkspaceSource
	userData := layoutSourceForTarget(f.cfg.Layout, sandboxpkg.MountUserData)
	view := sandboxpkg.FilesystemView{Home: workspace, SharedDataDir: userData, TempDir: tmpDir}
	if err := sandboxpkg.ApplyFilesystemEnv(env, view); err != nil {
		return sandboxpkg.Policy{}, err
	}

	if f.cfg.StellaHome != "" {
		// Recover the per-user mise home from the runtime env (MISE_DATA_DIR) to put
		// its shims on PATH; no remap here since none shares the host filesystem.
		userShims := ""
		if dir := sandboxpkg.PerUserMiseDataDir(env, f.cfg.StellaHome); dir != "" {
			userShims = sandboxpkg.MiseUserShimsDir(dir)
		}
		env["PATH"] = sandboxpkg.HostEnvBuildPath(f.cfg.StellaHome, userShims)
	}
	policy.Env = env
	return policy, nil
}

func layoutSourceForTarget(layout hostlayout.Layout, target string) string {
	clean := path.Clean(target)
	for _, mount := range layout.Mounts {
		if path.Clean(mount.Target) == clean {
			return mount.Source
		}
	}
	return ""
}

// noneSession implements sandboxpkg.Session with zero isolation.
type noneSession struct {
	id           string
	policy       sandboxpkg.Policy
	layout       hostlayout.Layout
	done         chan struct{}
	doneOnce     sync.Once
	mu           sync.RWMutex
	closed       bool
	closeErr     error
	procs        []*noneProcess
	ownedTempDir string
}

func (s *noneSession) Policy() sandboxpkg.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *noneSession) WorkingDir() string {
	if s.layout.WorkingDirSource != "" {
		return s.layout.WorkingDirSource
	}
	wd, _ := os.Getwd()
	return wd
}

// Filesystem is deliberately mediated even for unsafe none: the unsafe gate
// changes process isolation, never the containment rules of trusted file APIs.
//
// fsops opens each mount through os.OpenRoot, which requires the root directory
// to already exist. The none backend shares the host filesystem and does not
// pre-materialize its roots the way the local backend does, so a writable mount
// root is created here on demand. Read-only mounts are never created: a missing
// read-only root must fail closed.
func (s *noneSession) Filesystem() (sandboxpkg.Filesystem, error) {
	mounts := make([]fsops.Mount, 0, len(s.layout.Mounts))
	for _, mount := range s.layout.Mounts {
		if !sandboxpkg.IsCanonicalFilesystemPath(mount.Target) {
			continue
		}
		if mount.Access != hostlayout.ReadOnly {
			if err := os.MkdirAll(mount.Source, 0o755); err != nil {
				return nil, err
			}
		}
		mounts = append(mounts, fsops.Mount{Path: mount.Target, Directory: mount.Source, ReadOnly: mount.Access == hostlayout.ReadOnly})
	}
	if len(mounts) == 0 {
		mounts = append(mounts, fsops.Mount{Path: sandboxpkg.PathWorkspace, Directory: s.WorkingDir()})
	}
	if s.ownedTempDir != "" {
		mounts = append(mounts, fsops.Mount{Path: sandboxpkg.PathTemp, Directory: s.ownedTempDir})
	}
	return fsops.NewFilesystem(mounts)
}

func (s *noneSession) ProjectFilesystemPath(input string) (string, bool) {
	return s.projectFilesystemSourcePath(input)
}

// projectFilesystemSourcePath maps only a physical path through the authorized
// layout. none executes directly on the host, so its environment uses physical
// coordinates too.
func (s *noneSession) projectFilesystemSourcePath(input string) (string, bool) {
	if s.ownedTempDir != "" {
		if rel, err := filepath.Rel(s.ownedTempDir, input); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			canonical := path.Join(sandboxpkg.PathTemp, filepath.ToSlash(rel))
			return canonical, sandboxpkg.IsCanonicalFilesystemPath(canonical)
		}
	}
	if canonical, ok := s.layout.SourceToTarget(input); ok && sandboxpkg.IsCanonicalFilesystemPath(canonical) {
		return canonical, true
	}
	return "", false
}

// FilesystemWorkingDirectory returns the host working directory through the
// authorized layout instead of inferring authority from its spelling.
func (s *noneSession) FilesystemWorkingDirectory() (string, bool) {
	return s.projectFilesystemSourcePath(s.WorkingDir())
}

func (s *noneSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed
}

func (s *noneSession) Done() <-chan struct{} { return s.done }

func (s *noneSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	procs := s.procs
	s.procs = nil
	for _, p := range procs {
		p.Close() //nolint:errcheck
	}
	if s.ownedTempDir != "" {
		s.closeErr = os.RemoveAll(s.ownedTempDir)
	}
	s.doneOnce.Do(func() { close(s.done) })
	sandboxpkg.LogSessionClosed(s.id, "none", "explicit_close")
	return s.closeErr
}

// resolvePath resolves a relative path against the working directory.
// Absolute paths are returned as-is.
func (s *noneSession) resolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Join(s.WorkingDir(), path), nil
}

func (s *noneSession) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	s.mu.RLock()
	closed := s.closed
	policy := s.policy
	s.mu.RUnlock()
	if closed {
		return sandboxpkg.ExecResult{}, errors.New("none: session is closed")
	}

	cwd := opts.Cwd
	if cwd == "" {
		cwd = s.WorkingDir()
	}
	cwd, err := s.resolvePath(cwd)
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("none: resolve cwd: %w", err)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	sh, shFlag := shell()
	cmd := exec.Command(sh, shFlag, command)
	cmd.Dir = cwd
	cmd.Env = buildEnv(policy, opts.Env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return sandboxpkg.ExecResult{}, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return sandboxpkg.ExecResult{}, ctx.Err()
	case waitErr := <-done:
		exitCode := 0
		if waitErr != nil {
			exitErr := &exec.ExitError{}
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				return sandboxpkg.ExecResult{}, waitErr
			}
		}
		return sandboxpkg.ExecResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode,
		}, nil
	}
}

func (s *noneSession) StartProcess(ctx context.Context, req sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	s.mu.RLock()
	closed := s.closed
	policy := s.policy
	s.mu.RUnlock()
	if closed {
		return nil, errors.New("none: session is closed")
	}

	cwd := req.Cwd
	if cwd == "" {
		cwd = s.WorkingDir()
	}
	cwd, err := s.resolvePath(cwd)
	if err != nil {
		return nil, fmt.Errorf("none: resolve cwd: %w", err)
	}

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

	cmd := exec.Command(req.Path, req.Args...)
	cmd.Dir = cwd
	cmd.Env = buildEnv(policy, req.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		return nil, errors.New("none: session is closed")
	}
	proc := &noneProcess{
		session: s,
		cmd:     cmd,
		cancel:  cancel,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		exitCh:  make(chan struct{}),
	}
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

func (s *noneSession) deregisterProcess(p *noneProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, proc := range s.procs {
		if proc == p {
			s.procs = append(s.procs[:i], s.procs[i+1:]...)
			return
		}
	}
}

// buildEnv merges host env with policy env and per-call overrides.
// If InheritEnv is false, host environment is not included.
func buildEnv(policy sandboxpkg.Policy, overrides map[string]string) []string {
	merged := make(map[string]string)
	if policy.InheritEnv {
		for _, kv := range os.Environ() {
			k, v, ok := cutEnv(kv)
			if ok {
				merged[k] = v
			}
		}
	}
	maps.Copy(merged, policy.Env)
	maps.Copy(merged, overrides)
	delete(merged, "STELLA_USER_DIR")
	env := make([]string, 0, len(merged))
	for k, v := range merged {
		env = append(env, k+"="+v)
	}
	return env
}

func cutEnv(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}

// noneProcess implements sandboxpkg.ProcessHandle.
type noneProcess struct {
	session *noneSession
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	mu      sync.Mutex
	closed  bool
	exitCh  chan struct{}
}

func (p *noneProcess) PID() int {
	if p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func (p *noneProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *noneProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *noneProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *noneProcess) Wait(ctx context.Context) (sandboxpkg.ExecResult, error) {
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
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
		p.mu.Lock()
		if !p.closed {
			p.closed = true
			close(p.exitCh)
		}
		p.mu.Unlock()
		if p.session != nil {
			p.session.deregisterProcess(p)
		}
		done <- result{code, err}
	}()

	select {
	case <-ctx.Done():
		_ = p.Close()
		return sandboxpkg.ExecResult{}, ctx.Err()
	case r := <-done:
		return sandboxpkg.ExecResult{ExitCode: r.code}, r.err
	}
}

func (p *noneProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.exitCh)
	p.cancel()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return nil
}

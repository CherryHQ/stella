// Hardening layers applied: process-group isolation on Unix, rlimits on Linux,
// bwrap filesystem/network isolation on Linux, macOS Seatbelt (sandbox-exec)
// filesystem and network isolation on macOS.
// Use the docker backend when full container isolation is required.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// sandboxEnvDenyList is the set of host environment variable names that must
// never be copied into a sandbox environment, even when InheritEnv is true.
// These variables hold host-level secrets that sandboxed processes must not
// access.
var sandboxEnvDenyList = []string{"STELLA_VAULT_KEY"}

// Config configures the local sandbox factory.
type Config struct {
	// StellaHome is the host path to the stella home directory, used for
	// building a sandboxed PATH that includes $STELLA_HOME/bin.
	StellaHome string
}

// Factory creates local sandbox sessions that run directly on the host OS.
type Factory struct {
	cfg Config
}

// NewFactory returns a Factory for the local backend.
func NewFactory(cfg ...Config) sandboxpkg.Factory {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}
	return &Factory{cfg: c}
}

// Name returns the backend name.
func (f *Factory) Name() string { return "local" }

// Available always returns true — the local backend has no external dependencies.
func (f *Factory) Available() bool { return true }

// Supported returns an error if platform sandbox requirements are not met.
func (f *Factory) Supported(_ sandboxpkg.Policy) error { return checkSandboxRequirements() }

// tmpMount pairs a sandbox-space path (e.g. /tmp) with its backing real host path.
type tmpMount struct {
	sandboxPath string // absolute path the agent sees (e.g. /tmp)
	realPath    string // absolute host path backing it
	owned       bool   // true when the session created this directory and should remove it on close
}

// CreateSession creates a new localSession.
// If a StellaHome was provided via Config, the factory adjusts the policy env
// with a sandboxed PATH, HOME, and an allowlist of host variables so callers
// don't need to know about host-execution specifics.
func (f *Factory) CreateSession(_ context.Context, policy sandboxpkg.Policy) (sandboxpkg.Session, error) {
	sessionID := sandboxpkg.NewSessionID()
	if err := checkSandboxRequirements(); err != nil {
		return nil, err
	}

	hostStellaHome := f.cfg.StellaHome
	// Resolve the sandbox-space root first: adjustPolicy needs it to point HOME
	// and the XDG dirs at the user home as the agent sees it. resolveSandboxRoot
	// reads only WorkspaceRoot, which adjustPolicy leaves untouched.
	sandboxRoot, realRoot := resolveSandboxRoot(policy)
	userDataSandbox, userDataReal := resolveUserDataRoot(policy)
	policy = f.adjustPolicy(policy, sandboxRoot, realRoot, userDataSandbox, userDataReal)

	tmpMounts, err := createSessionTmpMounts(policy)
	if err != nil {
		return nil, fmt.Errorf("local: create session tmp: %w", err)
	}
	s := &localSession{
		id:                sessionID,
		policy:            policy,
		realRoot:          realRoot,
		sandboxRoot:       sandboxRoot,
		userDataReal:      userDataReal,
		userDataSandbox:   userDataSandbox,
		stellaHomeHost:    hostStellaHome,
		stellaHomeSandbox: adjustStellaHome(hostStellaHome),
		tmpMounts:         tmpMounts,
		done:              make(chan struct{}),
	}
	sandboxpkg.LogSessionCreated(sessionID, "local", policy)
	return s, nil
}

// adjustPolicy applies local-backend-specific environment adjustments.
// sandboxRoot/realRoot are the sandbox-space and host views of the agent
// workspace; userDataSandbox/userDataReal are the same for the shared user-data
// root (empty when none). HOME and the XDG dirs are anchored to the sandbox-space
// views so the agent sees a clean /workspace + /user pair.
func (f *Factory) adjustPolicy(policy sandboxpkg.Policy, sandboxRoot, realRoot, userDataSandbox, userDataReal string) sandboxpkg.Policy {
	if f.cfg.StellaHome == "" {
		return policy
	}
	env := maps.Clone(policy.Env)
	if env == nil {
		env = make(map[string]string)
	}
	sandboxSH := adjustStellaHome(f.cfg.StellaHome)
	hostSH := f.cfg.StellaHome
	// remapMise rewrites a mise path to the agent's view. The per-user mise tree
	// lives under the STELLA_HOME frame ($STELLA_HOME/users/{id}/.mise-tools), so it
	// falls through the user-data (/user) and workspace (/workspace) frames and
	// lands under STELLA_HOME (/opt/stella/users/{id}/.mise-tools) — the same root
	// as the system tree, so the relative seed/shim symlinks resolve (#505). A
	// project-local tree maps to /workspace. Composing is safe — once a path lands
	// under one sandbox root it is no longer under the next frame's host prefix, so
	// later steps leave it untouched.
	remapMise := func(p string) string {
		p = remapToSandboxRoot(p, userDataReal, userDataSandbox)
		p = remapToSandboxRoot(p, realRoot, sandboxRoot)
		return remapStellaHomePath(p, hostSH, sandboxSH)
	}
	// Recover the per-user mise home from the runtime env (MISE_DATA_DIR, still a
	// host path here) and remap it to the sandbox tree to put its shims on PATH.
	userShims := ""
	if dir := sandboxpkg.PerUserMiseDataDir(env, hostSH); dir != "" {
		userShims = sandboxpkg.MiseUserShimsDir(remapMise(dir))
	}
	env["PATH"] = sandboxpkg.HostEnvBuildPath(sandboxSH, userShims)
	// HOME is the agent workspace (/workspace), so XDG config/data/state default
	// under it and stay private to this agent; only the cache is shared, pointed at
	// the user-data root (/user). The project dir stays the cwd; only HOME differs.
	env["HOME"] = sandboxRoot
	setXDGDirs(env, sandboxRoot, userDataSandbox)
	if userDataSandbox != "" {
		// The shared user-data root, exposed so the agent (and skills/uploads) can
		// address it without learning the host users/{id} layout.
		env["STELLA_USER_DIR"] = userDataSandbox
	}
	env["STELLA_HOME"] = sandboxSH
	// Rewrite MISE_* path-valued env vars to the agent's view (see remapMise): both
	// the per-user tree and the system tree land under the sandbox STELLA_HOME, so
	// their host-relative seed/shim symlinks resolve identically in the sandbox.
	// All but MISE_TRUSTED_CONFIG_PATHS are single scalar paths, and
	// ':' is a legal character in a POSIX path, so they are remapped whole — only
	// the genuinely list-valued var is split on the path-list separator (each
	// element remapped independently; already-sandbox paths like /workspace
	// survive untouched).
	for k, v := range env {
		if !strings.HasPrefix(k, "MISE_") {
			continue
		}
		if k == "MISE_TRUSTED_CONFIG_PATHS" {
			seen := map[string]struct{}{}
			var parts []string
			for p := range strings.SplitSeq(v, string(filepath.ListSeparator)) {
				// Remapping can collapse distinct host paths onto one sandbox path
				// (e.g. the host user-root onto /workspace), so dedupe to keep the
				// trusted list clean and order-stable.
				rp := remapMise(p)
				if _, ok := seen[rp]; ok {
					continue
				}
				seen[rp] = struct{}{}
				parts = append(parts, rp)
			}
			env[k] = strings.Join(parts, string(filepath.ListSeparator))
			continue
		}
		env[k] = remapMise(v)
	}
	sandboxpkg.HostEnvCopy(env)
	policy.Env = env
	policy.InheritEnv = false
	return policy
}

// setXDGDirs points the agent's XDG config/data/state under its $HOME (the agent
// workspace), so each agent's credentials and state (e.g. ~/.config/gh) stay
// private to it — the workspace is per-agent and siblings are never mounted. The
// cache is pointed at the shared user-data root (userData, the sandbox-space
// /user) so toolchain/download caches are reused across the user's agents. When
// userData is "" (a user-less session with no shared root) the cache falls back
// to $HOME so nothing is shared.
//
// NOTE: /user is a shared writable trust domain for all of one user's agents, not
// an isolation boundary — a tool that writes credentials into its cache would
// expose them to the user's other agents. Credentials belong under the private
// XDG config/data/state (HOME), which is why only the cache is shared here.
func setXDGDirs(env map[string]string, home, userData string) {
	cacheHome := home
	if userData != "" {
		cacheHome = userData
	}
	env["XDG_CACHE_HOME"] = filepath.Join(cacheHome, ".cache")
	env["XDG_CONFIG_HOME"] = filepath.Join(home, ".config")
	env["XDG_DATA_HOME"] = filepath.Join(home, ".local", "share")
	env["XDG_STATE_HOME"] = filepath.Join(home, ".local", "state")
}

// remapToSandboxRoot rewrites a host path under realRoot to its sandbox-space
// location under sandboxRoot, leaving paths outside realRoot (and the macOS case
// realRoot == sandboxRoot) untouched. It mirrors localSession.toSandboxPath for
// the workspace root, but runs at policy-build time before a session exists.
func remapToSandboxRoot(hostPath, realRoot, sandboxRoot string) string {
	// An empty realRoot has no host prefix to match, so nothing can be "under" it;
	// filepath.Rel("", x) would otherwise treat any relative value as under realRoot
	// and rewrite scalar env values (e.g. MISE_YES=1) into bogus sandbox paths.
	if hostPath == "" || realRoot == "" || realRoot == sandboxRoot {
		return hostPath
	}
	rel, err := filepath.Rel(realRoot, filepath.Clean(hostPath))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return hostPath
	}
	if rel == "." {
		return sandboxRoot
	}
	return filepath.Join(sandboxRoot, rel)
}

// remapStellaHomePath rewrites a host path under hostSH to its sandbox-adjusted
// location under sandboxSH, leaving paths outside hostSH (e.g. /workspace)
// untouched. When hostSH == sandboxSH (macOS, no remap) it is a no-op. Shared by
// the env rewrite in adjustPolicy and the bwrap mount path in session_linux.go so
// the two can't drift.
func remapStellaHomePath(p, hostSH, sandboxSH string) string {
	switch {
	case p == hostSH:
		return sandboxSH
	case strings.HasPrefix(p, hostSH+string(filepath.Separator)):
		return sandboxSH + p[len(hostSH):]
	default:
		return p
	}
}

func mountBySandboxPath(mounts []sandboxpkg.Mount, sandboxPath string) (sandboxpkg.Mount, bool) {
	clean := filepath.Clean(sandboxPath)
	for _, m := range mounts {
		if filepath.Clean(m.SandboxPath) == clean {
			return m, true
		}
	}
	return sandboxpkg.Mount{}, false
}

// ─────────────────────────── localSession ─────────────────────────────

// localSession implements sandboxpkg.Session by running commands directly on
// the host OS with no container isolation.
type localSession struct {
	id                string
	policy            sandboxpkg.Policy
	realRoot          string     // actual host path (e.g. /home/stella/.stella-dev/...)
	sandboxRoot       string     // path the agent sees (/workspace on Linux+bwrap, else = realRoot)
	userDataReal      string     // host path of the shared user-data root, "" when none
	userDataSandbox   string     // path the agent sees for it (/user on Linux+bwrap, else = userDataReal)
	stellaHomeHost    string     // host-side STELLA_HOME for bwrap mounts
	stellaHomeSandbox string     // agent's view of STELLA_HOME (/opt/stella on Linux+bwrap, else = host)
	tmpMounts         []tmpMount // sandbox temp paths mapped to real host dirs (/tmp, /var/tmp)
	done              chan struct{}
	doneOnce          sync.Once
	mu                sync.RWMutex
	closed            bool
	procs             []*localProcess
}

func (s *localSession) Policy() sandboxpkg.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *localSession) WorkspaceRoot() string {
	return s.sandboxRoot
}

func (s *localSession) WorkingDir() string {
	wd := s.policy.Filesystem.WorkingDir
	if wd == "" {
		return s.sandboxRoot
	}
	// Translate real-root paths into sandbox-space paths.
	cleanReal := filepath.Clean(s.realRoot)
	if wd == cleanReal || strings.HasPrefix(wd, cleanReal+string(filepath.Separator)) {
		rel, err := filepath.Rel(cleanReal, wd)
		if err == nil {
			return filepath.Join(s.sandboxRoot, rel)
		}
	}
	return wd
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
	for _, m := range s.tmpMounts {
		if m.owned {
			os.RemoveAll(m.realPath) //nolint:errcheck
		}
	}
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

// ResolvePath resolves an agent-space path to a real host path, rejecting
// anything that resolves outside the workspace root.
// The agent may pass sandbox-space paths (e.g. /workspace/foo.go); this
// translates them to real host paths before any OS operations.
func (s *localSession) ResolvePath(agentPath string) (string, error) {
	resolved, _, err := s.resolvePath(agentPath)
	return resolved, err
}

// ResolveWritePath is like ResolvePath but additionally rejects paths that fall
// within read-only mounts.
func (s *localSession) ResolveWritePath(agentPath string) (string, error) {
	resolved, err := s.pathResolver().ResolveWritePath(agentPath)
	if err != nil {
		return "", fmt.Errorf("local: %w", err)
	}
	return resolved.HostPath, nil
}

// resolveCwd validates a requested working directory, then returns both its
// real host path and sandbox-space path. Linux bwrap needs the sandbox-space
// path for --chdir; direct local execution uses the real path.
func (s *localSession) resolveCwd(cwd string) (sandboxCwd, realCwd string, err error) {
	if cwd == "" {
		cwd = s.WorkingDir()
	}
	realCwd, sandboxCwd, err = s.resolvePath(cwd)
	if err != nil {
		return "", "", err
	}
	return sandboxCwd, realCwd, nil
}

// resolvePath translates an agent-space path to a real path and a normalized
// sandbox-space path. Existing symlink components under the workspace are
// rejected, including symlinked parents of non-existent creation targets.
func (s *localSession) resolvePath(agentPath string) (realPath, sandboxPath string, err error) {
	resolved, err := s.pathResolver().ResolvePath(agentPath)
	if err != nil {
		return "", "", fmt.Errorf("local: %w", err)
	}
	return resolved.HostPath, resolved.SandboxPath, nil
}

// matchingTmpMount returns the tmpMount with the longest sandboxPath that
// contains sandboxPath, or nil if none match.

// matchingExtraMount returns the longest extra read-only mount that contains
// resolved, or "" if none match.

func (s *localSession) pathResolver() *sandboxpkg.PathResolver {
	mounts := append([]sandboxpkg.Mount(nil), s.policy.Filesystem.Mounts...)
	if len(mounts) == 0 {
		if s.realRoot != "" && s.sandboxRoot != "" {
			mounts = append(mounts, sandboxpkg.Mount{HostPath: s.realRoot, SandboxPath: s.sandboxRoot, Access: sandboxpkg.MountReadWrite})
		}
		if s.userDataReal != "" && s.userDataSandbox != "" {
			mounts = append(mounts, sandboxpkg.Mount{HostPath: s.userDataReal, SandboxPath: s.userDataSandbox, Access: sandboxpkg.MountReadWrite})
		}
		for _, pair := range s.stellaHomeSubdirs() {
			mounts = append(mounts, sandboxpkg.Mount{HostPath: pair[1], SandboxPath: pair[0], Access: sandboxpkg.MountReadOnly})
		}
	}
	for _, m := range s.tmpMounts {
		mounts = append(mounts, sandboxpkg.Mount{HostPath: m.realPath, SandboxPath: m.sandboxPath, Access: sandboxpkg.MountReadWrite})
	}
	return sandboxpkg.NewPathResolver(sandboxpkg.PathResolverConfig{
		WorkspaceRoot: s.realRoot,
		WorkingDir:    s.WorkingDir(),
		Mounts:        mounts,
	})
}

// stellaHomeSubdirs returns the {sandboxRoot, hostRoot} pairs for each subtree of
// STELLA_HOME that an isolating backend RO-mounts (see StellaHomeSandboxDirs).
// File tools mirror exactly these mounts — reads are scoped to them and nothing
// broader, so the sibling users/ and agents/ host trees nested under STELLA_HOME
// stay invisible. Returns nil on identity backends (sandbox == host).
func (s *localSession) stellaHomeSubdirs() [][2]string {
	if s.stellaHomeHost == "" || s.stellaHomeSandbox == s.stellaHomeHost {
		return nil
	}
	out := make([][2]string, 0, len(sandboxpkg.StellaHomeSandboxDirs()))
	for _, name := range sandboxpkg.StellaHomeSandboxDirs() {
		out = append(out, [2]string{
			filepath.Join(s.stellaHomeSandbox, name),
			filepath.Join(s.stellaHomeHost, name),
		})
	}
	return out
}

// pathWithinRoot reports whether path is the root itself or is contained under it.

// rejectLocalSymlinkTraversal rejects any symlink component at or below the
// workspace root. For non-existent targets, checking stops at the first missing
// component so creating new files still works unless an existing parent is a
// symlink.

// toRealPath translates a sandbox-space absolute path to the real host path.
// When sandboxRoot == realRoot (no remapping), it is a no-op.
// Temp paths (/tmp, /var/tmp) are checked first against tmpMounts.
func (s *localSession) toRealPath(sandboxPath string) string {
	if hostPath, ok := s.pathResolver().ToHostPath(sandboxPath); ok {
		return hostPath
	}
	return sandboxPath
}

// toSandboxPath translates a real host path back into sandbox space.
// Temp mount real paths are checked first against tmpMounts.
func (s *localSession) toSandboxPath(realPath string) string {
	if sandboxPath, ok := s.pathResolver().ToSandboxPath(realPath); ok {
		return sandboxPath
	}
	return realPath
}

// Exec runs a shell command via sh -c on the host.
func (s *localSession) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	// Finding 5: check closed before starting. Per-exec env reads take a policy
	// snapshot under the same lock.
	s.mu.RLock()
	closed := s.closed
	policy := s.policy
	s.mu.RUnlock()
	if closed {
		return sandboxpkg.ExecResult{}, fmt.Errorf("local: session is closed")
	}

	sandboxCwd := opts.Cwd
	if sandboxCwd == "" {
		sandboxCwd = s.WorkingDir()
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

	sandboxCwd, realCwd, err := s.resolveCwd(sandboxCwd)
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("local exec: resolve cwd: %w", err)
	}

	execPath, execArgs, err := wrapCommand(policy, sandboxCwd, s.tmpMounts, s.stellaHomeHost, "sh", []string{"-c", command})
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("local exec: wrap: %w", err)
	}

	// Finding 2: do NOT use exec.CommandContext — it only kills the leader PID,
	// leaving process-group children alive. We manage cancellation manually.
	cmd := exec.Command(execPath, execArgs...)
	cmd.Dir = realCwd
	cmd.Env = buildEnv(policy, opts.Env)
	setSysProcAttr(cmd)

	stdout := sandboxpkg.NewExecOutputBuffer()
	stderr := sandboxpkg.NewExecOutputBuffer()
	cmd.Stdout = stdout
	cmd.Stderr = stderr

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
	// Per-exec env reads take a policy snapshot under the lock.
	s.mu.RLock()
	policy := s.policy
	s.mu.RUnlock()

	sandboxCwd := req.Cwd
	if sandboxCwd == "" {
		sandboxCwd = s.WorkingDir()
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
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

	sandboxCwd, realCwd, err := s.resolveCwd(sandboxCwd)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("local start_process: resolve cwd: %w", err)
	}

	execPath, execArgs, err := wrapCommand(policy, sandboxCwd, s.tmpMounts, s.stellaHomeHost, req.Path, args)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("local start_process: wrap: %w", err)
	}

	// Finding 2: do NOT use exec.CommandContext — kill the process group instead.
	cmd := exec.Command(execPath, execArgs...)
	cmd.Dir = realCwd
	cmd.Env = buildEnv(policy, req.Env)
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
				if slices.Contains(sandboxEnvDenyList, before) {
					continue
				}
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

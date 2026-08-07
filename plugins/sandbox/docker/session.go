package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// workspaceMount is the agent's per-agent workspace root inside the container —
// the sandbox HOME and initial cwd in the two-root layout. The bundled Dockerfile
// pre-creates it and bakes HOME=/workspace; the image toolchain (mise) is pinned
// to absolute /home/stella paths via MISE_DATA_DIR so it stays reachable.
const workspaceMount = sandboxpkg.MountWorkspace

// userDataMount is the shared user-data root inside the container (the second
// top-level root). The host users/{id}/data tree binds here so skills, assets,
// and the per-user mise tree are addressable without leaking the host layout.
const userDataMount = sandboxpkg.MountUserData

// stellaHomeMount is the in-container root for the read-only host STELLA_HOME
// assets sessions need. Only selected subdirectories are bind-mounted.
const stellaHomeMount = sandboxpkg.MountStellaHome

func nextSessionID() string { return sandboxpkg.NewSessionID() }

func logSessionCreated(sessionID, backend string, policy sandboxpkg.Policy) {
	sandboxpkg.LogSessionCreated(sessionID, backend, policy)
}

func logSessionClosed(sessionID, backend, reason string) {
	sandboxpkg.LogSessionClosed(sessionID, backend, reason)
}

// dockerFactory creates docker-backed sandbox sessions.
type dockerFactory struct {
	cfg                Config
	clientFn           func() (*dockerclient.Client, error)
	cleanupOrphansOnce sync.Once
	toolCacheGCOnce    sync.Once
}

func (f *dockerFactory) client() (*dockerclient.Client, error) {
	if f.clientFn != nil {
		return f.clientFn()
	}
	return getSharedClient()
}

// NewFactory returns a Factory backed by a Docker container-per-session strategy.
//
// When cfg.StellaHome is non-empty, construction performs I/O:
//   - Runtime mode resolution: reads $STELLA_DOCKER_SANDBOX_MODE and the
//     matching mode-specific env ($STELLA_HOME_HOST or $STELLA_HOME_VOLUME).
//     No container runtime auto-detection is used.
//   - User tool resolution: loads the builtin tool manifest to populate
//     UserToolBinaries (manifest-declared CLIs not baked into the image).
//
// Both steps are skipped when StellaHome is empty (e.g. unit tests), making
// construction cheap and infallible in that case.
func NewFactory(cfg Config) (sandboxpkg.Factory, error) {
	cfg.Layout = cfg.Layout.Clone()
	if cfg.StellaHome != "" {
		var err error
		cfg, err = applyDockerMode(cfg, cfg.StellaHome)
		if err != nil {
			return nil, err
		}
		detectCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cfg = autodetectServerReachability(detectCtx, cfg)
		cancel()
		if len(cfg.UserToolBinaries) == 0 {
			tools, err := resolveUserToolBinaries()
			if err != nil {
				return nil, fmt.Errorf("resolve docker user tools: %w", err)
			}
			cfg.UserToolBinaries = tools
		}
	}
	return &dockerFactory{cfg: cfg}, nil
}

func (f *dockerFactory) Name() string { return "docker" }

// Available reports whether a docker daemon is reachable. The CLI is not a
// runtime dependency — the moby SDK talks to the socket directly — so this
// builds a client and pings ServerVersion with a short timeout.
func (f *dockerFactory) Available() bool {
	var (
		c   *dockerclient.Client
		err error
	)
	if f.clientFn != nil {
		c, err = f.clientFn()
	} else {
		c, err = dockerclient.New()
		if err == nil {
			defer func() { _ = c.Close() }()
		}
	}
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.Version(ctx)
	return err == nil
}

// Supported returns a PolicyCompatibilityError when the docker daemon is unreachable.
func (f *dockerFactory) Supported(policy sandboxpkg.Policy) error {
	if err := f.cfg.Layout.Validate(); err != nil {
		return fmt.Errorf("docker sandbox: invalid host layout: %w", err)
	}
	if !f.Available() {
		return &sandboxpkg.PolicyCompatibilityError{
			Backend: f.Name(),
			Policy:  policy,
			Reason:  "docker daemon not reachable (check DOCKER_HOST and that the daemon is running)",
		}
	}

	return nil
}

// EnsureReady performs preflight checks (daemon reachability, image availability)
// and orphan cleanup. Safe to call multiple times; orphan cleanup runs at most once.
func (f *dockerFactory) EnsureReady(ctx context.Context) error {
	if err := Preflight(ctx, PreflightConfig{StellaHome: f.cfg.StellaHome, Docker: f.cfg}); err != nil {
		return err
	}
	f.cleanupOrphansOnce.Do(func() {
		scope := f.cfg.cleanupScope(f.cfg.StellaHome)
		if scope == "" {
			return
		}
		client, err := f.client()
		if err != nil {
			return
		}
		// Reap containers before their owned temp directories. A directory is
		// deleted only after this pass confirms no scoped container still names
		// its session, so startup never unmounts a live session's /tmp backing.
		dockerclient.CleanupOrphanedContainers(ctx, client, scope)
		cleanupStaleSessionTempDirs(ctx, client, scope, f.cfg.StellaHome)
	})
	f.toolCacheGCOnce.Do(func() {
		client, err := f.client()
		if err != nil {
			return
		}
		cleanupToolCacheVolumes(ctx, client, time.Now().UTC())
	})
	return nil
}

// CreateSession starts a new container and returns a dockerSession.
func (f *dockerFactory) CreateSession(ctx context.Context, policy sandboxpkg.Policy) (sandboxpkg.Session, error) {
	if err := f.Supported(policy); err != nil {
		return nil, err
	}

	if f.cfg.Image == "" {
		return nil, fmt.Errorf("docker session: Image is required")
	}

	if f.cfg.StellaHome != "" {
		if err := f.EnsureReady(ctx); err != nil {
			return nil, err
		}
	}

	layout := f.cfg.Layout
	workspaceHost := layout.WorkspaceSource

	sessionID := nextSessionID()

	tempDir, err := f.prepareSessionTempDir(sessionID)
	if err != nil {
		return nil, err
	}
	transferredTempOwnership := false
	defer func() {
		if !transferredTempOwnership {
			_ = os.RemoveAll(tempDir)
		}
	}()
	// Shared user-data root (mounted as /user). Empty for a user-less job, which
	// has no principal home — then no /user mount and shared assets stay unset.
	ctx, span := tracer.Start(ctx, "sandbox.docker.session",
		trace.WithAttributes(sessionTraceAttrs(sessionID, policy, f.cfg.Image, workspaceHost)...),
	)

	// Map network mode.
	networkMode := mapNetworkMode(policy)

	// Get the shared client.
	client, err := f.client()
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: client: %w", err)
	}

	cleanupScope := f.cfg.cleanupScope(f.cfg.StellaHome)
	opts := dockerclient.CreateOptions{
		Image:          f.cfg.Image,
		User:           dockerProcessUser(),
		WorkspaceHost:  workspaceHost,
		WorkspaceMount: workspaceMount,
		NetworkMode:    networkMode,
		Network:        f.cfg.SandboxNetwork,
		Labels: map[string]string{
			dockerclient.LabelSessionID:  sessionID,
			dockerclient.LabelStellaHome: cleanupScope,
			dockerclient.LabelCreatedAt:  time.Now().UTC().Format(time.RFC3339),
			dockerclient.LabelOwnerPID:   strconv.Itoa(os.Getpid()),
		},
		Name: "stella-sandbox-" + sessionID,
	}

	mountedLayout, mountedTempDirHost, mountedUserDataHost, err := f.configureSessionMounts(&opts, layout, tempDir)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, err
	}

	// Per-user mise tree(s) are generic writable mounts; keep their mounted pairs
	// so PATH can point at their shims.
	perUserTrees := writableToolTrees(mountedLayout)

	// Render only roots that actually have a container view. An unavailable
	// /user mount falls persistent XDG state back to the workspace. Values stay
	// host-side until translateEnvPaths maps them for container creation and exec.
	env := maps.Clone(policy.Env)
	if env == nil {
		env = make(map[string]string)
	}
	if err := applyDockerFilesystemEnv(env, workspaceHost, mountedUserDataHost, mountedTempDirHost); err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: apply filesystem environment: %w", err)
	}
	policy.Env = env

	// Point the in-sandbox CLI at stellad over the shared network. Without this
	// the CLI falls back to 127.0.0.1:25678 — the sandbox container's own
	// loopback, where nothing listens — so server-backed commands fail. Injecting
	// into policy.Env covers both the container's create-time env and every later
	// exec, which both derive from it. Set only when configured (DooD); the
	// local/host backend reaches stellad on loopback.
	policy.Env = withServerURL(policy.Env, f.cfg.ServerURL)

	// Build the mount table and env prefix maps before creating the container so
	// the create-time env can be translated to the container view too — otherwise
	// PID 1's environment carries host paths the agent can read via
	// /proc/1/environ, defeating the isolation (Pi critical).
	mountTable := buildMountTable(mountTableOptions{
		WorkspaceHost:  workspaceHost,
		WorkspaceMount: workspaceMount,
		Mounts:         mountedLayout,
		TempHost:       mountedTempDirHost,
	})
	agentCwd, err := agentWorkingDir(mountTable, layout.WorkingDirSource)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: working directory: %w", err)
	}
	policy.Filesystem.WorkingDir = agentCwd

	// The per-user trees are writable and resolve via the process view too.
	for _, tree := range perUserTrees {
		mountTable = append(mountTable, dockerclient.Mount{
			HostPath:      tree.Host,
			ContainerPath: tree.Container,
		})
	}

	var envMaps []envPathMap
	if hostHome := policy.Env["STELLA_HOME"]; hostHome != "" {
		envMaps = append(envMaps, envPathMap{
			HostPrefix:      hostHome,
			ContainerPrefix: stellaHomeMount,
		})
	}
	hostEnv := policy.Env
	opts.Env = translateEnvPaths(mergeEnv(hostEnv, nil), mountTable, envMaps)
	// Retain host coordinates only privately for exec-time translation. The
	// session policy is observable API and must expose container coordinates.
	policy.Env = maps.Clone(opts.Env)

	// Per-user mise shims go on PATH ahead of the image's system shims so a user's
	// own tool versions win (mirrors HostEnvBuildPath on the host backends). Only
	// the mise tree gets a shims/ entry, so guard against a future non-mise writable
	// mount contributing a bogus PATH dir.
	var toolBinPaths []string
	for _, tree := range perUserTrees {
		if path.Base(tree.Container) == ".mise-tools" {
			toolBinPaths = append(toolBinPaths, path.Join(tree.Container, "shims"))
		}
	}
	toolCache, err := ensureUserToolCache(ctx, client, f.cfg)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, err
	}
	if toolCache != nil {
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath:      toolCache.VolumeName,
			ContainerPath: containerUserToolsRoot,
			ReadOnly:      true,
			Type:          dockerclient.MountTypeVolume,
		})
		toolBinPaths = append(toolBinPaths, toolCache.BinPath)
	}

	slog.Info("docker session: creating sandbox container",
		"session_id", sessionID,
		"image", opts.Image,
		"container_name", opts.Name,
		"workspace", workspaceHost,
		"network_mode", opts.NetworkMode,
		"extra_mounts", len(opts.ExtraMounts),
	)
	containerID, err := client.CreateAndStart(ctx, opts)
	if err != nil {
		slog.Warn("docker session: sandbox container create/start failed",
			"session_id", sessionID,
			"image", opts.Image,
			"container_name", opts.Name,
			"error", err,
		)
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: create and start: %w", err)
	}

	slog.Info("docker session: sandbox container ready",
		"session_id", sessionID,
		"container_id", containerID,
		"container_name", opts.Name,
	)
	span.AddEvent("sandbox.docker.session.ready", trace.WithAttributes(
		attribute.String("stella.sandbox.container_id", containerID),
	))

	session := &dockerSession{
		id:           sessionID,
		policy:       policy,
		hostEnv:      hostEnv,
		client:       client,
		containerID:  containerID,
		mountTable:   mountTable,
		workingDir:   agentCwd,
		envPathMaps:  envMaps,
		toolBinPaths: toolBinPaths,
		ownedTempDir: tempDir,
		done:         make(chan struct{}),
		traceSpan:    span,
	}
	session.host = &dockerHost{session: session}
	transferredTempOwnership = true

	logSessionCreated(sessionID, "docker", policy)
	go session.watchContainer()

	return session, nil
}

// mapNetworkMode translates sandbox policy network mode to the dockerclient type.
func mapNetworkMode(policy sandboxpkg.Policy) dockerclient.NetworkMode {
	switch policy.NetworkModeOrDefault() {
	case sandboxpkg.NetworkDisabled:
		return dockerclient.NetworkDisabled
	default:
		return dockerclient.NetworkAllowAll
	}
}

// dockerSession is a docker-backed sandbox session backed by a single container.
type dockerSession struct {
	id           string
	policy       sandboxpkg.Policy
	hostEnv      map[string]string
	client       *dockerclient.Client
	containerID  string
	mountTable   []dockerclient.Mount
	workingDir   string
	envPathMaps  []envPathMap
	toolBinPaths []string
	ownedTempDir string
	host         *dockerHost
	done         chan struct{}
	doneOnce     sync.Once
	closed       bool
	closeErr     error
	traceSpan    trace.Span
	traceOnce    sync.Once
	mu           sync.RWMutex
}

func (s *dockerSession) Policy() sandboxpkg.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *dockerSession) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	return s.host.Exec(ctx, command, opts)
}

func (s *dockerSession) StartProcess(ctx context.Context, req sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	return s.host.StartProcess(ctx, req)
}

// WorkingDir is the agent-visible container coordinate corresponding exactly
// to the configured working directory, never the daemon-side source path.
func (s *dockerSession) WorkingDir() string { return s.workingDir }

func (s *dockerSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed
}

func (s *dockerSession) Done() <-chan struct{} { return s.done }

func (s *dockerSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *dockerSession) markClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	return true
}

func (s *dockerSession) endTrace(reason string, err error) {
	s.traceOnce.Do(func() {
		if s.traceSpan == nil {
			return
		}
		s.traceSpan.AddEvent("sandbox.docker.session.closed", trace.WithAttributes(
			attribute.String("stella.sandbox.close_reason", reason),
		))
		recordError(s.traceSpan, err)
		s.traceSpan.End()
	})
}

// watchContainer polls ContainerAlive every 5s. If the container dies unexpectedly,
// it asks the watcher close path to mark the session closed and best-effort reap
// the stopped container so long-running stellad processes do not accumulate corpses.
func (s *dockerSession) watchContainer() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		closed := s.closed
		s.mu.RUnlock()
		if closed {
			return
		}
		checkCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		alive, err := s.client.ContainerAlive(checkCtx, s.containerID)
		cancel()
		if err != nil || !alive {
			reason := "container_exited"
			if err != nil {
				reason = "container_liveness_error"
			}
			s.closeFromWatcher(reason, err)
			return
		}
	}
}

func (s *dockerSession) closeFromWatcher(reason string, livenessErr error) {
	if !s.markClosed() {
		return
	}

	s.clearContainerTemp()
	reapCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	closeErr := s.client.Stop(reapCtx, s.containerID)
	if closeErr != nil {
		slog.Warn("docker session: failed to reap exited container", "session_id", s.id, "container_id", s.containerID, "error", closeErr)
	} else if err := s.cleanupOwnedTempDir(); err != nil {
		closeErr = err
	}
	s.finishClose(reason, closeErr, errors.Join(livenessErr, closeErr))
}

// finishClose publishes the final cleanup result before Done closes. Losers of
// markClosed wait on Done, so this assignment establishes their result boundary.
func (s *dockerSession) finishClose(reason string, closeErr, traceErr error) {
	s.mu.Lock()
	s.closeErr = closeErr
	s.mu.Unlock()
	s.endTrace(reason, traceErr)
	logSessionClosed(s.id, "docker", reason)
	s.closeDone()
}

// Close stops the container and marks the session closed.
// Uses a fresh background context with a 30s timeout so that cancellation of the
// caller's context does not leave the container running.
func (s *dockerSession) Close() error {
	if !s.markClosed() {
		<-s.Done()
		s.mu.RLock()
		closeErr := s.closeErr
		s.mu.RUnlock()
		return closeErr
	}

	s.clearContainerTemp()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	closeErr := s.client.Stop(stopCtx, s.containerID)
	if closeErr == nil {
		closeErr = s.cleanupOwnedTempDir()
	}
	s.finishClose("explicit_close", closeErr, closeErr)
	return closeErr
}

func (h *dockerHost) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	cwd := opts.Cwd
	if cwd == "" {
		cwd = h.session.WorkingDir()
	}

	hostCwd, err := h.resolvePath(cwd)
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("docker host exec: resolve cwd: %w", err)
	}

	containerCwd, err := toContainerPath(h.session.mountTable, hostCwd)
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("docker host exec: cwd not in any mount: %w", err)
	}

	// Per-exec env reads take a snapshot under the lock.
	h.session.mu.RLock()
	policyEnv := h.session.hostEnv
	h.session.mu.RUnlock()
	env := injectToolPaths(translateEnvPaths(mergeEnv(policyEnv, opts.Env), h.session.mountTable, h.session.envPathMaps), h.session.toolBinPaths)

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = h.session.policy.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	result, err := h.session.client.Exec(ctx, dockerclient.ExecOptions{
		ContainerID: h.session.containerID,
		Command:     []string{"/bin/sh", "-c", command},
		Cwd:         containerCwd,
		Env:         env,
	})
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("docker host exec: %w", err)
	}

	return sandboxpkg.ExecResult{
		Stdout:   string(result.Stdout),
		Stderr:   string(result.Stderr),
		ExitCode: result.ExitCode,
	}, nil
}

func (h *dockerHost) StartProcess(ctx context.Context, req sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = h.session.WorkingDir()
	}

	hostCwd, err := h.resolvePath(cwd)
	if err != nil {
		return nil, fmt.Errorf("docker host start_process: resolve cwd: %w", err)
	}

	containerCwd, err := toContainerPath(h.session.mountTable, hostCwd)
	if err != nil {
		return nil, fmt.Errorf("docker host start_process: cwd not in any mount: %w", err)
	}

	// Per-exec env reads take a snapshot under the lock.
	h.session.mu.RLock()
	policyEnv := h.session.hostEnv
	h.session.mu.RUnlock()
	env := injectToolPaths(translateEnvPaths(mergeEnv(policyEnv, req.Env), h.session.mountTable, h.session.envPathMaps), h.session.toolBinPaths)

	timeout := req.Timeout
	if timeout == 0 {
		timeout = h.session.policy.Timeout
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

	command := make([]string, 0, 1+len(req.Args))
	command = append(command, req.Path)
	command = append(command, req.Args...)

	handle, err := h.session.client.StartExec(execCtx, dockerclient.ExecOptions{
		ContainerID: h.session.containerID,
		Command:     command,
		Cwd:         containerCwd,
		Env:         env,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("docker host start_process: %w", err)
	}

	return &dockerProcessHandle{
		handle: handle,
		cancel: cancel,
	}, nil
}

// ─────────────────────────── dockerProcessHandle ──────────────────────────

// dockerProcessHandle wraps an ExecHandle from dockerclient and implements ProcessHandle.
// PID returns 0 because `docker exec` does not expose the in-container PID through the CLI.
type dockerProcessHandle struct {
	handle *dockerclient.ExecHandle
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
}

func (p *dockerProcessHandle) PID() int { return 0 }

func (p *dockerProcessHandle) Stdin() io.WriteCloser { return p.handle.Stdin }
func (p *dockerProcessHandle) Stdout() io.ReadCloser { return p.handle.Stdout }
func (p *dockerProcessHandle) Stderr() io.ReadCloser { return p.handle.Stderr }

func (p *dockerProcessHandle) Wait(ctx context.Context) (sandboxpkg.ExecResult, error) {
	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := p.handle.Wait()
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

func (p *dockerProcessHandle) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true
	p.cancel()

	if p.handle.Kill != nil {
		_ = p.handle.Kill()
	}
	return nil
}

package docker

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// workspaceMount matches the path pre-created in the bundled Dockerfile
// (plugins/sandbox/docker/Dockerfile) under the stella user's HOME so mise
// activate, $PATH, and $HOME all line up.
const workspaceMount = "/home/stella/workspace"

// stellaHomeMount is the in-container root used for the host STELLA_HOME assets
// that sandbox sessions need. Only selected subdirectories are bind-mounted.
const stellaHomeMount = "/home/stella/.stella"

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
	cleanupOrphansOnce sync.Once
}

// NewFactory returns a Factory backed by a Docker container-per-session strategy.
func NewFactory(cfg Config) sandboxpkg.Factory { return &dockerFactory{cfg: cfg} }

func (f *dockerFactory) Name() string { return "docker" }

// Available reports whether a docker daemon is reachable. The CLI is not a
// runtime dependency — the moby SDK talks to the socket directly — so this
// builds a client and pings ServerVersion with a short timeout.
func (f *dockerFactory) Available() bool {
	c, err := dockerclient.New()
	if err != nil {
		return false
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.Version(ctx)
	return err == nil
}

// Supported returns a PolicyCompatibilityError when the docker daemon is unreachable.
func (f *dockerFactory) Supported(policy sandboxpkg.Policy) error {
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
		daemonHome := f.cfg.TranslateToDaemonPath(f.cfg.StellaHome)
		if daemonHome == "" {
			return
		}
		client, err := getSharedClient()
		if err != nil {
			return
		}
		dockerclient.CleanupOrphanedContainers(ctx, client, daemonHome)
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

	sessionID := nextSessionID()

	workspaceHost, err := filepath.Abs(policy.WorkspaceRootOrDefault())
	if err != nil {
		return nil, fmt.Errorf("docker session: abs workspace root: %w", err)
	}

	ctx, span := tracer.Start(ctx, "sandbox.docker.session",
		trace.WithAttributes(sessionTraceAttrs(sessionID, policy, f.cfg.Image, workspaceHost)...),
	)

	// Map network mode.
	networkMode := mapNetworkMode(policy)

	// Get the shared client.
	client, err := getSharedClient()
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: client: %w", err)
	}

	// Label with the daemon-view STELLA_HOME so orphan cleanup scopes to this
	// host installation. Under DooD two stella instances may share an in-container
	// STELLA_HOME path while living in different host directories; labeling with
	// the daemon-view path keeps their container sets disjoint.
	stellaHome := f.cfg.TranslateToDaemonPath(policy.Env["STELLA_HOME"])

	opts := dockerclient.CreateOptions{
		Image:          f.cfg.Image,
		WorkspaceHost:  f.cfg.TranslateToDaemonPath(workspaceHost),
		WorkspaceMount: workspaceMount,
		NetworkMode:    networkMode,
		Env:            mergeEnv(policy.Env, nil),
		Labels: map[string]string{
			dockerclient.LabelSessionID:  sessionID,
			dockerclient.LabelStellaHome: stellaHome,
			dockerclient.LabelCreatedAt:  time.Now().UTC().Format(time.RFC3339),
			dockerclient.LabelOwnerPID:   strconv.Itoa(os.Getpid()),
		},
		Name: "stella-sandbox-" + sessionID,
	}

	if stellaHome != "" {
		opts.ReadOnlyMounts = []dockerclient.Mount{
			{
				HostPath:      filepath.Join(stellaHome, ".agents", "skills"),
				ContainerPath: filepath.Join(stellaHomeMount, ".agents", "skills"),
				ReadOnly:      true,
			},
		}
	}
	// Mount extra read-only paths (e.g. skill dirs) at their host path inside the
	// container (same-path strategy). HostPath is translated for DooD scenarios.
	for _, hostPath := range policy.Filesystem.ExtraReadOnlyMounts {
		opts.ReadOnlyMounts = append(opts.ReadOnlyMounts, dockerclient.Mount{
			HostPath:      f.cfg.TranslateToDaemonPath(hostPath),
			ContainerPath: hostPath,
			ReadOnly:      true,
		})
		// For paths that live inside the workspace host dir, also mount them
		// read-only at their workspace-relative container path. Without this,
		// bash can write to them via the writable workspace mount.
		rel, relErr := filepath.Rel(workspaceHost, hostPath)
		if relErr == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			opts.ReadOnlyMounts = append(opts.ReadOnlyMounts, dockerclient.Mount{
				HostPath:      f.cfg.TranslateToDaemonPath(hostPath),
				ContainerPath: filepath.Join(workspaceMount, rel),
				ReadOnly:      true,
			})
		}
	}

	var toolBinPaths []string
	toolCache, err := ensureUserToolCache(ctx, client, f.cfg)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, err
	}
	if toolCache != nil {
		opts.ReadOnlyMounts = append(opts.ReadOnlyMounts, dockerclient.Mount{
			HostPath:      toolCache.VolumeName,
			ContainerPath: containerUserToolsRoot,
			ReadOnly:      true,
			Type:          dockerclient.MountTypeVolume,
		})
		toolBinPaths = append(toolBinPaths, toolCache.BinPath)
	}

	containerID, err := client.CreateAndStart(ctx, opts)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: create and start: %w", err)
	}

	span.AddEvent("sandbox.docker.session.ready", trace.WithAttributes(
		attribute.String("stella.sandbox.container_id", containerID),
	))

	// Build mount table for the workspace plus mounted STELLA_HOME assets and extra mounts.
	mountTable := buildMountTable(workspaceHost, workspaceMount, policy.Env["STELLA_HOME"], stellaHomeMount, policy.Filesystem.ExtraReadOnlyMounts)

	session := &dockerSession{
		id:           sessionID,
		policy:       policy,
		client:       client,
		containerID:  containerID,
		mountTable:   mountTable,
		toolBinPaths: toolBinPaths,
		done:         make(chan struct{}),
		traceSpan:    span,
	}
	session.host = &dockerHost{session: session}

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

// buildMountTable returns the bind mount set that toContainerPath should consult.
// extraHostPaths are same-path mounts: container path equals the original host path.
func buildMountTable(workspaceHost, workspaceMount, stellaHomeHost, stellaHomeContainer string, extraHostPaths []string) []dockerclient.Mount {
	mounts := []dockerclient.Mount{
		{
			HostPath:      workspaceHost,
			ContainerPath: workspaceMount,
			ReadOnly:      false,
		},
	}
	if stellaHomeHost != "" && stellaHomeContainer != "" {
		mounts = append(mounts,
			dockerclient.Mount{
				HostPath:      stellaHomeHost,
				ContainerPath: stellaHomeContainer,
				ReadOnly:      true,
			},
			dockerclient.Mount{
				HostPath:      filepath.Join(stellaHomeHost, ".agents", "skills"),
				ContainerPath: filepath.Join(stellaHomeContainer, ".agents", "skills"),
				ReadOnly:      true,
			},
		)
	}
	for _, hostPath := range extraHostPaths {
		mounts = append(mounts, dockerclient.Mount{
			HostPath:      hostPath,
			ContainerPath: hostPath,
			ReadOnly:      true,
		})
	}
	return mounts
}

// mergeEnv merges policy environment and per-call overrides into a map.
// NEVER inherits host environment — that is a host-process concept, not a container concept.
func mergeEnv(policyEnv, optsEnv map[string]string) map[string]string {
	out := make(map[string]string, len(policyEnv)+len(optsEnv))
	maps.Copy(out, policyEnv)
	maps.Copy(out, optsEnv)
	return out
}

// hostOnlyEnvKeys are variables that callers build from the stella process view
// and would mislead or break tools inside the container. They are dropped
// before exec so the image's baked values (ENV PATH, HOME, …) take effect.
//   - PATH: callers prepend stella-managed tool dirs (fd/rg/mise/tap shims) that
//     live on the stella host filesystem. Those paths don't exist in the
//     container, and passing them overrides the image's ENV PATH that points
//     at /home/stella/.local/share/mise/shims et al.
//   - HOME: the container's image-baked HOME (/home/stella) is the right value.
//     The stella host HOME would point at a dir that isn't mounted.
var hostOnlyEnvKeys = map[string]struct{}{
	"PATH": {},
	"HOME": {},
}

// translateEnvPaths rewrites env vars whose values are absolute host paths.
// Values that are mounted are translated to their in-container equivalents.
// Values that are absolute but not mounted are dropped — the path doesn't
// exist in the container and would mislead tools that read it.
// Keys in hostOnlyEnvKeys are dropped wholesale.
// Non-path values (TERM, LANG, …) pass through unchanged.
func translateEnvPaths(env map[string]string, mountTable []dockerclient.Mount) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if _, drop := hostOnlyEnvKeys[k]; drop {
			continue
		}
		if !filepath.IsAbs(v) {
			out[k] = v
			continue
		}
		container, err := toContainerPath(mountTable, v)
		if err != nil {
			// Absolute host path not in any mount — drop rather than pass a stale path.
			continue
		}
		out[k] = container
	}
	return out
}

// containerDefaultPATH is the image-baked PATH from the Dockerfile ENV directive.
// It is used as the base when building a container exec PATH that prepends
// container-native user tool cache paths. Keep in sync with the ENV PATH line
// in plugins/sandbox/docker/Dockerfile.
const containerDefaultPATH = "/home/stella/.local/bin:/home/stella/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// injectToolPaths prepends container-native user tool directories to PATH.
// Built-in tools come from the image-baked PATH; host $STELLA_HOME/bin is never
// used for docker executable resolution because it may contain host-platform
// binaries.
func injectToolPaths(env map[string]string, toolBinPaths []string) map[string]string {
	if len(toolBinPaths) == 0 {
		return env
	}
	base := env["PATH"]
	if base == "" {
		base = containerDefaultPATH
	}
	entries := append([]string(nil), toolBinPaths...)
	entries = append(entries, base)
	env["PATH"] = strings.Join(entries, ":")
	return env
}

// dockerSession is a docker-backed sandbox session backed by a single container.
type dockerSession struct {
	id           string
	policy       sandboxpkg.Policy
	client       *dockerclient.Client
	containerID  string
	mountTable   []dockerclient.Mount
	toolBinPaths []string
	host         *dockerHost
	done         chan struct{}
	doneOnce     sync.Once
	closed       bool
	closeErr     error
	traceSpan    trace.Span
	traceOnce    sync.Once
	mu           sync.RWMutex
}

func (s *dockerSession) Policy() sandboxpkg.Policy { return s.policy }

func (s *dockerSession) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	return s.host.Exec(ctx, command, opts)
}

func (s *dockerSession) StartProcess(ctx context.Context, req sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	return s.host.StartProcess(ctx, req)
}
func (s *dockerSession) ResolvePath(path string) (string, error) { return s.host.ResolvePath(path) }
func (s *dockerSession) ResolveWritePath(path string) (string, error) {
	return s.host.ResolveWritePath(path)
}
func (s *dockerSession) WorkingDir() string { return s.host.WorkingDir() }

func (s *dockerSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed
}

func (s *dockerSession) Done() <-chan struct{} { return s.done }

func (s *dockerSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
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
// it marks the session closed and closes the done channel.
func (s *dockerSession) watchContainer() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		closed := s.closed
		s.mu.RUnlock()
		if closed {
			s.closeDone()
			return
		}
		alive, err := s.client.ContainerAlive(context.Background(), s.containerID)
		if err != nil || !alive {
			reason := "container_exited"
			if err != nil {
				reason = "container_liveness_error"
			}
			s.mu.Lock()
			if !s.closed {
				s.closed = true
			}
			s.mu.Unlock()
			s.endTrace(reason, err)
			logSessionClosed(s.id, "docker", reason)
			s.closeDone()
			return
		}
	}
}

// Close stops the container and marks the session closed.
// Uses a fresh background context with a 30s timeout so that cancellation of the
// caller's context does not leave the container running.
func (s *dockerSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return s.closeErr
	}

	s.closed = true

	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.closeErr = s.client.Stop(stopCtx, s.containerID)

	s.closeDone()
	s.endTrace("explicit_close", s.closeErr)
	logSessionClosed(s.id, "docker", "explicit_close")
	return s.closeErr
}

// toContainerPath maps a host absolute path to its equivalent in-container path
// by finding the deepest mount in the mount table that covers it.
// Returns an error if no mount covers the path (fail closed).
func toContainerPath(mounts []dockerclient.Mount, hostPath string) (string, error) {
	bestRel := ""
	bestMount := dockerclient.Mount{}
	found := false

	for _, m := range mounts {
		rel, err := filepath.Rel(m.HostPath, hostPath)
		if err != nil {
			continue
		}
		// Must not be a parent traversal.
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// Pick the deepest (longest host path) match.
		if !found || len(m.HostPath) > len(bestMount.HostPath) {
			bestRel = rel
			bestMount = m
			found = true
		}
	}

	if !found {
		return "", fmt.Errorf("docker: host path %q is not covered by any container mount", hostPath)
	}

	// Exact match on the mount root — return the container path directly.
	if bestRel == "." {
		return bestMount.ContainerPath, nil
	}

	// Normalize to Linux path separators (containers are always Linux).
	linuxRel := strings.ReplaceAll(bestRel, "\\", "/")
	return bestMount.ContainerPath + "/" + linuxRel, nil
}

// ─────────────────────────── dockerHost ──────────────────────────────

// dockerHost implements the process/path surface of Host.
// File I/O is done directly via os.* on resolved host paths
// (bind-mount makes host paths the source of truth).
// Exec and StartProcess translate host cwd → container cwd via toContainerPath.
type dockerHost struct {
	session *dockerSession
}

func (h *dockerHost) WorkingDir() string {
	return h.session.policy.Filesystem.WorkingDir
}

// ResolvePath turns a relative or absolute path into an absolute host path
// covered by the session's mount set. Paths outside every mount are rejected
// so absolute-path inputs cannot bypass the workspace / read-only policy
// boundary on filesystem operations. Symlinks anywhere in the path are
// rejected — nothing in the codebase creates symlinks in a session
// workspace, so any are agent-planted and following them would let an
// agent escape the mount via a file that passes the string-based check.
func (h *dockerHost) ResolvePath(path string) (string, error) {
	resolved := path
	if !filepath.IsAbs(resolved) {
		workingDir := h.session.policy.Filesystem.WorkingDir
		resolved = filepath.Join(workingDir, path)
	}
	if _, err := toContainerPath(h.session.mountTable, resolved); err != nil {
		return "", fmt.Errorf("docker host: path %q is outside the session mount set: %w", path, err)
	}
	if err := rejectSymlinkTraversal(h.session.mountTable, resolved); err != nil {
		return "", fmt.Errorf("docker host: %w", err)
	}
	return resolved, nil
}

// rejectSymlinkTraversal errors if any component of `path` at or below its
// matching mount root is a symlink. Ancestors above the mount root are
// host infrastructure (e.g. macOS `/tmp → /private/tmp`) and not
// agent-controllable, so they are not checked. Components at or below the
// mount root are agent-writable and any symlink there is either
// agent-planted or unexpected — both are rejected.
func rejectSymlinkTraversal(mounts []dockerclient.Mount, path string) error {
	root := deepestMountRoot(mounts, path)
	if root == "" {
		return fmt.Errorf("path %q has no matching mount", path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("rel %q from %q: %w", path, root, err)
	}
	if rel == "." {
		return nil
	}
	current := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("lstat %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q traverses symlink at %q", path, current)
		}
	}
	return nil
}

// deepestMountRoot returns the longest-prefix mount HostPath that contains
// `path`, or "" if no mount covers it. Mirrors the selection rule in
// toContainerPath so both agree on which mount owns a given path.
func deepestMountRoot(mounts []dockerclient.Mount, path string) string {
	best := ""
	for _, m := range mounts {
		rel, err := filepath.Rel(m.HostPath, path)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(m.HostPath) > len(best) {
			best = m.HostPath
		}
	}
	return best
}

// deepestMount returns the mount with the longest HostPath prefix containing
// path, or nil if no mount covers it.
func deepestMount(mounts []dockerclient.Mount, path string) *dockerclient.Mount {
	var best *dockerclient.Mount
	for i := range mounts {
		m := &mounts[i]
		rel, err := filepath.Rel(m.HostPath, path)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if best == nil || len(m.HostPath) > len(best.HostPath) {
			best = m
		}
	}
	return best
}

// ResolveWritePath is like ResolvePath but additionally rejects paths that
// fall within a read-only mount.
func (h *dockerHost) ResolveWritePath(path string) (string, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return "", err
	}
	if m := deepestMount(h.session.mountTable, resolved); m != nil && m.ReadOnly {
		return "", fmt.Errorf("docker host: path %q is in a read-only mount", path)
	}
	return resolved, nil
}

func (h *dockerHost) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	cwd := opts.Cwd
	if cwd == "" {
		cwd = h.session.policy.Filesystem.WorkingDir
	}

	hostCwd, err := h.ResolvePath(cwd)
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("docker host exec: resolve cwd: %w", err)
	}

	containerCwd, err := toContainerPath(h.session.mountTable, hostCwd)
	if err != nil {
		return sandboxpkg.ExecResult{}, fmt.Errorf("docker host exec: cwd not in any mount: %w", err)
	}

	env := injectToolPaths(translateEnvPaths(mergeEnv(h.session.policy.Env, opts.Env), h.session.mountTable), h.session.toolBinPaths)

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
		cwd = h.session.policy.Filesystem.WorkingDir
	}

	hostCwd, err := h.ResolvePath(cwd)
	if err != nil {
		return nil, fmt.Errorf("docker host start_process: resolve cwd: %w", err)
	}

	containerCwd, err := toContainerPath(h.session.mountTable, hostCwd)
	if err != nil {
		return nil, fmt.Errorf("docker host start_process: cwd not in any mount: %w", err)
	}

	env := injectToolPaths(translateEnvPaths(mergeEnv(h.session.policy.Env, req.Env), h.session.mountTable), h.session.toolBinPaths)

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

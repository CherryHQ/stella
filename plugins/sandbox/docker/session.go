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

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

// workspaceMount and readonlyMountRoot match the paths pre-created in the
// bundled Dockerfile (plugins/sandbox/docker/Dockerfile) under the anna
// user's HOME so mise activate, $PATH, and $HOME all line up.
const (
	workspaceMount    = "/home/anna/workspace"
	readonlyMountRoot = "/home/anna/readonly"
)

func nextSessionID() string { return sandboxpkg.NewSessionID() }

func logSessionCreated(sessionID, backend string, policy sandboxpkg.Policy) {
	sandboxpkg.LogSessionCreated(sessionID, backend, policy)
}

func logSessionClosed(sessionID, backend, reason string) {
	sandboxpkg.LogSessionClosed(sessionID, backend, reason)
}

// dockerFactory creates docker-backed sandbox sessions.
type dockerFactory struct {
	cfg Config
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

// Supported returns a PolicyCompatibilityError when:
//   - The docker daemon is unreachable.
//   - Network mode is whitelist (not supported in phase 1).
func (f *dockerFactory) Supported(policy sandboxpkg.Policy) error {
	if !f.Available() {
		return &sandboxpkg.PolicyCompatibilityError{
			Backend: f.Name(),
			Policy:  policy,
			Reason:  "docker daemon not reachable (check DOCKER_HOST and that the daemon is running)",
		}
	}

	if policy.RequiresWhitelist() {
		return &sandboxpkg.PolicyCompatibilityError{
			Backend: f.Name(),
			Policy:  policy,
			Reason:  "docker backend does not support whitelist mode",
		}
	}

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

	sessionID := nextSessionID()

	workspaceHost, err := filepath.Abs(policy.WorkspaceRootOrDefault())
	if err != nil {
		return nil, fmt.Errorf("docker session: abs workspace root: %w", err)
	}

	ctx, span := tracer.Start(ctx, "sandbox.docker.session",
		trace.WithAttributes(sessionTraceAttrs(sessionID, policy, f.cfg.Image, workspaceHost)...),
	)

	// Build read-only mounts from policy. HostPath is kept as anna-view here so
	// the internal mount table can translate cwd/env paths with toContainerPath;
	// the daemon-side bind source is computed separately below.
	roMounts := make([]dockerclient.Mount, 0, len(policy.Filesystem.ReadOnlyPaths))
	for i, p := range policy.Filesystem.ReadOnlyPaths {
		absP, err := filepath.Abs(p)
		if err != nil {
			recordError(span, err)
			span.End()
			return nil, fmt.Errorf("docker session: abs read-only path %q: %w", p, err)
		}
		roMounts = append(roMounts, dockerclient.Mount{
			HostPath:      absP,
			ContainerPath: readonlyMountRoot + "/" + strconv.Itoa(i),
			ReadOnly:      true,
		})
	}

	// Map network mode.
	networkMode := mapNetworkMode(policy)

	// Get the shared client.
	client, err := getSharedClient()
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: client: %w", err)
	}

	// Label with the daemon-view ANNA_HOME so orphan cleanup scopes to this
	// host installation. Under DooD two anna instances may share an in-container
	// ANNA_HOME path while living in different host directories; labeling with
	// the daemon-view path keeps their container sets disjoint.
	annaHome := f.cfg.TranslateToDaemonPath(policy.Process.Environment["ANNA_HOME"])

	// Translate anna-view paths to daemon-view paths for bind-mount sources.
	// Only the CreateOptions struct receives translated paths; the internal
	// mountTable keeps anna-view paths so toContainerPath continues to map
	// cwd/env correctly.
	daemonRoMounts := translateMountsForDaemon(f.cfg, roMounts)

	opts := dockerclient.CreateOptions{
		Image:          f.cfg.Image,
		WorkspaceHost:  f.cfg.TranslateToDaemonPath(workspaceHost),
		WorkspaceMount: workspaceMount,
		ReadOnlyMounts: daemonRoMounts,
		NetworkMode:    networkMode,
		Env:            mergeEnv(policy.Process.Environment, nil),
		Labels: map[string]string{
			dockerclient.LabelSessionID: sessionID,
			dockerclient.LabelAnnaHome:  annaHome,
			dockerclient.LabelCreatedAt: time.Now().UTC().Format(time.RFC3339),
			dockerclient.LabelOwnerPID:  strconv.Itoa(os.Getpid()),
		},
		Name: "anna-sandbox-" + sessionID,
	}

	containerID, err := client.CreateAndStart(ctx, opts)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: create and start: %w", err)
	}

	span.AddEvent("sandbox.docker.session.ready", trace.WithAttributes(
		attribute.String("anna.sandbox.container_id", containerID),
	))

	// Build mount table: workspace + read-only mounts.
	mountTable := buildMountTable(workspaceHost, workspaceMount, roMounts)

	session := &dockerSession{
		id:          sessionID,
		policy:      policy,
		client:      client,
		containerID: containerID,
		mountTable:  mountTable,
		done:        make(chan struct{}),
		traceSpan:   span,
	}
	session.host = &dockerHost{session: session}

	logSessionCreated(sessionID, "docker", policy)
	go session.watchContainer()

	return session, nil
}

// translateMountsForDaemon rewrites Mount.HostPath values via the prefix
// mapping configured on cfg. The input slice is not mutated.
func translateMountsForDaemon(cfg Config, mounts []dockerclient.Mount) []dockerclient.Mount {
	if cfg.ContainerPathPrefix == "" || cfg.HostPathPrefix == "" {
		return mounts
	}
	out := make([]dockerclient.Mount, len(mounts))
	for i, m := range mounts {
		m.HostPath = cfg.TranslateToDaemonPath(m.HostPath)
		out[i] = m
	}
	return out
}

// mapNetworkMode translates sandbox policy network mode to the dockerclient type.
// Whitelist is already rejected by Supported() before reaching here.
func mapNetworkMode(policy sandboxpkg.Policy) dockerclient.NetworkMode {
	switch policy.NetworkModeOrDefault() {
	case sandboxpkg.NetworkDisabled:
		return dockerclient.NetworkDisabled
	default:
		return dockerclient.NetworkAllowAll
	}
}

// buildMountTable returns all bind mounts that toContainerPath should consult.
func buildMountTable(workspaceHost, workspaceMount string, roMounts []dockerclient.Mount) []dockerclient.Mount {
	table := make([]dockerclient.Mount, 0, 1+len(roMounts))
	table = append(table, dockerclient.Mount{
		HostPath:      workspaceHost,
		ContainerPath: workspaceMount,
		ReadOnly:      false,
	})
	table = append(table, roMounts...)
	return table
}

// mergeEnv merges policy environment and per-call overrides into a map.
// NEVER inherits host environment — that is a host-process concept, not a container concept.
func mergeEnv(policyEnv, optsEnv map[string]string) map[string]string {
	out := make(map[string]string, len(policyEnv)+len(optsEnv))
	maps.Copy(out, policyEnv)
	maps.Copy(out, optsEnv)
	return out
}

// hostOnlyEnvKeys are variables that callers build from the anna process view
// and would mislead or break tools inside the container. They are dropped
// before exec so the image's baked values (ENV PATH, HOME, …) take effect.
//   - PATH: callers prepend anna-managed tool dirs (fd/rg/mise/tap shims) that
//     live on the anna host filesystem. Those paths don't exist in the
//     container, and passing them overrides the image's ENV PATH that points
//     at /home/anna/.local/share/mise/shims et al.
//   - HOME: the container's image-baked HOME (/home/anna) is the right value.
//     The anna host HOME would point at a dir that isn't mounted.
var hostOnlyEnvKeys = map[string]struct{}{
	"PATH": {},
	"HOME": {},
}

// translateEnvPaths rewrites env vars whose values are absolute host paths.
// Values that are mounted are translated to their in-container equivalents.
// Values that are absolute but not mounted (e.g. ANNA_HOME) are dropped —
// the path doesn't exist in the container and would mislead tools that read it.
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

// dockerSession is a docker-backed sandbox session backed by a single container.
type dockerSession struct {
	id          string
	policy      sandboxpkg.Policy
	client      *dockerclient.Client
	containerID string
	mountTable  []dockerclient.Mount
	host        *dockerHost
	done        chan struct{}
	doneOnce    sync.Once
	closed      bool
	closeErr    error
	traceSpan   trace.Span
	traceOnce   sync.Once
	mu          sync.RWMutex
}

func (s *dockerSession) Policy() sandboxpkg.Policy { return s.policy }

// Host file/process ops — delegate to the internal dockerHost.
func (s *dockerSession) ReadFile(ctx context.Context, path string, offset, limit int) (sandboxpkg.ReadResult, error) {
	return s.host.ReadFile(ctx, path, offset, limit)
}

func (s *dockerSession) WriteFile(ctx context.Context, path string, content []byte) (sandboxpkg.WriteResult, error) {
	return s.host.WriteFile(ctx, path, content)
}

func (s *dockerSession) EditFile(ctx context.Context, path string, edits []sandboxpkg.Edit) (sandboxpkg.EditResult, error) {
	return s.host.EditFile(ctx, path, edits)
}

func (s *dockerSession) Stat(ctx context.Context, path string) (sandboxpkg.StatResult, error) {
	return s.host.Stat(ctx, path)
}

func (s *dockerSession) ListDir(ctx context.Context, path string) ([]sandboxpkg.DirEntry, error) {
	return s.host.ListDir(ctx, path)
}

func (s *dockerSession) MkdirAll(ctx context.Context, path string, perm uint32) error {
	return s.host.MkdirAll(ctx, path, perm)
}

func (s *dockerSession) Remove(ctx context.Context, path string, recursive bool) error {
	return s.host.Remove(ctx, path, recursive)
}

func (s *dockerSession) Rename(ctx context.Context, oldPath, newPath string) error {
	return s.host.Rename(ctx, oldPath, newPath)
}

func (s *dockerSession) CreateTemp(ctx context.Context, dir, pattern string) (sandboxpkg.TempFile, error) {
	return s.host.CreateTemp(ctx, dir, pattern)
}

func (s *dockerSession) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	return s.host.Exec(ctx, command, opts)
}

func (s *dockerSession) StartProcess(ctx context.Context, req sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	return s.host.StartProcess(ctx, req)
}
func (s *dockerSession) ResolvePath(path string) (string, error) { return s.host.ResolvePath(path) }
func (s *dockerSession) WorkingDir() string                      { return s.host.WorkingDir() }

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
			attribute.String("anna.sandbox.close_reason", reason),
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

// dockerHost implements Host.  Filesystem ops operate on host paths
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

func (h *dockerHost) ReadFile(_ context.Context, path string, offset, limit int) (sandboxpkg.ReadResult, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return sandboxpkg.ReadResult{}, err
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return sandboxpkg.ReadResult{}, err
	}

	if offset > 0 {
		if offset >= len(content) {
			return sandboxpkg.ReadResult{Content: nil, NextOffset: offset}, nil
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

	return sandboxpkg.ReadResult{
		Content:    content,
		Truncated:  truncated,
		NextOffset: nextOffset,
	}, nil
}

func (h *dockerHost) WriteFile(_ context.Context, path string, content []byte) (sandboxpkg.WriteResult, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return sandboxpkg.WriteResult{}, err
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return sandboxpkg.WriteResult{}, err
	}

	if err := os.WriteFile(resolved, content, 0o644); err != nil {
		return sandboxpkg.WriteResult{}, err
	}

	return sandboxpkg.WriteResult{BytesWritten: len(content)}, nil
}

func (h *dockerHost) EditFile(ctx context.Context, path string, edits []sandboxpkg.Edit) (sandboxpkg.EditResult, error) {
	readResult, err := h.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return sandboxpkg.EditResult{}, err
	}

	content := string(readResult.Content)
	applied := 0

	for _, edit := range edits {
		if strings.Contains(content, edit.OldText) {
			content = strings.Replace(content, edit.OldText, edit.NewText, 1)
			applied++
		}
	}

	_, err = h.WriteFile(ctx, path, []byte(content))
	if err != nil {
		return sandboxpkg.EditResult{}, err
	}

	return sandboxpkg.EditResult{AppliedEdits: applied}, nil
}

func (h *dockerHost) Stat(_ context.Context, path string) (sandboxpkg.StatResult, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return sandboxpkg.StatResult{}, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return sandboxpkg.StatResult{Exists: false}, nil
		}
		return sandboxpkg.StatResult{}, err
	}

	return sandboxpkg.StatResult{
		Exists:  true,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    uint32(info.Mode()),
		ModTime: info.ModTime(),
	}, nil
}

func (h *dockerHost) ListDir(_ context.Context, path string) ([]sandboxpkg.DirEntry, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}

	result := make([]sandboxpkg.DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, sandboxpkg.DirEntry{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}

	return result, nil
}

func (h *dockerHost) MkdirAll(_ context.Context, path string, perm uint32) error {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(resolved, os.FileMode(perm))
}

func (h *dockerHost) Remove(_ context.Context, path string, recursive bool) error {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}
	if recursive {
		return os.RemoveAll(resolved)
	}
	return os.Remove(resolved)
}

func (h *dockerHost) Rename(_ context.Context, oldPath, newPath string) error {
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

func (h *dockerHost) CreateTemp(_ context.Context, dir, pattern string) (sandboxpkg.TempFile, error) {
	resolvedDir, err := h.ResolvePath(dir)
	if err != nil {
		return nil, err
	}

	f, err := os.CreateTemp(resolvedDir, pattern)
	if err != nil {
		return nil, err
	}

	return &dockerTempFile{file: f}, nil
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

	env := translateEnvPaths(mergeEnv(h.session.policy.Process.Environment, opts.Env), h.session.mountTable)

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = h.session.policy.Process.Timeout
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

	env := translateEnvPaths(mergeEnv(h.session.policy.Process.Environment, req.Env), h.session.mountTable)

	timeout := req.Timeout
	if timeout == 0 {
		timeout = h.session.policy.Process.Timeout
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

// ─────────────────────────── dockerTempFile ──────────────────────────

type dockerTempFile struct {
	file *os.File
}

func (f *dockerTempFile) Path() string { return f.file.Name() }
func (f *dockerTempFile) Write(p []byte) (int, error) {
	return f.file.Write(p)
}

func (f *dockerTempFile) Close() error {
	path := f.file.Name()
	if err := f.file.Close(); err != nil {
		return err
	}
	return os.Remove(path)
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

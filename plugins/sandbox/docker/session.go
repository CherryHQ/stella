package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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
	cleanupOrphansOnce sync.Once
}

// NewFactory returns a Factory backed by a Docker container-per-session strategy.
//
// When cfg.StellaHome is non-empty, construction performs I/O:
//   - Runtime mode resolution: reads $STELLA_DOCKER_SANDBOX_MODE and the
//     matching mode-specific env ($STELLA_HOME_HOST or $STELLA_HOME_VOLUME).
//     No container runtime auto-detection is used.
//   - User tool resolution: loads the builtin and user plugin manifests
//     ($STELLA_HOME/plugins.yaml) to populate UserToolBinaries.
//
// Both steps are skipped when StellaHome is empty (e.g. unit tests), making
// construction cheap and infallible in that case.
func NewFactory(cfg Config) (sandboxpkg.Factory, error) {
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
			tools, err := resolveUserToolBinaries(cfg.StellaHome)
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
		scope := f.cfg.cleanupScope(f.cfg.StellaHome)
		if scope == "" {
			return
		}
		client, err := getSharedClient()
		if err != nil {
			return
		}
		dockerclient.CleanupOrphanedContainers(ctx, client, scope)
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

	// Shared user-data root (mounted as /user). Empty for a user-less job, which
	// has no principal home — then no /user mount and STELLA_USER_DIR stays unset.
	userDataHost := ""
	if ud := policy.Filesystem.UserDataDir; ud != "" {
		if abs, absErr := filepath.Abs(ud); absErr == nil {
			userDataHost = abs
		}
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

	stellaHome := policy.Env["STELLA_HOME"]
	cleanupScope := f.cfg.cleanupScope(stellaHome)
	opts := dockerclient.CreateOptions{
		Image:          f.cfg.Image,
		WorkspaceHost:  workspaceHost,
		WorkspaceMount: workspaceMount,
		NetworkMode:    networkMode,
		Network:        f.cfg.SandboxNetwork,
		Env:            mergeEnv(policy.Env, nil),
		Labels: map[string]string{
			dockerclient.LabelSessionID:  sessionID,
			dockerclient.LabelStellaHome: cleanupScope,
			dockerclient.LabelCreatedAt:  time.Now().UTC().Format(time.RFC3339),
			dockerclient.LabelOwnerPID:   strconv.Itoa(os.Getpid()),
		},
		Name: "stella-sandbox-" + sessionID,
	}

	mountedExtraReadOnly, mountedTempDirHost, mountedUserDataHost, err := f.configureSessionMounts(&opts, policy, workspaceHost, userDataHost)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, err
	}

	// Per-user mise tree(s): mounted writable at the /opt/stella-remapped path so
	// the agent can persist its own `mise install`s, mirroring bwrap. The image
	// supplies the shared read-only base at /opt/stella/.mise-tools; these overlay
	// the writable per-user layer on top.
	perUserTrees := f.mountPerUserToolTrees(&opts, policy)

	// Expose the user-data root as STELLA_USER_DIR — but only when /user was
	// actually mounted, keyed off the host path the mount really used. The value
	// is the HOST path; translateEnvPaths rewrites it to /user via the /user
	// mount (Pi C2). Setting it (or the mount table entry) when the mount failed
	// would make env translation and host-side path resolution lie about /user.
	if mountedUserDataHost != "" {
		env := maps.Clone(policy.Env)
		if env == nil {
			env = make(map[string]string)
		}
		env["STELLA_USER_DIR"] = mountedUserDataHost
		policy.Env = env
	}

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
		WorkspaceHost:       workspaceHost,
		WorkspaceMount:      workspaceMount,
		UserDataHost:        mountedUserDataHost,
		UserDataMount:       userDataMount,
		StellaHomeHost:      policy.Env["STELLA_HOME"],
		StellaHomeContainer: stellaHomeMount,
		ExtraReadOnlyMounts: mountedExtraReadOnly,
		TempDirHost:         mountedTempDirHost,
	})

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
	opts.Env = translateEnvPaths(mergeEnv(policy.Env, nil), mountTable, envMaps)

	// Per-user mise shims go on PATH ahead of the image's system shims so a user's
	// own tool versions win (mirrors HostEnvBuildPath on the host backends). Only
	// the mise tree gets a shims/ entry: ExtraWritableMounts is a generic policy
	// field, so guard against a future non-mise writable mount contributing a
	// bogus PATH dir.
	var toolBinPaths []string
	for _, tree := range perUserTrees {
		if filepath.Base(tree.Container) == ".mise-tools" {
			toolBinPaths = append(toolBinPaths, filepath.Join(tree.Container, "shims"))
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
		client:       client,
		containerID:  containerID,
		mountTable:   mountTable,
		envPathMaps:  envMaps,
		toolBinPaths: toolBinPaths,
		done:         make(chan struct{}),
		traceSpan:    span,
	}
	session.host = &dockerHost{session: session}

	logSessionCreated(sessionID, "docker", policy)
	go session.watchContainer()

	return session, nil
}

// configureSessionMounts wires the container mounts for the active mode and
// returns the extra read-only mounts, the mounted temp-dir host, and the mounted
// user-data host ("" when /user could not be mounted, so callers don't wire a
// /user that isn't really there).
func (f *dockerFactory) configureSessionMounts(opts *dockerclient.CreateOptions, policy sandboxpkg.Policy, workspaceHost, userDataHost string) ([]string, string, string, error) {
	if f.cfg.RuntimeMode == DockerSandboxModeVolume {
		mountedExtraReadOnly, mountedUserDataHost, err := f.configureVolumeMounts(opts, policy, workspaceHost, userDataHost)
		return mountedExtraReadOnly, "", mountedUserDataHost, err
	}
	return f.configureBindMounts(opts, policy, workspaceHost, userDataHost)
}

func (f *dockerFactory) configureVolumeMounts(opts *dockerclient.CreateOptions, policy sandboxpkg.Policy, workspaceHost, userDataHost string) ([]string, string, error) {
	workspaceSubpath, ok := relativePathWithin(f.cfg.StellaHome, workspaceHost)
	if !ok {
		return nil, "", fmt.Errorf("docker session: workspace %q is not inside STELLA_HOME %q; cannot use volume mode", workspaceHost, f.cfg.StellaHome)
	}
	if workspaceSubpath == "." {
		return nil, "", fmt.Errorf("docker session: workspace must be a subdirectory of STELLA_HOME, not STELLA_HOME itself")
	}
	opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
		HostPath:      f.cfg.StellaHomeVolume,
		ContainerPath: workspaceMount,
		ReadOnly:      false,
		Type:          dockerclient.MountTypeVolume,
		VolumeSubpath: filepath.ToSlash(workspaceSubpath),
	})
	// Shared user-data root → /user (RW). Lives under STELLA_HOME, so it comes
	// from the same named volume at its subpath.
	mountedUserDataHost := ""
	if userDataHost != "" {
		if subpath, ok := relativePathWithin(f.cfg.StellaHome, userDataHost); ok && subpath != "." {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      f.cfg.StellaHomeVolume,
				ContainerPath: userDataMount,
				ReadOnly:      false,
				Type:          dockerclient.MountTypeVolume,
				VolumeSubpath: filepath.ToSlash(subpath),
			})
			mountedUserDataHost = userDataHost
		} else {
			logSkippedSandboxMount(DockerSandboxModeVolume, userDataHost, "user-data root is outside STELLA_HOME and cannot be mounted from the named volume")
		}
	}
	for _, name := range dockerHostStellaDirs() {
		hostPath := filepath.Join(f.cfg.StellaHome, name)
		if _, err := os.Stat(hostPath); err != nil {
			continue
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath:      f.cfg.StellaHomeVolume,
			ContainerPath: filepath.Join(stellaHomeMount, name),
			ReadOnly:      true,
			Type:          dockerclient.MountTypeVolume,
			VolumeSubpath: filepath.ToSlash(name),
		})
	}
	// Agent-bound (system_agent) skills → /opt/stella/agent-skills (RO). Lives
	// under STELLA_HOME, so it comes from the same named volume at its subpath.
	// Skipped when not installed for this agent (the dir is absent).
	if as := policy.Filesystem.AgentSkillsDir; as != "" && dirExists(as) {
		if subpath, ok := relativePathWithin(f.cfg.StellaHome, as); ok && subpath != "." {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      f.cfg.StellaHomeVolume,
				ContainerPath: sandboxpkg.MountAgentSkills,
				ReadOnly:      true,
				Type:          dockerclient.MountTypeVolume,
				VolumeSubpath: filepath.ToSlash(subpath),
			})
		} else {
			logSkippedSandboxMount(DockerSandboxModeVolume, as, "agent skills dir is outside STELLA_HOME and cannot be mounted from the named volume")
		}
	}
	// DB-installed system skills → /opt/stella/db-skills (RO). Also under
	// STELLA_HOME, so it comes from the same named volume at its subpath.
	if sd := policy.Filesystem.SystemDBSkillsDir; sd != "" && dirExists(sd) {
		if subpath, ok := relativePathWithin(f.cfg.StellaHome, sd); ok && subpath != "." {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      f.cfg.StellaHomeVolume,
				ContainerPath: sandboxpkg.MountSystemDBSkills,
				ReadOnly:      true,
				Type:          dockerclient.MountTypeVolume,
				VolumeSubpath: filepath.ToSlash(subpath),
			})
		} else {
			logSkippedSandboxMount(DockerSandboxModeVolume, sd, "system DB skills dir is outside STELLA_HOME and cannot be mounted from the named volume")
		}
	}
	mountedExtraReadOnly := []string{}
	for _, hostPath := range policy.Filesystem.ExtraReadOnlyMounts {
		subpath, ok := relativePathWithin(f.cfg.StellaHome, hostPath)
		if !ok {
			logSkippedSandboxMount(DockerSandboxModeVolume, hostPath, "path is outside STELLA_HOME and cannot be mounted from the named volume")
			continue
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath:      f.cfg.StellaHomeVolume,
			ContainerPath: hostPath,
			ReadOnly:      true,
			Type:          dockerclient.MountTypeVolume,
			VolumeSubpath: filepath.ToSlash(subpath),
		})
		mountedExtraReadOnly = append(mountedExtraReadOnly, hostPath)
		appendWorkspaceRelativeReadOnlyMount(opts, f.cfg.StellaHomeVolume, hostPath, workspaceHost, subpath, dockerclient.MountTypeVolume)
	}
	// Workspace is handled by the volume mount above; clear WorkspaceHost so the
	// docker client skips the default bind mount. TempDirHost lives outside
	// STELLA_HOME and is intentionally unavailable in volume mode.
	opts.WorkspaceHost = ""
	return mountedExtraReadOnly, mountedUserDataHost, nil
}

func (f *dockerFactory) configureBindMounts(opts *dockerclient.CreateOptions, policy sandboxpkg.Policy, workspaceHost, userDataHost string) ([]string, string, string, error) {
	daemonWorkspaceHost, ok := f.cfg.daemonPath(workspaceHost)
	if !ok {
		return nil, "", "", fmt.Errorf("docker session: workspace %q is not under STELLA_HOME %q; cannot use bind-mount mode", workspaceHost, f.cfg.StellaHome)
	}
	opts.WorkspaceHost = daemonWorkspaceHost
	// Shared user-data root → /user (RW).
	mountedUserDataHost := ""
	if userDataHost != "" {
		if daemonPath, ok := f.cfg.daemonPath(userDataHost); ok {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      daemonPath,
				ContainerPath: userDataMount,
			})
			mountedUserDataHost = userDataHost
		} else {
			logSkippedSandboxMount(f.cfg.RuntimeMode, userDataHost, "user-data root is not visible to the Docker daemon")
		}
	}
	// Agent-bound (system_agent) skills → /opt/stella/agent-skills (RO). Skipped
	// when not installed for this agent (the dir is absent).
	if as := policy.Filesystem.AgentSkillsDir; as != "" && dirExists(as) {
		if daemonPath, ok := f.cfg.daemonPath(as); ok {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      daemonPath,
				ContainerPath: sandboxpkg.MountAgentSkills,
				ReadOnly:      true,
			})
		} else {
			logSkippedSandboxMount(f.cfg.RuntimeMode, as, "agent skills dir is not visible to the Docker daemon")
		}
	}
	// DB-installed system skills → /opt/stella/db-skills (RO).
	if sd := policy.Filesystem.SystemDBSkillsDir; sd != "" && dirExists(sd) {
		if daemonPath, ok := f.cfg.daemonPath(sd); ok {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      daemonPath,
				ContainerPath: sandboxpkg.MountSystemDBSkills,
				ReadOnly:      true,
			})
		} else {
			logSkippedSandboxMount(f.cfg.RuntimeMode, sd, "system DB skills dir is not visible to the Docker daemon")
		}
	}
	stellaHome := f.cfg.StellaHome
	if stellaHome != "" {
		for _, name := range dockerHostStellaDirs() {
			hostPath := filepath.Join(stellaHome, name)
			if _, err := os.Stat(hostPath); err != nil {
				continue
			}
			daemonPath, ok := f.cfg.daemonPath(hostPath)
			if !ok {
				continue
			}
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      daemonPath,
				ContainerPath: filepath.Join(stellaHomeMount, name),
				ReadOnly:      true,
			})
		}
	}
	mountedTempDirHost := ""
	if policy.Filesystem.TempDirHost != "" {
		if daemonPath, ok := f.cfg.daemonPath(policy.Filesystem.TempDirHost); ok {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      daemonPath,
				ContainerPath: "/tmp",
			})
			mountedTempDirHost = policy.Filesystem.TempDirHost
		} else {
			logSkippedSandboxMount(f.cfg.RuntimeMode, policy.Filesystem.TempDirHost, "path is not visible to the Docker daemon")
		}
	}
	mountedExtraReadOnly := []string{}
	for _, hostPath := range policy.Filesystem.ExtraReadOnlyMounts {
		daemonPath, ok := f.cfg.daemonPath(hostPath)
		if !ok {
			logSkippedSandboxMount(f.cfg.RuntimeMode, hostPath, "path is not visible to the Docker daemon")
			continue
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath:      daemonPath,
			ContainerPath: hostPath,
			ReadOnly:      true,
		})
		mountedExtraReadOnly = append(mountedExtraReadOnly, hostPath)
		appendWorkspaceRelativeReadOnlyMount(opts, daemonPath, hostPath, workspaceHost, "", dockerclient.MountTypeBind)
	}
	return mountedExtraReadOnly, mountedTempDirHost, mountedUserDataHost, nil
}

func logSkippedSandboxMount(mode DockerSandboxMode, path, reason string) {
	slog.Warn("docker backend: skipping sandbox mount",
		"component", "runner_sandbox",
		"mode", mode,
		"path", path,
		"reason", reason,
	)
}

// dirExists reports whether path exists on the host (file or directory). Used to
// skip optional mounts whose source is absent for this session.
func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func relativePathWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func appendWorkspaceRelativeReadOnlyMount(opts *dockerclient.CreateOptions, source, hostPath, workspaceHost, volumeSubpath string, mountType dockerclient.MountType) {
	rel, ok := relativePathWithin(workspaceHost, hostPath)
	if !ok || rel == "." {
		return
	}
	opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
		HostPath:      source,
		ContainerPath: filepath.Join(workspaceMount, rel),
		ReadOnly:      true,
		Type:          mountType,
		VolumeSubpath: filepath.ToSlash(volumeSubpath),
	})
}

// dockerImageProvidedStellaDirs are STELLA_HOME subdirs the sandbox image bakes
// itself, built for the container's linux platform: the mise binary (bin) and the
// shared system mise tree (.mise-tools). They must NOT be mounted from the host —
// whose binaries may be a different platform — so the /opt/stella image versions
// win and the per-user relative symlinks resolve against a runnable system tree.
var dockerImageProvidedStellaDirs = map[string]struct{}{
	"bin":         {},
	".mise-tools": {},
}

// dockerHostStellaDirs returns the STELLA_HOME subdirs docker mounts from the
// host — the shared set minus the image-provided ones.
func dockerHostStellaDirs() []string {
	all := sandboxpkg.StellaHomeSandboxDirs()
	dirs := make([]string, 0, len(all))
	for _, name := range all {
		if _, skip := dockerImageProvidedStellaDirs[name]; skip {
			continue
		}
		dirs = append(dirs, name)
	}
	return dirs
}

// writableMount is a per-user writable tree mounted into the container, recording
// both ends so the caller can register it in the mount table and derive PATH.
type writableMount struct {
	Host      string
	Container string
}

// mountPerUserToolTrees mounts each policy writable mount (the per-user mise
// tree) into the container at its /opt/stella-remapped path, RW, and returns the
// mounted pairs. Mode-aware: a volume subpath in volume mode, a daemon bind
// otherwise. Mounts outside STELLA_HOME are skipped (the remap can't place them).
func (f *dockerFactory) mountPerUserToolTrees(opts *dockerclient.CreateOptions, policy sandboxpkg.Policy) []writableMount {
	var mounted []writableMount
	for _, hostPath := range policy.Filesystem.ExtraWritableMounts {
		sub, ok := relativePathWithin(f.cfg.StellaHome, hostPath)
		if !ok || sub == "." {
			logSkippedSandboxMount(f.cfg.RuntimeMode, hostPath, "writable mount is outside STELLA_HOME and cannot be remapped")
			continue
		}
		containerPath := filepath.Join(stellaHomeMount, sub)
		if f.cfg.RuntimeMode == DockerSandboxModeVolume {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      f.cfg.StellaHomeVolume,
				ContainerPath: containerPath,
				Type:          dockerclient.MountTypeVolume,
				VolumeSubpath: filepath.ToSlash(sub),
			})
		} else {
			daemonPath, ok := f.cfg.daemonPath(hostPath)
			if !ok {
				logSkippedSandboxMount(f.cfg.RuntimeMode, hostPath, "writable mount is not visible to the Docker daemon")
				continue
			}
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      daemonPath,
				ContainerPath: containerPath,
			})
		}
		mounted = append(mounted, writableMount{Host: hostPath, Container: containerPath})
	}
	return mounted
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

type mountTableOptions struct {
	WorkspaceHost       string
	WorkspaceMount      string
	UserDataHost        string
	UserDataMount       string
	StellaHomeHost      string
	StellaHomeContainer string
	ExtraReadOnlyMounts []string
	TempDirHost         string
}

// buildMountTable returns the process-view bind mount set that path resolution
// should consult. Host paths here are intentionally not daemon-translated: file
// tools run in the Stella process namespace, not the Docker daemon namespace.
func buildMountTable(opts mountTableOptions) []dockerclient.Mount {
	mounts := []dockerclient.Mount{
		{
			HostPath:      opts.WorkspaceHost,
			ContainerPath: opts.WorkspaceMount,
			ReadOnly:      false,
		},
	}
	if opts.UserDataHost != "" && opts.UserDataMount != "" {
		mounts = append(mounts, dockerclient.Mount{
			HostPath:      opts.UserDataHost,
			ContainerPath: opts.UserDataMount,
			ReadOnly:      false,
		})
	}
	if opts.StellaHomeHost != "" && opts.StellaHomeContainer != "" {
		for _, name := range dockerHostStellaDirs() {
			hostPath := filepath.Join(opts.StellaHomeHost, name)
			if _, err := os.Stat(hostPath); err != nil {
				continue
			}
			mounts = append(mounts, dockerclient.Mount{
				HostPath:      hostPath,
				ContainerPath: filepath.Join(opts.StellaHomeContainer, name),
				ReadOnly:      true,
			})
		}
	}
	for _, hostPath := range opts.ExtraReadOnlyMounts {
		mounts = append(mounts, dockerclient.Mount{
			HostPath:      hostPath,
			ContainerPath: hostPath,
			ReadOnly:      true,
		})
	}
	if opts.TempDirHost != "" {
		mounts = append(mounts, dockerclient.Mount{
			HostPath:      opts.TempDirHost,
			ContainerPath: "/tmp",
		})
	}
	return mounts
}

// mergeEnv merges policy environment and per-call overrides into a map.
// NEVER inherits host environment — that is a host-process concept, not a container concept.
// withServerURL returns env with STELLA_SERVER_URL set to url, cloning so the
// caller's map is untouched. A blank url returns env unchanged (local/host
// backends keep the 127.0.0.1 default).
func withServerURL(env map[string]string, url string) map[string]string {
	if url == "" {
		return env
	}
	out := maps.Clone(env)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out["STELLA_SERVER_URL"] = url
	return out
}

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
//     at /opt/stella/.mise-tools/shims et al.
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
//
// envMaps provides extra prefix translations for paths that need env rewriting
// but must not appear in the mount table (e.g. STELLA_HOME — agents must not
// get file access to the entire directory).
func translateEnvPaths(env map[string]string, mountTable []dockerclient.Mount, envMaps []envPathMap) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if _, drop := hostOnlyEnvKeys[k]; drop {
			continue
		}
		// MISE_TRUSTED_CONFIG_PATHS is a path-list, not a scalar path; translate
		// each element and keep the ones that map (a host path with no container
		// view, like a backend-irrelevant entry, is dropped). Mirrors the local
		// backend, which splits the same var (see plugins/sandbox/local/session.go).
		if k == "MISE_TRUSTED_CONFIG_PATHS" {
			seen := map[string]struct{}{}
			var parts []string
			for p := range strings.SplitSeq(v, string(filepath.ListSeparator)) {
				tp, ok := translateEnvPath(p, mountTable, envMaps)
				if !ok {
					continue
				}
				// Translation can collapse distinct host paths onto one container
				// path (e.g. the host workspace and the literal /workspace), so
				// dedupe to keep the trusted list clean and order-stable.
				if _, dup := seen[tp]; dup {
					continue
				}
				seen[tp] = struct{}{}
				parts = append(parts, tp)
			}
			if len(parts) > 0 {
				// Join with the container's separator, not the host's: the value
				// is consumed by mise inside an always-Linux container, so it must
				// use ":" even when stella runs on a Windows host (where
				// filepath.ListSeparator is ";"). Splitting above uses the host
				// separator because the incoming value was joined host-side.
				out[k] = strings.Join(parts, ":")
			}
			continue
		}
		if tp, ok := translateEnvPath(v, mountTable, envMaps); ok {
			out[k] = tp
		}
	}
	return out
}

// translateEnvPath rewrites a single absolute host path to its container view via
// the mount table, then the env prefix maps. A non-absolute value passes through
// unchanged. A value that is already a container path (e.g. the literal
// "/workspace" mise trusts) passes through too. An absolute host path with no
// container view is reported as not ok so the caller drops it.
func translateEnvPath(v string, mountTable []dockerclient.Mount, envMaps []envPathMap) (string, bool) {
	if !filepath.IsAbs(v) {
		return v, true
	}
	if container, err := toContainerPath(mountTable, v); err == nil {
		return container, true
	}
	if mapped, ok := applyEnvPathMaps(envMaps, v); ok {
		return mapped, true
	}
	if isContainerPath(mountTable, v) {
		return v, true
	}
	return "", false
}

// isContainerPath reports whether v already names a path inside the container
// (equal to or under a mount's container path), so it needs no translation.
func isContainerPath(mountTable []dockerclient.Mount, v string) bool {
	for _, m := range mountTable {
		if m.ContainerPath == "" {
			continue
		}
		if v == m.ContainerPath || strings.HasPrefix(v, m.ContainerPath+"/") {
			return true
		}
	}
	return false
}

func applyEnvPathMaps(maps []envPathMap, hostPath string) (string, bool) {
	for _, m := range maps {
		if hostPath == m.HostPrefix {
			return m.ContainerPrefix, true
		}
		if strings.HasPrefix(hostPath, m.HostPrefix+string(filepath.Separator)) {
			return m.ContainerPrefix + hostPath[len(m.HostPrefix):], true
		}
	}
	return "", false
}

// containerDefaultPATH is the image-baked PATH from the Dockerfile ENV directive.
// It is used as the base when building a container exec PATH that prepends
// container-native user tool cache paths. Keep in sync with the ENV PATH line
// in plugins/sandbox/docker/Dockerfile.
const containerDefaultPATH = "/opt/stella/.mise-tools/shims:/opt/stella/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// injectToolPaths prepends container-native tool directories to PATH (the
// per-user mise shims so an agent's own installs win, then any manifest tool
// cache). Built-in tools resolve through the image-baked PATH (the shared
// /opt/stella mise tree); the host filesystem is never used for docker
// executable resolution because it may contain host-platform binaries.
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

// envPathMap is an extra host→container path translation that translateEnvPaths
// applies before consulting the mount table. Used for STELLA_HOME which needs
// env translation but must NOT be in the mount table (that would allow file
// reads across the entire directory).
type envPathMap struct {
	HostPrefix      string
	ContainerPrefix string
}

// dockerSession is a docker-backed sandbox session backed by a single container.
type dockerSession struct {
	id           string
	policy       sandboxpkg.Policy
	client       *dockerclient.Client
	containerID  string
	mountTable   []dockerclient.Mount
	envPathMaps  []envPathMap
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
	} else if hostPath, ok := toHostPath(h.session.mountTable, resolved); ok {
		resolved = hostPath
	}
	if _, err := toContainerPath(h.session.mountTable, resolved); err != nil {
		return "", fmt.Errorf("docker host: path %q is outside the session mount set: %w", path, err)
	}
	if err := rejectSymlinkTraversal(h.session.mountTable, resolved); err != nil {
		return "", fmt.Errorf("docker host: %w", err)
	}
	return resolved, nil
}

// toHostPath maps a container absolute path to its equivalent host path when
// the path is covered by a mount's container path.
func toHostPath(mounts []dockerclient.Mount, containerPath string) (string, bool) {
	bestRel := ""
	bestMount := dockerclient.Mount{}
	found := false
	for _, m := range mounts {
		rel, err := filepath.Rel(m.ContainerPath, containerPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if !found || len(m.ContainerPath) > len(bestMount.ContainerPath) {
			bestRel = rel
			bestMount = m
			found = true
		}
	}
	if !found {
		return "", false
	}
	if bestRel == "." {
		return bestMount.HostPath, true
	}
	return filepath.Join(bestMount.HostPath, bestRel), true
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

	// Snapshot env under the lock so a concurrent RefreshEnv can't race the read.
	h.session.mu.RLock()
	policyEnv := h.session.policy.Env
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

	// Snapshot env under the lock so a concurrent RefreshEnv can't race the read.
	h.session.mu.RLock()
	policyEnv := h.session.policy.Env
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

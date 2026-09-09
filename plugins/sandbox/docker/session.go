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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/sessionfs"
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
	mountSources       map[string]string
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

// NewFactoryWithMountSources returns a Factory backed by a Docker
// container-per-session strategy and binds process-visible policy roots to
// provider-private physical sources.
//
// When cfg.StellaHome is non-empty, construction resolves the runtime mode from
// $STELLA_DOCKER_SANDBOX_MODE and the matching mode-specific environment
// variable ($STELLA_HOME_HOST or $STELLA_HOME_VOLUME). It does not auto-detect
// the container runtime.
//
// The step is skipped when StellaHome is empty (e.g. unit tests), making
// construction cheap and infallible in that case. Callers that need user tool
// binaries pass them explicitly in Config.
func NewFactoryWithMountSources(cfg Config, mountSources map[string]string) (sandboxpkg.Factory, error) {
	if cfg.StellaHome != "" {
		var err error
		cfg, err = resolveDockerConfig(cfg, cfg.StellaHome)
		if err != nil {
			return nil, err
		}
		detectCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cfg = autodetectServerReachability(detectCtx, cfg)
		cancel()
	}
	return &dockerFactory{cfg: cfg, mountSources: maps.Clone(mountSources)}, nil
}

func (f *dockerFactory) Name() string { return "docker" }

// ResourceController returns a controller that can reconstruct and fence any
// Docker session owned by this factory's daemon. It intentionally does not
// infer ownership from labels or names; callers must provide the persisted
// daemon authority and full container ID.
func (f *dockerFactory) ResourceController(ctx context.Context) (sandboxpkg.ResourceController, error) {
	client, err := f.client()
	if err != nil {
		return nil, fmt.Errorf("docker factory: resource controller client: %w", err)
	}
	if _, err := client.DaemonID(ctx); err != nil {
		return nil, err
	}
	return client.Controller(), nil
}

// NewResourceController constructs the Docker controller used by startup
// reconciliation. It captures no container identity itself; Probe and
// Terminate require the persisted daemon authority and full container ID.
func NewResourceController(ctx context.Context) (sandboxpkg.ResourceController, error) {
	return dockerclient.NewResourceController(ctx)
}

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
		orphanCleanupComplete, err := dockerclient.CleanupOrphanedContainers(ctx, client, scope)
		if err != nil {
			slog.Warn("docker factory: orphan cleanup failed; retaining session temp directories", "error", err)
			return
		}
		if !orphanCleanupComplete {
			slog.Warn("docker factory: orphan cleanup incomplete; retaining session temp directories")
			return
		}
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

	sessionID := nextSessionID()
	policy.Filesystem.Mounts = normalizeDockerPolicyMounts(policy.Filesystem.Mounts)
	mountSources := maps.Clone(f.mountSources)
	if len(policy.Filesystem.Mounts) == 0 {
		workspaceSource := mountSources[workspaceMount]
		if workspaceSource == "" {
			return nil, errors.New("docker session: provider-private workspace source is required")
		}
		policy.Filesystem.Mounts = []sandboxpkg.Mount{{SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite}}
		if policy.Filesystem.WorkingDir == "" {
			policy.Filesystem.WorkingDir = workspaceMount
		}
	}
	policy.Filesystem.WorkingDir = cleanContainerPath(policy.Filesystem.WorkingDir)
	providerMounts, err := dockerProviderMounts(policy.Filesystem.Mounts, mountSources)
	if err != nil {
		return nil, err
	}
	if f.cfg.StableProjectionHostRoot != "" {
		providerMounts = slices.DeleteFunc(providerMounts, func(m sessionfs.Mount) bool {
			return filepath.Clean(m.HostPath) == filepath.Clean(f.cfg.StableProjectionHostRoot)
		})
	}
	workspaceHost := hostPathForSandboxMount(providerMounts, workspaceMount)
	if workspaceHost == "" {
		return nil, errors.New("docker session: provider-private workspace source is required")
	}
	workspaceHost, err = filepath.Abs(workspaceHost)
	if err != nil {
		return nil, fmt.Errorf("docker session: abs workspace root: %w", err)
	}

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
	userDataHost := ""
	if ud := hostPathForSandboxMount(providerMounts, userDataMount); ud != "" {
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
	client, err := f.client()
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: client: %w", err)
	}
	security, err := client.Security(ctx)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: inspect daemon security: %w", err)
	}
	// Capture the daemon authority before creating anything. If the endpoint is
	// unavailable or cannot identify itself, a created container would have no
	// safe durable owner and must not escape this call.
	daemonID, err := client.DaemonID(ctx)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: identify daemon: %w", err)
	}

	cleanupScope := f.cfg.cleanupScope(f.cfg.StellaHome)
	opts := dockerclient.CreateOptions{
		Image:          f.cfg.Image,
		Runtime:        f.cfg.Runtime,
		User:           dockerProcessUser(security.Rootless, f.cfg.RuntimeMode),
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
	if f.cfg.Generation > 0 || f.cfg.ExecutorBootID != "" {
		if f.cfg.Generation <= 0 || f.cfg.ExecutorBootID == "" {
			recordError(span, errors.New("docker session: incomplete generation ownership metadata"))
			span.End()
			return nil, errors.New("docker session: generation and executor boot identity must be supplied together")
		}
		opts.Labels[dockerclient.LabelGeneration] = strconv.FormatInt(f.cfg.Generation, 10)
		opts.Labels[dockerclient.LabelOwnerBootID] = f.cfg.ExecutorBootID
	}

	var selectionCaches *selectionToolCacheSet
	var stableProjection *stableSelectionProjection
	var toolPreparation ToolPreparationResult
	selectionImageID := ""
	if f.cfg.ExpectedBundleRevision != "" || len(f.cfg.SelectionToolBinaries) > 0 {
		if err := client.EnsureImageReady(ctx, f.cfg.Image, opts.Name); err != nil {
			recordError(span, err)
			span.End()
			return nil, fmt.Errorf("docker session: selection image: %w", err)
		}
		imageInfo, err := client.ImageInfo(ctx, f.cfg.Image)
		if err != nil {
			recordError(span, err)
			span.End()
			return nil, fmt.Errorf("docker session: resolve selection image: %w", err)
		}
		if imageInfo.ID == "" {
			err := fmt.Errorf("docker session: image inspect returned an empty image ID")
			recordError(span, err)
			span.End()
			return nil, err
		}
		selectionImageID = imageInfo.ID
		if expected := f.cfg.ExpectedBundleRevision; expected != "" && imageInfo.Labels[builtinBundleRevisionLabel] != expected {
			err := fmt.Errorf("docker session: image bundle revision does not match expected revision")
			recordError(span, err)
			span.End()
			return nil, err
		}
		opts.Image = imageInfo.ID
		selectionCaches, err = ensureSelectionToolCacheSet(ctx, client, f.cfg, imageInfo.ID)
		if err != nil {
			recordError(span, err)
			span.End()
			return nil, err
		}
		toolPreparation = selectionCaches.Preparation
		filterFailedPackageEnv(policy.Env, toolPreparation, f.cfg.SessionEnvRollbacks)
		stableProjection, err = publishStableSelectionProjection(ctx, client, f.cfg, imageInfo.ID, selectionCaches)
		if err != nil {
			recordError(span, err)
			span.End()
			return nil, err
		}
	}

	mountedPolicyMounts, mountedTempDirHost, mountedUserDataHost, err := f.configureSessionMounts(&opts, providerMounts, workspaceHost, userDataHost, tempDir)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, err
	}

	// Per-user mise tree(s) are generic writable mounts; keep their mounted pairs
	// so PATH can point at their shims.
	perUserTrees := writableToolTrees(mountedPolicyMounts)

	// Render only roots that actually have a container view. Policy.Env stays in
	// the same canonical coordinate system used by commands and Session.Files.
	filesystemView := dockerFilesystemView(mountedUserDataHost != "", mountedTempDirHost != "")
	env := maps.Clone(policy.Env)
	if env == nil {
		env = make(map[string]string)
	}
	if err := sandboxpkg.ApplyFilesystemEnv(env, filesystemView); err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: apply filesystem environment: %w", err)
	}
	// Point the in-sandbox CLI at stellad over the shared network. Without this
	// the CLI falls back to 127.0.0.1:25678 — the sandbox container's own
	// loopback, where nothing listens — so server-backed commands fail. Injecting
	// into policy.Env covers both the container's create-time env and every later
	// exec, which both derive from it. Set only when configured (DooD); the
	// local/host backend reaches stellad on loopback.
	env = withServerURL(env, f.cfg.ServerURL)
	if stableProjection != nil && f.cfg.StableProjectionHostRoot != "" {
		env[sandboxpkg.EnvCoreRuntimeDir] = filepath.Join(f.cfg.StableProjectionHostRoot, "core", "bin")
	}

	// Build the mount table and env prefix maps before creating the container so
	// the create-time env can be translated to the container view too — otherwise
	// PID 1's environment carries host paths the agent can read via
	// /proc/1/environ, defeating the isolation (Pi critical).
	mountTable := buildMountTable(mountTableOptions{
		WorkspaceHost:  workspaceHost,
		WorkspaceMount: workspaceMount,
		Mounts:         mountedPolicyMounts,
		TempHost:       mountedTempDirHost,
	})

	var envMaps []envPathMap
	if stableProjection != nil && f.cfg.StableProjectionHostRoot != "" {
		envMaps = append(envMaps, envPathMap{
			HostPrefix: f.cfg.StableProjectionHostRoot, ContainerPrefix: stableProjection.RootPath,
		})
	}
	if hostHome := env["STELLA_HOME"]; hostHome != "" {
		envMaps = append(envMaps, envPathMap{
			HostPrefix:      hostHome,
			ContainerPrefix: stellaHomeMount,
		})
	}
	policy.Env = translateEnvPaths(env, mountTable, envMaps)
	opts.Env = mergeEnv(policy.Env, nil)

	filesystemMounts := append([]sessionfs.Mount(nil), mountedPolicyMounts...)
	for _, mount := range providerMounts {
		// The release bundle is verified against the image revision during
		// preflight, so its host projection is byte-equivalent. Host bin and
		// .mise-tools trees may target a different OS/architecture and must not
		// impersonate the image-owned process view through Session.Files().
		if mount.SandboxPath == sandboxpkg.MountBuiltinSkills {
			filesystemMounts = append(filesystemMounts, mount)
		}
	}
	if mountedTempDirHost != "" {
		filesystemMounts = append(filesystemMounts, sessionfs.Mount{HostPath: mountedTempDirHost, SandboxPath: "/tmp"})
	}
	resolver, err := sessionfs.NewResolver(policy.Filesystem.WorkingDir, filesystemMounts)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: open filesystem plan: %w", err)
	}
	transferredResolverOwnership := false
	defer func() {
		if !transferredResolverOwnership {
			_ = resolver.Close()
		}
	}()
	policy.Filesystem.Mounts = sessionfs.PolicyMounts(filesystemMounts)

	var toolBinPaths []string
	var coreToolBinPaths []string
	// Per-user mise shims go first so the agent's own tool versions win. Bundled
	// and immutable selection aliases follow, with the image system PATH as fallback.
	for _, tree := range perUserTrees {
		if path.Base(tree.Container) == ".mise-tools" {
			toolBinPaths = append(toolBinPaths, path.Join(tree.Container, "shims"))
		}
	}
	// A verified release Docker runtime always gets a core-only selection volume,
	// even when the snapshot selects no plugin binaries. Lightweight callers
	// using arbitrary images without a bundle revision retain the client seam.
	if stableProjection != nil {
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath: stableProjection.VolumeName, ContainerPath: stableProjection.RootPath,
			ReadOnly: true, Type: dockerclient.MountTypeVolume, NoCopy: true,
		})
		toolBinPaths = append(toolBinPaths, stableProjection.CoreBinPath)
		coreToolBinPaths = append(coreToolBinPaths, stableProjection.CoreBinPath)
		for _, cache := range selectionCaches.Packages {
			if binPath := stableProjection.PackageBinPath[cache.VolumeName]; binPath != "" {
				toolBinPaths = append(toolBinPaths, binPath)
			}
		}
	} else if f.cfg.ExpectedBundleRevision != "" || len(f.cfg.SelectionToolBinaries) > 0 {
		// Mount the public root for sidecars and its bin subpath separately.
		// The latter overlays the image bin with NoCopy, so image-only tools do
		// not become visible through Docker's volume copy-up behavior.
		opts.ExtraMounts = append(opts.ExtraMounts,
			dockerclient.Mount{
				HostPath: selectionCaches.Core.VolumeName, ContainerPath: containerSelectionRoot,
				ReadOnly: true, Type: dockerclient.MountTypeVolume, NoCopy: true,
			},
			dockerclient.Mount{
				HostPath: selectionCaches.Core.VolumeName, ContainerPath: containerSelectionBin,
				ReadOnly: true, Type: dockerclient.MountTypeVolume, VolumeSubpath: "bin", NoCopy: true,
			},
			dockerclient.Mount{
				HostPath: selectionCaches.Core.MaskVolumeName, ContainerPath: stellaHomeMount + "/.mise-tools",
				ReadOnly: true, Type: dockerclient.MountTypeVolume, NoCopy: true,
			},
		)
		toolBinPaths = append(toolBinPaths, selectionCaches.Core.BinPath)
		coreToolBinPaths = append(coreToolBinPaths, selectionCaches.Core.BinPath)
		for _, selectionCache := range selectionCaches.Packages {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath: selectionCache.VolumeName, ContainerPath: selectionCache.RootPath,
				ReadOnly: true, Type: dockerclient.MountTypeVolume, NoCopy: true,
			})
			toolBinPaths = append(toolBinPaths, selectionCache.BinPath)
		}
	}

	slog.Info("docker session: creating sandbox container",
		"session_id", sessionID,
		"image", opts.Image,
		"container_name", opts.Name,
		"runtime", opts.Runtime,
		"workspace", workspaceHost,
		"network_mode", opts.NetworkMode,
		"extra_mounts", len(opts.ExtraMounts),
	)
	if err := resolver.ValidateBackingPaths(); err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("docker session: validate filesystem plan: %w", err)
	}
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

	selectionConfig := f.cfg
	selectionConfig.SelectionToolBinaries = nil
	selectionConfig.SessionEnvRollbacks = nil
	session := &dockerSession{
		id:               sessionID,
		policy:           policy,
		client:           client,
		daemonID:         daemonID,
		containerID:      containerID,
		mountTable:       mountTable,
		envPathMaps:      envMaps,
		toolBinPaths:     toolBinPaths,
		coreToolBinPaths: coreToolBinPaths,
		ownedTempDir:     tempDir,
		resolver:         resolver,
		files:            sessionfs.NewAccessWithTempDir(resolver, policy.Env[sandboxpkg.EnvTempDir]),
		done:             make(chan struct{}),
		traceSpan:        span,
		toolPreparation:  toolPreparation,
		selectionImageID: selectionImageID,
		selectionConfig:  selectionConfig,
		selectionCaches:  selectionCaches,
		stableProjection: stableProjection,
		filesystemView:   filesystemView,
		serverURL:        f.cfg.ServerURL,
		creationEnvKeys:  envKeys(policy.Env),
	}
	session.host = &dockerHost{session: session}
	transferredTempOwnership = true
	transferredResolverOwnership = true

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

// filterFailedPackageEnv removes package-owned variables before Docker creates
// the container. A failed optional CLI package must not retain credentials or
// other session inputs while its binaries and resources are hidden.
func filterFailedPackageEnv(env map[string]string, preparation ToolPreparationResult, rollbacks map[string]pkgplugins.SessionEnvRollback) {
	if len(env) == 0 || len(rollbacks) == 0 || len(preparation.FailedPackages) == 0 {
		return
	}
	failed := make(map[string]struct{}, len(preparation.FailedPackages))
	for _, failure := range preparation.FailedPackages {
		if failure.Package.PluginID != "" {
			failed[failure.Package.PluginID] = struct{}{}
		}
	}
	for envVar, rollback := range rollbacks {
		if _, failedPackage := failed[rollback.PluginID]; !failedPackage {
			continue
		}
		if rollback.PriorPresent {
			env[envVar] = rollback.PriorValue
		} else {
			delete(env, envVar)
		}
	}
}

// dockerSession is a docker-backed sandbox session backed by a single container.
type dockerSession struct {
	id               string
	policy           sandboxpkg.Policy
	client           *dockerclient.Client
	daemonID         string
	containerID      string
	mountTable       []dockerclient.Mount
	envPathMaps      []envPathMap
	toolBinPaths     []string
	coreToolBinPaths []string
	ownedTempDir     string
	host             *dockerHost
	resolver         *sessionfs.Resolver
	files            sandboxpkg.FileAccess
	done             chan struct{}
	doneOnce         sync.Once
	closeMu          sync.Mutex
	closed           bool
	closing          bool
	closeErr         error
	traceSpan        trace.Span
	traceOnce        sync.Once
	toolPreparation  ToolPreparationResult
	selectionImageID string
	selectionConfig  Config
	selectionCaches  *selectionToolCacheSet
	stableProjection *stableSelectionProjection
	filesystemView   sandboxpkg.FilesystemView
	serverURL        string
	creationEnvKeys  []string
	mu               sync.RWMutex
}

// ResourceIdentity exposes the exact Docker daemon and container that own this
// session. The daemon ID is resolved once and retained so a later daemon
// restart or endpoint switch cannot silently change the meaning of a durable
// identity.
func (s *dockerSession) ResourceIdentity(ctx context.Context) (sandboxpkg.ResourceIdentity, error) {
	if s == nil {
		return sandboxpkg.ResourceIdentity{}, errors.New("docker: nil session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.daemonID == "" {
		return sandboxpkg.ResourceIdentity{}, errors.New("docker: session has no captured daemon identity")
	}
	if s.containerID == "" {
		return sandboxpkg.ResourceIdentity{}, errors.New("docker: session has no container identity")
	}
	return sandboxpkg.ResourceIdentity{Backend: "docker", Authority: s.daemonID, Ref: s.containerID}, nil
}

func (s *dockerSession) Policy() sandboxpkg.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

// RenderEnv applies the fixed Docker filesystem view and server endpoint to a
// fresh logical turn environment. It intentionally does not copy retained
// policy variables, so removed package values cannot cross EnvReplace.
func (s *dockerSession) RenderEnv(ctx context.Context, logicalEnv map[string]string) (map[string]string, error) {
	// Keep RenderEnv's preflight failures distinguishable from a provider
	// request. The caller may use the rendered environment to prepare a later
	// Exec, but this method itself never contacts Docker.
	if err := ctx.Err(); err != nil {
		return nil, sandboxpkg.MarkNotStarted(err)
	}
	s.mu.RLock()
	closed := s.closed || s.closing
	view, serverURL := s.filesystemView, s.serverURL
	s.mu.RUnlock()
	if closed {
		return nil, sandboxpkg.MarkNotStarted(errors.New("docker: session is closed"))
	}
	env := maps.Clone(logicalEnv)
	if env == nil {
		env = make(map[string]string)
	}
	if err := sandboxpkg.ApplyFilesystemEnv(env, view); err != nil {
		return nil, sandboxpkg.MarkNotStarted(fmt.Errorf("docker: render filesystem environment: %w", err))
	}
	return withServerURL(env, serverURL), nil
}

func (s *dockerSession) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	return s.host.Exec(ctx, command, opts)
}

func (s *dockerSession) StartProcess(ctx context.Context, req sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	return s.host.StartProcess(ctx, req)
}
func (s *dockerSession) Files() sandboxpkg.FileAccess { return s.files }
func (s *dockerSession) WorkingDir() string           { return s.host.WorkingDir() }

// PluginPreparationResult implements the provider-neutral readiness bridge.
// Only package identities and bounded reasons cross the plugin boundary.
func (s *dockerSession) PluginPreparationResult() pkgplugins.PluginPreparationResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := pkgplugins.PluginPreparationResult{
		Packages: make([]pkgplugins.PluginPackageStatus, 0, len(s.toolPreparation.SuccessfulPackages)+len(s.toolPreparation.FailedPackages)),
		Binaries: slices.Clone(s.toolPreparation.BinaryEvidence),
	}
	for _, packageID := range s.toolPreparation.SuccessfulPackages {
		result.Packages = append(result.Packages, pkgplugins.PluginPackageStatus{PluginID: packageID.PluginID, Ready: true})
	}
	for _, failure := range s.toolPreparation.FailedPackages {
		result.Packages = append(result.Packages, pkgplugins.PluginPackageStatus{PluginID: failure.Package.PluginID, Reason: "Docker tool preparation failed"})
	}
	return result
}

// PreparePluginBinaries refreshes the optional selection on the already
// running container. The container image and Docker client are retained from
// CreateSession; only the read-only session projection is extended. The
// returned paths are optional package bin directories, in selection order;
// callers apply them through EnvReplace for the current turn. Core runtime
// paths remain owned by the session.
func (s *dockerSession) PreparePluginBinaries(ctx context.Context, specs []pkgplugins.PluginBinarySpec) (pkgplugins.PluginPreparationResult, []string, error) {
	if s == nil {
		return pkgplugins.PluginPreparationResult{}, nil, errors.New("docker: nil session")
	}
	if len(specs) == 0 {
		return pkgplugins.PluginPreparationResult{}, nil, nil
	}
	s.mu.RLock()
	closed := s.closed || s.closing
	imageID, cfg, client := s.selectionImageID, s.selectionConfig, s.client
	s.mu.RUnlock()
	if closed {
		return pkgplugins.PluginPreparationResult{}, nil, errors.New("docker: session is closed")
	}
	if imageID == "" || client == nil {
		return pkgplugins.PluginPreparationResult{}, nil, errors.New("docker: selection image was not retained")
	}
	if strings.TrimSpace(cfg.StableProjectionID) == "" {
		return pkgplugins.PluginPreparationResult{}, nil, errors.New("docker: stable selection projection is required for retained preparation")
	}
	binaries := make([]ToolBinary, 0, len(specs))
	for _, spec := range specs {
		binaries = append(binaries, ToolBinary{PluginID: spec.PluginID, ConfigID: spec.ConfigID, Scope: spec.Scope, Revision: spec.Revision, PackageDigest: spec.PackageDigest, Name: spec.Name, Tool: spec.Tool, Version: spec.Version, Options: maps.Clone(spec.Options)})
	}
	cfg.SelectionToolBinaries = binaries
	cfg.SessionEnvRollbacks = nil
	caches, err := ensureSelectionToolCacheSet(ctx, client, cfg, imageID)
	if err != nil {
		return pkgplugins.PluginPreparationResult{}, nil, err
	}
	projection, err := publishStableSelectionProjection(ctx, client, cfg, imageID, caches)
	if err != nil {
		return pkgplugins.PluginPreparationResult{}, nil, err
	}
	if projection == nil {
		return pkgplugins.PluginPreparationResult{}, nil, errors.New("docker: stable selection projection was not published")
	}
	result := pluginPreparationFromToolPreparation(caches.Preparation)
	var publicPaths []string
	for _, cache := range caches.Packages {
		if binPath := projection.PackageBinPath[cache.VolumeName]; binPath != "" {
			publicPaths = append(publicPaths, binPath)
		}
	}
	return result, publicPaths, nil
}

func pluginPreparationFromToolPreparation(preparation ToolPreparationResult) pkgplugins.PluginPreparationResult {
	result := pkgplugins.PluginPreparationResult{Binaries: slices.Clone(preparation.BinaryEvidence)}
	for _, pkg := range preparation.SuccessfulPackages {
		result.Packages = append(result.Packages, pkgplugins.PluginPackageStatus{PluginID: pkg.PluginID, Ready: true})
	}
	for _, failure := range preparation.FailedPackages {
		result.Packages = append(result.Packages, pkgplugins.PluginPackageStatus{PluginID: failure.Package.PluginID, Reason: "Docker tool preparation failed"})
	}
	return result
}

func (s *dockerSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed && !s.closing
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
// it asks the watcher close path to best-effort reap the stopped container so
// long-running stellad processes do not accumulate corpses.
func (s *dockerSession) watchContainer() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		closed := s.closed || s.closing
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
	// An explicit close already owns the teardown attempt. The watcher must not
	// block behind it or start a second Stop call against the same container;
	// the explicit path leaves closing latched on failure so a later retry can
	// finish the lifecycle.
	s.mu.RLock()
	busy := s.closed || s.closing
	s.mu.RUnlock()
	if busy {
		return
	}
	if err := s.closeLifecycle(reason, livenessErr); err != nil {
		slog.Warn("docker session: failed to reap exited container", "session_id", s.id, "container_id", s.containerID, "error", err)
	}
}

// closeLifecycle serializes teardown attempts. A failed attempt leaves the
// session open and its resources owned so a later Close can retry safely.
func (s *dockerSession) closeLifecycle(reason string, traceErr error) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()

	s.mu.Lock()
	if s.closed {
		closeErr := s.closeErr
		s.mu.Unlock()
		return closeErr
	}
	s.closing = true
	s.mu.Unlock()

	s.clearContainerTemp()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	stopErr := s.client.Stop(stopCtx, s.containerID)
	cancel()
	closeErr := stopErr
	if stopErr == nil && s.resolver != nil {
		closeErr = errors.Join(closeErr, s.resolver.Close())
	}
	if closeErr == nil {
		closeErr = s.cleanupOwnedTempDir()
	}

	s.mu.Lock()
	s.closeErr = closeErr
	if closeErr == nil {
		s.closing = false
		s.closed = true
	}
	s.mu.Unlock()
	if closeErr == nil {
		s.endTrace(reason, errors.Join(traceErr, closeErr))
		logSessionClosed(s.id, "docker", reason)
		s.closeDone()
	}
	return closeErr
}

// Close stops the container and marks the session closed.
// Uses a fresh background context with a 30s timeout so that cancellation of the
// caller's context does not leave the container running.
func (s *dockerSession) Close() error {
	return s.closeLifecycle("explicit_close", nil)
}

func (h *dockerHost) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	h.session.mu.RLock()
	closed := h.session.closed || h.session.closing
	h.session.mu.RUnlock()
	if closed {
		return sandboxpkg.ExecResult{}, sandboxpkg.MarkNotStarted(errors.New("docker: session is closed"))
	}
	if err := ctx.Err(); err != nil {
		return sandboxpkg.ExecResult{}, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host exec: context: %w", err))
	}
	if opts.EnvMode != sandboxpkg.EnvOverlay && opts.EnvMode != sandboxpkg.EnvReplace {
		return sandboxpkg.ExecResult{}, sandboxpkg.MarkNotStarted(errors.New("docker host exec: invalid environment mode"))
	}
	if err := h.session.resolver.ValidateBackingPaths(); err != nil {
		return sandboxpkg.ExecResult{}, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host exec: validate filesystem plan: %w", err))
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd = h.session.policy.Filesystem.WorkingDir
	}

	resolvedCwd, err := h.session.resolver.ResolveDirectory(cwd)
	if err != nil {
		return sandboxpkg.ExecResult{}, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host exec: resolve cwd: %w", err))
	}
	containerCwd := resolvedCwd.SandboxPath

	// Per-exec env reads take a snapshot under the lock.
	h.session.mu.RLock()
	policyEnv := maps.Clone(h.session.policy.Env)
	creationEnvKeys := slices.Clone(h.session.creationEnvKeys)
	h.session.mu.RUnlock()
	env := dockerExecEnvironmentMode(policyEnv, opts.Env, opts.EnvMode, h.session.mountTable, h.session.envPathMaps, h.session.toolBinPaths, h.session.coreToolBinPaths)

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = h.session.policy.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return sandboxpkg.ExecResult{}, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host exec: context: %w", err))
	}

	result, err := h.session.client.Exec(ctx, dockerclient.ExecOptions{
		ContainerID: h.session.containerID,
		Command:     []string{"/bin/sh", "-c", command},
		Cwd:         containerCwd,
		Env:         env,
		UnsetEnv:    unsetEnvKeys(creationEnvKeys, env, opts.EnvMode),
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
	h.session.mu.RLock()
	closed := h.session.closed || h.session.closing
	h.session.mu.RUnlock()
	if closed {
		return nil, sandboxpkg.MarkNotStarted(errors.New("docker: session is closed"))
	}
	if err := ctx.Err(); err != nil {
		return nil, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host start_process: context: %w", err))
	}
	if req.Path == "" {
		return nil, sandboxpkg.MarkNotStarted(errors.New("docker host start_process: path is required"))
	}
	if req.EnvMode != sandboxpkg.EnvOverlay && req.EnvMode != sandboxpkg.EnvReplace {
		return nil, sandboxpkg.MarkNotStarted(errors.New("docker host start_process: invalid environment mode"))
	}
	if err := h.session.resolver.ValidateBackingPaths(); err != nil {
		return nil, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host start_process: validate filesystem plan: %w", err))
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = h.session.policy.Filesystem.WorkingDir
	}

	resolvedCwd, err := h.session.resolver.ResolveDirectory(cwd)
	if err != nil {
		return nil, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host start_process: resolve cwd: %w", err))
	}
	containerCwd := resolvedCwd.SandboxPath

	// Per-exec env reads take a snapshot under the lock.
	h.session.mu.RLock()
	policyEnv := maps.Clone(h.session.policy.Env)
	creationEnvKeys := slices.Clone(h.session.creationEnvKeys)
	h.session.mu.RUnlock()
	env := dockerExecEnvironmentMode(policyEnv, req.Env, req.EnvMode, h.session.mountTable, h.session.envPathMaps, h.session.toolBinPaths, h.session.coreToolBinPaths)

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
	if err := execCtx.Err(); err != nil {
		cancel()
		return nil, sandboxpkg.MarkNotStarted(fmt.Errorf("docker host start_process: context: %w", err))
	}

	command := make([]string, 0, 1+len(req.Args))
	command = append(command, req.Path)
	command = append(command, req.Args...)

	handle, err := h.session.client.StartExec(execCtx, dockerclient.ExecOptions{
		ContainerID: h.session.containerID,
		Command:     command,
		Cwd:         containerCwd,
		Env:         env,
		UnsetEnv:    unsetEnvKeys(creationEnvKeys, env, req.EnvMode),
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

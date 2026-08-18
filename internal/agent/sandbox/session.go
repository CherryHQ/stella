package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
	localplugin "github.com/CherryHQ/stella/plugins/sandbox/local"
	noneplugin "github.com/CherryHQ/stella/plugins/sandbox/none"
	"github.com/CherryHQ/stella/resources"
)

// SyncSession copies changed files from the session overlay back to the source
// workspace without closing the session. No-op for sessions that don't
// support mid-session sync.
func SyncSession(session pkgsandbox.Session) error {
	if session == nil {
		return nil
	}
	type syncer interface{ Sync() error }
	if s, ok := session.(syncer); ok {
		return s.Sync()
	}
	return nil
}

func createDockerSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	paths, policy, mountSources, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}
	policy.InheritEnv = true

	slog.Info("creating docker session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.SandboxConfig.Network.Mode,
	)

	registry, err := resources.Default()
	if err != nil {
		return nil, fmt.Errorf("load builtin skill bundle: %w", err)
	}
	factory, err := dockerplugin.NewFactoryWithMountSources(dockerplugin.Config{
		Image: dockerImage(), StellaHome: paths.StellaHome,
		ExpectedBundleRevision: registry.BundleRevision(),
	}, mountSources)
	if err != nil {
		return nil, err
	}

	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		return nil, dockerSessionError(err)
	}

	return session, nil
}

func dockerSessionError(err error) error {
	var imageErr *dockerplugin.ImageUnavailableError
	if dockerImageIsDev() && errors.As(err, &imageErr) {
		return fmt.Errorf("%w (run `mise run sandbox:docker:build` to build the local %q image)", err, dockerImage())
	}
	return fmt.Errorf("create docker session: %w", err)
}

// createLocalSession creates a local (no container isolation) session.
// WARNING: commands run directly on the host OS with no container isolation.
func createLocalSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	paths, policy, mountSources, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}

	slog.Info("creating local session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.SandboxConfig.Network.Mode,
	)

	session, err := localplugin.NewFactoryWithMountSources(mountSources, localplugin.Config{
		StellaHome: paths.StellaHome,
	}).CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create local session: %w", err)
	}

	return session, nil
}

func createHostSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	paths, policy, mountSources, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}

	slog.Info("creating host session",
		"component", "runner_sandbox",
		"work_dir", paths.WorkDir,
	)

	session, err := noneplugin.NewFactoryWithMountSources(mountSources, noneplugin.Config{
		StellaHome: paths.StellaHome,
	}).CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create host session: %w", err)
	}

	return session, nil
}

// buildBasePolicy resolves paths and builds the backend-agnostic base policy
// (filesystem, network, env). Backend-specific adjustments are applied by
// each factory's CreateSession.
func buildBasePolicy(ctx context.Context, cfg Config) (Paths, pkgsandbox.Policy, map[string]string, error) {
	paths, err := ResolvePaths(cfg)
	if err != nil {
		return Paths{}, pkgsandbox.Policy{}, nil, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		return Paths{}, pkgsandbox.Policy{}, nil, err
	}

	fs, mountSources := runnerFilesystemPolicy(paths, cfg)
	// Mise tree prep, uniform across backends. EnsureMiseShims relinks the shared
	// system-tree shims to relative targets so they resolve after STELLA_HOME is
	// remapped (bwrap's /opt/stella) — otherwise a session started before the next
	// reconcile inherits stale absolute host-path shims that dangle in the sandbox
	// (#505). When a per-user tree exists it is also seeded (relative symlinks to
	// the read-only system installs) and mounted writable so the agent can install
	// its own tools. Docker consumes the same seeded host tree: it mounts the tree
	// writable at /opt/stella/users/{id}/.mise-tools and resolves the relative
	// symlinks against the image-baked linux system tree (#436).
	miseDir := miseUserDirHost(paths, cfg)
	if err := pkgsandbox.EnsureMiseShims(paths.StellaHome, miseDir); err != nil {
		return Paths{}, pkgsandbox.Policy{}, nil, fmt.Errorf("ensure mise shims: %w", err)
	}

	policy := pkgsandbox.Policy{
		Filesystem: fs,
		Network: pkgsandbox.NetworkPolicy{
			Mode: pkgsandbox.NetworkMode(cfg.SandboxConfig.Network.Mode),
		},
		Env: env,
	}
	return paths, policy, mountSources, nil
}

// resolveBackendName returns the active sandbox backend name from cfg,
// defaulting to local when no override is set.
func resolveBackendName(ctx context.Context, cfg Config) string {
	name := config.SandboxBackendLocal
	if cfg.SandboxBackendFn != nil {
		if override := cfg.SandboxBackendFn(ctx); override != "" {
			name = override
		}
	}
	return name
}

// ResolveSession creates a sandbox session from configuration.
// The active backend is determined by SandboxBackendFn, defaulting to local.
func ResolveSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	name := resolveBackendName(ctx, cfg)

	ctx, span := sandboxTracer.Start(ctx, "sandbox.create_session",
		trace.WithAttributes(
			attribute.String("stella.sandbox.backend", name),
			attribute.String("stella.sandbox.agent_root", cfg.Paths.AgentRoot),
			attribute.String("stella.sandbox.user_root", cfg.Paths.UserRoot),
			attribute.String("stella.sandbox.project_root", cfg.Paths.ProjectRoot),
		),
	)
	defer span.End()

	session, err := createSessionForBackend(ctx, cfg, name)
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
	}

	// One ResilientSession has one canonical process coordinate system. Pin
	// recreation to the backend that created the initial session; changing
	// between an isolating /workspace view and a host-coordinate view would make
	// paths already retained by tools ambiguous.
	return pkgsandbox.NewResilientSession(session, func(ctx context.Context) (pkgsandbox.Session, error) {
		return createSessionForBackend(ctx, cfg, name)
	}), nil
}

// createSessionForBackend creates a raw sandbox session for the given backend name.
func createSessionForBackend(ctx context.Context, cfg Config, name string) (pkgsandbox.Session, error) {
	switch name {
	case config.SandboxBackendDocker:
		return createDockerSession(ctx, cfg)
	case config.SandboxBackendLocal:
		return createLocalSession(ctx, cfg)
	case config.SandboxBackendNone:
		return createHostSession(ctx, cfg)
	default:
		return nil, fmt.Errorf("unknown sandbox backend: %q", name)
	}
}

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

// runnerSession wraps a sandbox.Session for runner use.
type runnerSession struct {
	session     sandbox.Session
	policy      sandbox.Policy
	alwaysAlive bool
}

// SessionDir returns the session workspace directory.
func (r *runnerSession) SessionDir() string {
	if r == nil || r.session == nil {
		return ""
	}
	resolved, err := r.session.Host().ResolvePath(string(os.PathSeparator))
	if err != nil {
		return ""
	}
	return resolved
}

func (r *runnerSession) Host() sandbox.Host {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.Host()
}

// Session returns the underlying sandbox session.
func (r *runnerSession) Session() sandbox.Session {
	if r == nil {
		return nil
	}
	return r.session
}

// Policy returns the session's effective policy.
func (r *runnerSession) Policy() sandbox.Policy {
	if r == nil {
		return sandbox.Policy{}
	}
	return r.policy
}

// Alive reports whether the session is healthy.
func (r *runnerSession) Alive() bool {
	if r == nil {
		return false
	}
	if r.session == nil {
		return r.alwaysAlive
	}
	return r.session.Alive()
}

// Done returns a channel that closes when the session terminates.
func (r *runnerSession) Done() <-chan struct{} {
	if r == nil || r.session == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return r.session.Done()
}

// Sync copies changed files from the session overlay back to the source
// workspace without closing the session. No-op for sessions that don't
// support mid-session sync.
func (r *runnerSession) Sync() error {
	if r == nil || r.session == nil {
		return nil
	}
	type syncer interface{ Sync() error }
	if s, ok := r.session.(syncer); ok {
		return s.Sync()
	}
	return nil
}

// Close shuts down the session.
func (r *runnerSession) Close() error {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.Close()
}

// sessionFactory creates a runnerSession from configuration.
type sessionFactory func(context.Context, GoRunnerConfig) (*runnerSession, error)

// registry manages session factories by name.
var sessionRegistry = map[string]sessionFactory{
	config.SandboxBackendLocal:  createLocalSession,
	config.SandboxBackendBoxsh:  createBoxshSession,
	config.SandboxBackendDocker: createDockerSession,
}

var platformSupportsBoxsh = boxshclient.PlatformSupportsBoxsh

func runnerFilesystemPolicy(paths sandboxPaths) sandbox.FilesystemPolicy {
	return sandbox.FilesystemPolicy{
		WorkspaceRoot: paths.UserRoot,
		WorkingDir:    paths.WorkDir,
		AllowEscapes:  false,
	}
}

func createLocalSession(_ context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	policy := sandbox.Policy{
		Backend:    config.SandboxBackendLocal,
		Relaxed:    true,
		Filesystem: runnerFilesystemPolicy(paths),
		Network: sandbox.NetworkPolicy{
			Mode: sandbox.NetworkAllowAll,
		},
		Process: sandbox.ProcessPolicy{
			Environment: sandboxProcessEnv(paths),
			InheritEnv:  true,
		},
	}

	factory := sandbox.GlobalRegistry().Get(config.SandboxBackendLocal)
	if factory == nil {
		return nil, fmt.Errorf("local factory not available")
	}

	session, err := factory.CreateSession(context.Background(), policy)
	if err != nil {
		return nil, fmt.Errorf("create local session: %w", err)
	}

	return &runnerSession{
		session: session,
		policy:  policy,
	}, nil
}

func createBoxshSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	ctx, span := sandboxTracer.Start(ctx, "sandbox.create_session",
		trace.WithAttributes(
			attribute.String("anna.sandbox.backend", config.SandboxBackendBoxsh),
			attribute.String("anna.sandbox.agent_root", cfg.AgentRoot),
			attribute.String("anna.sandbox.user_root", cfg.UserRoot),
			attribute.String("anna.sandbox.project_root", cfg.ProjectRoot),
		),
	)
	defer span.End()

	if !platformSupportsBoxsh() {
		err := fmt.Errorf("sandbox backend %q is not supported on %s", config.SandboxBackendBoxsh, runtime.GOOS)
		recordSandboxError(span, err)
		return nil, err
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		err = fmt.Errorf("resolve sandbox paths: %w", err)
		recordSandboxError(span, err)
		return nil, err
	}
	policy := sandbox.Policy{
		Backend:    config.SandboxBackendBoxsh,
		Relaxed:    false,
		Filesystem: runnerFilesystemPolicy(paths),
		Network: sandbox.NetworkPolicy{
			Mode:      sandbox.NetworkMode(cfg.Sandbox.Network.Mode),
			Allowlist: cfg.Sandbox.Network.Allowlist,
		},
		Process: sandbox.ProcessPolicy{
			Environment: sandboxProcessEnv(paths),
			InheritEnv:  true,
		},
	}

	span.SetAttributes(
		attribute.String("anna.sandbox.resolved_user_root", paths.UserRoot),
		attribute.String("anna.sandbox.work_dir", paths.WorkDir),
		attribute.String("anna.sandbox.network.mode", cfg.Sandbox.Network.Mode),
	)
	if len(cfg.Sandbox.Network.Allowlist) > 0 {
		span.SetAttributes(attribute.StringSlice("anna.sandbox.network.allowlist", cfg.Sandbox.Network.Allowlist))
	}

	slog.Info("creating boxsh session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.Sandbox.Network.Mode,
		"network_allowlist", cfg.Sandbox.Network.Allowlist,
	)

	factory := sandbox.GlobalRegistry().Get(config.SandboxBackendBoxsh)
	if factory == nil {
		err := fmt.Errorf("boxsh factory not available")
		recordSandboxError(span, err)
		return nil, err
	}

	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		err = fmt.Errorf("create boxsh session: %w", err)
		recordSandboxError(span, err)
		return nil, err
	}

	return &runnerSession{
		session: session,
		policy:  policy,
	}, nil
}

func createDockerSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	ctx, span := sandboxTracer.Start(ctx, "sandbox.create_session",
		trace.WithAttributes(
			attribute.String("anna.sandbox.backend", config.SandboxBackendDocker),
			attribute.String("anna.sandbox.agent_root", cfg.AgentRoot),
			attribute.String("anna.sandbox.user_root", cfg.UserRoot),
			attribute.String("anna.sandbox.project_root", cfg.ProjectRoot),
		),
	)
	defer span.End()

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		err = fmt.Errorf("resolve sandbox paths: %w", err)
		recordSandboxError(span, err)
		return nil, err
	}
	policy := sandbox.Policy{
		Backend:    config.SandboxBackendDocker,
		Relaxed:    false,
		Filesystem: runnerFilesystemPolicy(paths),
		Network: sandbox.NetworkPolicy{
			Mode:      sandbox.NetworkMode(cfg.Sandbox.Network.Mode),
			Allowlist: cfg.Sandbox.Network.Allowlist,
		},
		Process: sandbox.ProcessPolicy{
			Environment: sandboxProcessEnv(paths),
			InheritEnv:  true,
		},
	}

	span.SetAttributes(
		attribute.String("anna.sandbox.resolved_user_root", paths.UserRoot),
		attribute.String("anna.sandbox.work_dir", paths.WorkDir),
		attribute.String("anna.sandbox.network.mode", cfg.Sandbox.Network.Mode),
	)
	if len(cfg.Sandbox.Network.Allowlist) > 0 {
		span.SetAttributes(attribute.StringSlice("anna.sandbox.network.allowlist", cfg.Sandbox.Network.Allowlist))
	}

	slog.Info("creating docker session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.Sandbox.Network.Mode,
		"network_allowlist", cfg.Sandbox.Network.Allowlist,
	)

	dockerCfg := dockerplugin.Config{
		Image:               cfg.Sandbox.Docker.Image,
		User:                cfg.Sandbox.Docker.User,
		AllowPull:           cfg.Sandbox.Docker.AllowPull,
		ExtraMounts:         cfg.Sandbox.Docker.ExtraMounts,
		ContainerPathPrefix: cfg.Sandbox.Docker.ContainerPathPrefix,
		HostPathPrefix:      cfg.Sandbox.Docker.HostPathPrefix,
	}

	dockerCfg, err = applyDooDDefaults(dockerCfg, config.AnnaHome())
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
	}

	// Construct a per-runner factory with the resolved config. The global
	// registry carries a zero-value Config for probe/contract-test use only.
	factory := dockerplugin.NewFactory(dockerCfg)

	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		err = fmt.Errorf("create docker session: %w", err)
		recordSandboxError(span, err)
		return nil, err
	}

	return &runnerSession{
		session: session,
		policy:  policy,
	}, nil
}

var dockerOrphanCleanupOnce sync.Once

// cleanupOrphanedDockerContainers removes stale anna containers from previous
// crashed processes. Runs at most once per process.
func cleanupOrphanedDockerContainers(ctx context.Context, annaHome string) {
	dockerOrphanCleanupOnce.Do(func() {
		client, err := dockerclient.New()
		if err != nil {
			slog.Warn("docker orphan cleanup skipped: cannot construct docker client",
				"component", "runner_sandbox", "error", err)
			return
		}
		dockerclient.CleanupOrphanedContainers(ctx, client, annaHome)
	})
}

// resolveSession creates a runnerSession from configuration.
// Replaces the old resolveSandboxBackend which leaked boxsh types.
func resolveSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	name := resolveSessionBackendName(cfg.Sandbox)

	factory, ok := sessionRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown sandbox backend: %q", name)
	}
	return factory(ctx, cfg)
}

func resolveSessionBackendName(cfg config.SandboxConfig) string {
	name := cfg.BackendName()
	if name != config.SandboxBackendAuto {
		return name
	}
	if platformSupportsBoxsh() {
		return config.SandboxBackendBoxsh
	}
	return config.SandboxBackendLocal
}

var orphanCleanupOnce sync.Once

// cleanupOrphanedBoxshSessions removes leftover session dirs from crashed
// processes. Runs at most once per process to avoid interfering with
// sessions owned by concurrently-running runners.
func cleanupOrphanedBoxshSessions(annaHome, userRoot string) {
	orphanCleanupOnce.Do(func() {
		boxshclient.CleanupOrphanedSessions(annaHome)
		if userRoot != "" {
			boxshclient.CleanupLegacySessionDirs(filepath.Dir(userRoot))
		}
	})
}

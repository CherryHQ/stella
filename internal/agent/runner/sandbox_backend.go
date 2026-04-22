package runner

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/vaayne/anna/internal/cliwrap"
	"github.com/vaayne/anna/internal/config"
	oauth "github.com/vaayne/anna/internal/credentials/oauth"
	"github.com/vaayne/anna/internal/resources/binaries"
	"github.com/vaayne/anna/internal/sandbox"
	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
	localplugin "github.com/vaayne/anna/plugins/sandbox/local"
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
	resolved, err := r.session.ResolvePath(string(os.PathSeparator))
	if err != nil {
		return ""
	}
	return resolved
}

func (r *runnerSession) Host() sandbox.Host {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session
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
	config.SandboxBackendDocker: createDockerSession,
	config.SandboxBackendLocal:  createLocalSession,
}

func runnerFilesystemPolicy(paths sandboxPaths) sandbox.FilesystemPolicy {
	return sandbox.FilesystemPolicy{
		WorkspaceRoot: paths.UserRoot,
		WorkingDir:    paths.WorkDir,
	}
}

// buildSandboxEnv constructs the Policy.Env map for a sandbox session.
// Vault secrets (if any) are used as the base so that runner-set variables
// (e.g. ANNA_HOME) always take precedence over user-defined secrets.
func buildSandboxEnv(ctx context.Context, cfg GoRunnerConfig, paths sandboxPaths) map[string]string {
	env := make(map[string]string)

	if cfg.VaultEnvLoader != nil {
		vaultEnv, err := cfg.VaultEnvLoader.LoadEnv(ctx, cfg.UserID)
		if err != nil {
			slog.Warn("vault env injection skipped",
				"component", "runner_sandbox",
				"user_id", cfg.UserID,
				"error", err,
			)
		} else {
			maps.Copy(env, vaultEnv)
		}
	}

	// OAuth bundle keys are host-side only: they hold raw JSON credentials and
	// must not reach the sandbox process. The runner injects derived runtime
	// tokens below instead.
	delete(env, oauth.VaultKeyGitHub)
	delete(env, oauth.VaultKeyLark)

	// Inject runtime OAuth tokens when a TokenManager is available.
	if cfg.TokenManager != nil {
		if token, err := cfg.TokenManager.GetGHToken(ctx, cfg.UserID); err == nil && token != "" {
			env["GH_TOKEN"] = token
		} else if err != nil {
			slog.Debug("gh token injection skipped",
				"component", "runner_sandbox",
				"user_id", cfg.UserID,
				"error", err,
			)
		}

		if larkEnv, err := cfg.TokenManager.GetLarkRuntimeEnv(ctx, cfg.UserID); err == nil {
			maps.Copy(env, larkEnv)
		} else {
			slog.Debug("lark env injection skipped",
				"component", "runner_sandbox",
				"user_id", cfg.UserID,
				"error", err,
			)
		}
	}

	// Set real binary paths so wrapper scripts can locate them.
	if ghPath := binaries.ToolPath(paths.AnnaHome, "gh"); ghPath != "" {
		env["ANNA_GH_BIN"] = ghPath
	}
	if larkPath := binaries.ToolPath(paths.AnnaHome, "lark-cli"); larkPath != "" {
		env["ANNA_LARK_BIN"] = larkPath
	}

	// Runner-set vars overlay vault entries so they always take precedence.
	maps.Copy(env, sandboxProcessEnv(paths))
	return env
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
	env := buildSandboxEnv(ctx, cfg, paths)

	// Provision CLI wrappers under the user wrapper dir; expose the dir path via
	// ANNA_WRAPPER_DIR so Phase 5 can add it to PATH inside the container.
	wrapperDir := filepath.Join(paths.UserRoot, ".anna", "bin")
	if err := cliwrap.EnsureWrappers(wrapperDir); err != nil {
		slog.Warn("cliwrap provision failed", "component", "runner_sandbox", "error", err)
	} else {
		env["ANNA_WRAPPER_DIR"] = wrapperDir
	}

	policy := sandbox.Policy{
		Filesystem: runnerFilesystemPolicy(paths),
		Network: sandbox.NetworkPolicy{
			Mode: sandbox.NetworkMode(cfg.Sandbox.Network.Mode),
		},
		Env:        env,
		InheritEnv: true,
	}

	span.SetAttributes(
		attribute.String("anna.sandbox.resolved_user_root", paths.UserRoot),
		attribute.String("anna.sandbox.work_dir", paths.WorkDir),
		attribute.String("anna.sandbox.network.mode", cfg.Sandbox.Network.Mode),
	)

	slog.Info("creating docker session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.Sandbox.Network.Mode,
	)

	dockerCfg, err := resolveDockerConfig()
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

// createLocalSession creates a local (no container isolation) session.
// WARNING: commands run directly on the host OS with no container isolation.
func createLocalSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	env := buildSandboxEnv(ctx, cfg, paths)

	// Provision CLI wrappers and prepend wrapper dir to PATH for local sessions.
	wrapperDir := filepath.Join(paths.UserRoot, ".anna", "bin")
	if err := cliwrap.EnsureWrappers(wrapperDir); err != nil {
		slog.Warn("cliwrap provision failed", "component", "runner_sandbox", "error", err)
	} else {
		existing := env["PATH"]
		if existing == "" {
			existing = os.Getenv("PATH")
		}
		env["PATH"] = wrapperDir + string(os.PathListSeparator) + existing
	}

	policy := sandbox.Policy{
		Filesystem: runnerFilesystemPolicy(paths),
		Network: sandbox.NetworkPolicy{
			Mode: sandbox.NetworkMode(cfg.Sandbox.Network.Mode),
		},
		Env:        env,
		InheritEnv: true,
	}

	slog.Info("creating local session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.Sandbox.Network.Mode,
	)

	session, err := localplugin.NewFactory().CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create local session: %w", err)
	}

	return &runnerSession{
		session: session,
		policy:  policy,
	}, nil
}

var dockerOrphanCleanupOnce sync.Once

// resolveDockerConfig builds the docker plugin Config used by the runner,
// including any DooD path-translation prefixes derived from ANNA_HOME_HOST.
// Shared by session creation, preflight, and orphan cleanup so all three scope
// to the same daemon-view paths.
func resolveDockerConfig() (dockerplugin.Config, error) {
	return applyDooDDefaults(
		dockerplugin.Config{Image: config.SandboxDockerImage()},
		config.AnnaHome(),
	)
}

// cleanupOrphanedDockerContainers removes stale anna containers from previous
// crashed processes. Runs at most once per process. The annaHome argument must
// already be translated to the daemon-view path so it matches the label set at
// container creation time.
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
// Docker is the only supported sandbox backend; SandboxBackendFn is consulted
// only for plugin-registry overrides (which also resolve to docker).
func resolveSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	name := config.SandboxBackendDocker
	if cfg.SandboxBackendFn != nil {
		if override := cfg.SandboxBackendFn(ctx); override != "" {
			name = override
		}
	}

	factory, ok := sessionRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown sandbox backend: %q", name)
	}
	return factory(ctx, cfg)
}

package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
	dockerclient "github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	localplugin "github.com/CherryHQ/stella/plugins/sandbox/local"
	noneplugin "github.com/CherryHQ/stella/plugins/sandbox/none"
)

// Session wraps a pkgsandbox.Session for runner use.
type Session struct {
	session     pkgsandbox.Session
	policy      pkgsandbox.Policy
	alwaysAlive bool
}

// SessionDir returns the session workspace directory.
func (r *Session) SessionDir() string {
	if r == nil || r.session == nil {
		return ""
	}
	resolved, err := r.session.ResolvePath(string(os.PathSeparator))
	if err != nil {
		return ""
	}
	return resolved
}

func (r *Session) Host() pkgsandbox.Host {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session
}

// Session returns the underlying sandbox session.
func (r *Session) Session() pkgsandbox.Session {
	if r == nil {
		return nil
	}
	return r.session
}

// Policy returns the session's effective policy.
func (r *Session) Policy() pkgsandbox.Policy {
	if r == nil {
		return pkgsandbox.Policy{}
	}
	return r.policy
}

// Alive reports whether the session is healthy.
func (r *Session) Alive() bool {
	if r == nil {
		return false
	}
	if r.session == nil {
		return r.alwaysAlive
	}
	return r.session.Alive()
}

// Done returns a channel that closes when the session terminates.
func (r *Session) Done() <-chan struct{} {
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
func (r *Session) Sync() error {
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
func (r *Session) Close() error {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.Close()
}

// NewSession wraps a pkg/sandbox.Session for use by the parent agent package.
func NewSession(session pkgsandbox.Session) *Session {
	return &Session{session: session}
}

// sessionFactory creates a Session from configuration.
type sessionFactory func(context.Context, Config) (*Session, error)

// registry manages session factories by name.
var sessionRegistry = map[string]sessionFactory{
	config.SandboxBackendDocker: createDockerSession,
	config.SandboxBackendLocal:  createLocalSession,
	config.SandboxBackendNone:   createHostSession,
}

func createDockerSession(ctx context.Context, cfg Config) (*Session, error) {
	ctx, span := sandboxTracer.Start(ctx, "sandbox.create_session",
		trace.WithAttributes(
			attribute.String("stella.sandbox.backend", config.SandboxBackendDocker),
			attribute.String("stella.sandbox.agent_root", cfg.AgentRoot),
			attribute.String("stella.sandbox.user_root", cfg.UserRoot),
			attribute.String("stella.sandbox.project_root", cfg.ProjectRoot),
		),
	)
	defer span.End()

	paths, err := ResolvePaths(cfg)
	if err != nil {
		err = fmt.Errorf("resolve sandbox paths: %w", err)
		recordSandboxError(span, err)
		return nil, err
	}
	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
	}

	policy := pkgsandbox.Policy{
		Filesystem: runnerFilesystemPolicy(paths),
		Network: pkgsandbox.NetworkPolicy{
			Mode: pkgsandbox.NetworkMode(cfg.SandboxConfig.Network.Mode),
		},
		Env:        env,
		InheritEnv: true,
	}

	span.SetAttributes(
		attribute.String("stella.sandbox.resolved_user_root", paths.UserRoot),
		attribute.String("stella.sandbox.work_dir", paths.WorkDir),
		attribute.String("stella.sandbox.network.mode", cfg.SandboxConfig.Network.Mode),
	)

	slog.Info("creating docker session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.SandboxConfig.Network.Mode,
	)

	dockerCfg, err := ResolveDockerConfig()
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
	}
	userTools, err := resolveDockerUserToolBinaries(paths.StellaHome)
	if err != nil {
		err = fmt.Errorf("resolve docker user tools: %w", err)
		recordSandboxError(span, err)
		return nil, err
	}
	dockerCfg.UserToolBinaries = userTools

	// Construct a per-runner factory with the resolved config. The global
	// registry carries a zero-value Config for probe/contract-test use only.
	factory := dockerplugin.NewFactory(dockerCfg)

	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		err = fmt.Errorf("create docker session: %w", err)
		recordSandboxError(span, err)
		return nil, err
	}

	return &Session{
		session: session,
		policy:  policy,
	}, nil
}

// createLocalSession creates a local (no container isolation) session.
// WARNING: commands run directly on the host OS with no container isolation.
func createLocalSession(ctx context.Context, cfg Config) (*Session, error) {
	paths, err := ResolvePaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		return nil, err
	}
	env["PATH"] = localSandboxPath(paths.StellaHome)
	if paths.WorkDir != "" {
		env["HOME"] = localSandboxHome(paths.WorkDir)
	}
	copyLocalHostEnv(env)

	policy := pkgsandbox.Policy{
		Filesystem: runnerFilesystemPolicy(paths),
		Network: pkgsandbox.NetworkPolicy{
			Mode: pkgsandbox.NetworkMode(cfg.SandboxConfig.Network.Mode),
		},
		Env:        env,
		InheritEnv: false,
	}

	slog.Info("creating local session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.SandboxConfig.Network.Mode,
	)

	session, err := localplugin.NewFactory().CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create local session: %w", err)
	}

	return &Session{
		session: session,
		policy:  policy,
	}, nil
}

func createHostSession(ctx context.Context, cfg Config) (*Session, error) {
	paths, err := ResolvePaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		return nil, err
	}
	env["PATH"] = localSandboxPath(paths.StellaHome)

	policy := pkgsandbox.Policy{
		Filesystem: runnerFilesystemPolicy(paths),
		Network: pkgsandbox.NetworkPolicy{
			Mode: pkgsandbox.NetworkAllowAll,
		},
		Env:        env,
		InheritEnv: true,
	}

	slog.Info("creating host session",
		"component", "runner_sandbox",
		"work_dir", paths.WorkDir,
	)

	session, err := noneplugin.NewFactory().CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create host session: %w", err)
	}

	return &Session{
		session: session,
		policy:  policy,
	}, nil
}

// ResolveDockerConfig builds the docker plugin Config used by the runner,
// including any DooD path-translation prefixes derived from STELLA_HOME_HOST.
// Shared by session creation, preflight, and orphan cleanup so all three scope
// to the same daemon-view paths.
func ResolveDockerConfig() (dockerplugin.Config, error) {
	return applyDooDDefaults(
		dockerplugin.Config{Image: config.SandboxDockerImage()},
		config.StellaHome(),
	)
}

func resolveDockerUserToolBinaries(stellaHome string) ([]dockerplugin.ToolBinary, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	user, err := manifestplugins.LoadUser(filepath.Join(stellaHome, "plugins.yaml"))
	if err != nil {
		return nil, err
	}
	merged := manifestplugins.Merge(builtin, user)

	builtinByID := make(map[string]manifestplugins.ManifestPlugin, len(builtin.Plugins))
	for _, plugin := range builtin.Plugins {
		builtinByID[plugin.ID] = plugin
	}

	var out []dockerplugin.ToolBinary
	for _, plugin := range merged.Plugins {
		if !plugin.Enabled || len(plugin.Binaries) == 0 {
			continue
		}
		if builtinPlugin, ok := builtinByID[plugin.ID]; ok && reflect.DeepEqual(plugin.Binaries, builtinPlugin.Binaries) {
			continue
		}
		for _, binary := range plugin.Binaries {
			out = append(out, dockerplugin.ToolBinary{
				Name:    binary.Name,
				Tool:    binary.Tool,
				Version: binary.Version,
				Options: binary.Options,
			})
		}
	}
	return out, nil
}

var dockerOrphanCleanupOnce sync.Once

// CleanupOrphanedDockerContainers removes stale stella containers from previous
// crashed processes. Runs at most once per process. The stellaHome argument must
// already be translated to the daemon-view path so it matches the label set at
// container creation time.
func CleanupOrphanedDockerContainers(ctx context.Context, stellaHome string) {
	dockerOrphanCleanupOnce.Do(func() {
		client, err := dockerclient.New()
		if err != nil {
			slog.Warn("docker orphan cleanup skipped: cannot construct docker client",
				"component", "runner_sandbox", "error", err)
			return
		}
		dockerclient.CleanupOrphanedContainers(ctx, client, stellaHome)
	})
}

// ResolveSession creates a Session from configuration.
// The active backend is determined by SandboxBackendFn (global Plugins page
// selection), defaulting to local when no backend is explicitly enabled.
func ResolveSession(ctx context.Context, cfg Config) (*Session, error) {
	name := config.SandboxBackendLocal
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

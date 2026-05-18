package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
	localplugin "github.com/CherryHQ/stella/plugins/sandbox/local"
	noneplugin "github.com/CherryHQ/stella/plugins/sandbox/none"
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
	ctx, span := sandboxTracer.Start(ctx, "sandbox.create_session",
		trace.WithAttributes(
			attribute.String("stella.sandbox.backend", config.SandboxBackendDocker),
			attribute.String("stella.sandbox.agent_root", cfg.Paths.AgentRoot),
			attribute.String("stella.sandbox.user_root", cfg.Paths.UserRoot),
			attribute.String("stella.sandbox.project_root", cfg.Paths.ProjectRoot),
		),
	)
	defer span.End()

	paths, policy, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
	}
	policy.InheritEnv = true

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

	dockerCfg, err := ResolveDockerConfig(paths.StellaHome)
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

	factory := dockerplugin.NewFactory(dockerCfg)

	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		if config.SandboxDockerImageIsDev() {
			err = fmt.Errorf("%w (run `mise run sandbox:docker:build` to build the local %q image)", err, config.SandboxDockerImage())
		} else {
			err = fmt.Errorf("docker not available; install and start Docker Desktop or the docker daemon: %w", err)
		}
		recordSandboxError(span, err)
		return nil, err
	}

	return session, nil
}

// createLocalSession creates a local (no container isolation) session.
// WARNING: commands run directly on the host OS with no container isolation.
func createLocalSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	paths, policy, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}

	slog.Info("creating local session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.SandboxConfig.Network.Mode,
	)

	session, err := localplugin.NewFactory(localplugin.Config{
		StellaHome: paths.StellaHome,
	}).CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create local session: %w", err)
	}

	return session, nil
}

func createHostSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	paths, policy, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}

	slog.Info("creating host session",
		"component", "runner_sandbox",
		"work_dir", paths.WorkDir,
	)

	session, err := noneplugin.NewFactory(noneplugin.Config{
		StellaHome: paths.StellaHome,
	}).CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create host session: %w", err)
	}

	return session, nil
}

// ResolveDockerConfig builds the docker plugin Config used by the runner,
// including any DooD path-translation prefixes derived from STELLA_HOME_HOST.
func ResolveDockerConfig(stellaHome string) (dockerplugin.Config, error) {
	if stellaHome == "" {
		stellaHome = config.StellaHome()
	}
	return applyDooDDefaults(
		dockerplugin.Config{Image: config.SandboxDockerImage(), StellaHome: stellaHome},
		stellaHome,
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

// buildBasePolicy resolves paths and builds the backend-agnostic base policy
// (filesystem, network, env). Backend-specific adjustments are applied by
// each factory's CreateSession.
func buildBasePolicy(ctx context.Context, cfg Config) (Paths, pkgsandbox.Policy, error) {
	paths, err := ResolvePaths(cfg)
	if err != nil {
		return Paths{}, pkgsandbox.Policy{}, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		return Paths{}, pkgsandbox.Policy{}, err
	}

	policy := pkgsandbox.Policy{
		Filesystem: runnerFilesystemPolicy(paths),
		Network: pkgsandbox.NetworkPolicy{
			Mode: pkgsandbox.NetworkMode(cfg.SandboxConfig.Network.Mode),
		},
		Env: env,
	}
	return paths, policy, nil
}

// ResolveSession creates a sandbox session from configuration.
// The active backend is determined by SandboxBackendFn (global Plugins page
// selection), defaulting to local when no backend is explicitly enabled.
//
// This dispatches directly rather than using pkg/sandbox.Registry because the
// runtime path needs Config→Policy transformation that is per-backend, while
// the pkg Registry operates on Policy alone (useful for contract tests and
// external plugin registration).
func ResolveSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	name := config.SandboxBackendLocal
	if cfg.SandboxBackendFn != nil {
		if override := cfg.SandboxBackendFn(ctx); override != "" {
			name = override
		}
	}

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

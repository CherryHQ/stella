package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// BackendRequest is the host-prepared input to one sandbox backend.
type BackendRequest struct {
	Paths        Paths
	Policy       pkgsandbox.Policy
	MountSources map[string]string
	UserID       string
	GroupID      string
}

// Backend creates one raw sandbox session from host-prepared input.
type Backend func(context.Context, BackendRequest) (pkgsandbox.Session, error)

// BackendDefinition names one compiled-in sandbox backend.
type BackendDefinition struct {
	Name   string
	Create Backend
}

// BackendRegistry is an immutable index of compiled-in sandbox backends.
type BackendRegistry struct {
	backends map[string]Backend
}

// NewBackendRegistry validates and indexes sandbox backends.
func NewBackendRegistry(definitions ...BackendDefinition) (*BackendRegistry, error) {
	backends := make(map[string]Backend, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			return nil, errors.New("sandbox: empty backend name")
		}
		if definition.Create == nil {
			return nil, fmt.Errorf("sandbox: nil backend %q", definition.Name)
		}
		if _, exists := backends[definition.Name]; exists {
			return nil, fmt.Errorf("sandbox: duplicate backend %q", definition.Name)
		}
		backends[definition.Name] = definition.Create
	}
	return &BackendRegistry{backends: backends}, nil
}

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
	if cfg.Backends == nil {
		return nil, fmt.Errorf("sandbox backend registry is not configured")
	}
	backend, ok := cfg.Backends.backends[name]
	if !ok {
		return nil, fmt.Errorf("unknown sandbox backend: %q", name)
	}
	paths, policy, mountSources, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		return nil, err
	}
	slog.Info("creating sandbox session",
		"component", "runner_sandbox",
		"backend", name,
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.SandboxConfig.Network.Mode,
	)
	return backend(ctx, BackendRequest{
		Paths:        paths,
		Policy:       policy,
		MountSources: mountSources,
		UserID:       cfg.UserID,
		GroupID:      cfg.GroupID,
	})
}

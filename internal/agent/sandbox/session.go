package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	systemplugins "github.com/CherryHQ/stella/plugins/system"
)

// BackendRequest is the host-prepared input to one sandbox backend.
type BackendRequest struct {
	Paths        Paths
	Policy       pkgsandbox.Policy
	MountSources map[string]string
	UserID       string
	GroupID      string
	// Plugin plans capture authorized optional tools; SystemRuntimePlan contains
	// required release runtimes independently of plugin configuration.
	ContextBinaryPlan   *BinaryInstallPlan
	UserBinaryPlan      *BinaryInstallPlan
	SystemRuntimePlan   *systemplugins.RuntimePlan
	BinaryInstallResult *BinaryInstallResult
	BinarySpecs         []pkgplugins.PluginBinarySpec
	SessionEnvSpecs     []pkgplugins.SessionEnvSpec
	SessionEnvRollbacks map[string]pkgplugins.SessionEnvRollback
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

func hasUserBinarySpecs(specs []pkgplugins.PluginBinarySpec) bool {
	for _, spec := range specs {
		if spec.Scope == string(plugin.ScopeUser) || spec.Scope == string(plugin.ScopeUserAgent) {
			return true
		}
	}
	return false
}

// ResolveSession creates a sandbox session from configuration.
// The active backend is determined by SandboxBackendFn, defaulting to local.
func ResolveSession(ctx context.Context, cfg Config) (pkgsandbox.Session, error) {
	if cfg.SessionEnvRollbacks == nil {
		cfg.SessionEnvRollbacks = make(map[string]pkgplugins.SessionEnvRollback)
	}
	if cfg.PluginPreparationResult == nil {
		result := PrepareOAuthPackages(ctx, cfg, cfg.PluginRequirements)
		cfg.PluginPreparationResult = &result
	}
	cfg.BinarySpecs = readyBinarySpecs(cfg.BinarySpecs, *cfg.PluginPreparationResult)
	cfg.SessionEnvSpecs = readySessionEnvSpecs(cfg.SessionEnvSpecs, *cfg.PluginPreparationResult)
	var mergedBinaryResult BinaryInstallResult
	cfg.BinaryInstallResult = &mergedBinaryResult
	name := resolveBackendName(ctx, cfg)
	// ResolvePaths canonicalizes STELLA_HOME before building backend mounts. Do
	// the same before host-side binary publication so plan paths and the final
	// backend's physical paths cannot disagree when /var is symlinked on macOS.
	if cfg.Paths.StellaHome != "" {
		if resolved, err := filepath.EvalSymlinks(cfg.Paths.StellaHome); err == nil {
			cfg.Paths.StellaHome = resolved
		}
	}

	ctx, span := sandboxTracer.Start(ctx, "sandbox.create_session",
		trace.WithAttributes(
			attribute.String("stella.sandbox.backend", name),
			attribute.String("stella.sandbox.agent_root", cfg.Paths.AgentRoot),
			attribute.String("stella.sandbox.user_root", cfg.Paths.UserRoot),
			attribute.String("stella.sandbox.project_root", cfg.Paths.ProjectRoot),
		),
	)
	defer span.End()

	for _, spec := range cfg.BinarySpecs {
		for _, runtime := range systemplugins.EmbeddedRuntimeResources() {
			if spec.Name == runtime.Name {
				return nil, fmt.Errorf("sandbox: plugin binary %q conflicts with mandatory core runtime", spec.Name)
			}
		}
	}

	// Docker prepares S/SA and bundled resources in its Linux helper cache. Host
	// installation would bake the host OS/architecture into a container runner.
	if name != config.SandboxBackendDocker {
		if cfg.SystemRuntimePlan == nil {
			return nil, errors.New("sandbox: core runtimes were not prepared at startup")
		}
		if err := systemplugins.Verify(*cfg.SystemRuntimePlan); err != nil {
			recordSandboxError(span, err)
			return nil, fmt.Errorf("verify core runtimes: %w", err)
		}
		result, err := InstallContextBinaries(ctx, cfg.Paths.StellaHome, cfg.BinarySpecs)
		if err != nil {
			recordSandboxError(span, err)
			return nil, fmt.Errorf("install context plugin binaries: %w", err)
		}
		cfg.ContextBinaryPlan = &result.Plan
		mergeBinaryInstallResult(cfg.BinaryInstallResult, result)
		*cfg.PluginPreparationResult = cfg.PluginPreparationResult.Merge(result.PreparationResult())
		cfg.BinarySpecs = readyBinarySpecs(cfg.BinarySpecs, *cfg.PluginPreparationResult)
		cfg.SessionEnvSpecs = readySessionEnvSpecs(cfg.SessionEnvSpecs, *cfg.PluginPreparationResult)
	}

	create := func(ctx context.Context) (pkgsandbox.Session, error) {
		// User and user-agent installs need the internal mise engine during a
		// short preparation session. The final session is recreated from the
		// exact optional selection alongside the mandatory core runtimes.
		if name != config.SandboxBackendDocker && hasUserBinarySpecs(cfg.BinarySpecs) {
			prepCfg := cfg
			prepCfg.ContextBinaryPlan = nil
			prepCfg.UserBinaryPlan = nil
			principalDir, principalID := misePrincipal(cfg)
			if principalDir == "" || principalID == "" {
				return nil, fmt.Errorf("sandbox: user binary install requires a principal")
			}
			identity := "selection"
			if cfg.ContextBinaryPlan != nil && cfg.ContextBinaryPlan.Identity != "" {
				identity = cfg.ContextBinaryPlan.Identity
			}
			prepCfg.ManagedBinaryRoot = filepath.Join(cfg.Paths.StellaHome, ".mise-managed", principalDir, principalID, identity)
			if err := os.MkdirAll(prepCfg.ManagedBinaryRoot, 0o700); err != nil {
				return nil, fmt.Errorf("sandbox: create managed binary root: %w", err)
			}
			prep, err := createSessionForBackend(ctx, prepCfg, name)
			if err != nil {
				return nil, err
			}
			userResult, err := InstallSandboxBinaries(ctx, prep, readyBinarySpecs(cfg.BinarySpecs, *cfg.PluginPreparationResult))
			if err != nil {
				closeErr := prep.Close()
				if closeErr != nil {
					return nil, fmt.Errorf("install sandbox plugin binaries: %w; close preparation session: %w", err, closeErr)
				}
				_ = cleanupManagedBinaryPrep(prepCfg.ManagedBinaryRoot)
				return nil, fmt.Errorf("install sandbox plugin binaries: %w", err)
			}
			if err := prep.Close(); err != nil {
				return nil, fmt.Errorf("close sandbox binary preparation: %w", err)
			}
			// InstallSandboxBinaries operates in the preparation session's process
			// coordinates. Restore host coordinates for the final mount plan.
			userPlan := relocateBinaryPlan(userResult.Plan, prepCfg.ManagedBinaryRoot)
			if err := cleanupManagedBinaryPrep(prepCfg.ManagedBinaryRoot); err != nil {
				return nil, fmt.Errorf("sandbox: clean managed binary preparation: %w", err)
			}
			cfg.UserBinaryPlan = &userPlan
			mergeBinaryInstallResult(cfg.BinaryInstallResult, userResult)
			*cfg.PluginPreparationResult = cfg.PluginPreparationResult.Merge(userResult.PreparationResult())
			cfg.SessionEnvSpecs = readySessionEnvSpecs(cfg.SessionEnvSpecs, *cfg.PluginPreparationResult)
		}
		session, err := createSessionForBackend(ctx, cfg, name)
		if err != nil {
			return nil, err
		}
		// Docker may complete package preparation inside its Linux helper while
		// creating the raw session. Merge that provider result before wrapping it
		// in ResilientSession so prompt, env, and tool admission see one result.
		if provider, ok := session.(interface {
			PluginPreparationResult() pkgplugins.PluginPreparationResult
		}); ok && cfg.PluginPreparationResult != nil {
			*cfg.PluginPreparationResult = cfg.PluginPreparationResult.Merge(provider.PluginPreparationResult())
			cfg.BinarySpecs = readyBinarySpecs(cfg.BinarySpecs, *cfg.PluginPreparationResult)
			cfg.SessionEnvSpecs = readySessionEnvSpecs(cfg.SessionEnvSpecs, *cfg.PluginPreparationResult)
		}
		return session, nil
	}

	session, err := create(ctx)
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
	}

	// One ResilientSession has one canonical process coordinate system. Pin
	// recreation to the backend that created the initial session; changing
	// between an isolating /workspace view and a host-coordinate view would make
	// paths already retained by tools ambiguous.
	return pkgsandbox.NewResilientSession(session, create), nil
}

func mergeBinaryInstallResult(dst *BinaryInstallResult, src BinaryInstallResult) {
	if dst == nil {
		return
	}
	if dst.Plan.Identity == "" {
		dst.Plan = src.Plan
	} else {
		dst.Plan.Selections = append(dst.Plan.Selections, src.Plan.Selections...)
	}
	dst.SuccessfulPackages = append(dst.SuccessfulPackages, src.SuccessfulPackages...)
	dst.FailedPackages = append(dst.FailedPackages, src.FailedPackages...)
}

func readyBinarySpecs(specs []pkgplugins.PluginBinarySpec, result pkgplugins.PluginPreparationResult) []pkgplugins.PluginBinarySpec {
	if len(specs) == 0 || len(result.Packages) == 0 {
		return slices.Clone(specs)
	}
	ready := make(map[string]bool, len(result.Packages))
	for _, status := range result.Packages {
		ready[status.PluginID] = status.Ready
	}
	filtered := make([]pkgplugins.PluginBinarySpec, 0, len(specs))
	for _, spec := range specs {
		if spec.PluginID == "" {
			filtered = append(filtered, spec)
			continue
		}
		if ok, known := ready[spec.PluginID]; known && !ok {
			continue
		}
		filtered = append(filtered, spec)
	}
	return filtered
}

func readySessionEnvSpecs(specs []pkgplugins.SessionEnvSpec, result pkgplugins.PluginPreparationResult) []pkgplugins.SessionEnvSpec {
	if len(specs) == 0 || len(result.Packages) == 0 {
		return slices.Clone(specs)
	}
	ready := make(map[string]bool, len(result.Packages))
	for _, status := range result.Packages {
		ready[status.PluginID] = status.Ready
	}
	filtered := make([]pkgplugins.SessionEnvSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.PluginID == "" {
			filtered = append(filtered, spec)
			continue
		}
		if ok, known := ready[spec.PluginID]; known && !ok {
			continue
		}
		filtered = append(filtered, spec)
	}
	return filtered
}

func relocateBinaryPlan(plan BinaryInstallPlan, managedRoot string) BinaryInstallPlan {
	plan.DataDir = managedRoot
	for i := range plan.Selections {
		selection := &plan.Selections[i]
		selection.DataDir = managedRoot
		selection.PublicDir = filepath.Join(managedRoot, "public", selection.Identity)
		selection.PublicBinDir = selection.PublicDir
	}
	return plan
}

func cleanupManagedBinaryPrep(root string) error {
	for _, private := range []string{"contexts", "installs", "config", "cache", "state"} {
		if err := os.RemoveAll(filepath.Join(root, private)); err != nil {
			return err
		}
	}
	return nil
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
	if cfg.ContextBinaryPlan != nil {
		policy.Env = OverlayBinaryInstallPlan(policy.Env, *cfg.ContextBinaryPlan, BinarySystemLayer)
	}
	if cfg.UserBinaryPlan != nil {
		policy.Env = OverlayBinaryInstallPlan(policy.Env, *cfg.UserBinaryPlan, BinaryUserLayer)
	}
	if cfg.SystemRuntimePlan != nil {
		// Core adds executable paths without replacing optional selection or mise state.
		policy.Env[pkgsandbox.EnvCoreRuntimeDir] = cfg.SystemRuntimePlan.PublicBinDir
		if policy.Env["PATH"] == "" {
			policy.Env["PATH"] = cfg.SystemRuntimePlan.PublicBinDir
		} else {
			policy.Env["PATH"] += string(os.PathListSeparator) + cfg.SystemRuntimePlan.PublicBinDir
		}
		policy.Env[pkgsandbox.EnvRunnerPath] = policy.Env["PATH"]
	}

	slog.Info("creating sandbox session",
		"component", "runner_sandbox",
		"backend", name,
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"network_mode", cfg.SandboxConfig.Network.Mode,
	)
	return backend(ctx, BackendRequest{
		Paths:               paths,
		Policy:              policy,
		MountSources:        mountSources,
		UserID:              cfg.UserID,
		GroupID:             cfg.GroupID,
		ContextBinaryPlan:   cfg.ContextBinaryPlan,
		UserBinaryPlan:      cfg.UserBinaryPlan,
		SystemRuntimePlan:   cfg.SystemRuntimePlan,
		BinaryInstallResult: cfg.BinaryInstallResult,
		BinarySpecs:         slices.Clone(cfg.BinarySpecs),
		SessionEnvSpecs:     slices.Clone(cfg.SessionEnvSpecs),
		SessionEnvRollbacks: maps.Clone(cfg.SessionEnvRollbacks),
	})
}

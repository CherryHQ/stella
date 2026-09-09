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
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/toolinstall"
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
	// StableProjectionRoot is the host-side read-only public selection root for
	// this principal/session. Docker uses it only for path translation; the
	// actual process mount is a named volume populated from verified caches.
	StableProjectionRoot string
	StableProjectionID   string
	// Generation and ExecutorBootID bind backend-created compute to the
	// PostgreSQL SessionSandbox owner. Backends must carry both values into
	// provider labels/metadata when they support cross-boot reconciliation.
	Generation     int64
	ExecutorBootID string
}

// Backend creates one raw sandbox session from host-prepared input.
type Backend func(context.Context, BackendRequest) (pkgsandbox.Session, error)

// BackendDefinition names one compiled-in sandbox backend.
type BackendDefinition struct {
	Name              string
	Create            Backend
	ControllerFactory func(context.Context) (pkgsandbox.ResourceController, error)
}

// BackendRegistry is an immutable index of compiled-in sandbox backends.
type BackendRegistry struct {
	backends    map[string]Backend
	controllers map[string]func(context.Context) (pkgsandbox.ResourceController, error)
}

// NewBackendRegistry validates and indexes sandbox backends.
func NewBackendRegistry(definitions ...BackendDefinition) (*BackendRegistry, error) {
	backends := make(map[string]Backend, len(definitions))
	controllers := make(map[string]func(context.Context) (pkgsandbox.ResourceController, error), len(definitions))
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
		if definition.ControllerFactory != nil {
			controllers[definition.Name] = definition.ControllerFactory
		}
	}
	return &BackendRegistry{backends: backends, controllers: controllers}, nil
}

// Controller returns a backend's durable-resource controller. A backend may
// omit one when it cannot prove cross-boot absence, in which case callers must
// preserve an unknown/fenced generation instead of recreating it.
func (r *BackendRegistry) Controller(ctx context.Context, backend string) (pkgsandbox.ResourceController, error) {
	if r == nil {
		return nil, nil
	}
	factory := r.controllers[backend]
	if factory == nil {
		return nil, nil
	}
	return factory(ctx)
}

// HasController reports whether a backend advertises a durable-resource
// controller without constructing that controller. Session creation uses this
// to require a durable identity before declaring an isolating resource active.
func (r *BackendRegistry) HasController(backend string) bool {
	if r == nil {
		return false
	}
	return r.controllers[backend] != nil
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

// TurnPreparation is the immutable per-turn carrier. Env is a complete
// process environment for this turn; callers pass it through their process
// request's EnvReplace mode rather than mutating the retained session.
type TurnPreparation struct {
	// OAuth is the admission-time authorization predicate, kept separate from
	// Preparation so later CLI failures cannot make an OAuth denial look like a
	// tool-install failure or resurrect a package that OAuth rejected.
	OAuth       pkgplugins.PluginPreparationResult
	Preparation pkgplugins.PluginPreparationResult
	Binaries    BinaryInstallResult
	Env         map[string]string
}

// PrepareTurnSession resolves the complete package readiness and process
// environment for one admitted turn. User CLIs are installed through the
// existing sandbox session, while host-side context selections are published
// into the session-scoped stable projection.
func PrepareTurnSession(ctx context.Context, session pkgsandbox.Session, cfg Config) (TurnPreparation, error) {
	if session == nil {
		return TurnPreparation{}, errors.New("sandbox: sandbox session is required")
	}
	oauthPreparation := PrepareOAuthPackages(ctx, cfg, cfg.PluginRequirements)
	preparation := oauthPreparation.Clone()
	cfg.PluginPreparationResult = &preparation
	// Plans describe only this turn. Never let a retained startup plan keep a
	// removed selection mounted or influence the next selection identity.
	cfg.ContextBinaryPlan = nil
	cfg.UserBinaryPlan = nil
	readySpecs := readyBinarySpecs(cfg.BinarySpecs, preparation)
	var binaryResult BinaryInstallResult
	var contextPlan *BinaryInstallPlan
	var userPlan *BinaryInstallPlan
	backendName := resolveBackendName(ctx, cfg)
	var dockerSelectionPaths []string
	if backendName == config.SandboxBackendDocker && len(readySpecs) > 0 {
		raw, err := pkgsandbox.SelectSession(ctx, session)
		if err != nil {
			return TurnPreparation{Preparation: preparation, Binaries: binaryResult}, fmt.Errorf("select sandbox generation for Docker CLI preparation: %w", err)
		}
		preparer, ok := raw.(interface {
			PreparePluginBinaries(context.Context, []pkgplugins.PluginBinarySpec) (pkgplugins.PluginPreparationResult, []string, error)
		})
		if !ok {
			return TurnPreparation{Preparation: preparation, Binaries: binaryResult}, errors.New("sandbox: Docker session does not support per-turn CLI preparation")
		}
		update, paths, err := preparer.PreparePluginBinaries(ctx, readySpecs)
		if err != nil {
			return TurnPreparation{Preparation: preparation, Binaries: binaryResult}, fmt.Errorf("prepare Docker CLI binaries: %w", err)
		}
		preparation = preparation.Merge(update)
		dockerSelectionPaths = paths
		binaryResult.BinaryEvidence = slices.Clone(update.Binaries)
		for _, status := range update.Packages {
			if !status.Ready {
				continue
			}
			for _, spec := range readySpecs {
				if spec.PluginID == status.PluginID {
					binaryResult.SuccessfulPackages = append(binaryResult.SuccessfulPackages, BinaryPackage{PluginResourceIdentity: spec.PluginResourceIdentity, PackageDigest: spec.PackageDigest})
					break
				}
			}
		}
	}
	contextSpecs := slices.DeleteFunc(slices.Clone(readySpecs), func(spec pkgplugins.PluginBinarySpec) bool { return !isSystemBinary(spec) })
	if len(contextSpecs) > 0 && backendName != config.SandboxBackendDocker {
		publicRoot := stablePublicSelectionRoot(cfg.Paths.StellaHome, cfg)
		if publicRoot == "" {
			return TurnPreparation{Preparation: preparation, Binaries: binaryResult}, errors.New("sandbox: session-scoped public selection identity is unsafe")
		}
		result, err := InstallContextBinariesAt(ctx, cfg.Paths.StellaHome, publicRoot, contextSpecs)
		if err != nil {
			return TurnPreparation{Preparation: preparation, Binaries: binaryResult}, fmt.Errorf("prepare context CLI binaries: %w", err)
		}
		binaryResult = result
		contextPlan = &result.Plan
		preparation = preparation.Merge(result.PreparationResult())
		readySpecs = readyBinarySpecs(cfg.BinarySpecs, preparation)
	}
	userSpecs := slices.DeleteFunc(slices.Clone(readySpecs), func(spec pkgplugins.PluginBinarySpec) bool { return !isUserBinary(spec) })
	if len(userSpecs) > 0 && backendName != config.SandboxBackendDocker {
		turnCfg := cfg
		turnCfg.BinarySpecs = userSpecs
		turnCfg.PluginPreparationResult = &preparation
		result, err := prepareUserBinarySelection(ctx, turnCfg, backendName)
		if err != nil {
			return TurnPreparation{Preparation: preparation, Binaries: binaryResult}, fmt.Errorf("prepare user CLI binaries: %w", err)
		}
		mergeBinaryInstallResult(&binaryResult, result)
		userPlan = &result.Plan
		preparation = preparation.Merge(result.PreparationResult())
	}
	if contextPlan != nil {
		cfg.ContextBinaryPlan = contextPlan
	}
	if userPlan != nil {
		cfg.UserBinaryPlan = userPlan
	}
	cfg.PluginPreparationResult = &preparation
	cfg.BinarySpecs = readyBinarySpecs(cfg.BinarySpecs, preparation)
	cfg.SessionEnvSpecs = readySessionEnvSpecs(cfg.SessionEnvSpecs, preparation)
	// Rebuild from the current static policy instead of session.Policy().Env.
	// The retained session may still carry a removed package's env or PATH;
	// carrying that map forward would defeat EnvReplace's revocation guarantee.
	_, policy, _, err := buildBasePolicy(ctx, cfg)
	if err != nil {
		return TurnPreparation{Preparation: preparation, Binaries: binaryResult}, fmt.Errorf("prepare session environment: %w", err)
	}
	base := maps.Clone(policy.Env)
	if cfg.ContextBinaryPlan != nil {
		base = OverlayBinaryInstallPlan(base, *cfg.ContextBinaryPlan, BinarySystemLayer)
	}
	if userPlan != nil && userPlan.Identity != "" {
		base = OverlayBinaryInstallPlan(base, *userPlan, BinaryUserLayer)
	}
	if cfg.SystemRuntimePlan != nil {
		base[pkgsandbox.EnvCoreRuntimeDir] = cfg.SystemRuntimePlan.PublicBinDir
		if base["PATH"] == "" {
			base["PATH"] = cfg.SystemRuntimePlan.PublicBinDir
		} else {
			base["PATH"] += string(os.PathListSeparator) + cfg.SystemRuntimePlan.PublicBinDir
		}
		base[pkgsandbox.EnvRunnerPath] = base["PATH"]
	}
	if len(dockerSelectionPaths) > 0 {
		// Docker receives the current selection through marker variables. PATH is
		// deliberately dropped by its EnvReplace renderer because a host PATH is
		// never valid inside the Linux container.
		base[pkgsandbox.EnvNativeSelectionDir] = strings.Join(dockerSelectionPaths, string(os.PathListSeparator))
		if base["PATH"] == "" {
			base["PATH"] = strings.Join(dockerSelectionPaths, string(os.PathListSeparator))
		} else {
			base["PATH"] = strings.Join(append(slices.Clone(dockerSelectionPaths), base["PATH"]), string(os.PathListSeparator))
		}
		base[pkgsandbox.EnvRunnerPath] = base["PATH"]
	}
	return TurnPreparation{OAuth: oauthPreparation, Preparation: preparation, Binaries: binaryResult, Env: base}, nil
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
	if cfg.SessionID != "" && stablePublicSelectionRoot(cfg.Paths.StellaHome, cfg) == "" {
		return nil, errors.New("sandbox: session-scoped public selection identity is unsafe")
	}
	if cfg.SessionID != "" && cfg.Paths.StellaHome != "" {
		if err := os.MkdirAll(stablePublicSelectionRoot(cfg.Paths.StellaHome, cfg), 0o700); err != nil {
			return nil, fmt.Errorf("sandbox: create stable public selection root: %w", err)
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
		publicRoot := stablePublicSelectionRoot(cfg.Paths.StellaHome, cfg)
		result, err := InstallContextBinariesAt(ctx, cfg.Paths.StellaHome, publicRoot, cfg.BinarySpecs)
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

	createRaw := func(ctx context.Context) (pkgsandbox.Session, error) {
		// User and user-agent installs need the internal mise engine during a
		// short preparation session. The final session is recreated from the
		// exact optional selection alongside the mandatory core runtimes.
		if name != config.SandboxBackendDocker && hasUserBinarySpecs(cfg.BinarySpecs) {
			userResult, err := prepareUserBinarySelection(ctx, cfg, name)
			if err != nil {
				return nil, err
			}
			cfg.UserBinaryPlan = &userResult.Plan
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

	createWithGeneration := func(ctx context.Context, generation int64, ownerBootID string) (pkgsandbox.Session, error) {
		previousGeneration, previousBoot := cfg.Generation, cfg.ExecutorBootID
		cfg.Generation, cfg.ExecutorBootID = generation, ownerBootID
		defer func() { cfg.Generation, cfg.ExecutorBootID = previousGeneration, previousBoot }()
		return createRaw(ctx)
	}

	if cfg.GenerationStore != nil && cfg.SessionID != "" {
		if cfg.ExecutorBootID == "" {
			cfg.ExecutorBootID = cfg.GenerationStore.OwnerBootID()
		}
		if cfg.ConfigDigest == "" {
			_, policy, mountSources, digestErr := buildBasePolicy(ctx, cfg)
			if digestErr != nil {
				return nil, fmt.Errorf("derive sandbox generation digest: %w", digestErr)
			}
			if name == config.SandboxBackendDocker {
				policy.InheritEnv = true
			}
			cfg.ConfigDigest = SandboxConfigDigest(name, policy, mountSources)
		}
		session, err := cfg.GenerationStore.Open(ctx, GenerationSpec{
			SessionID: cfg.SessionID, Backend: name, ConfigDigest: cfg.ConfigDigest,
			Create: createWithGeneration,
		})
		if err != nil {
			recordSandboxError(span, err)
			return nil, err
		}
		return session, nil
	}

	session, err := createRaw(ctx)
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
	}

	// One ResilientSession has one canonical process coordinate system. Pin
	// recreation to the backend that created the initial session; changing
	// between an isolating /workspace view and a host-coordinate view would make
	// paths already retained by tools ambiguous.
	return pkgsandbox.NewResilientSession(session, createRaw), nil
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

// reusableUserBinarySelection reconstructs the stable user plan from the
// current logical specs without opening a writable preparation session. The
// private contexts/installs/config/cache/state tree is deliberately disposable;
// a complete stable selection is the cache hit and is sufficient to build the
// final mount and readiness result.
func reusableUserBinarySelection(cfg Config, specs []pkgplugins.PluginBinarySpec) (BinaryInstallResult, bool, error) {
	if cfg.SessionID == "" || len(specs) == 0 {
		return BinaryInstallResult{}, false, nil
	}
	principalDir, principalID := misePrincipal(cfg)
	if principalDir == "" || principalID == "" {
		return BinaryInstallResult{}, false, nil
	}
	managedRoot := filepath.Join(cfg.Paths.StellaHome, ".mise-managed", principalDir, principalID, "selection")
	stableRoot := stablePublicSelectionRoot(cfg.Paths.StellaHome, cfg)
	if stableRoot == "" {
		return BinaryInstallResult{}, false, errors.New("sandbox: session-scoped public selection identity is unsafe")
	}
	identity, err := binarySelectionIdentity(specs, managedRoot)
	if err != nil {
		return BinaryInstallResult{}, false, err
	}
	result := BinaryInstallResult{Plan: BinaryInstallPlan{Identity: identity, DataDir: stableRoot}}
	for _, group := range groupedBinarySpecs(specs, isUserBinary) {
		packageIdentity, err := binarySelectionIdentity(group.specs, managedRoot)
		if err != nil {
			return BinaryInstallResult{}, false, err
		}
		selection := selectionPlan(packageIdentity, group.pkg, stableRoot, stableRoot)
		evidence, err := toolinstall.ReadNativeSelectionEvidence(selection.PublicDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return BinaryInstallResult{}, false, nil
			}
			return BinaryInstallResult{}, false, fmt.Errorf("inspect stable sandbox CLI selection %q: %w", selection.Identity, err)
		}
		tools, err := miseToolsFromSpecs(group.specs, isUserBinary)
		if err != nil {
			return BinaryInstallResult{}, false, err
		}
		if !selectionEvidenceMatches(evidence, tools) {
			return BinaryInstallResult{}, false, fmt.Errorf("stable sandbox CLI selection %q has mismatched evidence", selection.Identity)
		}
		result.SuccessfulPackages = append(result.SuccessfulPackages, group.pkg)
		result.Plan.Selections = append(result.Plan.Selections, selection)
		result.BinaryEvidence = append(result.BinaryEvidence, binaryEvidence(group.pkg, selection.Identity, "sandbox", tools, evidence)...)
	}
	return result, true, nil
}

func selectionEvidenceMatches(evidence pkgplugins.BinaryInstallEvidence, tools []toolinstall.Tool) bool {
	if len(evidence.Tools) != len(tools) {
		return false
	}
	for _, tool := range tools {
		lookup := tool.Lookup
		if lookup == "" {
			lookup = tool.Key
		}
		publicName := tool.PublicName
		if publicName == "" {
			publicName = lookup
		}
		requested := tool.Version
		if requested == "" {
			requested = "latest"
		}
		found := false
		for _, item := range evidence.Tools {
			if item.Key == tool.Key && item.Lookup == lookup && item.PublicName == publicName && item.RequestedVersion == requested {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// prepareUserBinarySelection owns the writable preparation lifecycle for a
// native backend. It is shared by startup and turn refreshes so a turn never
// attempts to install into the retained final session's read-only projection.
func prepareUserBinarySelection(ctx context.Context, cfg Config, backendName string) (BinaryInstallResult, error) {
	specs := readyBinarySpecs(cfg.BinarySpecs, valueOrEmptyPreparation(cfg.PluginPreparationResult))
	if reusable, reused, err := reusableUserBinarySelection(cfg, specs); err != nil {
		return BinaryInstallResult{}, err
	} else if reused {
		return reusable, nil
	}
	principalDir, principalID := misePrincipal(cfg)
	if principalDir == "" || principalID == "" {
		return BinaryInstallResult{}, errors.New("sandbox: user binary install requires a principal")
	}
	parent := filepath.Join(cfg.Paths.StellaHome, ".mise-managed", principalDir, principalID)
	logicalRoot := filepath.Join(parent, "selection")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return BinaryInstallResult{}, fmt.Errorf("sandbox: create managed binary parent: %w", err)
	}
	// A retained native descendant may still use an earlier staging tree. Give
	// every attempt a distinct backing root, even for the same package selection.
	backingRoot, err := os.MkdirTemp(parent, "selection-")
	if err != nil {
		return BinaryInstallResult{}, fmt.Errorf("sandbox: create managed binary root: %w", err)
	}
	prepCfg := cfg
	prepCfg.ContextBinaryPlan = nil
	prepCfg.UserBinaryPlan = nil
	prepCfg.ManagedBinaryRoot = backingRoot
	create := func(ctx context.Context, generation int64, ownerBootID string) (pkgsandbox.Session, error) {
		creationCfg := prepCfg
		creationCfg.Generation, creationCfg.ExecutorBootID = generation, ownerBootID
		return createSessionForBackend(ctx, creationCfg, backendName)
	}
	var userResult BinaryInstallResult
	install := func(prep pkgsandbox.Session) error {
		var installErr error
		userResult, installErr = installSandboxBinaries(ctx, prep, specs, logicalRoot)
		return installErr
	}
	observation := pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown}
	if cfg.GenerationStore != nil && cfg.SessionID != "" {
		observation, err = cfg.GenerationStore.Prepare(ctx, PreparationSpec{
			SessionID: cfg.SessionID, Generation: cfg.Generation, Backend: backendName,
			BackingRoot: backingRoot, Create: create,
		}, install)
	} else {
		// Startup callers without a durable Session retain the same conservative
		// backing rule. A successful Close alone never authorizes deletion.
		var prep pkgsandbox.Session
		prep, err = create(ctx, cfg.Generation, cfg.ExecutorBootID)
		if err == nil {
			err = install(prep)
			closeErr := prep.Close()
			err = errors.Join(err, closeErr)
			if observer, ok := prep.(pkgsandbox.ResourceObservationProvider); ok && closeErr == nil {
				var observeErr error
				observation, observeErr = observer.ObserveResource(context.WithoutCancel(ctx))
				err = errors.Join(err, observeErr)
			}
		}
	}
	if err != nil {
		if observation.State == pkgsandbox.ResourceStateAbsent {
			err = errors.Join(err, os.RemoveAll(backingRoot))
		}
		return BinaryInstallResult{}, err
	}
	userPlan := userResult.Plan
	if cfg.SessionID != "" {
		stableRoot := stablePublicSelectionRoot(cfg.Paths.StellaHome, cfg)
		if stableRoot == "" {
			return BinaryInstallResult{}, errors.New("sandbox: session-scoped public selection identity is unsafe")
		}
		userPlan.DataDir = stableRoot
		for i := range userPlan.Selections {
			selection := &userPlan.Selections[i]
			// Plan paths belong to the provider's process namespace. Publication
			// is a host operation, so derive its source from our exact owned root.
			source := filepath.Join(backingRoot, "public", selection.Identity)
			destination := filepath.Join(stableRoot, selection.Identity)
			if err := toolinstall.PublishNativeSelectionTree(source, destination); err != nil {
				return BinaryInstallResult{}, fmt.Errorf("publish sandbox CLI selection: %w", err)
			}
			selection.DataDir = stableRoot
			selection.PublicDir = destination
			selection.PublicBinDir = destination
		}
		if observation.State == pkgsandbox.ResourceStateAbsent {
			if err := os.RemoveAll(backingRoot); err != nil {
				return BinaryInstallResult{}, fmt.Errorf("sandbox: clean proved-absent preparation: %w", err)
			}
		}
	} else {
		userPlan = relocateBinaryPlan(userResult.Plan, backingRoot)
		if observation.State == pkgsandbox.ResourceStateAbsent {
			if err := cleanupManagedBinaryPrep(backingRoot); err != nil {
				return BinaryInstallResult{}, fmt.Errorf("sandbox: clean managed binary preparation: %w", err)
			}
		}
	}
	userResult.Plan = userPlan
	return userResult, nil
}

func valueOrEmptyPreparation(result *pkgplugins.PluginPreparationResult) pkgplugins.PluginPreparationResult {
	if result == nil {
		return pkgplugins.PluginPreparationResult{}
	}
	return *result
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
		Paths:                paths,
		Policy:               policy,
		MountSources:         mountSources,
		UserID:               cfg.UserID,
		GroupID:              cfg.GroupID,
		ContextBinaryPlan:    cfg.ContextBinaryPlan,
		UserBinaryPlan:       cfg.UserBinaryPlan,
		SystemRuntimePlan:    cfg.SystemRuntimePlan,
		BinaryInstallResult:  cfg.BinaryInstallResult,
		BinarySpecs:          slices.Clone(cfg.BinarySpecs),
		SessionEnvSpecs:      slices.Clone(cfg.SessionEnvSpecs),
		SessionEnvRollbacks:  maps.Clone(cfg.SessionEnvRollbacks),
		StableProjectionRoot: stablePublicSelectionRoot(paths.StellaHome, cfg),
		StableProjectionID:   stableProjectionID(cfg),
		Generation:           cfg.Generation,
		ExecutorBootID:       cfg.ExecutorBootID,
	})
}

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/vaayne/anna/internal/config"
	oauth "github.com/vaayne/anna/internal/credentials/oauth"
	"github.com/vaayne/anna/internal/manifestplugins"
	"github.com/vaayne/anna/internal/sandbox"
	internaltools "github.com/vaayne/anna/internal/tools"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
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
func buildSandboxEnv(ctx context.Context, cfg GoRunnerConfig, paths sandboxPaths) (map[string]string, error) {
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
	delete(env, oauth.VaultKeyFeishu)
	if err := injectSessionEnv(ctx, cfg, env); err != nil {
		return nil, err
	}

	// Runner-set vars overlay vault entries so they always take precedence.
	maps.Copy(env, sandboxProcessEnv(paths))
	return env, nil
}

func injectSessionEnv(ctx context.Context, cfg GoRunnerConfig, env map[string]string) error {
	// oauthBundles caches loaded bundles per provider to avoid redundant vault hits.
	oauthBundles := make(map[string]*oauth.OAuthBundle)
	for _, spec := range cfg.SessionEnvSpecs {
		src := string(spec.Source)
		if spec.Source == pkgplugins.SessionEnvSourceStatic {
			env[spec.EnvVar] = spec.Value
			continue
		}
		if !strings.HasPrefix(src, "oauth.") {
			if spec.Required {
				return fmt.Errorf("required session env %q (source %q) for plugin %q could not be resolved", spec.EnvVar, spec.Source, spec.PluginID)
			}
			continue
		}
		if cfg.TokenManager == nil {
			if spec.Required {
				return fmt.Errorf("required session env %q (source %q) for plugin %q could not be resolved", spec.EnvVar, spec.Source, spec.PluginID)
			}
			continue
		}
		providerID := spec.OAuthProviderID
		if providerID == "" {
			if spec.Required {
				return fmt.Errorf("required session env %q has oauth source but no OAuthProviderID", spec.EnvVar)
			}
			continue
		}
		bundle, ok := oauthBundles[providerID]
		if !ok {
			var err error
			bundle, err = cfg.TokenManager.GetOAuthToken(ctx, providerID, cfg.UserID)
			if err != nil {
				slog.Debug("session env injection skipped",
					"component", "runner_sandbox",
					"user_id", cfg.UserID,
					"env_var", spec.EnvVar,
					"source", spec.Source,
					"error", err,
				)
			}
			oauthBundles[providerID] = bundle
		}
		if bundle == nil {
			if spec.Required {
				return fmt.Errorf("required session env %q (source %q) for plugin %q could not be resolved", spec.EnvVar, spec.Source, spec.PluginID)
			}
			continue
		}
		field := strings.TrimPrefix(src, "oauth.")
		var value string
		switch field {
		case "access_token":
			value = bundle.AccessToken
		case "client_id":
			value = bundle.ClientID
		case "brand":
			value = bundle.Brand
		case "refresh_token":
			value = bundle.RefreshToken
		default:
			if spec.Required {
				return fmt.Errorf("required session env %q (source %q) for plugin %q: unknown oauth field %q", spec.EnvVar, spec.Source, spec.PluginID, field)
			}
			continue
		}
		if value != "" {
			env[spec.EnvVar] = value
		} else if spec.Required {
			return fmt.Errorf("required session env %q (source %q) for plugin %q could not be resolved", spec.EnvVar, spec.Source, spec.PluginID)
		}
	}
	return nil
}

func prependPathEntry(entry, existing string) string {
	if entry == "" {
		return existing
	}
	if existing == "" {
		return entry
	}
	return entry + string(os.PathListSeparator) + existing
}

func localSandboxHome(workDir string) string {
	if runtime.GOOS == "linux" {
		return "/workspace"
	}
	return workDir
}

func localSandboxPath(annaHome string) string {
	annaBin := internaltools.BinDir(annaHome)
	if runtime.GOOS != "linux" {
		return prependPathEntry(annaBin, os.Getenv("PATH"))
	}

	entries := []string{annaBin}
	for entry := range strings.SplitSeq(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if localSandboxPathAllowed(entry, annaBin) {
			entries = append(entries, entry)
		}
	}
	entries = append(entries,
		"/run/current-system/sw/bin",
		"/usr/local/sbin",
		"/usr/local/bin",
		"/usr/sbin",
		"/usr/bin",
		"/sbin",
		"/bin",
	)
	return strings.Join(dedupePathEntries(entries), string(os.PathListSeparator))
}

func localSandboxPathAllowed(entry, annaBin string) bool {
	if entry == "" {
		return false
	}
	if annaBin != "" && entry == annaBin {
		return true
	}
	for _, root := range []string{"/usr", "/bin", "/sbin", "/nix", "/run/current-system/sw"} {
		if entry == root || strings.HasPrefix(entry, root+"/") {
			return true
		}
	}
	return false
}

func dedupePathEntries(entries []string) []string {
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func copyLocalHostEnv(env map[string]string) {
	for _, key := range []string{
		"TERM", "COLORTERM", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
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
	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		recordSandboxError(span, err)
		return nil, err
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
	userTools, err := resolveDockerUserToolBinaries(paths.AnnaHome)
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
	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		return nil, err
	}
	env["PATH"] = localSandboxPath(paths.AnnaHome)
	if paths.WorkDir != "" {
		env["HOME"] = localSandboxHome(paths.WorkDir)
	}
	copyLocalHostEnv(env)

	policy := sandbox.Policy{
		Filesystem: runnerFilesystemPolicy(paths),
		Network: sandbox.NetworkPolicy{
			Mode: sandbox.NetworkMode(cfg.Sandbox.Network.Mode),
		},
		Env:        env,
		InheritEnv: false,
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

func resolveDockerUserToolBinaries(annaHome string) ([]dockerplugin.ToolBinary, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	user, err := manifestplugins.LoadUser(filepath.Join(annaHome, "plugins.yaml"))
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
				Name:            binary.Name,
				Backend:         binary.Backend,
				Repo:            binary.Repo,
				URL:             binary.URL,
				Package:         binary.Package,
				Version:         binary.Version,
				StripComponents: binary.StripComponents,
				BinPath:         binary.BinPath,
				Bin:             binary.Bin,
				RenameExe:       binary.RenameExe,
				Checksum:        binary.Checksum,
				AssetPattern:    binary.AssetPattern,
				VersionPrefix:   binary.VersionPrefix,
				NoApp:           binary.NoApp,
				FilterBins:      binary.FilterBins,
				Prerelease:      binary.Prerelease,
				APIURL:          binary.APIURL,
				Size:            binary.Size,
				Format:          binary.Format,
				VersionListURL:  binary.VersionListURL,
				VersionRegex:    binary.VersionRegex,
				VersionJSONPath: binary.VersionJSONPath,
				VersionExpr:     binary.VersionExpr,
				Extras:          binary.Extras,
				PipxArgs:        binary.PipxArgs,
				UVX:             binary.UVX,
				UVXArgs:         binary.UVXArgs,
			})
		}
	}
	return out, nil
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
// The active backend is determined by SandboxBackendFn (global Plugins page
// selection), defaulting to local when no backend is explicitly enabled.
func resolveSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
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

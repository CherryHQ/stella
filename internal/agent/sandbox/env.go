package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func runnerFilesystemPolicy(paths Paths, cfg Config) pkgsandbox.FilesystemPolicy {
	principalDir, id := misePrincipal(cfg)
	return pkgsandbox.FilesystemPolicy{
		WorkspaceRoot:       paths.UserRoot,
		WorkingDir:          paths.WorkDir,
		ExtraReadOnlyMounts: skillMountsForSandbox(paths),
		TempDirHost:         userTempDir(principalDir, id),
	}
}

// miseUserDirHost returns the host path of this session's writable per-user mise
// home, or "" when there is no per-user tree: no principal, or an ID that fails
// the safe-path-component check (the session then falls back to the shared
// read-only system tree). A downgrade from an unsafe ID is logged so a malformed
// ID is diagnosable instead of mysterious. The caller seeds the tree and adds it
// to the policy's writable mounts; keeping that mise-specific wiring here leaves
// the FilesystemPolicy mise-agnostic.
func miseUserDirHost(paths Paths, cfg Config) string {
	principalDir, id := misePrincipal(cfg)
	dir := pkgsandbox.MiseUserToolsDir(paths.StellaHome, principalDir, id)
	if id != "" && dir == "" {
		slog.Warn("per-user mise tree disabled: unsafe id, using shared read-only system tree",
			"component", "runner_sandbox",
			"user_id", cfg.UserID,
			"group_id", cfg.GroupID,
			"principal_dir", principalDir,
		)
	}
	return dir
}

// misePrincipal returns the home subtree and raw ID for this session's per-user
// temp and mise trees. Users and groups resolve to disjoint top-level subtrees
// ("users"/"groups") keyed by the raw ID, so the two principal namespaces live in
// separate trees and equal IDs across them can never collide into one shared
// writable tree — without a name prefix. Empty dir when neither is set, which
// makes callers fall back to a session-local temp dir and the shared read-only
// system mise tree. Groups take precedence: a group session carries both a
// GroupID and a synthetic UserID and must key off the group.
func misePrincipal(cfg Config) (principalDir, id string) {
	if cfg.GroupID != "" {
		return "groups", cfg.GroupID
	}
	if cfg.UserID == "" {
		return "", ""
	}
	return "users", cfg.UserID
}

var validUserTempDirID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// userTempDir returns the per-principal scratch dir mounted as /tmp. principalDir
// ("users"/"groups") keeps the user and group namespaces in separate subtrees so
// equal IDs across them can't share a temp dir. Empty for an empty or unsafe id.
func userTempDir(principalDir, id string) string {
	if principalDir == "" || !validUserTempDirID.MatchString(id) {
		return ""
	}
	// Resolve symlinks in the base temp dir so that pathWithinRoot checks
	// work after filepath.EvalSymlinks (macOS: /var → /private/var).
	base := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	return filepath.Join(base, principalDir, id)
}

// skillMountsForSandbox returns host paths for all skill directories that must
// be mounted read-only in the sandbox. Each path is mounted at its exact host
// path (same-path strategy) so that skill_dir values returned by the skills
// tool are valid inside the sandbox without any translation.
func skillMountsForSandbox(paths Paths) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, base := range []string{paths.StellaHome, paths.AgentRoot, paths.UserRoot, paths.ProjectRoot} {
		if base == "" {
			continue
		}
		dir := filepath.Join(base, ".agents", "skills")
		if seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// buildSandboxEnv constructs the Policy.Env map for a sandbox session.
// Vault secrets (if any) are used as the base so that runner-set variables
// (e.g. STELLA_HOME) always take precedence over user-defined secrets.
func buildSandboxEnv(ctx context.Context, cfg Config, paths Paths) (map[string]string, error) {
	env := make(map[string]string)

	// Group sessions never load human vault secrets (D9 isolation).
	var vaultEnv map[string]string
	if cfg.GroupID == "" {
		// vaultEnv is the full decrypted vault snapshot, retained so OAuth bundle
		// resolution can read from it instead of decrypting the vault again. env is
		// the sandbox-facing copy, which has the host-only bundle keys stripped below.
		if cfg.VaultEnvLoader != nil {
			var ve map[string]string
			var err error
			if envEnsurer, ok := cfg.TokenEnsurer.(agentTokenEnvEnsurer); ok {
				ve, err = envEnsurer.EnsureAutoTokenEnvForAgent(ctx, cfg.UserID, cfg.AgentID)
			} else {
				if cfg.TokenEnsurer != nil {
					if err := cfg.TokenEnsurer.EnsureAutoToken(ctx, cfg.UserID); err != nil {
						slog.Warn("ensure auto token failed",
							"component", "runner_sandbox",
							"user_id", cfg.UserID,
							"error", err,
						)
					}
				}
				if scopedLoader, ok := cfg.VaultEnvLoader.(scopedVaultEnvLoader); ok {
					ve, err = scopedLoader.LoadEnvForAgent(ctx, cfg.UserID, cfg.AgentID)
				} else if envEnsurer, ok := cfg.TokenEnsurer.(tokenEnvEnsurer); ok {
					ve, err = envEnsurer.EnsureAutoTokenEnv(ctx, cfg.UserID)
				} else {
					ve, err = cfg.VaultEnvLoader.LoadEnv(ctx, cfg.UserID)
				}
			}
			if err != nil {
				slog.Warn("vault env injection skipped",
					"component", "runner_sandbox",
					"user_id", cfg.UserID,
					"agent_id", cfg.AgentID,
					"error", err,
				)
			} else {
				vaultEnv = ve
				maps.Copy(env, ve)
			}
		} else if envEnsurer, ok := cfg.TokenEnsurer.(tokenEnvEnsurer); ok {
			ve, err := envEnsurer.EnsureAutoTokenEnv(ctx, cfg.UserID)
			if err != nil {
				slog.Warn("vault env injection skipped",
					"component", "runner_sandbox",
					"user_id", cfg.UserID,
					"error", err,
				)
			} else {
				vaultEnv = ve
				maps.Copy(env, ve)
			}
		}
	}

	// OAuth bundle keys are host-side only: they hold raw JSON credentials and
	// must not reach the sandbox process. The runner injects derived runtime
	// tokens below instead.
	delete(env, oauth.VaultKeyGitHub)
	delete(env, oauth.VaultKeyLark)
	delete(env, oauth.VaultKeyFeishu)
	if cfg.GroupID == "" {
		if err := injectSessionEnv(ctx, cfg, env, vaultEnv); err != nil {
			return nil, err
		}
	}

	if shouldInjectScopedToken(cfg) {
		tokenUserID := cfg.UserID
		if cfg.GroupID != "" {
			tokenUserID = "group:" + cfg.GroupID
		}
		tok, err := cfg.TokenEnsurer.CreateScopedToken(ctx, tokenUserID, cfg.AgentID, cfg.SessionID, cfg.ProjectID)
		if err != nil {
			return nil, err
		}
		env["STELLA_TOKEN"] = tok
	} else {
		delete(env, "STELLA_TOKEN")
	}

	// Runner-set vars overlay vault entries so they always take precedence.
	maps.Copy(env, ProcessEnv(paths))

	// Host-execution backends (none, local) run tools via mise shims on the host
	// PATH, so they need the mise env pointed at the org's config. Docker carries
	// its own in-image mise tree and PATH, so host-side paths must not leak in.
	if resolveBackendName(ctx, cfg) != config.SandboxBackendDocker {
		principalDir, id := misePrincipal(cfg)
		userDataDir := pkgsandbox.MiseUserToolsDir(paths.StellaHome, principalDir, id)
		maps.Copy(env, manifestplugins.RuntimeMiseEnv(paths.StellaHome, userDataDir, paths.UserRoot))
	}

	return env, nil
}

func shouldInjectScopedToken(cfg Config) bool {
	hasIdentity := cfg.UserID != "" || cfg.GroupID != ""
	return cfg.TokenEnsurer != nil && hasIdentity && cfg.AgentID != ""
}

// injectSessionEnv resolves plugin SessionEnvSpecs into env. vaultEnv is the
// decrypted vault snapshot used to read OAuth bundles without re-decrypting.
func injectSessionEnv(ctx context.Context, cfg Config, env map[string]string, vaultEnv map[string]string) error {
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
			bundle, err = cfg.TokenManager.GetOAuthTokenFromEnv(ctx, vaultEnv, providerID, cfg.UserID)
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
			// Provider not connected: the tool will run without this env var.
			// Expected when a user hasn't connected the tool's credential yet,
			// so keep it at Debug — the credentials page surfaces the prompt.
			slog.Debug("session env skipped: oauth provider not connected",
				"component", "runner_sandbox",
				"user_id", cfg.UserID,
				"env_var", spec.EnvVar,
				"provider", providerID,
				"plugin", spec.PluginID,
			)
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

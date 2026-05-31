package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func runnerFilesystemPolicy(paths Paths) pkgsandbox.FilesystemPolicy {
	return pkgsandbox.FilesystemPolicy{
		WorkspaceRoot:       paths.UserRoot,
		WorkingDir:          paths.WorkDir,
		ExtraReadOnlyMounts: skillMountsForSandbox(paths),
	}
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

	if cfg.AgentID != "" {
		env["STELLA_AGENT_ID"] = cfg.AgentID
	}
	if cfg.SessionID != "" {
		env["STELLA_SESSION_ID"] = cfg.SessionID
	}

	// Runner-set vars overlay vault entries so they always take precedence.
	maps.Copy(env, ProcessEnv(paths))

	// Host-execution backends (none, local) run tools via mise shims on the host
	// PATH, so they need the mise env pointed at the org's config. Docker carries
	// its own in-image mise tree and PATH, so host-side paths must not leak in.
	if resolveBackendName(ctx, cfg) != config.SandboxBackendDocker {
		maps.Copy(env, manifestplugins.RuntimeMiseEnv(paths.StellaHome))
	}

	return env, nil
}

func injectSessionEnv(ctx context.Context, cfg Config, env map[string]string) error {
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

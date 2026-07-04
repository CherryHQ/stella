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

	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func runnerFilesystemPolicy(paths Paths, cfg Config) pkgsandbox.FilesystemPolicy {
	principalDir, id := misePrincipal(cfg)
	return pkgsandbox.FilesystemPolicy{
		WorkspaceRoot:     paths.WorkspaceRoot,
		WorkingDir:        paths.WorkDir,
		UserDataDir:       userDataDirHost(paths, cfg),
		AgentSkillsDir:    agentSkillsDirHost(paths),
		SystemDBSkillsDir: systemDBSkillsDirHost(paths),
		TempDirHost:       userTempDir(principalDir, id),
	}
}

// systemDBSkillsDirHost returns the host path of the DB-installed system-scope
// skills dir, STELLA_HOME/.agents/db-skills (a sibling of the shipped built-ins).
// Isolating backends mount it read-only at /opt/stella/db-skills.
func systemDBSkillsDirHost(paths Paths) string {
	if paths.StellaHome == "" {
		return ""
	}
	return filepath.Join(paths.StellaHome, ".agents", "db-skills")
}

// agentSkillsDirHost returns the host path of the admin-managed, agent-bound
// (system_agent scope) skills dir, AgentRoot/.agents/skills. Isolating backends
// mount it read-only at /opt/stella/agent-skills so those skills stay loadable
// without leaking the host path. Empty when the session has no agent definition
// root.
func agentSkillsDirHost(paths Paths) string {
	if paths.AgentRoot == "" {
		return ""
	}
	return filepath.Join(paths.AgentRoot, ".agents", "skills")
}

// userDataDirHost returns the host path of the shared user-data root mounted as
// /user, or "" for a user-less job (no principal home, so no shared root to
// mount; the agent writes only its workspace and tmp).
func userDataDirHost(paths Paths, cfg Config) string {
	if cfg.UserID == "" && cfg.GroupID == "" {
		return ""
	}
	return paths.UserDataDir
}

// miseUserDirHost returns the host path of this session's writable per-user mise
// home, under the STELLA_HOME frame ($STELLA_HOME/users/{id}/.mise-tools), a
// sibling of the user-data root rather than inside it. This keeps the per-user
// tree under the same root as the shared system tree once a backend remaps
// STELLA_HOME (bwrap's /opt/stella), so the relative seed/shim symlinks that
// bridge per-user tree -> system tree resolve identically on host and in the
// sandbox. Putting it under the /user-mounted user-data root instead would split
// the two trees across separate sandbox roots (/user vs /opt/stella) and dangle
// those symlinks (#505). The cost is one dedicated writable bind, wired via
// ExtraWritableMounts. Returns "" when there is no per-user tree: no principal, or
// an ID that fails the safe-path-component check (the session then falls back to
// the shared read-only system tree). A downgrade from an unsafe ID is logged so a
// malformed ID is diagnosable.
func miseUserDirHost(paths Paths, cfg Config) string {
	principalDir, id := misePrincipal(cfg)
	dir := pkgsandbox.MiseUserToolsDir(paths.StellaHome, principalDir, id)
	if dir == "" {
		if id != "" {
			slog.Warn("per-user mise tree disabled: unsafe id, using shared read-only system tree",
				"component", "runner_sandbox",
				"user_id", cfg.UserID,
				"group_id", cfg.GroupID,
				"principal_dir", principalDir,
			)
		}
		return ""
	}
	return dir
}

// misePrincipal returns the home subtree and ID for this session's per-principal
// temp and mise trees. Every principal lives in the single "users" subtree (the
// only top-level isolation boundary, #442): a real user keys off its raw ID, a
// channel group off the group ID under a "group-" prefix so a group can never
// collide with a user of the same raw ID into one shared writable tree. Empty dir
// when neither is set, which makes callers fall back to a session-local temp dir
// and the shared read-only system mise tree. Groups take precedence: a group
// session carries both a GroupID and a synthetic UserID and must key off the group.
func misePrincipal(cfg Config) (principalDir, id string) {
	if cfg.GroupID != "" {
		return "users", "group-" + cfg.GroupID
	}
	if cfg.UserID == "" {
		return "", ""
	}
	return "users", cfg.UserID
}

var validUserTempDirID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// userTempDir returns the per-principal scratch dir mounted as /tmp. principalDir
// is always "users"; a channel group's "group-" ID prefix keeps user and group
// scratch dirs from colliding. Empty for an empty or unsafe id.
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

// buildSandboxEnv constructs the Policy.Env map for a sandbox session.
// Vault secrets (if any) are used as the base so that runner-set variables
// (e.g. STELLA_HOME) always take precedence over user-defined secrets.
func buildSandboxEnv(ctx context.Context, cfg Config, paths Paths) (map[string]string, error) {
	env := make(map[string]string)

	// Group sessions never load human vault secrets (D9 isolation).
	var vaultEnv map[string]string
	if cfg.GroupID == "" {
		// vaultEnv is the full decrypted vault snapshot, retained so OAuth bundle
		// resolution can read from it even when the sandbox-facing copy is filtered
		// to explicitly injectable entries.
		if cfg.VaultEnvLoader != nil {
			var ve map[string]string
			var err error
			if scopedLoader, ok := cfg.VaultEnvLoader.(scopedVaultEnvLoader); ok {
				ve, err = scopedLoader.LoadEnvForAgent(ctx, cfg.UserID, cfg.AgentID)
			} else {
				ve, err = cfg.VaultEnvLoader.LoadEnv(ctx, cfg.UserID)
			}
			if err != nil {
				slog.Warn("vault env injection skipped",
					"component", "runner_sandbox",
					"user_id", cfg.UserID,
					"agent_id", cfg.AgentID,
					"error", err,
				)
			} else {
				// The loads above resolve the agent-bound view (ProjectID unset);
				// a project session narrows/widens to the project-bound view here.
				if projectLoader, ok := cfg.VaultEnvLoader.(projectVaultEnvLoader); ok && cfg.ProjectID != "" {
					if projectEnv, err := projectLoader.LoadEnvForAgentProject(ctx, cfg.UserID, cfg.AgentID, cfg.ProjectID); err == nil {
						ve = projectEnv
					} else {
						slog.Warn("project vault env load skipped",
							"component", "runner_sandbox",
							"user_id", cfg.UserID,
							"agent_id", cfg.AgentID,
							"project_id", cfg.ProjectID,
							"error", err,
						)
					}
				}
				vaultEnv = ve
				if fullLoader, ok := cfg.VaultEnvLoader.(fullVaultEnvLoader); ok {
					if full, err := fullLoader.LoadFullEnvForAgent(ctx, cfg.UserID, cfg.AgentID); err == nil {
						vaultEnv = full
					} else {
						slog.Warn("full vault env load skipped",
							"component", "runner_sandbox",
							"user_id", cfg.UserID,
							"agent_id", cfg.AgentID,
							"error", err,
						)
					}
				}
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

	// Every backend resolves tools through the same mise layout: the per-user
	// writable tree ($STELLA_HOME/users/{id}/.mise-tools) over the shared system
	// base, with the agent workspace trusted for a project mise.toml. The emitted
	// values are host paths; docker rewrites them to its /opt/stella container view
	// (translateEnvPaths), while local/none use them via the host PATH or bwrap
	// remap — so an agent sees identical mise paths whichever backend runs it.
	maps.Copy(env, manifestplugins.RuntimeMiseEnv(paths.StellaHome, miseUserDirHost(paths, cfg), paths.WorkspaceRoot))

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

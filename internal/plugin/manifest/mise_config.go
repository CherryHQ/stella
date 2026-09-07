package manifest

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// miseInstallMu serializes installs into the shared MISE_DATA_DIR. mise reshim
// rewrites the whole shims directory, so concurrent installs must not run in
// parallel or they clobber each other's shims.
var miseInstallMu sync.Mutex

// miseDirEnv returns the DATA/CONFIG/CACHE/STATE layout rooted at dataDir. The
// install side roots it at the shared system tree; the runtime side roots it at
// the per-user writable tree so both agree on the layout without drifting.
func miseDirEnv(dataDir string) map[string]string {
	return map[string]string{
		"MISE_DATA_DIR":   dataDir,
		"MISE_CONFIG_DIR": filepath.Join(dataDir, "config"),
		"MISE_CACHE_DIR":  filepath.Join(dataDir, "cache"),
		"MISE_STATE_DIR":  filepath.Join(dataDir, "state"),
	}
}

// miseBaseEnv returns the mise env entries shared by every Stella mise
// invocation — the system data-dir layout plus the non-interactive flags.
func miseBaseEnv(stellaHome string) map[string]string {
	env := miseDirEnv(miseToolsDir(stellaHome))
	env["MISE_YES"] = "1"
	env["MISE_NO_ANALYTICS"] = "1"
	env["MISE_EXPERIMENTAL"] = "1"
	return env
}

// RuntimeMiseEnv returns the mise environment for a sandbox session. A
// principal gets a writable tools tree plus a global config under its shared
// XDG config root;
// workspace mise.toml files remain the most specific layer. HOME and the XDG
// roots themselves are rendered by the selected sandbox backend.
//
// userToolsDir deliberately stays in the STELLA_HOME frame rather than under
// /user: relative seed and shim links must resolve against the shared system tree
// identically on local, none, and Docker backends. userConfigDir is separate and
// lives under the principal's shared data mount, where `mise use --global` may
// safely write without touching Stella's system config.
//
// When userToolsDir is empty (no safe user/group principal), runtime falls back
// to the read-only system tree with auto-install disabled and exposes no writable
// global config.
//
// MISE_DATA_DIR is load-bearing beyond mise itself: sandbox backends recover the
// per-user mise home from it via pkgsandbox.PerUserMiseDataDir to prepend its
// shims to PATH. Keep it pointing at userToolsDir, or at the system tree when the
// per-user tree is unavailable.
func RuntimeMiseEnv(stellaHome, userToolsDir, userConfigDir, workspaceDir string) map[string]string {
	dataDir := userToolsDir
	if dataDir == "" {
		dataDir = miseToolsDir(stellaHome)
	}
	env := miseDirEnv(dataDir)
	env["MISE_YES"] = "1"
	env["MISE_NO_ANALYTICS"] = "1"
	env["MISE_EXPERIMENTAL"] = "1"
	// Non-interactive Bash reads BASH_ENV after login profiles, restoring the
	// runner-owned PATH if /etc/profile replaced it. Backends translate this
	// system path into their process view; isolating backends expose it read-only.
	env["BASH_ENV"] = pkgsandbox.ShellEnvPath(stellaHome)
	// Backends replace this with their final rendered PATH. An explicit empty
	// runner-owned value prevents Vault configuration from turning shell startup
	// into an ambient PATH injection channel.
	env[pkgsandbox.EnvRunnerPath] = ""

	// The system config is supplied only by a non-empty snapshot selection via
	// OverlayBinaryInstallPlan. Keeping it unset here prevents disabled or
	// userless sessions from falling back to the shared _builtin.toml layer.
	trusted := []string{}
	if userToolsDir != "" && userConfigDir != "" {
		env["MISE_CONFIG_DIR"] = userConfigDir
		env["MISE_GLOBAL_CONFIG_FILE"] = filepath.Join(userConfigDir, "config.toml")
		trusted = append(trusted, userConfigDir)
	}

	// Trust a superset so workspace mise.toml resolves on every backend: the
	// literal /workspace is the bind-mount path on isolating backends, while the
	// host workspaceDir is needed by none and macOS local. Irrelevant entries are
	// inert and backend path translation deduplicates aliases.
	trusted = append(trusted, pkgsandbox.MountWorkspace)
	if workspaceDir != "" && workspaceDir != pkgsandbox.MountWorkspace {
		trusted = append(trusted, workspaceDir)
	}
	env["MISE_TRUSTED_CONFIG_PATHS"] = strings.Join(trusted, string(filepath.ListSeparator))

	if userToolsDir == "" {
		// No writable per-user tree: keep runtime off the network. Mutable config,
		// cache, and state follow backend-rendered XDG roots under Agent HOME; only
		// the read-only system data/install tree remains pinned explicitly.
		delete(env, "MISE_CONFIG_DIR")
		delete(env, "MISE_CACHE_DIR")
		delete(env, "MISE_STATE_DIR")
		env["MISE_NOT_FOUND_AUTO_INSTALL"] = "false"
	} else {
		env["MISE_NOT_FOUND_AUTO_INSTALL"] = "true"
	}
	return env
}

// miseTool is a single entry rendered into a mise config.
type miseTool struct {
	Key        string         // mise tool key, e.g. "github:cli/cli", "npm:serve", "uv"
	Version    string         // version spec; empty means "latest"
	Options    map[string]any // extra mise tool options (mise.toml option names)
	Lookup     string         // binary name passed to `mise which` for verification
	PublicName string         // manifest name published in a native selection bin
}

// renderMiseTOML builds a mise.toml [tools] table from the given tools. On a
// duplicate key the last entry wins. Two different keys exposing the same shim
// name (Lookup) are rejected: shims live in one shared directory, so the
// collision would non-deterministically shadow one tool with the other.
func renderMiseTOML(tools []miseTool) (string, error) {
	out := make(map[string]any, len(tools))
	lookupKey := make(map[string]string, len(tools))
	for _, t := range tools {
		if t.Key == "" {
			return "", fmt.Errorf("mise tool with empty key")
		}
		if t.Lookup != "" {
			if prev, ok := lookupKey[t.Lookup]; ok && prev != t.Key {
				return "", fmt.Errorf("mise tools %q and %q both expose shim %q", prev, t.Key, t.Lookup)
			}
			lookupKey[t.Lookup] = t.Key
		}
		ver := t.Version
		if ver == "" {
			ver = "latest"
		}
		options := maps.Clone(t.Options)
		if len(options) > 0 {
			if _, ok := options["version"]; !ok {
				options["version"] = ver
			}
			out[t.Key] = options
		} else {
			out[t.Key] = ver
		}
	}
	data, err := toml.Marshal(map[string]any{"tools": out})
	if err != nil {
		return "", fmt.Errorf("marshal mise.toml: %w", err)
	}
	return string(data), nil
}

// runMise runs a mise subcommand in dir with the given env. Stderr is discarded
// so installer diagnostics cannot cross the agent boundary.
func runMise(ctx context.Context, miseBin string, env []string, dir string, args ...string) error {
	cmd := managedCommandContext(ctx, miseBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return closedMiseError(ctx, args[0], err)
	}
	return nil
}

// BinaryLookupName returns the name used to verify a manifest binary via
// `mise which`. rename_exe wins (archive rename), then bin, then the tool name.
func BinaryLookupName(b ManifestBinary) string {
	if renameExe, ok := stringOption(b.Options, "rename_exe"); ok {
		return renameExe
	}
	if bin, ok := stringOption(b.Options, "bin"); ok {
		return bin
	}
	return b.Name
}

// miseToolFromBinary maps a manifest binary to a renderable mise tool entry.
func miseToolFromBinary(b ManifestBinary) miseTool {
	return miseTool{
		Key:        b.Tool,
		Version:    b.Version,
		Options:    b.Options,
		Lookup:     BinaryLookupName(b),
		PublicName: b.Name,
	}
}

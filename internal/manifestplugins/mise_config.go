package manifestplugins

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// builtinScope is the scope name for the global base config.
const builtinScope = "_builtin"

// miseInstallMu serializes installs into the shared MISE_DATA_DIR. mise reshim
// rewrites the whole shims directory, so concurrent installs must not run in
// parallel or they clobber each other's shims.
var miseInstallMu sync.Mutex

// miseBaseEnv returns the mise env entries shared by every Stella mise
// invocation — the isolated data-dir layout plus the non-interactive flags.
// Both the install side (isolatedMiseEnv) and the runtime side (RuntimeMiseEnv)
// build on this, so the directories they must agree on cannot silently drift.
func miseBaseEnv(stellaHome string) map[string]string {
	dataDir := miseToolsDir(stellaHome)
	return map[string]string{
		"MISE_DATA_DIR":     dataDir,
		"MISE_CONFIG_DIR":   filepath.Join(dataDir, "config"),
		"MISE_CACHE_DIR":    filepath.Join(dataDir, "cache"),
		"MISE_STATE_DIR":    filepath.Join(dataDir, "state"),
		"MISE_YES":          "1",
		"MISE_NO_ANALYTICS": "1",
		"MISE_EXPERIMENTAL": "1",
	}
}

// runtimeScopeConfigPath returns the mise config the sandbox resolves against.
func runtimeScopeConfigPath(stellaHome string) string {
	return ScopeConfigPath(stellaHome, builtinScope)
}

// RuntimeMiseEnv returns the mise environment variables a sandbox needs so its
// shims resolve tool versions from the persisted config against the shared
// install dir. HOME/XDG are intentionally left untouched — the sandbox owns
// those. Auto-install is disabled so runtime never reaches the network.
func RuntimeMiseEnv(stellaHome string) map[string]string {
	env := miseBaseEnv(stellaHome)
	configPath := runtimeScopeConfigPath(stellaHome)
	env["MISE_GLOBAL_CONFIG_FILE"] = configPath
	env["MISE_TRUSTED_CONFIG_PATHS"] = configPath
	env["MISE_NOT_FOUND_AUTO_INSTALL"] = "false"
	return env
}

// enabledBuiltinTools collects all mise tools from enabled manifest plugins.
func enabledBuiltinTools(m *Manifest) []miseTool {
	var tools []miseTool
	for _, p := range m.Plugins {
		if !p.Enabled {
			continue
		}
		for _, b := range p.Binaries {
			tools = append(tools, miseToolFromBinary(b))
		}
	}
	return tools
}

// miseTool is a single entry rendered into a mise config.
type miseTool struct {
	Key     string         // mise tool key, e.g. "github:cli/cli", "npm:serve", "uv"
	Version string         // version spec; empty means "latest"
	Options map[string]any // extra mise tool options (mise.toml option names)
	Lookup  string         // binary name passed to `mise which` for verification
}

// miseConfigsDir holds the persisted per-scope mise configs. Runtime points
// MISE_GLOBAL_CONFIG_FILE at one of these so shims resolve the right version.
func miseConfigsDir(stellaHome string) string {
	return filepath.Join(miseToolsDir(stellaHome), "configs")
}

// ScopeConfigPath returns the persisted mise config path for a scope.
func ScopeConfigPath(stellaHome, scope string) string {
	return filepath.Join(miseConfigsDir(stellaHome), scope+".toml")
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

// writeScopeConfig renders and persists the scope's mise config. It touches no
// network and runs no mise commands, so it is always safe to call.
func writeScopeConfig(stellaHome, scope string, tools []miseTool) (string, error) {
	tomlContent, err := renderMiseTOML(tools)
	if err != nil {
		return "", err
	}
	configPath := ScopeConfigPath(stellaHome, scope)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return "", fmt.Errorf("create mise configs dir: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(tomlContent), 0o644); err != nil {
		return "", fmt.Errorf("write mise config %s: %w", configPath, err)
	}
	return configPath, nil
}

// runScopeInstall installs every tool in the scope's persisted config into the
// shared MISE_DATA_DIR and regenerates shims. Tools are exposed via shims on
// PATH; nothing is copied to $STELLA_HOME/bin. mise runs in a neutral cwd so no
// ambient project mise.toml is picked up.
func runScopeInstall(ctx context.Context, stellaHome, scope string) error {
	miseInstallMu.Lock()
	defer miseInstallMu.Unlock()

	miseBin, err := findMiseBin(stellaHome)
	if err != nil {
		return err
	}
	configPath := ScopeConfigPath(stellaHome, scope)
	env, err := scopeMiseEnv(stellaHome, scope)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "stella-mise-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	if err := runMise(ctx, miseBin, env, dir, "trust", configPath); err != nil {
		return fmt.Errorf("mise trust: %w", err)
	}
	if err := runMise(ctx, miseBin, env, dir, "install"); err != nil {
		return fmt.Errorf("mise install: %w", err)
	}
	if err := runMise(ctx, miseBin, env, dir, "reshim"); err != nil {
		return fmt.Errorf("mise reshim: %w", err)
	}
	return nil
}

// installScope persists the scope config and installs its tools. Convenience
// wrapper for callers that always want both (org sync, tests).
func installScope(ctx context.Context, stellaHome, scope string, tools []miseTool) error {
	if _, err := writeScopeConfig(stellaHome, scope, tools); err != nil {
		return err
	}
	return runScopeInstall(ctx, stellaHome, scope)
}

// scopeMiseEnv returns the isolated mise env with MISE_GLOBAL_CONFIG_FILE
// pointed at the scope's persisted config. MISE_TRUSTED_CONFIG_PATHS mirrors
// RuntimeMiseEnv so install, resolve, and runtime all trust the config the same
// way rather than depending on the persisted trust store under the isolated HOME.
func scopeMiseEnv(stellaHome, scope string) ([]string, error) {
	env, err := isolatedMiseEnv(stellaHome)
	if err != nil {
		return nil, err
	}
	configPath := ScopeConfigPath(stellaHome, scope)
	return append(env,
		"MISE_GLOBAL_CONFIG_FILE="+configPath,
		"MISE_TRUSTED_CONFIG_PATHS="+configPath,
	), nil
}

// resolveToolVersion returns the concrete installed version mise resolves for
// the given lookup name under the provided env, running in a neutral cwd.
func resolveToolVersion(ctx context.Context, miseBin string, env []string, dir, lookup string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := managedCommandContext(ctx, miseBin, "which", lookup, "--version")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mise which --version %s: %w\nstderr: %s", lookup, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// runMise runs a mise subcommand in dir with the given env, capturing stderr.
func runMise(ctx context.Context, miseBin string, env []string, dir string, args ...string) error {
	var stderr bytes.Buffer
	cmd := managedCommandContext(ctx, miseBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return nil
}

// binaryLookupName returns the name used to verify a manifest binary via
// `mise which`. rename_exe wins (archive rename), then bin, then the tool name.
func binaryLookupName(b ManifestBinary) string {
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
		Key:     b.Tool,
		Version: b.Version,
		Options: b.Options,
		Lookup:  binaryLookupName(b),
	}
}

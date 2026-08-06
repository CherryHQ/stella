package manifestplugins

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// builtinScope is the scope name for the global base config.
const builtinScope = "_builtin"

var runtimeScopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

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

// runtimeScopeConfigPath returns the mise config the sandbox resolves against.
func runtimeScopeConfigPath(stellaHome string) string {
	return ScopeConfigPath(stellaHome, builtinScope)
}

// RuntimeMiseEnv returns the mise environment for a sandbox session, layered
// like a real machine: the shared system installs supply the builtin tools and
// the _builtin config supplies their default versions, while the per-user
// writable tree (userDataDir) holds anything the agent installs itself. HOME/XDG
// are left untouched — the sandbox owns those.
//
// When userDataDir is set the per-user tree is writable, so DATA/CACHE/STATE live
// there and auto-install is enabled; a user's own tool versions win because the
// per-user shims sort ahead on PATH. (Auto-install still only reaches the network
// when the session's NetworkPolicy permits egress — mise doesn't widen it.)
// workspaceDir (the host workspace root) is trusted alongside the bwrap
// "/workspace" mount so a project's mise.toml participates in resolution regardless
// of backend.
//
// When userDataDir is empty (no user/group) it falls back to the read-only system
// tree with auto-install disabled and state redirected to a writable temp dir,
// matching the historical behavior.
//
// MISE_DATA_DIR is load-bearing beyond mise itself: the sandbox host backends
// recover the per-user mise home from it via pkgsandbox.PerUserMiseDataDir to put
// the per-user shims on PATH, so the FilesystemPolicy carries no mise-specific
// field. Keep DATA_DIR pointing at userDataDir (or the system tree when empty).
func RuntimeMiseEnv(stellaHome, userDataDir, workspaceDir string) map[string]string {
	dataDir := userDataDir
	if dataDir == "" {
		dataDir = miseToolsDir(stellaHome)
	}
	env := miseDirEnv(dataDir)
	env["MISE_YES"] = "1"
	env["MISE_NO_ANALYTICS"] = "1"
	env["MISE_EXPERIMENTAL"] = "1"

	configPath := runtimeScopeConfigPath(stellaHome)
	env["MISE_GLOBAL_CONFIG_FILE"] = configPath

	// Trust a superset so the project mise.toml resolves on every backend: the
	// literal "/workspace" is the bind-mount path (load-bearing on Linux/bwrap),
	// the host workspaceDir is load-bearing on none/macOS where there is no remap.
	// The entry irrelevant to a given backend is inert, so neither may be dropped.
	trusted := []string{configPath, pkgsandbox.MountWorkspace}
	if workspaceDir != "" && workspaceDir != pkgsandbox.MountWorkspace {
		trusted = append(trusted, workspaceDir)
	}
	env["MISE_TRUSTED_CONFIG_PATHS"] = strings.Join(trusted, string(filepath.ListSeparator))

	if userDataDir == "" {
		// No writable per-user tree: keep runtime off the network. Mutable config,
		// cache, and state follow the backend-rendered XDG roots under Agent HOME;
		// only the read-only system data/install tree remains pinned explicitly.
		delete(env, "MISE_CONFIG_DIR")
		delete(env, "MISE_CACHE_DIR")
		delete(env, "MISE_STATE_DIR")
		env["MISE_NOT_FOUND_AUTO_INSTALL"] = "false"
	} else {
		env["MISE_NOT_FOUND_AUTO_INSTALL"] = "true"
	}
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
	return runScopeInstallWithShims(
		ctx,
		stellaHome,
		scope,
		filepath.Join(miseToolsDir(stellaHome), "shims"),
	)
}

func runScopeInstallWithShims(ctx context.Context, stellaHome, scope, shimsDir string) error {
	miseInstallMu.Lock()
	defer miseInstallMu.Unlock()

	miseBin, err := findMiseBin(stellaHome)
	if err != nil {
		return err
	}
	configPath := ScopeConfigPath(stellaHome, scope)
	env, err := scopeMiseEnvWithShims(stellaHome, scope, shimsDir)
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
	if err := relinkShims(stellaHome, miseBin); err != nil {
		return fmt.Errorf("relink shims: %w", err)
	}
	return nil
}

// ProvisionRuntimeBinary synchronously installs one daemon-owned binary into
// the shared mise package tree, but gives the runtime a dedicated config and
// shim directory so plugin enablement cannot remove its executable surface.
func ProvisionRuntimeBinary(
	ctx context.Context,
	stellaHome string,
	scope string,
	binary ManifestBinary,
) (string, map[string]string, error) {
	if !runtimeScopePattern.MatchString(scope) {
		return "", nil, fmt.Errorf("invalid runtime mise scope %q", scope)
	}
	if strings.TrimSpace(binary.Name) == "" || strings.TrimSpace(binary.Tool) == "" {
		return "", nil, fmt.Errorf("runtime binary name and tool are required")
	}
	if err := bootstrapMise(ctx, stellaHome); err != nil {
		return "", nil, fmt.Errorf("bootstrap runtime mise: %w", err)
	}
	if _, err := writeScopeConfig(stellaHome, scope, []miseTool{miseToolFromBinary(binary)}); err != nil {
		return "", nil, fmt.Errorf("write runtime mise scope: %w", err)
	}
	shimsDir := filepath.Join(miseToolsDir(stellaHome), "runtime-shims", scope)
	if err := runScopeInstallWithShims(ctx, stellaHome, scope, shimsDir); err != nil {
		return "", nil, fmt.Errorf("install runtime binary %s: %w", binary.Name, err)
	}
	environment := runtimeScopeMiseEnv(stellaHome, scope, shimsDir)
	return filepath.Join(shimsDir, runtimeBinaryName(binaryLookupName(binary))), environment, nil
}

func runtimeScopeMiseEnv(stellaHome, scope, shimsDir string) map[string]string {
	environment := miseBaseEnv(stellaHome)
	configPath := ScopeConfigPath(stellaHome, scope)
	environment["MISE_GLOBAL_CONFIG_FILE"] = configPath
	environment["MISE_TRUSTED_CONFIG_PATHS"] = configPath
	environment["MISE_SHIMS_DIR"] = shimsDir
	environment["MISE_NOT_FOUND_AUTO_INSTALL"] = "false"
	return environment
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
	return scopeMiseEnvWithShims(
		stellaHome,
		scope,
		filepath.Join(miseToolsDir(stellaHome), "shims"),
	)
}

func scopeMiseEnvWithShims(stellaHome, scope, shimsDir string) ([]string, error) {
	env, err := isolatedMiseEnvWithShims(stellaHome, shimsDir)
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

package manifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/resources/binaries"
)

// BinaryInstallPlan identifies one complete CLI selection. The identity is
// derived from every selected binary, including its config scope and revision,
// so two runners cannot overwrite one another's mise config or shim set.
// DataDir is shared only for immutable mise artifacts. ConfigPath and ShimsDir
// are used by the user-sandbox installer; native selections use PublicDir.
type BinaryInstallPlan struct {
	Identity   string
	DataDir    string
	ConfigPath string
	ShimsDir   string
	// PublicDir and PublicBinDir are native-only, exact selection paths. The
	// installer copies selected complete installs here and publishes direct
	// aliases, so a sandbox never needs the shared mise tree or its config.
	PublicDir    string
	PublicBinDir string
}

// BundledBinaryInstallPlan identifies the exact public selection for release
// bundled executables. The binary is copied with its adjacent runtime files;
// only a direct alias is published on the selection PATH.
type BundledBinaryInstallPlan struct {
	Identity     string
	ShimsDir     string // retained for the user-sandbox shape; native uses PublicBinDir
	PublicDir    string
	PublicBinDir string
}

// BinaryConfigLayer selects which mise precedence layer a plan represents.
// System selections replace MISE_SYSTEM_CONFIG_FILE; user selections replace
// MISE_GLOBAL_CONFIG_FILE. Both layers share the plan's selection-local shims.
type BinaryConfigLayer uint8

const (
	BinarySystemLayer BinaryConfigLayer = iota
	BinaryUserLayer
)

var nativePublicationMu sync.Mutex

// OverlayBinaryInstallPlan applies a completed plan to a runner environment.
// It only changes runner-owned mise fields and PATH; caller-supplied secrets or
// unrelated environment entries are copied unchanged.
func OverlayBinaryInstallPlan(base map[string]string, plan BinaryInstallPlan, layer BinaryConfigLayer) map[string]string {
	env := maps.Clone(base)
	if plan.PublicBinDir != "" {
		clearNativeMisePaths(env)
		if layer == BinaryUserLayer {
			env[pkgsandbox.EnvUserNativeSelectionDir] = plan.PublicBinDir
		} else {
			env[pkgsandbox.EnvNativeSelectionDir] = plan.PublicBinDir
		}
		env["PATH"] = prependPath(env["PATH"], plan.PublicBinDir)
		env[pkgsandbox.EnvRunnerPath] = env["PATH"]
	}
	env["MISE_SHIMS_DIR"] = plan.ShimsDir
	if plan.ConfigPath != "" {
		if layer == BinaryUserLayer {
			env["MISE_GLOBAL_CONFIG_FILE"] = plan.ConfigPath
		} else {
			env["MISE_SYSTEM_CONFIG_FILE"] = plan.ConfigPath
		}
		trusted := []string{plan.ConfigPath}
		for path := range strings.SplitSeq(env["MISE_TRUSTED_CONFIG_PATHS"], string(filepath.ListSeparator)) {
			if path == "" || path == plan.ConfigPath {
				continue
			}
			trusted = append(trusted, path)
		}
		env["MISE_TRUSTED_CONFIG_PATHS"] = strings.Join(trusted, string(filepath.ListSeparator))
	}
	env["PATH"] = prependPath(env["PATH"], plan.ShimsDir)
	env[pkgsandbox.EnvRunnerPath] = env["PATH"]
	return env
}

// OverlayBundledBinaryPlan adds the exact bundled selection directory to a
// runner environment. It does not expose the shared Stella bin directory.
func OverlayBundledBinaryPlan(base map[string]string, plan BundledBinaryInstallPlan) map[string]string {
	env := maps.Clone(base)
	publicBin := plan.PublicBinDir
	if publicBin == "" {
		publicBin = plan.ShimsDir
	}
	if publicBin == "" {
		delete(env, pkgsandbox.EnvBundledShimsDir)
		return env
	}
	clearNativeMisePaths(env)
	if env[pkgsandbox.EnvNativeSelectionDir] == "" {
		env[pkgsandbox.EnvNativeSelectionDir] = publicBin
	}
	env[pkgsandbox.EnvBundledShimsDir] = publicBin
	env["PATH"] = prependPath(env["PATH"], publicBin)
	env[pkgsandbox.EnvRunnerPath] = env["PATH"]
	return env
}

// FilterUnavailableBundledSkills removes bundled runtime skills whose trusted
// executable is absent for this platform or installation. Plugin owned skills
// keep their normal projection; only a skill with the exact bundled resource
// identity is coupled to that release binary.
func FilterUnavailableBundledSkills(stellaHome string, view pkgplugins.SessionPluginView) pkgplugins.SessionPluginView {
	if len(view.BundledBinarySpecs) == 0 {
		return view
	}
	unavailablePlugins := make(map[string]struct{}, len(view.BundledBinarySpecs))
	for _, spec := range view.BundledBinarySpecs {
		if binaries.ToolPath(stellaHome, spec.Name) == "" {
			unavailablePlugins[spec.PluginID] = struct{}{}
		}
	}
	if len(unavailablePlugins) == 0 {
		return view
	}
	view.ExposedPluginIDs = slices.DeleteFunc(slices.Clone(view.ExposedPluginIDs), func(id string) bool {
		_, unavailable := unavailablePlugins[id]
		return unavailable
	})
	view.SessionEnvSpecs = slices.DeleteFunc(slices.Clone(view.SessionEnvSpecs), func(spec pkgplugins.SessionEnvSpec) bool {
		_, unavailable := unavailablePlugins[spec.PluginID]
		return unavailable
	})
	view.BinarySpecs = slices.DeleteFunc(slices.Clone(view.BinarySpecs), func(spec pkgplugins.PluginBinarySpec) bool {
		_, unavailable := unavailablePlugins[spec.PluginID]
		return unavailable
	})
	view.BundledBinarySpecs = slices.DeleteFunc(slices.Clone(view.BundledBinarySpecs), func(spec pkgplugins.PluginBundledBinarySpec) bool {
		_, unavailable := unavailablePlugins[spec.PluginID]
		return unavailable
	})
	filtered := make([]pkgplugins.PluginSkillSpec, 0, len(view.SkillSpecs))
	for _, skill := range view.SkillSpecs {
		if _, unavailable := unavailablePlugins[skill.PluginID]; unavailable {
			continue
		}
		filtered = append(filtered, skill)
	}
	view.SkillSpecs = filtered
	return view
}

// InstallBundledBinaries materializes platform-available release binaries into
// an exact selection directory. Snapshot identity and config revision are part
// of the directory identity, so a changed or cross-scope selection cannot reuse
// an older publication. Missing platform assets are omitted.
func InstallBundledBinaries(stellaHome string, specs []pkgplugins.PluginBundledBinarySpec) (BundledBinaryInstallPlan, error) {
	identity, err := bundledBinarySelectionIdentity(specs)
	if err != nil {
		return BundledBinaryInstallPlan{}, err
	}
	plan := BundledBinaryInstallPlan{Identity: identity}
	if len(specs) == 0 {
		return plan, nil
	}

	root := filepath.Join(stellaHome, ".mise-tools", "public", identity)
	available := make([]struct {
		name   string
		source string
	}, 0, len(specs))
	for _, spec := range specs {
		source := binaries.ToolPath(stellaHome, spec.Name)
		if source == "" {
			continue
		}
		available = append(available, struct {
			name   string
			source string
		}{name: spec.Name, source: source})
	}
	if len(available) == 0 {
		return plan, nil
	}
	aliases := make([]string, 0, len(available))
	for _, binary := range available {
		aliases = appendUniqueNativeAlias(aliases, binary.name)
	}
	if err := publishNativeSelection(root, aliases, func(publicBin string) error {
		for _, binary := range available {
			if err := materializeBundledBinary(publicBin, binary.name, binary.source); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return BundledBinaryInstallPlan{}, err
	}
	plan.PublicDir = root
	plan.PublicBinDir = root
	return plan, nil
}

func bundledBinarySelectionIdentity(specs []pkgplugins.PluginBundledBinarySpec) (string, error) {
	canonical := slices.Clone(specs)
	slices.SortFunc(canonical, func(left, right pkgplugins.PluginBundledBinarySpec) int {
		for _, pair := range [][2]string{{left.PluginID, right.PluginID}, {left.Name, right.Name}, {left.ConfigID, right.ConfigID}, {left.Scope, right.Scope}} {
			if pair[0] != pair[1] {
				return strings.Compare(pair[0], pair[1])
			}
		}
		return cmpRevision(left.Revision, right.Revision)
	})
	seenNames := make(map[string]struct{}, len(canonical))
	for _, spec := range canonical {
		if spec.PluginID == "" || spec.ConfigID == "" || spec.Scope == "" || spec.Name == "" {
			return "", fmt.Errorf("manifest: bundled binary %q is missing resource identity", spec.Name)
		}
		if err := validateNativeBinaryName(spec.Name); err != nil {
			return "", fmt.Errorf("manifest: bundled binary %q: %w", spec.Name, err)
		}
		switch spec.Scope {
		case string(plugin.ScopeSystem), string(plugin.ScopeSystemAgent), string(plugin.ScopeUser), string(plugin.ScopeUserAgent):
		default:
			return "", fmt.Errorf("manifest: bundled binary %q has unknown resource scope %q", spec.Name, spec.Scope)
		}
		if spec.Revision <= 0 {
			return "", fmt.Errorf("manifest: bundled binary %q has non-positive config revision", spec.Name)
		}
		if _, exists := seenNames[spec.Name]; exists {
			return "", fmt.Errorf("manifest: duplicate bundled binary name %q", spec.Name)
		}
		seenNames[spec.Name] = struct{}{}
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("manifest: encode bundled binary selection identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:16]), nil
}

func cmpRevision(left, right int64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

// ContextBinaryInstallPlan returns the host-visible paths for one selected
// binary set. It performs no filesystem or network operation. Callers should
// pass all enabled namespace-winner binaries, across all scopes, in one call.
func ContextBinaryInstallPlan(stellaHome string, specs []pkgplugins.PluginBinarySpec) (BinaryInstallPlan, error) {
	if stellaHome == "" {
		return BinaryInstallPlan{}, errors.New("manifest: stella home is required")
	}
	identity, err := binarySelectionIdentity(specs)
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	dataDir := miseToolsDir(stellaHome)
	return BinaryInstallPlan{
		Identity:     identity,
		DataDir:      dataDir,
		PublicDir:    filepath.Join(dataDir, "public", identity),
		PublicBinDir: filepath.Join(dataDir, "public", identity),
	}, nil
}

// InstallContextBinaries installs trusted system and system-agent selections
// into the shared immutable mise artifact cache, then copies the selected full
// installs into a public selection with direct aliases. The temporary config is
// deleted before returning, so options never become cross-agent state.
func InstallContextBinaries(ctx context.Context, stellaHome string, specs []pkgplugins.PluginBinarySpec) (BinaryInstallPlan, error) {
	plan, err := ContextBinaryInstallPlan(stellaHome, specs)
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	tools, err := miseToolsFromSpecs(specs, func(spec pkgplugins.PluginBinarySpec) bool {
		return spec.Scope == string(plugin.ScopeSystem) || spec.Scope == string(plugin.ScopeSystemAgent)
	})
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	if len(tools) == 0 {
		// A user-only selection does not own the system config layer. Keep the
		// system layer unset, but still publish the core-only selection. This
		// prevents an empty snapshot from falling back to the entire host bin.
		plan.ConfigPath = ""
		plan.ShimsDir = ""
		if err := materializeNativeCoreSelection(stellaHome, plan); err != nil {
			return BinaryInstallPlan{}, err
		}
		return plan, nil
	}
	if err := runContextInstall(ctx, stellaHome, plan, tools); err != nil {
		return BinaryInstallPlan{}, err
	}
	return plan, nil
}

// InstallSandboxBinaries installs user and user-agent selections through the
// already-created sandbox session. The host only supplies validated specs;
// mise, its config write, and its cache writes all happen through the session
// capability. The selected system config must have been prepared with the
// same complete spec list first.
func InstallSandboxBinaries(ctx context.Context, session pkgsandbox.Session, specs []pkgplugins.PluginBinarySpec) (BinaryInstallPlan, error) {
	if session == nil {
		return BinaryInstallPlan{}, errors.New("manifest: sandbox session is required")
	}
	nativePublicationMu.Lock()
	defer nativePublicationMu.Unlock()
	identity, err := binarySelectionIdentity(specs)
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	baseEnv := session.Policy().Env
	dataDir := baseEnv["MISE_DATA_DIR"]
	if dataDir == "" || baseEnv["MISE_NOT_FOUND_AUTO_INSTALL"] != "true" {
		return BinaryInstallPlan{}, errors.New("manifest: user CLI install requires a writable sandbox mise home")
	}
	root := filepath.Join(dataDir, "contexts", identity)
	plan := BinaryInstallPlan{
		Identity:     identity,
		DataDir:      dataDir,
		ConfigPath:   filepath.Join(root, "config.toml"),
		ShimsDir:     filepath.Join(root, "shims"),
		PublicDir:    filepath.Join(dataDir, "public", identity),
		PublicBinDir: filepath.Join(dataDir, "public", identity),
	}
	tools, err := miseToolsFromSpecs(specs, func(spec pkgplugins.PluginBinarySpec) bool {
		return spec.Scope == string(plugin.ScopeUser) || spec.Scope == string(plugin.ScopeUserAgent)
	})
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	if len(tools) == 0 {
		return plan, nil
	}

	env := sandboxMiseEnv(baseEnv, plan)
	if _, err := session.Exec(ctx, sandboxMisePrepareCommand(), pkgsandbox.ExecOptions{Env: env}); err != nil {
		return BinaryInstallPlan{}, fmt.Errorf("manifest: prepare sandbox mise dirs: %w", err)
	}
	content, err := renderMiseTOML(tools)
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	if err := session.Files().WriteFile(plan.ConfigPath, []byte(content), 0o600); err != nil {
		return BinaryInstallPlan{}, fmt.Errorf("manifest: write sandbox mise config: %w", err)
	}
	result, err := session.Exec(ctx, sandboxMiseInstallCommand(), pkgsandbox.ExecOptions{Env: env})
	if err != nil {
		return BinaryInstallPlan{}, fmt.Errorf("manifest: install sandbox CLI binaries: %w", err)
	}
	if result.ExitCode != 0 {
		return BinaryInstallPlan{}, fmt.Errorf("manifest: install sandbox CLI binaries exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	publicEnv := maps.Clone(env)
	publicEnv["STELLA_NATIVE_PUBLIC_DIR"] = plan.PublicDir
	if result, err := session.Exec(ctx, sandboxMiseMaterializeCommand(tools), pkgsandbox.ExecOptions{Env: publicEnv}); err != nil {
		return BinaryInstallPlan{}, fmt.Errorf("manifest: publish sandbox CLI selection: %w", err)
	} else if result.ExitCode != 0 {
		return BinaryInstallPlan{}, fmt.Errorf("manifest: publish sandbox CLI selection exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	// ConfigPath and ShimsDir describe the private preparation session only. The
	// returned plan is consumed by the final session, which mounts PublicDir and
	// must never inherit the managed config or shim tree.
	plan.ConfigPath = ""
	plan.ShimsDir = ""
	return plan, nil
}

func sandboxMiseMaterializeCommand(tools []miseTool) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("stage=\"$STELLA_NATIVE_PUBLIC_DIR.tmp.$$\"\n")
	b.WriteString("trap 'rm -rf \"$stage\"' EXIT\n")
	b.WriteString("if [ -f \"$STELLA_NATIVE_PUBLIC_DIR/.selection-complete\" ]; then exit 0; fi\n")
	b.WriteString("rm -rf \"$stage\"\n")
	b.WriteString("mkdir -p \"$stage/installs\" \"$(dirname \"$STELLA_NATIVE_PUBLIC_DIR\")\"\n")
	for _, tool := range tools {
		alias := tool.PublicName
		if alias == "" {
			alias = tool.Lookup
		}
		key := nativeInstallKey(tool)
		fmt.Fprintf(&b, "install_dir=$(\"$STELLA_HOME/bin/mise\" where %s)\n", shellQuotePOSIX(tool.Key))
		fmt.Fprintf(&b, "binary_path=$(\"$STELLA_HOME/bin/mise\" which %s)\n", shellQuotePOSIX(tool.Lookup))
		b.WriteString("case \"$binary_path\" in \"$install_dir\"/*) ;; *) echo 'mise binary escaped install' >&2; exit 1 ;; esac\n")
		fmt.Fprintf(&b, "rel=\"${binary_path#\"$install_dir\"/}\"\n")
		fmt.Fprintf(&b, "mkdir -p \"$stage/installs/%s\"\n", key)
		fmt.Fprintf(&b, "cp -R \"$install_dir/.\" \"$stage/installs/%s/\"\n", key)
		fmt.Fprintf(&b, "ln -s \"installs/%s/$rel\" \"$stage\"/%s\n", key, shellQuotePOSIX(alias))
	}
	b.WriteString("touch \"$stage/.selection-complete\"\n")
	b.WriteString("if [ -e \"$STELLA_NATIVE_PUBLIC_DIR\" ]; then exit 0; fi\n")
	b.WriteString("mv \"$stage\" \"$STELLA_NATIVE_PUBLIC_DIR\"\n")
	b.WriteString("trap - EXIT\n")
	return b.String()
}

func shellQuotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func sandboxMisePrepareCommand() string {
	if runtime.GOOS == "windows" {
		return `if not exist "%STELLA_MISE_CONTEXT_DIR%" mkdir "%STELLA_MISE_CONTEXT_DIR%" && if not exist "%MISE_CONFIG_DIR%" mkdir "%MISE_CONFIG_DIR%" && if not exist "%MISE_SHIMS_DIR%" mkdir "%MISE_SHIMS_DIR%" && if not exist "%MISE_CACHE_DIR%" mkdir "%MISE_CACHE_DIR%" && if not exist "%MISE_STATE_DIR%" mkdir "%MISE_STATE_DIR%"`
	}
	return `mkdir -p "$MISE_CONFIG_DIR" "$(dirname "$MISE_GLOBAL_CONFIG_FILE")" "$MISE_SHIMS_DIR" "$MISE_CACHE_DIR" "$MISE_STATE_DIR"`
}

func sandboxMiseInstallCommand() string {
	if runtime.GOOS == "windows" {
		return `"%STELLA_HOME%\bin\mise.exe" trust "%MISE_GLOBAL_CONFIG_FILE%" && "%STELLA_HOME%\bin\mise.exe" install && "%STELLA_HOME%\bin\mise.exe" reshim`
	}
	return `"$STELLA_HOME/bin/mise" trust "$MISE_GLOBAL_CONFIG_FILE" && "$STELLA_HOME/bin/mise" install && "$STELLA_HOME/bin/mise" reshim`
}

func runContextInstall(ctx context.Context, stellaHome string, plan BinaryInstallPlan, tools []miseTool) (retErr error) {
	miseInstallMu.Lock()
	defer miseInstallMu.Unlock()

	miseBin, err := findMiseBin(stellaHome)
	if err != nil {
		return err
	}
	privateRoot := filepath.Join(stellaHome, ".mise-private")
	if err := ensureNativePrivateRoot(privateRoot); err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp(privateRoot, "install-")
	if err != nil {
		return fmt.Errorf("manifest: create native install dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	tempConfig := filepath.Join(tempDir, "config.toml")
	content, err := renderMiseTOML(tools)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tempConfig, []byte(content), 0o600); err != nil {
		return fmt.Errorf("manifest: write native mise config: %w", err)
	}
	if err := os.Chmod(tempConfig, 0o600); err != nil {
		return fmt.Errorf("manifest: chmod native mise config: %w", err)
	}
	systemConfig := filepath.Join(tempDir, "system.toml")
	if err := os.WriteFile(systemConfig, nil, 0o600); err != nil {
		return fmt.Errorf("manifest: write native system mise config: %w", err)
	}
	if err := os.Chmod(systemConfig, 0o600); err != nil {
		return fmt.Errorf("manifest: chmod native system mise config: %w", err)
	}
	defer func() {
		if err := removeNativeMiseConfig(tempConfig); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("manifest: remove native mise config: %w", err))
		}
		retErr = errors.Join(retErr, os.RemoveAll(tempDir))
	}()

	shimsDir := filepath.Join(tempDir, "shims")
	env, err := nativeMiseInstallEnv(stellaHome, plan.DataDir, shimsDir, tempDir, tempConfig, systemConfig)
	if err != nil {
		return err
	}
	for _, args := range [][]string{{"trust", tempConfig}, {"install"}} {
		if err := runMise(ctx, miseBin, env, tempDir, args...); err != nil {
			return fmt.Errorf("manifest: mise %s: %w", args[0], err)
		}
	}
	if err := materializeNativeSelection(ctx, stellaHome, plan, tools, miseBin, env, tempDir); err != nil {
		return err
	}
	return nil
}

// nativeMiseInstallEnv gives a context install its own config files and
// project ceiling. The ceiling stops mise's upward search before any parent
// mise.toml/.tool-versions file can contribute tools to the selection.
func nativeMiseInstallEnv(stellaHome, dataDir, shimsDir, configRoot, globalConfig, systemConfig string) ([]string, error) {
	env, err := isolatedMiseEnvAt(stellaHome, dataDir, shimsDir)
	if err != nil {
		return nil, err
	}
	ceiling, err := canonicalNativePath(configRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve native mise config ceiling: %w", err)
	}
	configDir := filepath.Join(filepath.Dir(globalConfig), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("create native mise config dir: %w", err)
	}
	return append(env,
		"MISE_CONFIG_DIR="+configDir,
		"MISE_GLOBAL_CONFIG_FILE="+globalConfig,
		"MISE_SYSTEM_CONFIG_FILE="+systemConfig,
		"MISE_TRUSTED_CONFIG_PATHS="+strings.Join([]string{globalConfig, systemConfig}, string(filepath.ListSeparator)),
		"MISE_PROJECT_ROOT="+ceiling,
		"MISE_CEILING_PATHS="+ceiling,
	), nil
}

func ensureNativePrivateRoot(root string) error {
	info, err := os.Lstat(root)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("manifest: native private root must be a directory: %s", root)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("manifest: inspect native private root: %w", err)
	} else if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("manifest: create native private root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("manifest: protect native private root: %w", err)
	}
	return nil
}

func removeNativeMiseConfig(path string) error { return os.Remove(path) }

func runMiseOutput(ctx context.Context, miseBin string, env []string, dir string, args ...string) (string, error) {
	var stdout bytes.Buffer
	cmd := managedCommandContext(ctx, miseBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", closedMiseError(ctx, args[0], err)
	}
	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return "", fmt.Errorf("mise %s returned empty output", args[0])
	}
	return output, nil
}

func materializeNativeSelection(ctx context.Context, stellaHome string, plan BinaryInstallPlan, tools []miseTool, miseBin string, env []string, dir string) error {
	aliases := nativeCoreAliases(stellaHome)
	for _, tool := range tools {
		aliasName := tool.PublicName
		if aliasName == "" {
			aliasName = tool.Lookup
		}
		aliases = appendUniqueNativeAlias(aliases, aliasName)
	}
	return publishNativeSelection(plan.PublicDir, aliases, func(publicDir string) error {
		selectionPlan := plan
		selectionPlan.PublicDir = publicDir
		selectionPlan.PublicBinDir = publicDir
		return materializeNativeSelectionAt(ctx, stellaHome, selectionPlan, tools, miseBin, env, dir)
	})
}

func materializeNativeSelectionAt(ctx context.Context, stellaHome string, plan BinaryInstallPlan, tools []miseTool, miseBin string, env []string, dir string) error {
	if err := os.MkdirAll(plan.PublicBinDir, 0o755); err != nil {
		return fmt.Errorf("manifest: create native public bin: %w", err)
	}
	if err := copyNativeCoreBinaries(stellaHome, plan.PublicBinDir); err != nil {
		return err
	}
	for _, tool := range tools {
		publicName := tool.PublicName
		if publicName == "" {
			publicName = tool.Lookup
		}
		installDir, err := runMiseOutput(ctx, miseBin, env, dir, "where", tool.Key)
		if err != nil {
			return fmt.Errorf("manifest: locate native install %q: %w", publicName, err)
		}
		binaryPath, err := runMiseOutput(ctx, miseBin, env, dir, "which", tool.Lookup)
		if err != nil {
			return fmt.Errorf("manifest: locate native binary %q: %w", publicName, err)
		}
		installDir, err = canonicalNativePath(installDir)
		if err != nil {
			return fmt.Errorf("manifest: resolve native install %q: %w", publicName, err)
		}
		binaryPath, err = canonicalNativePath(binaryPath)
		if err != nil {
			return fmt.Errorf("manifest: resolve native binary %q: %w", publicName, err)
		}
		rel, err := filepath.Rel(installDir, binaryPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("manifest: native binary %q escapes selected install", publicName)
		}
		targetRoot := filepath.Join(plan.PublicDir, "installs", nativeInstallKey(tool))
		if err := copyNativeTree(installDir, targetRoot); err != nil {
			return fmt.Errorf("manifest: copy native install %q: %w", publicName, err)
		}
		aliasName := tool.PublicName
		if aliasName == "" {
			aliasName = tool.Lookup
		}
		if err := publishNativeAlias(filepath.Join(plan.PublicBinDir, aliasName), filepath.Join(targetRoot, rel)); err != nil {
			return fmt.Errorf("manifest: publish native binary %q: %w", aliasName, err)
		}
	}
	return nil
}

// canonicalNativePath makes paths reported by separate mise commands
// comparable on platforms where an OS-managed alias such as /var resolves to
// /private/var. The subsequent relative-path check still rejects binaries
// whose resolved target is outside the resolved install root.
func canonicalNativePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func closedMiseError(ctx context.Context, stage string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("mise %s: %w", stage, ctxErr)
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		return fmt.Errorf("mise %s failed with exit code %d", stage, exitErr.ExitCode())
	}
	return fmt.Errorf("mise %s failed", stage)
}

func materializeNativeCoreSelection(stellaHome string, plan BinaryInstallPlan) error {
	return publishNativeSelection(plan.PublicDir, nativeCoreAliases(stellaHome), func(publicDir string) error {
		return copyNativeCoreBinaries(stellaHome, publicDir)
	})
}

func nativeCoreAliases(stellaHome string) []string {
	aliases := make([]string, 0, 1)
	names := []string{".stella-shell-env"}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(stellaHome, "bin", name)); err == nil {
			aliases = append(aliases, name)
		}
	}
	return aliases
}

func appendUniqueNativeAlias(aliases []string, alias string) []string {
	if alias == "" {
		return aliases
	}
	if slices.Contains(aliases, alias) {
		return aliases
	}
	return append(aliases, alias)
}

func publishNativeSelection(root string, aliases []string, build func(string) error) error {
	nativePublicationMu.Lock()
	defer nativePublicationMu.Unlock()

	if nativePublicationComplete(root, aliases) {
		return nil
	}
	if _, err := os.Lstat(root); err == nil {
		return fmt.Errorf("manifest: native selection %q exists but is incomplete", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("manifest: inspect native selection %q: %w", root, err)
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("manifest: create native selection parent: %w", err)
	}
	temp, err := os.MkdirTemp(parent, ".native-selection-")
	if err != nil {
		return fmt.Errorf("manifest: create native selection staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := build(temp); err != nil {
		return err
	}
	if err := os.Chmod(temp, 0o755); err != nil {
		return fmt.Errorf("manifest: finalize native selection staging dir: %w", err)
	}
	if nativePublicationComplete(root, aliases) {
		return nil
	}
	if _, err := os.Lstat(root); err == nil {
		return fmt.Errorf("manifest: native selection %q appeared incomplete during publication", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("manifest: inspect native selection %q before publication: %w", root, err)
	}
	if err := os.Rename(temp, root); err != nil {
		return fmt.Errorf("manifest: publish native selection: %w", err)
	}
	return nil
}

func nativePublicationComplete(root string, aliases []string) bool {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	for _, alias := range aliases {
		info, err := os.Stat(filepath.Join(root, alias))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func nativeInstallKey(tool miseTool) string {
	digest := sha256.Sum256([]byte(tool.Key + "\x00" + tool.Lookup))
	return hex.EncodeToString(digest[:8])
}

func copyNativeCoreBinaries(stellaHome, publicBin string) error {
	names := []string{".stella-shell-env"}
	for _, name := range names {
		source := filepath.Join(stellaHome, "bin", name)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("manifest: inspect core runtime %q: %w", name, err)
		}
		if err := copyNativeFile(source, filepath.Join(publicBin, name)); err != nil {
			return fmt.Errorf("manifest: publish core runtime %q: %w", name, err)
		}
	}
	return nil
}

func materializeBundledBinary(publicBin, name, source string) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("manifest: resolve bundled binary %q: %w", name, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("manifest: stat bundled binary %q: %w", name, err)
	}
	var target string
	root := filepath.Join(publicBin, "bundled", name)
	if info.IsDir() {
		return fmt.Errorf("manifest: bundled binary %q resolves to a directory", name)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("manifest: create bundled runtime %q: %w", name, err)
	}
	if err := copyNativeFile(resolved, filepath.Join(root, filepath.Base(resolved))); err != nil {
		return fmt.Errorf("manifest: copy bundled binary %q: %w", name, err)
	}
	target = filepath.Join(root, filepath.Base(resolved))
	if linkInfo, linkErr := os.Lstat(source); linkErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
		// A versioned embedded bundle is exposed through a launcher symlink.
		bundleDir := filepath.Dir(resolved)
		if filepath.Clean(bundleDir) != filepath.Clean(filepath.Dir(source)) {
			if err := os.RemoveAll(root); err != nil {
				return err
			}
			if err := copyNativeTree(bundleDir, root); err != nil {
				return fmt.Errorf("manifest: copy bundled runtime %q: %w", name, err)
			}
			target = filepath.Join(root, filepath.Base(resolved))
		}
	}
	return publishNativeAlias(filepath.Join(publicBin, name), target)
}

func copyNativeTree(source, destination string) error {
	root, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory")
	}
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open source root: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	return fs.WalkDir(sourceRoot.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		dest := destination
		if name != "." {
			dest = filepath.Join(destination, filepath.FromSlash(name))
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := sourceRoot.Readlink(name)
			if err != nil {
				return err
			}
			if err := validateNativeSymlinkTarget(sourceRoot.FS(), name, target); err != nil {
				return fmt.Errorf("symlink %q: %w", name, err)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, dest)
		}
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		return copyNativeRootFile(sourceRoot, name, dest)
	})
}

func validateNativeSymlinkTarget(root fs.FS, name, target string) error {
	if target == "" || filepath.IsAbs(target) || strings.HasPrefix(target, "/") || strings.ContainsRune(target, '\\') {
		return fmt.Errorf("absolute or unsafe target %q is not portable", target)
	}
	links, ok := root.(fs.ReadLinkFS)
	if !ok {
		return errors.New("source root does not support symlink inspection")
	}
	current := pathpkg.Clean(pathpkg.Join(pathpkg.Dir(name), target))
	seen := make(map[string]struct{})
	for {
		if current == ".." || strings.HasPrefix(current, "../") || !fs.ValidPath(current) {
			return errors.New("target escapes install")
		}
		if _, exists := seen[current]; exists {
			return errors.New("target contains a symlink cycle")
		}
		seen[current] = struct{}{}
		info, err := fs.Lstat(root, current)
		if err != nil {
			return fmt.Errorf("resolve target: %w", err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return nil
		}
		next, err := links.ReadLink(current)
		if err != nil {
			return fmt.Errorf("read target: %w", err)
		}
		if next == "" || filepath.IsAbs(next) || strings.HasPrefix(next, "/") || strings.ContainsRune(next, '\\') {
			return fmt.Errorf("absolute or unsafe target %q is not portable", next)
		}
		current = pathpkg.Clean(pathpkg.Join(pathpkg.Dir(current), next))
	}
}

func copyNativeRootFile(root *os.Root, name, destination string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(destination, info.Mode().Perm())
}

func copyNativeFile(source, destination string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(destination, info.Mode().Perm())
}

func publishNativeAlias(alias, target string) error {
	if err := os.Remove(alias); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rel, err := filepath.Rel(filepath.Dir(alias), target)
	if err != nil {
		return err
	}
	return os.Symlink(rel, alias)
}

func clearNativeMisePaths(env map[string]string) {
	for _, key := range []string{"MISE_SHIMS_DIR", "MISE_TRUSTED_CONFIG_PATHS", "MISE_SYSTEM_CONFIG_FILE"} {
		delete(env, key)
	}
	// A native system selection must not expose the shared system mise tree. A
	// user selection still needs its own writable tree for InstallSandboxBinaries;
	// the explicit auto-install flag is the trusted marker already emitted by
	// RuntimeMiseEnv for that private scope.
	if env["MISE_NOT_FOUND_AUTO_INSTALL"] == "true" {
		return
	}
	for _, key := range []string{"MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR", "MISE_GLOBAL_CONFIG_FILE"} {
		delete(env, key)
	}
}

func sandboxMiseEnv(base map[string]string, plan BinaryInstallPlan) map[string]string {
	env := maps.Clone(base)
	env["MISE_DATA_DIR"] = plan.DataDir
	env["MISE_CONFIG_DIR"] = filepath.Join(plan.DataDir, "config")
	env["MISE_CACHE_DIR"] = filepath.Join(plan.DataDir, "cache")
	env["MISE_STATE_DIR"] = filepath.Join(plan.DataDir, "state")
	env["MISE_SHIMS_DIR"] = plan.ShimsDir
	env["MISE_GLOBAL_CONFIG_FILE"] = plan.ConfigPath
	env["STELLA_MISE_CONTEXT_DIR"] = filepath.Dir(plan.ConfigPath)
	trusted := []string{plan.ConfigPath}
	if system := base["MISE_SYSTEM_CONFIG_FILE"]; system != "" {
		trusted = append(trusted, system)
	}
	env["MISE_TRUSTED_CONFIG_PATHS"] = strings.Join(trusted, string(filepath.ListSeparator))
	return env
}

func prependPath(pathValue, entry string) string {
	if entry == "" {
		return pathValue
	}
	if pathValue == "" {
		return entry
	}
	return entry + string(filepath.ListSeparator) + pathValue
}

func miseToolsFromSpecs(specs []pkgplugins.PluginBinarySpec, keep func(pkgplugins.PluginBinarySpec) bool) ([]miseTool, error) {
	tools := make([]miseTool, 0, len(specs))
	seen := make(map[string]miseTool)
	for _, spec := range specs {
		if !keep(spec) {
			continue
		}
		if spec.Name == "" || spec.Tool == "" {
			return nil, fmt.Errorf("manifest: binary %q has incomplete identity", spec.Name)
		}
		tool := miseToolFromBinary(ManifestBinary{
			Name: spec.Name, Tool: spec.Tool, Version: spec.Version, Options: maps.Clone(spec.Options),
		})
		if previous, exists := seen[tool.Key]; exists && !reflect.DeepEqual(previous, tool) {
			return nil, fmt.Errorf("manifest: selected binaries disagree on mise tool %q", tool.Key)
		}
		if _, exists := seen[tool.Key]; !exists {
			seen[tool.Key] = tool
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

func binarySelectionIdentity(specs []pkgplugins.PluginBinarySpec) (string, error) {
	canonical := slices.Clone(specs)
	slices.SortFunc(canonical, func(left, right pkgplugins.PluginBinarySpec) int {
		for _, pair := range [][2]string{{left.PluginID, right.PluginID}, {left.Name, right.Name}, {left.Tool, right.Tool}, {left.Version, right.Version}, {left.ConfigID, right.ConfigID}, {left.Scope, right.Scope}} {
			if pair[0] != pair[1] {
				return strings.Compare(pair[0], pair[1])
			}
		}
		if left.Revision < right.Revision {
			return -1
		}
		if left.Revision > right.Revision {
			return 1
		}
		return 0
	})
	for _, spec := range canonical {
		if spec.PluginID == "" || spec.ConfigID == "" || spec.Scope == "" || spec.Name == "" || spec.Tool == "" {
			return "", fmt.Errorf("manifest: binary %q is missing resource identity", spec.Name)
		}
		if err := validateNativeBinaryName(spec.Name); err != nil {
			return "", fmt.Errorf("manifest: binary %q: %w", spec.Name, err)
		}
		switch spec.Scope {
		case string(plugin.ScopeSystem), string(plugin.ScopeSystemAgent), string(plugin.ScopeUser), string(plugin.ScopeUserAgent):
		default:
			return "", fmt.Errorf("manifest: binary %q has unknown resource scope %q", spec.Name, spec.Scope)
		}
		if spec.Revision <= 0 {
			return "", fmt.Errorf("manifest: binary %q has non-positive config revision", spec.Name)
		}
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("manifest: encode binary selection identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:16]), nil
}

func validateNativeBinaryName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return errors.New("has unsafe path name")
	}
	if strings.ContainsRune(name, 0) {
		return errors.New("contains NUL")
	}
	return nil
}

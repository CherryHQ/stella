package toolinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Selection names the immutable public directory for one tool selection. It
// intentionally carries no plugin or configuration identity.
type Selection struct {
	DataDir       string
	ConfigPath    string
	ShimsDir      string
	PublicDir     string
	PublicBinDir  string
	EmbeddedNames []string
}

// InstallSession installs tools through an already-created sandbox session.
// The caller chooses the private selection paths; this package only performs
// the mise operations and publishes the resulting immutable selection.
func InstallSession(ctx context.Context, session pkgsandbox.Session, selection Selection, tools []Tool) error {
	if session == nil {
		return errors.New("toolinstall: sandbox session is required")
	}
	if selection.DataDir == "" || selection.ConfigPath == "" || selection.ShimsDir == "" || selection.PublicDir == "" {
		return errors.New("toolinstall: session selection paths are required")
	}
	if len(tools) == 0 {
		return nil
	}
	nativePublicationMu.Lock()
	defer nativePublicationMu.Unlock()
	baseEnv := session.Policy().Env
	if baseEnv["MISE_DATA_DIR"] == "" || baseEnv["MISE_NOT_FOUND_AUTO_INSTALL"] != "true" {
		return errors.New("toolinstall: user CLI install requires a writable sandbox mise home")
	}
	env := sessionMiseEnv(baseEnv, selection)
	if _, err := session.Exec(ctx, sandboxMisePrepareCommand(), pkgsandbox.ExecOptions{Env: env}); err != nil {
		return fmt.Errorf("toolinstall: prepare sandbox mise dirs: %w", err)
	}
	content, err := RenderTOML(tools)
	if err != nil {
		return err
	}
	if err := session.Files().WriteFile(selection.ConfigPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("toolinstall: write sandbox mise config: %w", err)
	}
	result, err := session.Exec(ctx, sandboxMiseInstallCommand(), pkgsandbox.ExecOptions{Env: env})
	if err != nil {
		return fmt.Errorf("toolinstall: install sandbox CLI binaries: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("toolinstall: install sandbox CLI binaries exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	publicEnv := maps.Clone(env)
	publicEnv["STELLA_NATIVE_PUBLIC_DIR"] = selection.PublicDir
	if result, err := session.Exec(ctx, sandboxMiseMaterializeCommand(tools), pkgsandbox.ExecOptions{Env: publicEnv}); err != nil {
		return fmt.Errorf("toolinstall: publish sandbox CLI selection: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("toolinstall: publish sandbox CLI selection exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// InstallSelection installs fixed, release-owned mise tools into an
// exact public selection. The caller must have extracted embedded runtimes
// before invoking this primitive, so core and plugin packages share the same
// lower-level install and atomic publication behavior without importing each
// other's ownership model.
func InstallSelection(ctx context.Context, stellaHome string, selection Selection, tools []Tool) error {
	if stellaHome == "" || selection.DataDir == "" || selection.PublicDir == "" {
		return errors.New("toolinstall: selection paths are required")
	}
	publicBinDir := selection.PublicBinDir
	if publicBinDir == "" {
		publicBinDir = selection.PublicDir
	}
	selection.PublicBinDir = publicBinDir
	if len(tools) == 0 {
		return materializeNativeRuntimeSelection(stellaHome, selection)
	}
	return runSelectionInstall(ctx, stellaHome, selection, tools)
}

var nativePublicationMu sync.Mutex

func sandboxMiseMaterializeCommand(tools []Tool) string {
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

func runSelectionInstall(ctx context.Context, stellaHome string, plan Selection, tools []Tool) error {
	miseInstallMu.Lock()
	defer miseInstallMu.Unlock()
	if nativePublicationComplete(plan.PublicDir, nativeSelectionAliases(stellaHome, plan, tools)) {
		return nil
	}

	return withNativeMiseInstall(ctx, stellaHome, plan.DataDir, tools, func(miseBin string, env []string, dir string) error {
		return materializeNativeSelection(ctx, stellaHome, plan, tools, miseBin, env, dir)
	})
}

// withNativeMiseInstall owns the temporary installation configuration. Callers
// hold miseInstallMu until optional selection publication and cleanup finish.
func withNativeMiseInstall(ctx context.Context, stellaHome, dataDir string, tools []Tool, publish func(string, []string, string) error) (retErr error) {
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
		return fmt.Errorf("toolinstall: create native install dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	tempConfig := filepath.Join(tempDir, "config.toml")
	content, err := RenderTOML(tools)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tempConfig, []byte(content), 0o600); err != nil {
		return fmt.Errorf("toolinstall: write native mise config: %w", err)
	}
	if err := os.Chmod(tempConfig, 0o600); err != nil {
		return fmt.Errorf("toolinstall: chmod native mise config: %w", err)
	}
	systemConfig := filepath.Join(tempDir, "system.toml")
	if err := os.WriteFile(systemConfig, nil, 0o600); err != nil {
		return fmt.Errorf("toolinstall: write native system mise config: %w", err)
	}
	if err := os.Chmod(systemConfig, 0o600); err != nil {
		return fmt.Errorf("toolinstall: chmod native system mise config: %w", err)
	}
	defer func() {
		if err := removeNativeMiseConfig(tempConfig); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("toolinstall: remove native mise config: %w", err))
		}
		retErr = errors.Join(retErr, os.RemoveAll(tempDir))
	}()

	shimsDir := filepath.Join(tempDir, "shims")
	env, err := nativeMiseInstallEnv(stellaHome, dataDir, shimsDir, tempDir, tempConfig, systemConfig)
	if err != nil {
		return err
	}
	for _, args := range [][]string{{"trust", tempConfig}, {"install"}} {
		if err := runMise(ctx, miseBin, env, tempDir, args...); err != nil {
			return fmt.Errorf("toolinstall: mise %s: %w", args[0], err)
		}
	}
	if publish != nil {
		return publish(miseBin, env, tempDir)
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
			return fmt.Errorf("toolinstall: native private root must be a directory: %s", root)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("toolinstall: inspect native private root: %w", err)
	} else if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("toolinstall: create native private root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("toolinstall: protect native private root: %w", err)
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

func materializeNativeSelection(ctx context.Context, stellaHome string, plan Selection, tools []Tool, miseBin string, env []string, dir string) error {
	aliases := nativeSelectionAliases(stellaHome, plan, tools)
	return publishNativeSelection(plan.PublicDir, aliases, func(publicDir string) error {
		selectionPlan := plan
		selectionPlan.PublicDir = publicDir
		selectionPlan.PublicBinDir = publicDir
		return materializeNativeSelectionAt(ctx, stellaHome, selectionPlan, tools, miseBin, env, dir)
	})
}

func nativeSelectionAliases(stellaHome string, plan Selection, tools []Tool) []string {
	aliases := nativeRuntimeAliases(stellaHome, plan.EmbeddedNames)
	for _, tool := range tools {
		aliasName := tool.PublicName
		if aliasName == "" {
			aliasName = tool.Lookup
		}
		aliases = appendUniqueNativeAlias(aliases, aliasName)
	}
	return aliases
}

func materializeNativeSelectionAt(ctx context.Context, stellaHome string, plan Selection, tools []Tool, miseBin string, env []string, dir string) error {
	if err := os.MkdirAll(plan.PublicBinDir, 0o755); err != nil {
		return fmt.Errorf("toolinstall: create native public bin: %w", err)
	}
	if err := copyNativeRuntimeBinaries(stellaHome, plan.PublicBinDir, plan.EmbeddedNames); err != nil {
		return err
	}
	for _, tool := range tools {
		publicName := tool.PublicName
		if publicName == "" {
			publicName = tool.Lookup
		}
		installDir, err := runMiseOutput(ctx, miseBin, env, dir, "where", tool.Key)
		if err != nil {
			return fmt.Errorf("toolinstall: locate native install %q: %w", publicName, err)
		}
		binaryPath, err := runMiseOutput(ctx, miseBin, env, dir, "which", tool.Lookup)
		if err != nil {
			return fmt.Errorf("toolinstall: locate native binary %q: %w", publicName, err)
		}
		installDir, err = canonicalNativePath(installDir)
		if err != nil {
			return fmt.Errorf("toolinstall: resolve native install %q: %w", publicName, err)
		}
		binaryPath, err = canonicalNativePath(binaryPath)
		if err != nil {
			return fmt.Errorf("toolinstall: resolve native binary %q: %w", publicName, err)
		}
		rel, err := filepath.Rel(installDir, binaryPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("toolinstall: native binary %q escapes selected install", publicName)
		}
		targetRoot := filepath.Join(plan.PublicDir, "installs", nativeInstallKey(tool))
		if err := copyNativeTree(installDir, targetRoot); err != nil {
			return fmt.Errorf("toolinstall: copy native install %q: %w", publicName, err)
		}
		aliasName := tool.PublicName
		if aliasName == "" {
			aliasName = tool.Lookup
		}
		if err := publishNativeAlias(filepath.Join(plan.PublicBinDir, aliasName), filepath.Join(targetRoot, rel)); err != nil {
			return fmt.Errorf("toolinstall: publish native binary %q: %w", aliasName, err)
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

func materializeNativeRuntimeSelection(stellaHome string, plan Selection) error {
	return publishNativeSelection(plan.PublicDir, nativeRuntimeAliases(stellaHome, plan.EmbeddedNames), func(publicDir string) error {
		return copyNativeRuntimeBinaries(stellaHome, publicDir, plan.EmbeddedNames)
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

func nativeRuntimeAliases(stellaHome string, names []string) []string {
	aliases := nativeCoreAliases(stellaHome)
	for _, name := range names {
		publicName := runtimeBinaryName(name)
		if _, err := os.Stat(filepath.Join(stellaHome, "bin", publicName)); err == nil {
			aliases = appendUniqueNativeAlias(aliases, publicName)
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
		return fmt.Errorf("toolinstall: native selection %q exists but is incomplete", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("toolinstall: inspect native selection %q: %w", root, err)
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("toolinstall: create native selection parent: %w", err)
	}
	temp, err := os.MkdirTemp(parent, ".native-selection-")
	if err != nil {
		return fmt.Errorf("toolinstall: create native selection staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := build(temp); err != nil {
		return err
	}
	if err := os.Chmod(temp, 0o755); err != nil {
		return fmt.Errorf("toolinstall: finalize native selection staging dir: %w", err)
	}
	if nativePublicationComplete(root, aliases) {
		return nil
	}
	if _, err := os.Lstat(root); err == nil {
		return fmt.Errorf("toolinstall: native selection %q appeared incomplete during publication", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("toolinstall: inspect native selection %q before publication: %w", root, err)
	}
	if err := os.Rename(temp, root); err != nil {
		return fmt.Errorf("toolinstall: publish native selection: %w", err)
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

func nativeInstallKey(tool Tool) string {
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
			return fmt.Errorf("toolinstall: inspect core runtime %q: %w", name, err)
		}
		if err := copyNativeFile(source, filepath.Join(publicBin, name)); err != nil {
			return fmt.Errorf("toolinstall: publish core runtime %q: %w", name, err)
		}
	}
	return nil
}

func copyNativeRuntimeBinaries(stellaHome, publicBin string, names []string) error {
	if err := copyNativeCoreBinaries(stellaHome, publicBin); err != nil {
		return err
	}
	for _, name := range names {
		source := filepath.Join(stellaHome, "bin", runtimeBinaryName(name))
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("toolinstall: inspect core runtime %q: %w", name, err)
		}
		publicName := runtimeBinaryName(name)
		if name == "xberg" {
			if err := materializeBundledBinary(publicBin, name, source); err != nil {
				return fmt.Errorf("toolinstall: publish core runtime %q: %w", name, err)
			}
			continue
		}
		if err := copyNativeFile(source, filepath.Join(publicBin, publicName)); err != nil {
			return fmt.Errorf("toolinstall: publish core runtime %q: %w", name, err)
		}
	}
	return nil
}

func materializeBundledBinary(publicBin, name, source string) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("toolinstall: resolve bundled binary %q: %w", name, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("toolinstall: stat bundled binary %q: %w", name, err)
	}
	var target string
	root := filepath.Join(publicBin, "bundled", name)
	if info.IsDir() {
		return fmt.Errorf("toolinstall: bundled binary %q resolves to a directory", name)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("toolinstall: create bundled runtime %q: %w", name, err)
	}
	if err := copyNativeFile(resolved, filepath.Join(root, filepath.Base(resolved))); err != nil {
		return fmt.Errorf("toolinstall: copy bundled binary %q: %w", name, err)
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
				return fmt.Errorf("toolinstall: copy bundled runtime %q: %w", name, err)
			}
			target = filepath.Join(root, filepath.Base(resolved))
		}
	}
	return publishNativeAlias(filepath.Join(publicBin, name), target)
}

func copyNativeTree(source, destination string) error {
	lexicalRoot, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
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
			portableTarget, err := validateNativeSymlinkTarget(sourceRoot.FS(), root, lexicalRoot, name, target)
			if err != nil {
				return fmt.Errorf("symlink %q: %w", name, err)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return os.Symlink(portableTarget, dest)
		}
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		return copyNativeRootFile(sourceRoot, name, dest)
	})
}

func validateNativeSymlinkTarget(root fs.FS, canonicalRoot, lexicalRoot, name, target string) (string, error) {
	links, ok := root.(fs.ReadLinkFS)
	if !ok {
		return "", errors.New("source root does not support symlink inspection")
	}
	target, err := portableNativeSymlinkTarget(canonicalRoot, lexicalRoot, name, target)
	if err != nil {
		return "", err
	}
	current := pathpkg.Clean(pathpkg.Join(pathpkg.Dir(name), target))
	seen := make(map[string]struct{})
	for {
		if current == ".." || strings.HasPrefix(current, "../") || !fs.ValidPath(current) {
			return "", errors.New("target escapes install")
		}
		if _, exists := seen[current]; exists {
			return "", errors.New("target contains a symlink cycle")
		}
		seen[current] = struct{}{}
		info, err := fs.Lstat(root, current)
		if err != nil {
			return "", fmt.Errorf("resolve target: %w", err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return target, nil
		}
		next, err := links.ReadLink(current)
		if err != nil {
			return "", fmt.Errorf("read target: %w", err)
		}
		next, err = portableNativeSymlinkTarget(canonicalRoot, lexicalRoot, current, next)
		if err != nil {
			return "", err
		}
		current = pathpkg.Clean(pathpkg.Join(pathpkg.Dir(current), next))
	}
}

func portableNativeSymlinkTarget(canonicalRoot, lexicalRoot, name, target string) (string, error) {
	if target == "" || strings.ContainsRune(target, '\\') {
		return "", fmt.Errorf("absolute or unsafe target %q is not portable", target)
	}
	if !filepath.IsAbs(target) && !strings.HasPrefix(target, "/") {
		return target, nil
	}
	if canonicalRoot == "" || lexicalRoot == "" {
		return "", fmt.Errorf("absolute or unsafe target %q is not portable", target)
	}
	targetAbs := filepath.Clean(target)
	rel, err := filepath.Rel(canonicalRoot, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		rel, err = filepath.Rel(lexicalRoot, targetAbs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", fmt.Errorf("absolute or unsafe target %q is not portable", target)
		}
		targetAbs = filepath.Join(canonicalRoot, rel)
	}
	linkAbs := filepath.Join(canonicalRoot, filepath.FromSlash(name))
	portable, err := filepath.Rel(filepath.Dir(linkAbs), targetAbs)
	if err != nil || portable == "" {
		return "", fmt.Errorf("absolute or unsafe target %q is not portable", target)
	}
	return filepath.ToSlash(portable), nil
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

func sessionMiseEnv(base map[string]string, selection Selection) map[string]string {
	env := maps.Clone(base)
	env["MISE_DATA_DIR"] = selection.DataDir
	env["MISE_CONFIG_DIR"] = filepath.Join(selection.DataDir, "config")
	env["MISE_CACHE_DIR"] = filepath.Join(selection.DataDir, "cache")
	env["MISE_STATE_DIR"] = filepath.Join(selection.DataDir, "state")
	env["MISE_SHIMS_DIR"] = selection.ShimsDir
	env["MISE_GLOBAL_CONFIG_FILE"] = selection.ConfigPath
	env["STELLA_MISE_CONTEXT_DIR"] = filepath.Dir(selection.ConfigPath)
	trusted := []string{selection.ConfigPath}
	if system := base["MISE_SYSTEM_CONFIG_FILE"]; system != "" {
		trusted = append(trusted, system)
	}
	env["MISE_TRUSTED_CONFIG_PATHS"] = strings.Join(trusted, string(filepath.ListSeparator))
	return env
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

package manifestplugins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// miseToolsDir returns the MISE_DATA_DIR path for isolated mise installs.
func miseToolsDir(annaHome string) string {
	return filepath.Join(annaHome, ".mise-tools")
}

// findMiseBin returns the path to the mise binary. It prefers the Anna-managed
// binary in $ANNA_HOME/bin, then falls back to mise on PATH.
func findMiseBin(annaHome string) (string, error) {
	local := filepath.Join(annaHome, "bin", runtimeBinaryName("mise"))
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	if path, err := exec.LookPath("mise"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("mise not found at %s or on PATH", local)
}

func bootstrapMise(_ context.Context, annaHome string) error {
	_, err := findMiseBin(annaHome)
	return err
}

var misePassthroughEnv = []string{
	"PATH",
	"SystemRoot",
	"WINDIR",
	"ComSpec",
	"PATHEXT",
	"TMPDIR",
	"TMP",
	"TEMP",
	"LANG",
	"LC_ALL",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"ALL_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"all_proxy",
	"no_proxy",
	"GITHUB_TOKEN",
	"GH_TOKEN",
}

func isolatedMiseEnv(annaHome string) ([]string, error) {
	dataDir := miseToolsDir(annaHome)
	paths := map[string]string{
		"MISE_DATA_DIR":   dataDir,
		"MISE_CONFIG_DIR": filepath.Join(dataDir, "config"),
		"MISE_CACHE_DIR":  filepath.Join(dataDir, "cache"),
		"MISE_STATE_DIR":  filepath.Join(dataDir, "state"),
		"MISE_SHIMS_DIR":  filepath.Join(dataDir, "shims"),
		"HOME":            filepath.Join(dataDir, "home"),
		"XDG_CONFIG_HOME": filepath.Join(dataDir, "xdg", "config"),
		"XDG_CACHE_HOME":  filepath.Join(dataDir, "xdg", "cache"),
		"XDG_STATE_HOME":  filepath.Join(dataDir, "xdg", "state"),
	}
	if runtime.GOOS == "windows" {
		paths["USERPROFILE"] = paths["HOME"]
	}

	for _, dir := range paths {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create isolated mise dir %s: %w", dir, err)
		}
	}

	env := make(map[string]string, len(misePassthroughEnv)+len(paths)+2)
	for _, key := range misePassthroughEnv {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	maps.Copy(env, paths)
	env["MISE_YES"] = "1"
	env["MISE_NO_ANALYTICS"] = "1"

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out, nil
}

func runtimeBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// resolvedBackend returns the backend prefix from the Tool key (e.g. "github").
func (b ManifestBinary) resolvedBackend() string {
	if idx := strings.IndexByte(b.Tool, ':'); idx >= 0 {
		return b.Tool[:idx]
	}
	return ""
}

// miseToolKey returns the mise tool key used in mise.toml and CLI commands.
func (b ManifestBinary) miseToolKey() string {
	return b.Tool
}

// sharedOptions populates the options shared across github and http backends.
func sharedOptions(b ManifestBinary, m map[string]any) {
	if b.StripComponents > 0 {
		m["strip_components"] = b.StripComponents
	}
	if b.BinPath != "" {
		m["bin_path"] = b.BinPath
	}
	if b.Bin != "" {
		m["bin"] = b.Bin
	}
	if b.RenameExe != "" {
		m["rename_exe"] = b.RenameExe
	}
	if b.Checksum != "" {
		m["checksum"] = b.Checksum
	}
}

// generateMiseTOML returns a valid mise.toml for the binary's backend.
// When Version is empty, "latest" is used for github/pipx/npm. For the http
// backend with a templated URL, an explicit version is required.
func generateMiseTOML(b ManifestBinary) (string, error) {
	toolKey := b.miseToolKey()
	if toolKey == "" {
		return "", fmt.Errorf("cannot determine mise tool key: set backend or repo/url/package")
	}

	ver := b.Version
	if ver == "" {
		ver = "latest"
	}

	var toolValue any
	switch b.resolvedBackend() {
	case "github":
		m := map[string]any{"version": ver}
		if b.AssetPattern != "" {
			m["asset_pattern"] = b.AssetPattern
		}
		if b.VersionPrefix != "" {
			m["version_prefix"] = b.VersionPrefix
		}
		if b.NoApp {
			m["no_app"] = true
		}
		if b.FilterBins != "" {
			m["filter_bins"] = b.FilterBins
		}
		if b.Prerelease {
			m["prerelease"] = true
		}
		if b.APIURL != "" {
			m["api_url"] = b.APIURL
		}
		sharedOptions(b, m)
		if len(m) == 1 {
			toolValue = ver
		} else {
			toolValue = m
		}

	case "http":
		m := map[string]any{"version": ver, "url": b.URL}
		if b.Size != "" {
			m["size"] = b.Size
		}
		if b.Format != "" {
			m["format"] = b.Format
		}
		if b.VersionListURL != "" {
			m["version_list_url"] = b.VersionListURL
		}
		if b.VersionRegex != "" {
			m["version_regex"] = b.VersionRegex
		}
		if b.VersionJSONPath != "" {
			m["version_json_path"] = b.VersionJSONPath
		}
		if b.VersionExpr != "" {
			m["version_expr"] = b.VersionExpr
		}
		sharedOptions(b, m)
		toolValue = m

	case "pipx":
		m := map[string]any{"version": ver}
		if b.Extras != "" {
			m["extras"] = b.Extras
		}
		if b.PipxArgs != "" {
			m["pipx_args"] = b.PipxArgs
		}
		if b.UVX {
			m["uvx"] = true
		}
		if b.UVXArgs != "" {
			m["uvx_args"] = b.UVXArgs
		}
		if len(m) == 1 {
			toolValue = ver
		} else {
			toolValue = m
		}

	case "npm":
		// npm has no per-tool options beyond version
		toolValue = ver

	default:
		return "", fmt.Errorf("unsupported backend %q", b.resolvedBackend())
	}

	data, err := toml.Marshal(map[string]any{
		"tools": map[string]any{toolKey: toolValue},
	})
	if err != nil {
		return "", fmt.Errorf("marshal mise.toml: %w", err)
	}
	return string(data), nil
}

// installBinaryWithMise installs a single binary via mise's github backend and
// copies the resulting binary to $ANNA_HOME/bin/.
func installBinaryWithMise(ctx context.Context, b ManifestBinary, annaHome string) (string, error) {
	miseBin, err := findMiseBin(annaHome)
	if err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "anna-mise-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tomlContent, err := generateMiseTOML(b)
	if err != nil {
		return "", err
	}
	tomlPath := filepath.Join(tmpDir, "mise.toml")
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0o644); err != nil {
		return "", fmt.Errorf("write mise.toml: %w", err)
	}

	env, err := isolatedMiseEnv(annaHome)
	if err != nil {
		return "", err
	}

	// Trust the temp config so mise doesn't refuse to read it.
	var stderr bytes.Buffer
	trustCmd := managedCommandContext(ctx, miseBin, "trust", tmpDir)
	trustCmd.Dir = tmpDir
	trustCmd.Env = env
	trustCmd.Stderr = &stderr
	if err := trustCmd.Run(); err != nil {
		return "", fmt.Errorf("mise trust: %w\nstderr: %s", err, stderr.String())
	}

	// Run mise install for only the manifest tool. Avoid installing any global or
	// inherited mise config that may be visible to the Anna process.
	toolKey := b.miseToolKey()
	stderr.Reset()
	installCmd := managedCommandContext(ctx, miseBin, "install", toolKey)
	installCmd.Dir = tmpDir
	installCmd.Env = env
	installCmd.Stderr = &stderr
	if err := installCmd.Run(); err != nil {
		return "", fmt.Errorf("mise install: %w\nstderr: %s", err, stderr.String())
	}

	// Determine which binary name to pass to mise which.
	// rename_exe takes precedence (archive extraction rename), then bin (single
	// binary rename), then the tool name.
	lookupName := b.Name
	if b.RenameExe != "" {
		lookupName = b.RenameExe
	} else if b.Bin != "" {
		lookupName = b.Bin
	}

	// Resolve the binary path via mise which — it handles any archive layout.
	var stdout bytes.Buffer
	stderr.Reset()
	whichCmd := managedCommandContext(ctx, miseBin, "which", lookupName)
	whichCmd.Dir = tmpDir
	whichCmd.Env = env
	whichCmd.Stdout = &stdout
	whichCmd.Stderr = &stderr
	if err := whichCmd.Run(); err != nil {
		return "", fmt.Errorf("mise which %s: %w\nstderr: %s", lookupName, err, stderr.String())
	}
	srcPath := strings.TrimSpace(stdout.String())
	if srcPath == "" {
		return "", fmt.Errorf("mise which %s returned empty path", lookupName)
	}

	// Resolve the installed version via mise which --version.
	stdout.Reset()
	stderr.Reset()
	verCmd := managedCommandContext(ctx, miseBin, "which", lookupName, "--version")
	verCmd.Dir = tmpDir
	verCmd.Env = env
	verCmd.Stdout = &stdout
	verCmd.Stderr = &stderr
	if err := verCmd.Run(); err != nil {
		return "", fmt.Errorf("mise which --version %s: %w\nstderr: %s", lookupName, err, stderr.String())
	}
	version := strings.TrimSpace(stdout.String())

	// Always install as b.Name in $ANNA_HOME/bin/ regardless of exe alias.
	dstName := b.Name
	if runtime.GOOS == "windows" {
		dstName += ".exe"
	}
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}
	dstPath := filepath.Join(binDir, dstName)
	if err := atomicCopy(srcPath, dstPath, 0o755); err != nil {
		return "", err
	}
	return version, nil
}

// atomicCopy copies src to dst using a temp file + rename.
func atomicCopy(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }()

	dir := filepath.Dir(dst)
	base := filepath.Base(dst)
	tmp, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

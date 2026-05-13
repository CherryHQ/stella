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
func miseToolsDir(stellaHome string) string {
	return filepath.Join(stellaHome, ".mise-tools")
}

// findMiseBin returns the path to the mise binary. It prefers the Stella-managed
// binary in $STELLA_HOME/bin, then falls back to mise on PATH.
func findMiseBin(stellaHome string) (string, error) {
	local := filepath.Join(stellaHome, "bin", runtimeBinaryName("mise"))
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	if path, err := exec.LookPath("mise"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("mise not found at %s or on PATH", local)
}

func bootstrapMise(_ context.Context, stellaHome string) error {
	_, err := findMiseBin(stellaHome)
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

func isolatedMiseEnv(stellaHome string) ([]string, error) {
	dataDir := miseToolsDir(stellaHome)
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
	env["MISE_EXPERIMENTAL"] = "1"

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

// miseToolKey returns the mise tool key used in mise.toml and CLI commands.
func (b ManifestBinary) miseToolKey() string {
	return b.Tool
}

// generateMiseTOML returns a valid mise.toml for the binary.
func generateMiseTOML(b ManifestBinary) (string, error) {
	toolKey := b.miseToolKey()
	if toolKey == "" {
		return "", fmt.Errorf("cannot determine mise tool key")
	}

	ver := b.Version
	if ver == "" {
		ver = "latest"
	}

	options := maps.Clone(b.Options)
	if options == nil {
		options = make(map[string]any)
	}

	var toolValue any = ver
	if len(options) > 0 {
		if _, ok := options["version"]; !ok {
			options["version"] = ver
		}
		toolValue = options
	}

	data, err := toml.Marshal(map[string]any{
		"tools": map[string]any{toolKey: toolValue},
	})
	if err != nil {
		return "", fmt.Errorf("marshal mise.toml: %w", err)
	}
	return string(data), nil
}

// installBinaryWithMise installs a single binary via mise and copies the resulting
// binary to $STELLA_HOME/bin/.
func installBinaryWithMise(ctx context.Context, b ManifestBinary, stellaHome string) (string, error) {
	miseBin, err := findMiseBin(stellaHome)
	if err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "stella-mise-*")
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

	env, err := isolatedMiseEnv(stellaHome)
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
	// inherited mise config that may be visible to the Stella process.
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
	if renameExe, ok := stringOption(b.Options, "rename_exe"); ok {
		lookupName = renameExe
	} else if bin, ok := stringOption(b.Options, "bin"); ok {
		lookupName = bin
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

	// Always install as b.Name in $STELLA_HOME/bin/ regardless of exe alias.
	dstName := b.Name
	if runtime.GOOS == "windows" {
		dstName += ".exe"
	}
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}
	dstPath := filepath.Join(binDir, dstName)
	if err := atomicCopy(srcPath, dstPath, 0o755); err != nil {
		return "", err
	}
	return version, nil
}

func stringOption(options map[string]any, key string) (string, bool) {
	value, ok := options[key]
	if !ok {
		return "", false
	}
	s, ok := value.(string)
	return s, ok && s != ""
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

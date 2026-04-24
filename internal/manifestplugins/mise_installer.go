package manifestplugins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func runtimeBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// generateMiseTOML returns a valid mise.toml for a single github backend tool.
// When b.Version is empty, "latest" is used for the simple form (TOML requires a
// value); the table form omits the version field so mise resolves it.
// Specify an explicit version for repos that don't publish a "latest" release
// (e.g. version: "nightly").
func generateMiseTOML(b ManifestBinary) (string, error) {
	toolKey := "github:" + b.Repo

	var toolValue any
	if b.BinPath == "" && b.Exe == "" {
		// Simple form: "github:owner/repo" = "version"
		ver := b.Version
		if ver == "" {
			ver = "latest"
		}
		toolValue = ver
	} else {
		// Table form: extra options as a map.
		m := map[string]any{}
		if b.Version != "" {
			m["version"] = b.Version
		}
		if b.BinPath != "" {
			m["bin_path"] = b.BinPath
		}
		if b.Exe != "" {
			m["exe"] = b.Exe
		}
		toolValue = m
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

	dataDir := miseToolsDir(annaHome)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create mise data dir: %w", err)
	}

	env := append(os.Environ(), "MISE_DATA_DIR="+dataDir, "MISE_YES=1")

	// Trust the temp config so mise doesn't refuse to read it.
	var stderr bytes.Buffer
	trustCmd := exec.CommandContext(ctx, miseBin, "trust", tmpDir)
	trustCmd.Dir = tmpDir
	trustCmd.Env = env
	trustCmd.Stderr = &stderr
	if err := trustCmd.Run(); err != nil {
		return "", fmt.Errorf("mise trust: %w\nstderr: %s", err, stderr.String())
	}

	// Run mise install
	stderr.Reset()
	installCmd := exec.CommandContext(ctx, miseBin, "install")
	installCmd.Dir = tmpDir
	installCmd.Env = env
	installCmd.Stderr = &stderr
	if err := installCmd.Run(); err != nil {
		return "", fmt.Errorf("mise install: %w\nstderr: %s", err, stderr.String())
	}

	// Resolve the install directory
	toolKey := fmt.Sprintf("github:%s", b.Repo)
	var stdout bytes.Buffer
	stderr.Reset()
	whereCmd := exec.CommandContext(ctx, miseBin, "where", toolKey)
	whereCmd.Dir = tmpDir
	whereCmd.Env = env
	whereCmd.Stdout = &stdout
	whereCmd.Stderr = &stderr
	if err := whereCmd.Run(); err != nil {
		return "", fmt.Errorf("mise where %s: %w\nstderr: %s", toolKey, err, stderr.String())
	}

	installDir := strings.TrimSpace(stdout.String())
	if installDir == "" {
		return "", fmt.Errorf("mise where %s returned empty path", toolKey)
	}

	// Get the installed version from the directory name
	version := filepath.Base(installDir)

	// Find the binary: use exe name when set, fall back to name.
	lookupName := b.Name
	if b.Exe != "" {
		lookupName = b.Exe
	}
	if runtime.GOOS == "windows" {
		lookupName += ".exe"
	}
	srcPath, err := findInstalledBinary(installDir, lookupName)
	if err != nil {
		return "", fmt.Errorf("find binary %s in %s: %w", lookupName, installDir, err)
	}

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

// findInstalledBinary searches common locations within a mise install directory.
func findInstalledBinary(installDir, name string) (string, error) {
	candidates := []string{
		filepath.Join(installDir, "bin", name),
		filepath.Join(installDir, name),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("binary %q not found in %s (checked bin/ and root)", name, installDir)
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

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

	"github.com/vaayne/anna/internal/tools"
)

// miseToolsDir returns the MISE_DATA_DIR path for isolated mise installs.
func miseToolsDir(annaHome string) string {
	return filepath.Join(annaHome, ".mise-tools")
}

// findMiseBin returns the path to the mise binary. It prefers $ANNA_HOME/bin/mise,
// then falls back to mise on PATH.
func findMiseBin(annaHome string) (string, error) {
	local := filepath.Join(annaHome, "bin", "mise")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	if path, err := exec.LookPath("mise"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("mise not found at %s or on PATH", local)
}

// bootstrapMise ensures mise is available at $ANNA_HOME/bin/mise, downloading it
// from GitHub if necessary. This is the only place direct (non-mise) download is used.
func bootstrapMise(ctx context.Context, annaHome string) error {
	if _, err := findMiseBin(annaHome); err == nil {
		return nil // already available
	}

	miseTool := tools.Tool{
		Name: "mise",
		Repo: "jdx/mise",
		AssetTemplates: map[string]tools.AssetTemplate{
			"darwin-arm64": {File: "mise-{tag}-macos-arm64.tar.gz"},
			"darwin-amd64": {File: "mise-{tag}-macos-x64.tar.gz"},
			"linux-amd64":  {File: "mise-{tag}-linux-x64.tar.gz"},
			"linux-arm64":  {File: "mise-{tag}-linux-arm64.tar.gz"},
		},
	}
	version, err := tools.FetchLatestVersion(ctx, &miseTool)
	if err != nil {
		return fmt.Errorf("fetch latest mise version: %w", err)
	}
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	return tools.DownloadVersion(ctx, &miseTool, version, binDir, tools.Platform())
}

// generateMiseTOML returns a mise.toml snippet for a single github backend tool.
// When b.Version is empty the version field is omitted; mise resolves the version
// itself (typically the latest GitHub release). Specify an explicit version for
// tools whose repos use non-standard release channels (e.g. "nightly").
func generateMiseTOML(b ManifestBinary) string {
	// Simple single-line form: "github:owner/repo" = "version"
	if b.BinPath == "" && b.Exe == "" {
		ver := b.Version
		if ver == "" {
			ver = "latest"
		}
		return fmt.Sprintf("[tools]\n\"github:%s\" = %q\n", b.Repo, ver)
	}
	// Table form for extra options.
	var sb strings.Builder
	fmt.Fprintf(&sb, "[tools.\"github:%s\"]\n", b.Repo)
	if b.Version != "" {
		fmt.Fprintf(&sb, "version = %q\n", b.Version)
	}
	if b.BinPath != "" {
		fmt.Fprintf(&sb, "bin_path = %q\n", b.BinPath)
	}
	if b.Exe != "" {
		fmt.Fprintf(&sb, "exe = %q\n", b.Exe)
	}
	return sb.String()
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

	tomlPath := filepath.Join(tmpDir, "mise.toml")
	if err := os.WriteFile(tomlPath, []byte(generateMiseTOML(b)), 0o644); err != nil {
		return "", fmt.Errorf("write mise.toml: %w", err)
	}

	dataDir := miseToolsDir(annaHome)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create mise data dir: %w", err)
	}

	env := append(os.Environ(), "MISE_DATA_DIR="+dataDir)

	// Run mise install
	var stderr bytes.Buffer
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

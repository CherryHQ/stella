package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

const BoxshBinaryName = "boxsh"

type PreflightConfig struct {
	AnnaHome    string
	Workspace   string
	UserDataDir string
	Sandbox     config.SandboxConfig
}

func RequiresBoxsh(goos string) bool {
	switch goos {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}

func SandboxRoot(cfg PreflightConfig) string {
	if cfg.UserDataDir != "" {
		return cfg.UserDataDir
	}
	return cfg.Workspace
}

func ResolveManagedBoxshPath(annaHome string) (string, error) {
	path := embedded.ToolPath(annaHome, BoxshBinaryName)
	if path == "" {
		return "", fmt.Errorf("sandbox: managed %s binary not found in %s", BoxshBinaryName, embedded.BinDir(annaHome))
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("sandbox: stat managed %s binary: %w", BoxshBinaryName, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("sandbox: managed %s path %q is a directory", BoxshBinaryName, path)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("sandbox: managed %s binary %q is not executable", BoxshBinaryName, path)
	}

	return path, nil
}

func ValidateManagedBoxshBinary(ctx context.Context, annaHome string) (string, error) {
	path, err := ResolveManagedBoxshPath(annaHome)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, path, "--version")
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		return "", fmt.Errorf("sandbox: validate managed %s binary %q: %w: %s", BoxshBinaryName, path, err, trimmed)
	}
	if !strings.Contains(strings.ToLower(trimmed), BoxshBinaryName) {
		return "", fmt.Errorf("sandbox: validate managed %s binary %q: unexpected version output %q", BoxshBinaryName, path, trimmed)
	}

	return path, nil
}

func Preflight(ctx context.Context, cfg PreflightConfig) error {
	if !RequiresBoxsh(runtime.GOOS) {
		return nil
	}
	if err := cfg.Sandbox.Validate(); err != nil {
		return err
	}

	root := SandboxRoot(cfg)
	if root == "" {
		return fmt.Errorf("sandbox: workspace root is required")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("sandbox: workspace root must be absolute: %q", root)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("sandbox: stat workspace root %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sandbox: workspace root %q is not a directory", root)
	}

	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = config.AnnaHome()
	}
	stateDir := filepath.Join(annaHome, "cache", "sandbox")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("sandbox: create state dir %q: %w", stateDir, err)
	}
	probe, err := os.CreateTemp(stateDir, "preflight-*")
	if err != nil {
		return fmt.Errorf("sandbox: create temp file in %q: %w", stateDir, err)
	}
	if err := probe.Close(); err != nil {
		return fmt.Errorf("sandbox: close temp file in %q: %w", stateDir, err)
	}
	if err := os.Remove(probe.Name()); err != nil {
		return fmt.Errorf("sandbox: remove temp file in %q: %w", stateDir, err)
	}

	validateCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = ValidateManagedBoxshBinary(validateCtx, annaHome)
	return err
}

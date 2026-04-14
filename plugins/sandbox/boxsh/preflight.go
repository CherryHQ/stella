package boxsh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
)

const BoxshBinaryName = "boxsh"

func RequiresBoxsh(goos string) bool {
	switch goos {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}

func SandboxRoot(cfg PreflightConfig) string {
	return cfg.UserRoot
}

func ResolveManagedBoxshPath(annaHome string) (string, error) {
	return boxshclient.ResolveManagedBoxshPath(annaHome)
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
	if err := cfg.Validate(); err != nil {
		return err
	}

	root := SandboxRoot(cfg)
	if root == "" {
		return fmt.Errorf("sandbox: user root is required")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("sandbox: user root must be absolute: %q", root)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("sandbox: stat user root %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sandbox: user root %q is not a directory", root)
	}

	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = boxshclient.DefaultAnnaHome()
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

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUpgradeTargetDefaultUsesExecutablePath(t *testing.T) {
	oldExecutablePath := executablePath
	exePath := filepath.Join(string(filepath.Separator), "opt", "stella", binaryNameForGOOS("linux"))
	executablePath = func() (string, error) { return exePath, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	got, err := resolveUpgradeTarget("", "linux")
	if err != nil {
		t.Fatalf("resolveUpgradeTarget: %v", err)
	}
	if got != exePath {
		t.Fatalf("resolveUpgradeTarget() = %q, want %q", got, exePath)
	}
}

func TestResolveUpgradeTargetWithInstallDir(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "usr", "local", "bin")
	got, err := resolveUpgradeTarget(dir, "linux")
	if err != nil {
		t.Fatalf("resolveUpgradeTarget: %v", err)
	}
	want := filepath.Join(dir, binaryNameForGOOS("linux"))
	if got != want {
		t.Fatalf("resolveUpgradeTarget() = %q, want %q", got, want)
	}
}

func TestResolveUpgradeTargetReportsExecutablePathError(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return "", errors.New("boom") }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	_, err := resolveUpgradeTarget("", "linux")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUpgradeInstallErrorAddsPermissionHint(t *testing.T) {
	err := upgradeInstallError(errors.New("rename stella.tmp stella: permission denied"), "/usr/local/bin/stella", "linux")

	if !strings.Contains(err.Error(), "permission denied replacing /usr/local/bin/stella") {
		t.Fatalf("error = %q, want permission hint", err.Error())
	}
}

func TestUpgradeInstallErrorAddsWindowsBusyHint(t *testing.T) {
	err := upgradeInstallError(errors.New("The process cannot access the file because it is being used by another process."), `C:\\bin\\stella.exe`, "windows")

	if !strings.Contains(err.Error(), "locked by a running process") {
		t.Fatalf("error = %q, want busy hint", err.Error())
	}
}

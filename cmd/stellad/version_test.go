package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUpgradeDirDefaultUsesExecutableDir(t *testing.T) {
	tmpDir := t.TempDir()
	exePath := filepath.Join(tmpDir, "stellad")
	if err := os.WriteFile(exePath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return exePath, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	got, err := resolveUpgradeDir("")
	if err != nil {
		t.Fatalf("resolveUpgradeDir: %v", err)
	}
	want, _ := filepath.EvalSymlinks(tmpDir)
	if got != want {
		t.Fatalf("resolveUpgradeDir() = %q, want %q", got, want)
	}
}

func TestResolveUpgradeDirWithInstallDir(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "usr", "local", "bin")
	got, err := resolveUpgradeDir(dir)
	if err != nil {
		t.Fatalf("resolveUpgradeDir: %v", err)
	}
	if got != dir {
		t.Fatalf("resolveUpgradeDir() = %q, want %q", got, dir)
	}
}

func TestResolveUpgradeDirReportsExecutablePathError(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return "", errors.New("boom") }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	_, err := resolveUpgradeDir("")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBinariesToUpgrade(t *testing.T) {
	got := binariesToUpgrade("linux")
	if len(got) != 2 || got[0] != "stella" || got[1] != "stellad" {
		t.Fatalf("linux: got %v, want [stella stellad]", got)
	}
	got = binariesToUpgrade("windows")
	if len(got) != 2 || got[0] != "stella.exe" || got[1] != "stellad.exe" {
		t.Fatalf("windows: got %v, want [stella.exe stellad.exe]", got)
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

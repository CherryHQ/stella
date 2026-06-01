package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultInstallDirUsesRunningExecutableDir(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) {
		return filepath.Join(string(filepath.Separator), "opt", "stella", binaryNameForGOOS("linux")), nil
	}
	t.Cleanup(func() { executablePath = oldExecutablePath })

	dir, err := defaultInstallDir()
	if err != nil {
		t.Fatalf("defaultInstallDir: %v", err)
	}

	want := filepath.Join(string(filepath.Separator), "opt", "stella")
	if dir != want {
		t.Fatalf("defaultInstallDir() = %q, want %q", dir, want)
	}
}

func TestDefaultInstallDirReportsExecutablePathError(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) {
		return "", errors.New("boom")
	}
	t.Cleanup(func() { executablePath = oldExecutablePath })

	_, err := defaultInstallDir()
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

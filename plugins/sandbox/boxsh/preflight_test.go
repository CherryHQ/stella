package boxsh

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRequiresBoxsh(t *testing.T) {
	if !RequiresBoxsh("linux") {
		t.Fatal("linux should require boxsh")
	}
	if !RequiresBoxsh("darwin") {
		t.Fatal("darwin should require boxsh")
	}
	if RequiresBoxsh("windows") {
		t.Fatal("windows should not require boxsh")
	}
}

func TestResolveManagedBoxshPath(t *testing.T) {
	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, BoxshBinaryName)
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho boxsh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveManagedBoxshPath(annaHome)
	if err != nil {
		t.Fatalf("ResolveManagedBoxshPath: %v", err)
	}
	if got != boxshPath {
		t.Fatalf("ResolveManagedBoxshPath() = %q, want %q", got, boxshPath)
	}
}

func TestResolveManagedBoxshPathMissing(t *testing.T) {
	_, err := ResolveManagedBoxshPath(t.TempDir())
	if err == nil {
		t.Fatal("expected missing boxsh error")
	}
}

func TestResolveManagedBoxshPathNotExecutable(t *testing.T) {
	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, BoxshBinaryName)
	if err := os.WriteFile(boxshPath, []byte("noop"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveManagedBoxshPath(annaHome)
	if err == nil {
		t.Fatal("expected not executable error")
	}
}

func TestValidateManagedBoxshBinary(t *testing.T) {
	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, BoxshBinaryName)
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho boxsh 2.0.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ValidateManagedBoxshBinary(context.Background(), annaHome)
	if err != nil {
		t.Fatalf("ValidateManagedBoxshBinary: %v", err)
	}
	if got != boxshPath {
		t.Fatalf("ValidateManagedBoxshBinary() = %q, want %q", got, boxshPath)
	}
}

func TestValidateManagedBoxshBinaryFailure(t *testing.T) {
	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, BoxshBinaryName)
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := ValidateManagedBoxshBinary(context.Background(), annaHome)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateManagedBoxshBinaryRejectsUnexpectedOutput(t *testing.T) {
	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, BoxshBinaryName)
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho bash 5.2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := ValidateManagedBoxshBinary(context.Background(), annaHome)
	if err == nil {
		t.Fatal("expected unexpected output validation error")
	}
}

func TestPreflightSkipsWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		if err := Preflight(context.Background(), PreflightConfig{}); err != nil {
			t.Fatalf("Preflight: %v", err)
		}
		return
	}
	t.Skip("only meaningful on windows hosts")
}

func TestPreflight(t *testing.T) {
	if !RequiresBoxsh(runtime.GOOS) {
		t.Skip("boxsh preflight only applies on linux/darwin")
	}
	annaHome := t.TempDir()
	userRoot := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, BoxshBinaryName)
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho boxsh 2.0.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := PreflightConfig{
		AnnaHome: annaHome,
		UserRoot: userRoot,
		Network:  NetworkConfig{Mode: NetworkDisabled},
	}
	if err := Preflight(context.Background(), cfg); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
}

func TestPreflightRejectsInvalidNetworkMode(t *testing.T) {
	if !RequiresBoxsh(runtime.GOOS) {
		t.Skip("boxsh preflight only applies on linux/darwin")
	}
	cfg := PreflightConfig{
		AnnaHome: t.TempDir(),
		UserRoot: t.TempDir(),
		Network:  NetworkConfig{Mode: "invalid"},
	}
	if err := Preflight(context.Background(), cfg); err == nil {
		t.Fatal("expected invalid network mode error")
	}
}

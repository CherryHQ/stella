package processpath

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesProvidedPath(t *testing.T) {
	bin := t.TempDir()
	tool := filepath.Join(bin, "turn-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("turn-tool", []string{"PATH=" + bin})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != tool {
		t.Fatalf("resolved path = %q, want %q", got, tool)
	}
}

func TestResolveHonorsErrDotForRelativePath(t *testing.T) {
	bin := t.TempDir()
	tool := filepath.Join(bin, "turn-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(filepath.Dir(bin)); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve("turn-tool", []string{"PATH=" + filepath.Base(bin)})
	if !errors.Is(err, exec.ErrDot) {
		t.Fatalf("error = %v, want exec.ErrDot", err)
	}
}

func TestResolveDoesNotUseHostPath(t *testing.T) {
	bin := t.TempDir()
	tool := filepath.Join(bin, "turn-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve("turn-tool", []string{"PATH=" + t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("error = %v, want PATH-scoped not-found error", err)
	}
}

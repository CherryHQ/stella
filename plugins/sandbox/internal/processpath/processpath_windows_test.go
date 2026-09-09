//go:build windows

package processpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUsesWindowsPATHEXT(t *testing.T) {
	bin := t.TempDir()
	tool := filepath.Join(bin, "turn-tool.exe")
	if err := os.WriteFile(tool, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("turn-tool", []string{"Path=" + bin})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(tool) {
		t.Fatalf("resolved path = %q, want %q", got, tool)
	}
}

func TestResolveWindowsExplicitPathsBypassPATH(t *testing.T) {
	for _, name := range []string{`./tool.exe`, `C:/tools/tool.exe`, `C:tool.exe`} {
		got, err := Resolve(name, []string{"PATH=" + t.TempDir()})
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		if got != name {
			t.Fatalf("Resolve(%q) = %q, want passthrough", name, got)
		}
	}
}

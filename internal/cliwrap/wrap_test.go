package cliwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWrappers_CreatesBinDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "newdir", "bin")

	// dir does not exist yet.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expected dir to not exist before EnsureWrappers")
	}

	if err := EnsureWrappers(dir); err != nil {
		t.Fatalf("EnsureWrappers: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat after EnsureWrappers: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected binDir to be a directory")
	}
}

func TestEnsureWrappers_WritesGhScript(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureWrappers(dir); err != nil {
		t.Fatalf("EnsureWrappers: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "gh"))
	if err != nil {
		t.Fatalf("ReadFile gh: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "#!/bin/sh") {
		t.Errorf("gh wrapper does not start with #!/bin/sh, got: %q", content[:min(len(content), 20)])
	}
}

func TestEnsureWrappers_WritesLarkCliScript(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureWrappers(dir); err != nil {
		t.Fatalf("EnsureWrappers: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "lark-cli"))
	if err != nil {
		t.Fatalf("ReadFile lark-cli: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "#!/bin/sh") {
		t.Errorf("lark-cli wrapper does not start with #!/bin/sh, got: %q", content[:min(len(content), 20)])
	}
}

func TestEnsureWrappers_ScriptsAreExecutable(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureWrappers(dir); err != nil {
		t.Fatalf("EnsureWrappers: %v", err)
	}

	for _, name := range []string{"gh", "lark-cli"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("Stat %s: %v", name, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("%s is not executable (mode %o)", name, info.Mode())
		}
	}
}

func TestEnsureWrappers_Idempotent(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureWrappers(dir); err != nil {
		t.Fatalf("EnsureWrappers (first call): %v", err)
	}
	if err := EnsureWrappers(dir); err != nil {
		t.Fatalf("EnsureWrappers (second call): %v", err)
	}

	// Content should be identical on both calls.
	for _, name := range []string{"gh", "lark-cli"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if !strings.HasPrefix(string(data), "#!/bin/sh") {
			t.Errorf("%s wrapper corrupt after second call", name)
		}
	}
}

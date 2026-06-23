package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/pgruntime"
)

func TestPostgresDownloadRuntimeHelp(t *testing.T) {
	app := newApp()
	if err := app.Run([]string{"stellad", "postgres", "download-runtime", "--help"}); err != nil {
		t.Fatalf("run postgres download-runtime --help: %v", err)
	}
}

func TestDownloadPostgresRuntimeUsesExistingInstall(t *testing.T) {
	stellaHome := t.TempDir()
	root := pgruntime.RuntimeRoot(stellaHome, "testsource")
	mkdir := filepath.Join(root, "postgres", "bin")
	if err := os.MkdirAll(mkdir, 0o755); err != nil {
		t.Fatal(err)
	}
	pgCtl := "pg_ctl"
	if runtime.GOOS == "windows" {
		pgCtl = "pg_ctl.exe"
	}
	if err := os.WriteFile(filepath.Join(mkdir, pgCtl), []byte("pg_ctl"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	got, err := downloadPostgresRuntime(context.Background(), &out, stellaHome, "invalid/repo", "testsource", false)
	if err != nil {
		t.Fatalf("downloadPostgresRuntime: %v", err)
	}
	if got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}
	if !strings.Contains(out.String(), "already installed") {
		t.Fatalf("output = %q, want already installed", out.String())
	}
}

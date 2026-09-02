package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/pgruntime"
)

func TestPostgresDownloadHelp(t *testing.T) {
	app := newApp()
	if err := app.Run([]string{"stellad", "postgres", "download", "--help"}); err != nil {
		t.Fatalf("run postgres download --help: %v", err)
	}
}

// The command shipped as download-runtime and appears in released docs and in
// error hints users have already read, so the old name has to keep resolving.
func TestPostgresDownloadRuntimeAliasStillResolves(t *testing.T) {
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

func installRuntimeDir(t *testing.T, stellaHome, name string, size int) string {
	t.Helper()
	dir := filepath.Join(stellaHome, "pg-runtime", name, "downloaded", "trixie", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "postgres"), make([]byte, size), 0o755); err != nil {
		t.Fatalf("write runtime file: %v", err)
	}
	return filepath.Join(stellaHome, "pg-runtime", name)
}

// Without --force the command is a report. A maintenance command that deleted
// hundreds of megabytes on a bare invocation would be exactly the surprise the
// CLI rules forbid.
func TestPruneRuntimesWithoutForceRemovesNothing(t *testing.T) {
	home := t.TempDir()
	old := installRuntimeDir(t, home, "pg17.0-old-linux-amd64", 128)
	installRuntimeDir(t, home, pgruntime.CurrentRuntimeDir(), 64)

	var out bytes.Buffer
	if err := pruneRuntimes(&out, home, false, false); err != nil {
		t.Fatalf("pruneRuntimes: %v", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("the old runtime should still exist: %v", err)
	}
	if !strings.Contains(out.String(), "--force") {
		t.Errorf("the report should say how to actually remove them, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "pg17.0-old-linux-amd64") {
		t.Errorf("the report should name the unused runtime, got:\n%s", out.String())
	}
}

// The runtime this binary uses must survive --force; removing it would leave a
// server that cannot start and call it a cleanup.
func TestPruneRuntimesKeepsTheCurrentRuntime(t *testing.T) {
	home := t.TempDir()
	old := installRuntimeDir(t, home, "pg17.0-old-linux-amd64", 128)
	current := installRuntimeDir(t, home, pgruntime.CurrentRuntimeDir(), 64)

	var out bytes.Buffer
	if err := pruneRuntimes(&out, home, true, false); err != nil {
		t.Fatalf("pruneRuntimes: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the old runtime should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("the current runtime must survive: %v", err)
	}
	if !strings.Contains(out.String(), "Reclaimed") {
		t.Errorf("output should report what it reclaimed, got:\n%s", out.String())
	}
}

func TestPruneRuntimesJSONReportsWhatItRemoved(t *testing.T) {
	home := t.TempDir()
	installRuntimeDir(t, home, "pg17.0-old-linux-amd64", 128)
	installRuntimeDir(t, home, pgruntime.CurrentRuntimeDir(), 64)

	var out bytes.Buffer
	if err := pruneRuntimes(&out, home, true, true); err != nil {
		t.Fatalf("pruneRuntimes: %v", err)
	}
	var report pruneReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if report.Current != pgruntime.CurrentRuntimeDir() {
		t.Errorf("current = %q, want %q", report.Current, pgruntime.CurrentRuntimeDir())
	}
	if !report.Pruned {
		t.Error("pruned should be true after --force")
	}
	if len(report.Runtimes) != 1 || report.Runtimes[0].Name != "pg17.0-old-linux-amd64" {
		t.Fatalf("runtimes = %+v, want only the old one", report.Runtimes)
	}
	if report.Bytes != 128 {
		t.Errorf("bytes = %d, want 128", report.Bytes)
	}
}

// --json is for scripts, so an empty result still has to parse rather than
// emitting `null` or a prose line.
func TestPruneRuntimesJSONWithNothingToRemove(t *testing.T) {
	home := t.TempDir()
	installRuntimeDir(t, home, pgruntime.CurrentRuntimeDir(), 64)

	var out bytes.Buffer
	if err := pruneRuntimes(&out, home, false, true); err != nil {
		t.Fatalf("pruneRuntimes: %v", err)
	}
	var report pruneReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if report.Runtimes == nil {
		t.Error("runtimes should be an empty array, not null")
	}
	if len(report.Runtimes) != 0 || report.Bytes != 0 {
		t.Errorf("report = %+v, want nothing to remove", report)
	}
}

func TestPostgresPruneHelp(t *testing.T) {
	app := newApp()
	if err := app.Run([]string{"stellad", "postgres", "prune", "--help"}); err != nil {
		t.Fatalf("run postgres prune --help: %v", err)
	}
}

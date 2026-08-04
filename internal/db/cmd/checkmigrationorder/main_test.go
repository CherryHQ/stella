package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMigrations(t *testing.T) {
	t.Parallel()

	migrations, err := parseMigrations([]string{"internal/db/migrations/20260804120001_add_widget.sql"})
	if err != nil {
		t.Fatal(err)
	}
	if migrations[0].version != 20260804120001 {
		t.Fatalf("version = %d, want 20260804120001", migrations[0].version)
	}

	for _, name := range []string{
		"internal/db/migrations/0_bad.sql",
		"internal/db/migrations/01_bad.sql",
		"internal/db/migrations/999999999999999999999_overflow.sql",
		"internal/db/migrations/no_version.sql",
	} {
		if _, err := parseMigrations([]string{name}); err == nil {
			t.Errorf("parseMigrations(%q) succeeded", name)
		}
	}
}

func TestValidateAdded(t *testing.T) {
	t.Parallel()

	valid := []migration{{path: "11_one.sql", version: 11}, {path: "12_two.sql", version: 12}}
	if err := validateAdded(10, valid); err != nil {
		t.Fatalf("validate valid additions: %v", err)
	}
	if err := validateAdded(10, []migration{{path: "10_equal.sql", version: 10}}); err == nil {
		t.Fatal("equal version succeeded")
	}
	if err := validateAdded(10, []migration{{path: "11_one.sql", version: 11}, {path: "11_two.sql", version: 11}}); err == nil {
		t.Fatal("duplicate versions succeeded")
	}
}

func TestCheckGitRepository(t *testing.T) {
	cases := []struct {
		name        string
		baseFiles   map[string]string
		headFiles   map[string]string
		stagedFiles map[string]string
		removeBase  bool
		base        string
		wantErr     string
	}{
		{
			name:      "no additions",
			baseFiles: map[string]string{"100_base.sql": "-- base"},
		},
		{
			name:      "valid later version",
			baseFiles: map[string]string{"100_base.sql": "-- base"},
			headFiles: map[string]string{"101_later.sql": "-- later"},
			base:      "base",
		},
		{
			name:      "stale version",
			baseFiles: map[string]string{"100_base.sql": "-- base"},
			headFiles: map[string]string{"99_stale.sql": "-- stale"},
			wantErr:   "must be greater than base maximum 100",
		},
		{
			name:      "equal version",
			baseFiles: map[string]string{"100_base.sql": "-- base"},
			headFiles: map[string]string{"100_equal.sql": "-- equal"},
			wantErr:   "must be greater than base maximum 100",
		},
		{
			name:      "duplicate added versions",
			baseFiles: map[string]string{"100_base.sql": "-- base"},
			headFiles: map[string]string{"101_one.sql": "-- one", "101_two.sql": "-- two"},
			wantErr:   "not strictly increasing",
		},
		{
			name:      "malformed added filename",
			baseFiles: map[string]string{"100_base.sql": "-- base"},
			headFiles: map[string]string{"001_bad.sql": "-- bad"},
			wantErr:   "not a canonical Goose migration filename",
		},
		{
			name:        "staged migration",
			baseFiles:   map[string]string{"100_base.sql": "-- base"},
			stagedFiles: map[string]string{"99_staged.sql": "-- staged"},
			wantErr:     "must be greater than base maximum 100",
		},
		{
			name:      "malformed base filename",
			baseFiles: map[string]string{"bad.sql": "-- base"},
			wantErr:   "not a canonical Goose migration filename",
		},
		{
			name:       "renamed migration is treated as added",
			baseFiles:  map[string]string{"100_base.sql": "-- identical"},
			headFiles:  map[string]string{"99_renamed.sql": "-- identical"},
			removeBase: true,
			wantErr:    "must be greater than base maximum 100",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, baseSHA := newRepository(t, tc.baseFiles, tc.headFiles, tc.stagedFiles, tc.removeBase)
			base := tc.base
			if base == "" {
				base = baseSHA
			}
			var output bytes.Buffer
			err := check(repo, base, &output)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "no additions" && !strings.Contains(output.String(), "no migrations added") {
					t.Fatalf("output = %q, want no-additions message", output.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckRejectsMissingBase(t *testing.T) {
	repo, _ := newRepository(t, map[string]string{"100_base.sql": "-- base"}, nil, nil, false)
	if err := check(repo, "does-not-exist", io.Discard); err == nil || !strings.Contains(err.Error(), "resolve base") {
		t.Fatalf("error = %v, want missing base error", err)
	}
}

func TestRunUsesExplicitBase(t *testing.T) {
	repo, _ := newRepository(t, map[string]string{"100_base.sql": "-- base"}, map[string]string{"101_later.sql": "-- later"}, nil, false)
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	var output, stderr bytes.Buffer
	if err := run([]string{"--base", "base"}, func(string) string { return "does-not-exist" }, &output, &stderr); err != nil {
		t.Fatal(err)
	}
}

func newRepository(t *testing.T, baseFiles, headFiles, stagedFiles map[string]string, removeBase bool) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitCommand(t, repo, "init", "--initial-branch=main")
	gitCommand(t, repo, "config", "user.email", "test@example.com")
	gitCommand(t, repo, "config", "user.name", "Test")
	writeMigrations(t, repo, baseFiles)
	gitCommand(t, repo, "add", ".")
	gitCommand(t, repo, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	gitCommand(t, repo, "branch", "base", baseSHA)
	if removeBase {
		for name := range baseFiles {
			if err := os.Remove(filepath.Join(repo, migrationsDir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeMigrations(t, repo, headFiles)
	if len(headFiles) > 0 || removeBase {
		gitCommand(t, repo, "add", ".")
		gitCommand(t, repo, "commit", "-m", "head")
	}
	writeMigrations(t, repo, stagedFiles)
	if len(stagedFiles) > 0 {
		gitCommand(t, repo, "add", ".")
	}
	return repo, baseSHA
}

func writeMigrations(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(repo, migrationsDir)
	for name, contents := range files {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitCommand(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

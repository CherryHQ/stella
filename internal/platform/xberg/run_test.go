package xberg

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestChildEnvironmentDropsSecrets is the regression guard for the reason this
// package exists: Xberg parses untrusted input, so nothing outside the whitelist
// may reach it — least of all provider credentials.
func TestChildEnvironmentDropsSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	t.Setenv("STELLA_DATABASE_URL", "postgres://should-not-leak")
	t.Setenv("LANG", "en_US.UTF-8")

	env := childEnvironment()

	for _, entry := range env {
		if strings.Contains(entry, "should-not-leak") {
			t.Errorf("secret crossed the process boundary: %q", entry)
		}
		key, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(allowedEnvironment, key) && key != "NO_PROXY" && key != "no_proxy" {
			t.Errorf("unexpected variable %q passed to Xberg", key)
		}
	}
	if !slices.Contains(env, "LANG=en_US.UTF-8") {
		t.Error("whitelisted LANG was dropped")
	}
}

func TestCommandAnchorsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	binary := dir + "/xberg"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := Command(context.Background(), binary, "extract", "/tmp/whatever")
	if cmd.Dir != dir {
		t.Errorf("cmd.Dir = %q, want %q — a caller-controlled cwd lets a local user plant xberg.toml", cmd.Dir, dir)
	}
}

// TestRunRejectsOversizedOutput proves output limits fail loudly. A silently
// truncated extraction would be indistinguishable from a short document.
func TestRunRejectsOversizedOutput(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no POSIX shell")
	}
	script := t.TempDir() + "/spew"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -c 100000 /dev/zero | tr '\\0' 'x'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := Run(context.Background(), script, nil, Limits{Stdout: 1024})
	if err == nil {
		t.Fatal("oversized stdout was accepted")
	}
	if !strings.Contains(err.Error(), "stdout exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

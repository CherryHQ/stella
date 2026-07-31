package manifestplugins

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestRuntimeMiseEnv_PerUser(t *testing.T) {
	stellaHome := t.TempDir()
	userDataDir := filepath.Join(stellaHome, "users", "u1", ".mise-tools")
	workspace := "/home/agent/workspace"

	env := RuntimeMiseEnv(stellaHome, userDataDir, workspace)

	if got := env["MISE_DATA_DIR"]; got != userDataDir {
		t.Fatalf("MISE_DATA_DIR = %q, want per-user dir %q", got, userDataDir)
	}
	for key, sub := range map[string]string{
		"MISE_CACHE_DIR":  "cache",
		"MISE_STATE_DIR":  "state",
		"MISE_CONFIG_DIR": "config",
	} {
		want := filepath.Join(userDataDir, sub)
		if env[key] != want {
			t.Fatalf("%s = %q, want %q (under per-user tree)", key, env[key], want)
		}
	}
	if env["MISE_NOT_FOUND_AUTO_INSTALL"] != "true" {
		t.Fatalf("auto-install should be enabled for a writable per-user tree, got %q", env["MISE_NOT_FOUND_AUTO_INSTALL"])
	}
	if env["MISE_STATE_DIR"] == "/tmp/mise-state" {
		t.Fatalf("state should live in the per-user tree, not the read-only fallback, got %q", env["MISE_STATE_DIR"])
	}

	// Global config stays the shared system _builtin layer.
	wantGlobal := ScopeConfigPath(stellaHome, builtinScope)
	if env["MISE_GLOBAL_CONFIG_FILE"] != wantGlobal {
		t.Fatalf("MISE_GLOBAL_CONFIG_FILE = %q, want _builtin %q", env["MISE_GLOBAL_CONFIG_FILE"], wantGlobal)
	}

	// The project workspace is trusted (both the bwrap /workspace mount and the
	// host path, so it resolves regardless of backend).
	trusted := strings.Split(env["MISE_TRUSTED_CONFIG_PATHS"], string(filepath.ListSeparator))
	for _, want := range []string{wantGlobal, pkgsandbox.MountWorkspace, workspace} {
		if !slices.Contains(trusted, want) {
			t.Fatalf("trusted paths %v missing %q", trusted, want)
		}
	}
}

func TestRuntimeMiseEnv_FallbackWhenNoUser(t *testing.T) {
	stellaHome := t.TempDir()

	env := RuntimeMiseEnv(stellaHome, "", "")

	wantData := filepath.Join(stellaHome, ".mise-tools")
	if env["MISE_DATA_DIR"] != wantData {
		t.Fatalf("MISE_DATA_DIR = %q, want system tree %q", env["MISE_DATA_DIR"], wantData)
	}
	if env["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" {
		t.Fatalf("auto-install must stay off without a writable tree, got %q", env["MISE_NOT_FOUND_AUTO_INSTALL"])
	}
	for _, key := range []string{"MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR"} {
		if value, ok := env[key]; ok {
			t.Fatalf("%s should follow sandbox XDG roots without a writable tree, got %q", key, value)
		}
	}
}

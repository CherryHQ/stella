package main

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testState(t *testing.T, pid int, instance string) supervisorState {
	t.Helper()
	root, err := os.MkdirTemp("", "stella-testbed-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return supervisorState{Version: stateVersion, Owner: processIdentity{PID: pid, Started: "now"}, Instance: instance, Root: root}
}

func TestPublishStateDoesNotReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	first := testState(t, 1, "first")
	if err := publishState(path, first); err != nil {
		t.Fatal(err)
	}
	if err := publishState(path, testState(t, 2, "second")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("publishState error = %v, want EEXIST", err)
	}
	got, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("state = %#v, want %#v", got, first)
	}
}

func TestLoadStateRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(path); err == nil {
		t.Fatal("loadState accepted mode 0644")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(path); err == nil {
		t.Fatal("loadState accepted symlink")
	}
}

func TestRemoveOwnedStatePreservesNewOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	old, next := testState(t, 1, "old"), testState(t, 2, "next")
	if err := publishState(path, next); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedState(path, old); err == nil {
		t.Fatal("removeOwnedState removed changed state")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state removed: %v", err)
	}
}

func TestStatePathIsCheckoutSpecific(t *testing.T) {
	if statePath("/tmp/a") == statePath("/tmp/b") {
		t.Fatal("different checkouts share state path")
	}
	first, second := statePath("/tmp/a"), statePath("/tmp/a")
	if first != second {
		t.Fatal("state path is unstable")
	}
}

func TestStopIsIdempotentWhenNoStateExists(t *testing.T) {
	called := false
	err := stopWith(context.Background(), filepath.Join(t.TempDir(), "missing"), func(int) (processIdentity, error) {
		called = true
		return processIdentity{}, nil
	}, func(int) error { called = true; return nil })
	if err != nil || called {
		t.Fatalf("stop = %v, called = %v; want nil and no process operation", err, called)
	}
}

func TestStopRemovesStaleStateWithoutSignalling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	state := testState(t, 42, "old")
	if err := publishState(path, state); err != nil {
		t.Fatal(err)
	}
	signalled := false
	err := stopWith(context.Background(), path, func(int) (processIdentity, error) {
		return processIdentity{PID: 42, Started: "reused"}, nil
	}, func(int) error { signalled = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if signalled {
		t.Fatal("stop signalled a reused PID")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale state remains: %v", err)
	}
	if _, err := os.Stat(state.Root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale test root remains: %v", err)
	}
}

func TestStopRemovesDeadStateWithoutSignalling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	state := testState(t, 42, "old")
	if err := publishState(path, state); err != nil {
		t.Fatal(err)
	}
	signalled := false
	err := stopWith(context.Background(), path, func(int) (processIdentity, error) {
		return processIdentity{}, fs.ErrNotExist
	}, func(int) error { signalled = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if signalled {
		t.Fatal("stop signalled a dead PID")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale state remains: %v", err)
	}
	if _, err := os.Stat(state.Root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale test root remains: %v", err)
	}
}

func TestEnsurePortAvailableRejectsExistingListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := ensurePortAvailable(port); err == nil {
		t.Fatal("ensurePortAvailable accepted an occupied port")
	}
}

func TestServerEnvironmentDoesNotInheritStellaOrAuthConfig(t *testing.T) {
	t.Setenv("STELLA_BASE_URL", "https://must-not-leak.example")
	t.Setenv("AUTH_OAUTH_PROVIDERS", "must-not-leak")
	t.Setenv("OIDC_ISSUER_URL", "https://must-not-leak.example")

	got := map[string]string{}
	for _, entry := range serverEnvironment("/tmp/test-home", "postgres://test", "vault-secret", 25678) {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed environment entry %q", entry)
		}
		got[name] = value
	}
	for _, name := range []string{"STELLA_BASE_URL", "AUTH_OAUTH_PROVIDERS", "OIDC_ISSUER_URL"} {
		if _, exists := got[name]; exists {
			t.Fatalf("%s leaked into isolated server environment", name)
		}
	}
	if got["STELLA_HOME"] != "/tmp/test-home" || got["STELLA_DATABASE_URL"] != "postgres://test" || got["STELLA_VAULT_KEY"] != "vault-secret" || got["PORT"] != "25678" {
		t.Fatalf("isolated environment missing explicit values: %#v", got)
	}
}

func TestServerEnvironmentPassesSandboxBackendSelection(t *testing.T) {
	t.Setenv("STELLA_SANDBOX_BACKEND", "bridge")
	t.Setenv("STELLA_DOCKER_SANDBOX_MODE", "host")
	t.Setenv("STELLA_EVAL_BRIDGE_DIR", "/tmp/bindings")
	t.Setenv("STELLA_EVAL_CODE_TOOL_SURFACE", "only")

	got := map[string]string{}
	for _, entry := range serverEnvironment("/tmp/test-home", "postgres://test", "vault-secret", 25678) {
		name, value, _ := strings.Cut(entry, "=")
		got[name] = value
	}
	if got["STELLA_SANDBOX_BACKEND"] != "bridge" || got["STELLA_DOCKER_SANDBOX_MODE"] != "host" || got["STELLA_EVAL_BRIDGE_DIR"] != "/tmp/bindings" || got["STELLA_EVAL_CODE_TOOL_SURFACE"] != "only" {
		t.Fatalf("sandbox backend selection must reach stellad: %#v", got)
	}
}

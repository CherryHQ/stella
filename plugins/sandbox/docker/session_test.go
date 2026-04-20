package docker

import (
	"errors"
	"testing"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// pointToUnreachableDaemon steers the moby SDK at a socket path that cannot
// exist, so Available()'s ServerVersion probe fails. Clears TLS/cert env so
// none of them override DOCKER_HOST.
func pointToUnreachableDaemon(t *testing.T) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/anna-test-docker.sock")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
}

func TestFactoryName(t *testing.T) {
	f := NewFactory(Config{})
	if f.Name() != "docker" {
		t.Fatalf("expected %q, got %q", "docker", f.Name())
	}
}

func TestFactoryAvailable_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f := NewFactory(Config{})
	if f.Available() {
		t.Fatal("expected Available() == false when daemon is unreachable")
	}
}

func TestFactorySupported_WhitelistRejected(t *testing.T) {
	f := NewFactory(Config{})
	policy := Policy{
		Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()},
		Network:    sandboxNetworkWhitelist(),
		Relaxed:    false,
	}
	err := f.Supported(policy)
	if err == nil {
		t.Fatal("expected error for whitelist mode, got nil")
	}
	pce := &PolicyCompatibilityError{}
	ok := errors.As(err, &pce)
	if !ok {
		t.Fatalf("expected *PolicyCompatibilityError, got %T", err)
	}
	if !pce.RelaxedWouldHelp {
		t.Fatal("expected RelaxedWouldHelp=true for whitelist rejection")
	}
}

func TestFactorySupported_WhitelistRelaxedAllowed(t *testing.T) {
	f := NewFactory(Config{})
	if !f.Available() {
		t.Skip("docker daemon not reachable; skipping")
	}
	policy := Policy{
		Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()},
		Network:    sandboxNetworkWhitelist(),
		Relaxed:    true,
	}
	if err := f.Supported(policy); err != nil {
		t.Fatalf("expected no error for whitelist+relaxed, got %v", err)
	}
}

func TestFactorySupported_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f := NewFactory(Config{})
	policy := Policy{
		Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()},
	}
	err := f.Supported(policy)
	if err == nil {
		t.Fatal("expected error when daemon is unreachable")
	}
	pce := &PolicyCompatibilityError{}
	ok := errors.As(err, &pce)
	if !ok {
		t.Fatalf("expected *PolicyCompatibilityError, got %T", err)
	}
	if pce.RelaxedWouldHelp {
		t.Fatal("expected RelaxedWouldHelp=false when daemon unreachable")
	}
}

// sandboxNetworkWhitelist returns a NetworkPolicy with whitelist mode.
func sandboxNetworkWhitelist() sandboxpkg.NetworkPolicy {
	return sandboxpkg.NetworkPolicy{
		Mode:      sandboxpkg.NetworkWhitelist,
		Allowlist: []string{"example.com"},
	}
}

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

func TestFactorySupported_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f := NewFactory(Config{})
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: t.TempDir()},
	}
	err := f.Supported(policy)
	if err == nil {
		t.Fatal("expected error when daemon is unreachable")
	}
	pce := &sandboxpkg.PolicyCompatibilityError{}
	ok := errors.As(err, &pce)
	if !ok {
		t.Fatalf("expected *PolicyCompatibilityError, got %T", err)
	}
}

package docker

import (
	"errors"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// pointToUnreachableDaemon steers the moby SDK at a socket path that cannot
// exist, so Available()'s ServerVersion probe fails. Clears TLS/cert env so
// none of them override DOCKER_HOST.
func pointToUnreachableDaemon(t *testing.T) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/stella-test-docker.sock")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
}

func TestNewFactory_NoStellaHome_Infallible(t *testing.T) {
	f, err := NewFactory(Config{})
	if err != nil {
		t.Fatalf("NewFactory(Config{}) should not fail: %v", err)
	}
	if f.Name() != "docker" {
		t.Fatalf("expected %q, got %q", "docker", f.Name())
	}
}

func TestNewFactory_VolumeError_Propagated(t *testing.T) {
	withVolumeEnv(t, true, "")
	_, err := NewFactory(Config{StellaHome: "/fake/stella"})
	if err == nil {
		t.Fatal("expected error when in-container and STELLA_HOME_VOLUME unset")
	}
	if !strings.Contains(err.Error(), "STELLA_HOME_VOLUME") {
		t.Fatalf("error should mention STELLA_HOME_VOLUME, got: %v", err)
	}
}

func TestFactoryAvailable_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f, _ := NewFactory(Config{})
	if f.Available() {
		t.Fatal("expected Available() == false when daemon is unreachable")
	}
}

func TestFactorySupported_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f, _ := NewFactory(Config{})
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

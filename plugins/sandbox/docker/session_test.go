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
	f, err := NewFactoryWithMountSources(Config{}, nil)
	if err != nil {
		t.Fatalf("NewFactoryWithMountSources should not fail: %v", err)
	}
	if f.Name() != "docker" {
		t.Fatalf("expected %q, got %q", "docker", f.Name())
	}
}

func TestNewFactory_DockerModeError_Propagated(t *testing.T) {
	withDockerModeEnv(t, "", "", "")
	_, err := NewFactoryWithMountSources(Config{StellaHome: "/fake/stella"}, nil)
	if err == nil {
		t.Fatal("expected error when docker sandbox mode is unset")
	}
	if !strings.Contains(err.Error(), dockerSandboxModeEnv) {
		t.Fatalf("error should mention %s, got: %v", dockerSandboxModeEnv, err)
	}
}

func TestFactoryAvailable_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f, _ := NewFactoryWithMountSources(Config{}, nil)
	if f.Available() {
		t.Fatal("expected Available() == false when daemon is unreachable")
	}
}

func TestFactorySupported_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f, _ := NewFactoryWithMountSources(Config{}, nil)
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

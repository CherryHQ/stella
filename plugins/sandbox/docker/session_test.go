package docker

import (
	"errors"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
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

func TestNewFactory_DockerModeError_Propagated(t *testing.T) {
	withDockerModeEnv(t, "", "", "")
	_, err := NewFactory(Config{StellaHome: "/fake/stella"})
	if err == nil {
		t.Fatal("expected error when docker sandbox mode is unset")
	}
	if !strings.Contains(err.Error(), dockerSandboxModeEnv) {
		t.Fatalf("error should mention %s, got: %v", dockerSandboxModeEnv, err)
	}
}

func TestFactoryAvailable_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f, _ := NewFactory(Config{Layout: hostlayout.Layout{WorkspaceSource: "/workspace", WorkingDirSource: "/workspace", Mounts: []hostlayout.Mount{{Source: "/workspace", Target: workspaceMount, Access: hostlayout.ReadWrite}}}})
	if f.Available() {
		t.Fatal("expected Available() == false when daemon is unreachable")
	}
}

func TestFactorySupported_DaemonUnreachable(t *testing.T) {
	pointToUnreachableDaemon(t)
	f, _ := NewFactory(Config{Layout: hostlayout.Layout{WorkspaceSource: "/workspace", WorkingDirSource: "/workspace", Mounts: []hostlayout.Mount{{Source: "/workspace", Target: workspaceMount, Access: hostlayout.ReadWrite}}}})
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: "/host/secret", WorkingDir: t.TempDir(), Mounts: []sandboxpkg.Mount{{HostPath: "/host/secret", SandboxPath: "/workspace", Access: sandboxpkg.MountReadWrite}}},
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
	if pce.Policy.Filesystem.WorkspaceRoot != "" || len(pce.Policy.Filesystem.Mounts) != 0 {
		t.Fatalf("compatibility error retained physical layout: %#v", pce.Policy.Filesystem)
	}
}

func TestNewFactoryClonesLayout(t *testing.T) {
	layout := hostlayout.Layout{WorkspaceSource: "/workspace", WorkingDirSource: "/workspace/project", Mounts: []hostlayout.Mount{{Source: "/workspace", Target: workspaceMount, Access: hostlayout.ReadWrite}}}
	factory, err := NewFactory(Config{Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	layout.Mounts[0].Source = "/redirected"
	got := factory.(*dockerFactory).cfg.Layout
	if got.Mounts[0].Source != "/workspace" {
		t.Fatalf("factory layout was mutated through Config: %#v", got)
	}
}

func TestSupportedRejectsMissingLayoutBeforeDockerAPI(t *testing.T) {
	calls := 0
	factory := &dockerFactory{clientFn: func() (*dockerclient.Client, error) {
		calls++
		return nil, errors.New("must not connect")
	}}
	if err := factory.Supported(sandboxpkg.Policy{}); err == nil {
		t.Fatal("Supported accepted missing Layout")
	}
	if calls != 0 {
		t.Fatalf("Supported consulted Docker API for invalid Layout: %d calls", calls)
	}
}

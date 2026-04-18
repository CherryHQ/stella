package docker

import (
	"errors"
	"os"
	"testing"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

func TestFactoryName(t *testing.T) {
	f := NewFactory(Config{})
	if f.Name() != "docker" {
		t.Fatalf("expected %q, got %q", "docker", f.Name())
	}
}

func TestFactoryAvailable_NoBinary(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("DOCKER_BIN", "") // ensure DOCKER_BIN doesn't interfere
	f := NewFactory(Config{})
	if f.Available() {
		t.Fatal("expected Available() == false when docker is not on PATH")
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
	// When a real docker binary is present, whitelist+Relaxed should succeed.
	if os.Getenv("DOCKER_BIN") == "" {
		_, lookErr := os.Stat("/usr/bin/docker")
		if lookErr != nil {
			t.Skip("docker not available")
		}
	}
	f := NewFactory(Config{})
	policy := Policy{
		Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()},
		Network:    sandboxNetworkWhitelist(),
		Relaxed:    true,
	}
	// Should not error — whitelist+Relaxed is allowed.
	if err := f.Supported(policy); err != nil {
		t.Fatalf("expected no error for whitelist+relaxed, got %v", err)
	}
}

func TestFactorySupported_DockerNotOnPath(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("DOCKER_BIN", "")
	f := NewFactory(Config{})
	policy := Policy{
		Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()},
	}
	err := f.Supported(policy)
	if err == nil {
		t.Fatal("expected error when docker not on PATH")
	}
	pce := &PolicyCompatibilityError{}
	ok := errors.As(err, &pce)
	if !ok {
		t.Fatalf("expected *PolicyCompatibilityError, got %T", err)
	}
	if pce.RelaxedWouldHelp {
		t.Fatal("expected RelaxedWouldHelp=false when docker binary missing")
	}
}

// sandboxNetworkWhitelist returns a NetworkPolicy with whitelist mode.
func sandboxNetworkWhitelist() sandboxpkg.NetworkPolicy {
	return sandboxpkg.NetworkPolicy{
		Mode:      sandboxpkg.NetworkWhitelist,
		Allowlist: []string{"example.com"},
	}
}

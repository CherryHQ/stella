package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestPolicyValidate(t *testing.T) {
	t.Run("valid disabled network", func(t *testing.T) {
		p := Policy{
			Filesystem: FilesystemPolicy{WorkingDir: "/tmp/ws"},
			Network:    NetworkPolicy{Mode: NetworkDisabled},
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("empty network mode defaults ok", func(t *testing.T) {
		p := Policy{Filesystem: FilesystemPolicy{WorkingDir: "/tmp/ws"}}
		if err := p.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("allow_all network mode ok", func(t *testing.T) {
		p := Policy{
			Filesystem: FilesystemPolicy{WorkingDir: "/tmp/ws"},
			Network:    NetworkPolicy{Mode: NetworkAllowAll},
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("invalid network mode", func(t *testing.T) {
		p := Policy{
			Filesystem: FilesystemPolicy{WorkingDir: "/tmp/ws"},
			Network:    NetworkPolicy{Mode: "bad"},
		}
		if err := p.Validate(); err == nil {
			t.Fatal("expected error for invalid network mode")
		}
	})
	t.Run("missing working dir", func(t *testing.T) {
		p := Policy{}
		if err := p.Validate(); err == nil {
			t.Fatal("expected error: missing WorkingDir")
		}
	})
}

func TestNetworkModeOrDefault(t *testing.T) {
	if got := (Policy{}).NetworkModeOrDefault(); got != NetworkAllowAll {
		t.Fatalf("empty mode should default to allow_all, got %q", got)
	}
	p := Policy{Network: NetworkPolicy{Mode: NetworkAllowAll}}
	if got := p.NetworkModeOrDefault(); got != NetworkAllowAll {
		t.Fatalf("expected allow_all, got %q", got)
	}
}

func TestWorkspaceRootOrDefault(t *testing.T) {
	p := Policy{Filesystem: FilesystemPolicy{WorkingDir: "/work"}}
	if got := p.WorkspaceRootOrDefault(); got != "/work" {
		t.Fatalf("expected /work, got %q", got)
	}
	p2 := Policy{Filesystem: FilesystemPolicy{WorkspaceRoot: "/root", WorkingDir: "/work"}}
	if got := p2.WorkspaceRootOrDefault(); got != "/root" {
		t.Fatalf("expected /root, got %q", got)
	}
}

func TestPolicyCompatibilityError(t *testing.T) {
	e := &PolicyCompatibilityError{Backend: "docker", Reason: "unsupported"}
	if !strings.Contains(e.Error(), "docker") {
		t.Fatal("error should mention backend")
	}
	if !strings.Contains(e.Error(), "unsupported") {
		t.Fatal("error should mention reason")
	}

	e2 := &PolicyCompatibilityError{Backend: "docker", Reason: "no daemon"}
	if !strings.Contains(e2.Error(), "docker") {
		t.Fatal("error should mention backend name")
	}
}

func TestIsPolicyCompatibilityError(t *testing.T) {
	if IsPolicyCompatibilityError(nil) {
		t.Fatal("nil should return false")
	}
	if IsPolicyCompatibilityError(errors.New("plain error")) {
		t.Fatal("plain error should return false")
	}
	pce := &PolicyCompatibilityError{Backend: "b", Reason: "r"}
	if !IsPolicyCompatibilityError(pce) {
		t.Fatal("PolicyCompatibilityError should return true")
	}
}

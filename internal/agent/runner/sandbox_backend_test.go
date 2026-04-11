package runner

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
)

func TestResolveSandboxBackendNoopWhenDisabled(t *testing.T) {
	backend, err := resolveSandboxBackend(context.Background(), GoRunnerConfig{
		DisableSandbox: true,
	})
	if err != nil {
		t.Fatalf("resolveSandboxBackend() error = %v", err)
	}
	if backend == nil {
		t.Fatal("resolveSandboxBackend() returned nil backend")
	}
	if !backend.Alive() {
		t.Fatal("noop backend should be alive")
	}
	if backend.Boxsh() != nil {
		t.Fatal("noop backend should not expose boxsh backend")
	}
	if backend.Runtime() == nil {
		t.Fatal("noop backend should expose disabled sandbox runtime")
	}
}

func TestResolveSandboxBackendRejectsUnknownBackend(t *testing.T) {
	_, err := resolveSandboxBackend(context.Background(), GoRunnerConfig{
		Sandbox: config.SandboxConfig{Backend: "unknown"},
	})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

package plugintools

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
)

func TestSandboxRuntimeFromBackendNil(t *testing.T) {
	got := SandboxRuntimeFromBackend(nil)
	if got == nil {
		t.Fatal("SandboxRuntimeFromBackend(nil) returned nil runtime")
	}
	if got.Enabled() {
		t.Fatal("expected disabled runtime when backend is nil")
	}
	if _, err := got.Exec(context.Background(), "echo hi", 1); err == nil {
		t.Fatal("expected unavailable sandbox error")
	}
}

func TestBoxshSandboxRuntimeDisabledWhenBackendNotAlive(t *testing.T) {
	runtime := SandboxRuntimeFromBackend(&boxshclient.SharedBackend{})
	if runtime == nil {
		t.Fatal("expected runtime")
	}
	if runtime.Enabled() {
		t.Fatal("expected runtime to be disabled")
	}
	if _, err := runtime.Exec(context.Background(), "echo hi", 1); err == nil {
		t.Fatal("expected backend-not-running error")
	}
}

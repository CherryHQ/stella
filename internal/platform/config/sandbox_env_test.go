package config

import (
	"os"
	"testing"
)

func TestActiveSandboxBackend(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		unset bool
		want  string
	}{
		{name: "unset defaults to local", unset: true, want: SandboxBackendLocal},
		{name: "empty defaults to local", env: "", want: SandboxBackendLocal},
		{name: "docker", env: "docker", want: SandboxBackendDocker},
		{name: "kubernetes", env: "kubernetes", want: SandboxBackendKubernetes},
		{name: "bridge", env: "bridge", want: SandboxBackendBridge},
		{name: "none", env: "none", want: SandboxBackendNone},
		{name: "padded value is trimmed", env: "  docker  ", want: SandboxBackendDocker},
		{name: "unknown value preserved for validation", env: "podman", want: "podman"},
		{name: "case-sensitive: DOCKER is unknown", env: "DOCKER", want: "DOCKER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set first so t.Setenv registers the restore of the ambient value,
			// then unset for the "operator set nothing" case.
			t.Setenv(sandboxBackendEnv, tt.env)
			if tt.unset {
				if err := os.Unsetenv(sandboxBackendEnv); err != nil {
					t.Fatal(err)
				}
			}
			if got := ActiveSandboxBackend(); got != tt.want {
				t.Fatalf("ActiveSandboxBackend() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateSandboxBackendRejectsUnknown(t *testing.T) {
	t.Setenv(sandboxBackendEnv, "kubernets")
	if err := ValidateSandboxBackend(); err == nil {
		t.Fatal("unknown backend must fail instead of falling back")
	}
	t.Setenv(sandboxBackendEnv, "kubernetes")
	if err := ValidateSandboxBackend(); err != nil {
		t.Fatal(err)
	}
}

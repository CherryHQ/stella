package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/version"
	dockerbackend "github.com/CherryHQ/stella/plugins/sandbox/docker"
)

func TestSandboxDockerImage(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{version: "", want: dockerDevImage},
		{version: "dev", want: dockerDevImage},
		{version: "v0.1.0-5-gabcdef-dirty", want: dockerDevImage},
		{version: "v0.1.0-5-gabcdef", want: dockerDevImage},
		{version: "v0.1.0-rc.1", want: dockerImageRepo + ":0.1.0-rc.1"},
		{version: "v0.1.0", want: dockerImageRepo + ":0.1.0"},
		{version: "0.1.0", want: dockerImageRepo + ":0.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			original := version.Version
			t.Cleanup(func() { version.Version = original })
			version.Version = tt.version
			if got := sandboxImage(); got != tt.want {
				t.Fatalf("sandboxImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSandboxDockerSessionErrorAddsBuildHintOnlyForDevImageFailure(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	version.Version = "dev"

	runtimeErr := errors.New(`docker preflight: runtime "runsc" is not registered`)
	if got := sandboxDockerSessionError(runtimeErr).Error(); strings.Contains(got, "sandbox:docker:build") {
		t.Fatalf("runtime error received image build hint: %s", got)
	}

	imageErr := &dockerbackend.ImageUnavailableError{Err: errors.New("image pull failed")}
	if got := sandboxDockerSessionError(imageErr).Error(); !strings.Contains(got, "sandbox:docker:build") {
		t.Fatalf("dev image error missing build hint: %s", got)
	}
}

func TestKubernetesBackendConfigScopesStartupCleanupToLegacyResources(t *testing.T) {
	cfg := config.ServerConfig{KubernetesSandbox: config.KubernetesSandboxConfig{
		Image: "sandbox:test", ServerPort: 8080, StartupTimeout: time.Minute, ServerURL: "https://sandbox.example",
	}}
	for _, tt := range []struct {
		name            string
		bootID          string
		skipBootCleanup bool
	}{
		{name: "server boot", bootID: "executor-boot", skipBootCleanup: false},
		{name: "maintenance command", bootID: "", skipBootCleanup: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := kubernetesBackendConfig(cfg, tt.bootID, "bundle-revision")
			if got.Image != cfg.KubernetesSandbox.Image || got.ServerPort != cfg.KubernetesSandbox.ServerPort || got.StartupTimeout != cfg.KubernetesSandbox.StartupTimeout || got.ServerURL != cfg.KubernetesSandbox.ServerURL {
				t.Fatalf("kubernetes config lost deployment settings: %#v", got)
			}
			if got.BootID != tt.bootID || got.BundleRevision != "bundle-revision" {
				t.Fatalf("kubernetes config identity = boot %q, bundle %q; want boot %q, bundle %q", got.BootID, got.BundleRevision, tt.bootID, "bundle-revision")
			}
			if got.SkipPreviousBootCleanup != tt.skipBootCleanup {
				t.Fatalf("SkipPreviousBootCleanup = %v, want %v", got.SkipPreviousBootCleanup, tt.skipBootCleanup)
			}
		})
	}
}

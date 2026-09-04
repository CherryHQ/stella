package main

import (
	"errors"
	"strings"
	"testing"

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
			if got := sandboxDockerImage(); got != tt.want {
				t.Fatalf("sandboxDockerImage() = %q, want %q", got, tt.want)
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

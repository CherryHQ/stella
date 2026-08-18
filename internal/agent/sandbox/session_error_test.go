package sandbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/version"
	dockerplugin "github.com/CherryHQ/stella/plugins/sandbox/docker"
)

func TestDockerSessionErrorAddsBuildHintOnlyForDevImageFailure(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	version.Version = "dev"

	runtimeErr := errors.New(`docker preflight: runtime "runsc" is not registered`)
	if got := dockerSessionError(runtimeErr).Error(); strings.Contains(got, "sandbox:docker:build") {
		t.Fatalf("runtime error received image build hint: %s", got)
	}

	imageErr := &dockerplugin.ImageUnavailableError{Err: errors.New("image pull failed")}
	if got := dockerSessionError(imageErr).Error(); !strings.Contains(got, "sandbox:docker:build") {
		t.Fatalf("dev image error missing build hint: %s", got)
	}
}

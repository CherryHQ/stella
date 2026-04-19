package config

import (
	"strings"

	"github.com/vaayne/anna/internal/version"
)

// sandboxDockerImageRepo is the published registry/repo for the anna-sandbox
// image. Release builds pull from this repo at a tag matching the anna
// binary version.
const sandboxDockerImageRepo = "ghcr.io/vaayne/anna-sandbox"

// sandboxDockerDevImage is the tag used for local dev builds. Produced by
// `mise run sandbox:docker:build`; never pushed to a registry.
const sandboxDockerDevImage = "anna-sandbox:dev"

// SandboxDockerImage returns the sandbox container image tag for the current
// anna build. Dev builds use a local `anna-sandbox:dev` tag (produced by
// `mise run sandbox:docker:build`); tagged releases pull
// `ghcr.io/vaayne/anna-sandbox:<version>` from GHCR. The version is
// normalized to strip any leading "v" so "v1.2.3" and "1.2.3" resolve to
// the same image tag.
func SandboxDockerImage() string {
	if version.IsDev() {
		return sandboxDockerDevImage
	}
	return sandboxDockerImageRepo + ":" + strings.TrimPrefix(version.Version, "v")
}

// SandboxDockerImageIsDev reports whether SandboxDockerImage resolves to the
// local dev tag. Callers use this to surface a build-image hint when the
// image is missing locally.
func SandboxDockerImageIsDev() bool {
	return version.IsDev()
}

package sandbox

import (
	"strings"

	"github.com/CherryHQ/stella/internal/platform/version"
)

// dockerImageRepo is the published registry/repo for the stella-sandbox
// image. Release builds pull from this repo at a tag matching the stella
// binary version.
const dockerImageRepo = "ghcr.io/cherryhq/stella-sandbox"

// dockerDevImage is the tag used for local dev builds. Produced by
// `mise run sandbox:docker:build`; never pushed to a registry.
const dockerDevImage = "stella-sandbox:dev"

// dockerImage returns the sandbox container image tag for the current
// stella build. Dev builds use a local `stella-sandbox:dev` tag (produced by
// `mise run sandbox:docker:build`); tagged releases pull
// `ghcr.io/cherryhq/stella-sandbox:<version>` from GHCR. The version is
// normalized to strip any leading "v" so "v1.2.3" and "1.2.3" resolve to
// the same image tag.
func dockerImage() string {
	if version.IsDev() {
		return dockerDevImage
	}
	return dockerImageRepo + ":" + strings.TrimPrefix(version.Version, "v")
}

// dockerImageIsDev reports whether dockerImage resolves to the local dev tag.
// Callers use this to surface a build-image hint when the image is missing
// locally.
func dockerImageIsDev() bool {
	return version.IsDev()
}

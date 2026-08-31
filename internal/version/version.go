// Package version exposes the stella binary version.
//
// The Version variable is set via -ldflags="-X github.com/CherryHQ/stella/internal/version.Version=..."
// during release builds. At dev-build time it defaults to "dev".
package version

import (
	"regexp"
	"strings"
)

// Version is the stella build version. Set via ldflags.
var Version = "dev"

var (
	gitDescribeVersion = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+(?:-dirty)?$`)
	devVersion         = regexp.MustCompile(`-[dD]ev(?:[.-]|$)`)
	releaseVersion     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
)

// Commit is the git commit for this build. Set via ldflags.
var Commit = ""

// BuildDate is the build timestamp. Set via ldflags.
var BuildDate = ""

// IsDev reports whether the current build is a dev build (not a tagged release).
// Tagged releases include prereleases such as "0.1.0-rc.1"; dev builds produce
// values such as "dev", a bare commit, or git-describe output.
func IsDev() bool {
	normalized := strings.TrimPrefix(strings.TrimSpace(Version), "v")
	if normalized == "" || normalized == "dev" {
		return true
	}
	if gitDescribeVersion.MatchString(normalized) || devVersion.MatchString(normalized) {
		return true
	}
	return !releaseVersion.MatchString(normalized)
}

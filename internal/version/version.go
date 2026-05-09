// Package version exposes the stella binary version.
//
// The Version variable is set via -ldflags="-X github.com/CherryHQ/stella/internal/version.Version=..."
// during release builds. At dev-build time it defaults to "dev".
package version

// Version is the stella build version. Set via ldflags.
var Version = "dev"

// Commit is the git commit for this build. Set via ldflags.
var Commit = ""

// BuildDate is the build timestamp. Set via ldflags.
var BuildDate = ""

// IsDev reports whether the current build is a dev build (not a tagged release).
// Tagged releases set Version to a semver like "0.1.0" or "v0.1.0"; dev builds
// produce values like "dev" or "v0.1.0-5-gabcdef-dirty".
func IsDev() bool {
	if Version == "" || Version == "dev" {
		return true
	}
	// Any git-describe output that isn't an exact tag contains "-g<sha>" and/or "-dirty".
	for i := 0; i < len(Version); i++ {
		if Version[i] == '-' {
			return true
		}
	}
	return false
}

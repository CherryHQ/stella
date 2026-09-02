package version

import "strings"

// DisplayVersion returns the normalized build version, or "dev" for non-release builds.
func DisplayVersion() string {
	normalized := NormalizeVersion(Version)
	if normalized == "" {
		return "dev"
	}
	return normalized
}

// NormalizeVersion strips a leading "v" prefix and validates the version string.
func NormalizeVersion(v string) string {
	trimmed := strings.TrimSpace(v)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if trimmed == "" {
		return ""
	}
	for part := range strings.SplitSeq(trimmed, ".") {
		if part == "" {
			return ""
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return trimmed
			}
		}
	}
	return trimmed
}

package skills

import "regexp"

// safeNameRe matches valid skill names: alphanumeric, hyphens, dots, underscores.
// Aligned with safeSegmentRe in install.go to ensure install/remove consistency.
var safeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$|^[a-zA-Z0-9]$`)

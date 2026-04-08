package skills

import "regexp"

// safeNameRe matches valid skill names: alphanumeric, hyphens, dots, underscores.
var safeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$|^[a-zA-Z0-9]$`)

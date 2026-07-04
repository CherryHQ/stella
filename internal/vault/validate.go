package vault

import (
	"fmt"
	"regexp"
	"strings"
)

var nameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// reservedPrefixes are env var name prefixes that vault entries must not use.
var reservedPrefixes = []string{
	"STELLA_",
	"LC_",
	"XDG_",
}

// reservedWordPrefixes are single-word reserved names. A vault entry name is
// rejected if it equals one of these words or starts with WORD_ (so PATH_FOO
// is rejected but PATHOLOGICAL is not).
var reservedWordPrefixes = []string{
	"PATH",
	"HOME",
	"USER",
	"SHELL",
	"LANG",
	"TERM",
	"TMPDIR",
}

// StellaTokenName is the reserved sandbox session token env var.
const StellaTokenName = "STELLA_TOKEN"

// ValidateName checks that a vault entry name is a valid env var name
// and is not reserved.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("vault: name must not be empty")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("vault: name %q is invalid: must match ^[A-Z][A-Z0-9_]{0,127}$", name)
	}

	// Check reserved prefixes (STELLA_, LC_, XDG_). STELLA_TOKEN is Stella-managed;
	// user-facing writes must never set it, so it is rejected here like any other
	// STELLA_ name. Allowing it through would let a vault entry shadow the scoped
	// sandbox session token.
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return fmt.Errorf("vault: name %q is reserved: must not start with %q", name, prefix)
		}
	}

	// Check single-word reserved names used as a prefix (PATH, HOME, etc.).
	// A name is rejected if it equals the reserved word or starts with WORD_
	// (to catch PATH_EXTRA but not PATHOLOGICAL).
	for _, word := range reservedWordPrefixes {
		if name == word || strings.HasPrefix(name, word+"_") {
			return fmt.Errorf("vault: name %q is reserved", name)
		}
	}

	return nil
}

package skill

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrSkillNotMutable rejects immutable, deprecated, and project writes.
var ErrSkillNotMutable = errors.New("skill is not mutable")

// ErrInvalidSkillFilePath rejects keys whose runtime path would differ from the
// canonical relative path committed to Home.
var ErrInvalidSkillFilePath = errors.New("invalid skill file path")

func validateSkillFilePaths(files map[string]string) error {
	for raw := range files {
		clean := path.Clean(raw)
		if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") || path.IsAbs(raw) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
			return fmt.Errorf("%w: %q must be a canonical relative path", ErrInvalidSkillFilePath, raw)
		}
	}
	return nil
}

package skills

import (
	"fmt"
	"os"
)

// Remove deletes an installed skill directory after validating the name.
func Remove(name, skillDir string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}

	if !safeNameRe.MatchString(name) {
		return fmt.Errorf("invalid skill name %q: must be lowercase alphanumeric with hyphens", name)
	}

	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not found at %s", name, skillDir)
	}

	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("remove skill %q: %w", name, err)
	}

	return nil
}

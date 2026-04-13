package skills

import (
	"context"
	"fmt"
	"os"

	"github.com/vaayne/anna/internal/sandbox"
)

// Remove deletes an installed skill directory after validating the name.
func Remove(ctx context.Context, host sandbox.Host, name, skillDir string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if !safeNameRe.MatchString(name) {
		return fmt.Errorf("invalid skill name %q: must be lowercase alphanumeric with hyphens", name)
	}
	info, err := statSkillPath(ctx, host, skillDir)
	if err != nil {
		return fmt.Errorf("skill %q not found at %s: %w", name, skillDir, err)
	}
	if !info.Exists {
		return fmt.Errorf("skill %q not found at %s", name, skillDir)
	}
	if host != nil {
		if err := host.Remove(ctx, skillDir, true); err != nil {
			return fmt.Errorf("remove skill %q: %w", name, err)
		}
		return nil
	}
	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("remove skill %q: %w", name, err)
	}
	return nil
}

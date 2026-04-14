package skills

import (
	"context"
	"fmt"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// Remove deletes an installed skill directory after validating the name.
func Remove(ctx context.Context, runtime pkgplugins.ToolRuntime, name, skillDir string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if !safeNameRe.MatchString(name) {
		return fmt.Errorf("invalid skill name %q: must be lowercase alphanumeric with hyphens", name)
	}
	info, err := statSkillPath(ctx, runtime, skillDir)
	if err != nil {
		return fmt.Errorf("skill %q not found at %s: %w", name, skillDir, err)
	}
	if !info.Exists {
		return fmt.Errorf("skill %q not found at %s", name, skillDir)
	}
	runtime, closeRuntime, err := resolveSkillRuntime(ctx, runtime, skillDir)
	if err != nil {
		return fmt.Errorf("remove skill %q: %w", name, err)
	}
	defer closeRuntime()
	if err := runtime.Remove(ctx, skillDir, true); err != nil {
		return fmt.Errorf("remove skill %q: %w", name, err)
	}
	return nil
}

package skills

import (
	"context"
	"fmt"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// Remove deletes an installed skill directory after validating the name.
func Remove(ctx context.Context, runtime pkgplugins.ToolRuntime, name, skillDir string) error {
	if err := skillNameValidationError(name, name); err != nil {
		return err
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

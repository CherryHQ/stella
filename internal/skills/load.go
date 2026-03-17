package skills

import (
	"fmt"
	"os"

	"github.com/vaayne/anna/internal/agent/runner"
)

func (t *SkillsTool) load(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for load action")
	}

	all := runner.LoadSkills(t.annaHome, t.workspace, t.cwd)
	for _, s := range all {
		if s.Name == name {
			data, err := os.ReadFile(s.FilePath)
			if err != nil {
				return "", fmt.Errorf("load skill %q: %w", name, err)
			}
			return fmt.Sprintf("<skill_content name=%q base_dir=%q>\n%s\n</skill_content>", s.Name, s.BaseDir, data), nil
		}
	}

	return "", fmt.Errorf("skill %q not found", name)
}

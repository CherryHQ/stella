package skills

import "encoding/json"

type installedSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Removable   bool   `json:"removable"`
}

func (t *SkillsTool) list() (string, error) {
	all := LoadSkills(t.annaHome, t.workspace, t.cwd, t.userSkillsDir)
	if len(all) == 0 {
		return "No skills installed.", nil
	}

	results := make([]installedSkill, len(all))
	for i, s := range all {
		results[i] = installedSkill{
			Name:        s.Name,
			Description: s.Description,
			Status:      s.Status,
			Source:      s.Source,
			Path:        s.FilePath,
			Removable:   s.Source == "project" || s.Source == "user",
		}
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	return string(out), nil
}

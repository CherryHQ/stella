package skills

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func validateCreateInput(name, description string) []string {
	var errs []string
	if err := skillNameValidationError(name, name); err != nil {
		errs = append(errs, err.Error())
	}
	if strings.TrimSpace(description) == "" {
		errs = append(errs, "description is required")
	}
	return errs
}

func buildSkillFile(name, description, createdAt, body string) string {
	fm := map[string]string{
		"name":        name,
		"description": description,
		"created-at":  createdAt,
	}
	keys := make([]string, 0, len(fm))
	for k := range fm {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range keys {
		line, _ := yaml.Marshal(map[string]string{k: fm[k]})
		b.Write(line)
	}
	b.WriteString("---\n")
	if body != "" {
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

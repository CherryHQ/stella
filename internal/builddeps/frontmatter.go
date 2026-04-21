package builddeps

import (
	"strings"

	"gopkg.in/yaml.v3"
)

func unmarshalYAMLFrontmatter(content string, out any) bool {
	block, ok := splitYAMLFrontmatter(content)
	if !ok {
		return false
	}
	return yaml.Unmarshal([]byte(block), out) == nil
}

func splitYAMLFrontmatter(content string) (string, bool) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return "", false
	}
	rest := strings.TrimPrefix(content, "---\n")
	block, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return "", false
	}
	return block, true
}

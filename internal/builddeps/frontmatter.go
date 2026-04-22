package builddeps

import (
	"fmt"
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

func setFrontmatterMetadata(content, key string, value any) (string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return "", fmt.Errorf("missing YAML frontmatter")
	}
	rest := strings.TrimPrefix(content, "---\n")
	block, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return "", fmt.Errorf("missing closing YAML frontmatter delimiter")
	}
	meta := map[string]any{}
	if err := yaml.Unmarshal([]byte(block), &meta); err != nil {
		return "", err
	}
	metadata, _ := meta["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[key] = value
	meta["metadata"] = metadata
	updated, err := yaml.Marshal(meta)
	if err != nil {
		return "", err
	}
	return "---\n" + string(updated) + "---\n" + body, nil
}

package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseResource parses a markdown file's YAML frontmatter into a Resource.
// id falls back to the provided fallbackID (usually the dir or file basename) when
// the frontmatter lacks an explicit id/name.
func parseResource(kind Kind, fallbackID, raw string) (Resource, error) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	fmBlock, body, err := splitFrontmatter(normalized)
	if err != nil {
		return Resource{}, err
	}

	meta := map[string]any{}
	if fmBlock != "" {
		if err := yaml.Unmarshal([]byte(fmBlock), &meta); err != nil {
			return Resource{}, fmt.Errorf("invalid yaml frontmatter: %w", err)
		}
	}

	r := Resource{
		Kind:     kind,
		ID:       firstString(meta, "id", "name", fallbackID),
		Name:     firstString(meta, "name", "id", fallbackID),
		Content:  strings.TrimLeft(body, "\n"),
		Metadata: map[string]any{},
	}
	r.Description = firstString(meta, "description")
	r.Tags = stringSlice(meta["tags"])
	sum := sha256.Sum256([]byte(r.Content))
	r.Hash = hex.EncodeToString(sum[:])

	// Keep only kind-specific extras in Metadata; drop common fields already promoted.
	for k, v := range meta {
		switch k {
		case "id", "name", "description", "tags":
			continue
		default:
			r.Metadata[k] = v
		}
	}

	return r, nil
}

// splitFrontmatter extracts the YAML block between leading "---" delimiters.
// Returns ("", content, nil) when no frontmatter is present.
func splitFrontmatter(content string) (string, string, error) {
	if !strings.HasPrefix(content, "---") {
		return "", content, nil
	}
	rest := content[3:]
	before, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return "", "", fmt.Errorf("no closing frontmatter delimiter")
	}
	yamlBlock := strings.TrimPrefix(before, "\n")
	body := after
	body = strings.TrimPrefix(body, "\n")
	return yamlBlock, body, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	if len(keys) > 0 {
		return keys[len(keys)-1]
	}
	return ""
}

func stringSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

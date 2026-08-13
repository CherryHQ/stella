package delegate

import (
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/resources"
)

// PresetRoot is one process-visible delegate search tier.
type PresetRoot struct {
	Path   string
	Source string
}

// LoadDelegatePresets loads embedded release presets, then discovers overrides
// through the active Session filesystem in increasing priority order.
func LoadDelegatePresets(files pkgsandbox.FileAccess, roots []PresetRoot) []DelegatePreset {
	registry, err := resources.Default()
	if err != nil {
		slog.Error("failed to load builtin delegate presets", "error", err)
		return nil
	}
	return mergeDelegatePresets(builtinDelegatePresets(registry), loadDelegatePresets(files, roots))
}

func loadDelegatePresets(files pkgsandbox.FileAccess, roots []PresetRoot) []DelegatePreset {
	if files == nil {
		return nil
	}
	indexByName := map[string]int{}
	var presets []DelegatePreset

	add := func(p DelegatePreset) {
		if idx, ok := indexByName[p.Name]; ok {
			presets[idx] = p
			return
		}
		indexByName[p.Name] = len(presets)
		presets = append(presets, p)
	}

	dedupPaths := map[string]bool{}
	addDir := func(dir, source string) {
		clean := path.Clean(dir)
		if dir == "" || dedupPaths[clean] {
			return
		}
		dedupPaths[clean] = true
		for _, p := range loadPresetsFromDir(files, dir, source) {
			add(p)
		}
	}

	for _, root := range roots {
		if root.Path != "" && root.Source != "" {
			addDir(path.Join(root.Path, ".agents", "delegates"), root.Source)
		}
	}

	return presets
}

func mergeDelegatePresets(base, overrides []DelegatePreset) []DelegatePreset {
	indexByName := make(map[string]int, len(base)+len(overrides))
	out := append([]DelegatePreset(nil), base...)
	for i := range out {
		indexByName[out[i].Name] = i
	}
	for _, preset := range overrides {
		if index, ok := indexByName[preset.Name]; ok {
			out[index] = preset
			continue
		}
		indexByName[preset.Name] = len(out)
		out = append(out, preset)
	}
	return out
}

func builtinDelegatePresets(registry *resources.Registry) []DelegatePreset {
	var presets []DelegatePreset
	for _, resource := range registry.List(resources.KindDelegate) {
		preset := DelegatePreset{
			Name: resource.Name, Description: resource.Description,
			System: strings.TrimSpace(resource.Content), Source: "builtin",
		}
		if preset.Name == "" {
			preset.Name = resource.ID
		}
		if value, ok := resource.Metadata["model"].(string); ok {
			preset.Model = value
		}
		if value, exists := resource.Metadata["tools"]; exists {
			preset.HasTools = true
			switch tools := value.(type) {
			case []string:
				preset.Tools = append([]string(nil), tools...)
			case []any:
				for _, tool := range tools {
					name, ok := tool.(string)
					if !ok {
						preset.Tools = nil
						break
					}
					preset.Tools = append(preset.Tools, name)
				}
			default:
				continue
			}
		}
		if value, ok := resource.Metadata["timeout"].(string); ok && value != "" {
			timeout, err := time.ParseDuration(value)
			if err != nil {
				slog.Error("invalid builtin delegate timeout", "delegate", preset.Name, "timeout", value, "error", err)
				continue
			}
			preset.Timeout = timeout
		}
		if preset.Name == "" || strings.TrimSpace(preset.Description) == "" {
			continue
		}
		presets = append(presets, preset)
	}
	return presets
}

// loadPresetsFromDir scans a directory for delegate preset .md files.
func loadPresetsFromDir(files pkgsandbox.FileAccess, dir, source string) []DelegatePreset {
	info, err := files.Stat(dir)
	if err != nil || !info.IsDir {
		return nil
	}

	entries, err := files.ReadDir(dir)
	if err != nil {
		return nil
	}

	var presets []DelegatePreset
	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		if strings.HasPrefix(entry.Name, ".") {
			continue
		}

		filePath := path.Join(dir, entry.Name)
		if p, ok := loadPresetFromFile(files, filePath, source); ok {
			presets = append(presets, p)
		}
	}

	return presets
}

// loadPresetFromFile parses an delegate preset from a markdown file with YAML frontmatter.
func loadPresetFromFile(files pkgsandbox.FileAccess, filePath, source string) (DelegatePreset, bool) {
	data, err := files.ReadFile(filePath)
	if err != nil {
		slog.Debug("failed to read delegate preset file", "path", filePath, "error", err)
		return DelegatePreset{}, false
	}

	content := string(data)
	fm, body, err := parseDelegateFrontmatter(content)
	if err != nil {
		slog.Debug("failed to parse delegate preset frontmatter", "path", filePath, "error", err)
		return DelegatePreset{}, false
	}

	// Name is required — fall back to filename without extension.
	name := fm.Name
	if name == "" {
		name = strings.TrimSuffix(path.Base(filePath), ".md")
	}

	// Description is required.
	if strings.TrimSpace(fm.Description) == "" {
		slog.Debug("delegate preset missing description", "path", filePath)
		return DelegatePreset{}, false
	}

	var timeout time.Duration
	if fm.Timeout != "" {
		timeout, err = time.ParseDuration(fm.Timeout)
		if err != nil {
			slog.Debug("invalid timeout in delegate preset", "path", filePath, "timeout", fm.Timeout, "error", err)
			return DelegatePreset{}, false
		}
	}

	return DelegatePreset{
		Name:        name,
		Description: fm.Description,
		System:      strings.TrimSpace(body),
		Tools:       fm.Tools,
		HasTools:    fm.HasTools,
		Timeout:     timeout,
		Model:       fm.Model,
		Source:      source,
	}, true
}

// parseDelegateFrontmatter extracts YAML frontmatter and body from markdown content.
func parseDelegateFrontmatter(content string) (delegateFrontmatter, string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	if !strings.HasPrefix(content, "---") {
		return delegateFrontmatter{}, "", fmt.Errorf("no frontmatter")
	}

	endIdx := strings.Index(content[3:], "\n---")
	if endIdx == -1 {
		return delegateFrontmatter{}, "", fmt.Errorf("no closing frontmatter delimiter")
	}

	yamlStr := content[4 : 3+endIdx]
	body := content[3+endIdx+4:] // skip "\n---\n"

	var fm delegateFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return delegateFrontmatter{}, "", fmt.Errorf("invalid yaml: %w", err)
	}

	// Detect whether tools was explicitly set in the YAML.
	// yaml.Unmarshal sets fm.Tools to nil when the key is absent,
	// and to an empty slice when the key is present with `[]`.
	// We use a raw map parse to distinguish the two cases.
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &raw); err == nil {
		if _, ok := raw["tools"]; ok {
			fm.HasTools = true
		}
	}

	return fm, body, nil
}

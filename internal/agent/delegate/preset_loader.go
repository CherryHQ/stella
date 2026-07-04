package delegate

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadDelegatePresetsConfig configures the delegate preset discovery paths.
type LoadDelegatePresetsConfig struct {
	StellaHome  string // stella home dir (e.g. ~/.stella)
	AgentRoot   string // agent root dir (e.g. ~/.stella/agents/{agentID})
	UserRoot    string // shared user-data root, mounted as /user (e.g. ~/.stella/users/{userID}/data)
	ProjectRoot string // optional project root for local/project-attached runs
}

// LoadDelegatePresets discovers delegate presets in increasing priority order:
// STELLA_HOME -> agent root -> user root -> project root.
func LoadDelegatePresets(cfg LoadDelegatePresetsConfig) []DelegatePreset {
	return loadDelegatePresets(context.Background(), cfg.StellaHome, cfg.AgentRoot, cfg.UserRoot, cfg.ProjectRoot)
}

func loadDelegatePresets(ctx context.Context, stellaHome, agentRoot, userRoot, projectRoot string) []DelegatePreset {
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
		abs, _ := filepath.Abs(dir)
		if dedupPaths[abs] {
			return
		}
		dedupPaths[abs] = true
		for _, p := range loadPresetsFromDir(ctx, dir, source) {
			add(p)
		}
	}

	scanTier := func(root, source string) {
		addDir(filepath.Join(root, ".agents", "delegates"), source)
	}

	if stellaHome != "" {
		scanTier(stellaHome, "stella")
	}
	if agentRoot != "" {
		scanTier(agentRoot, "agent")
	}
	if userRoot != "" {
		scanTier(userRoot, "user")
	}
	if projectRoot != "" {
		scanTier(projectRoot, "project")
	}

	return presets
}

// loadPresetsFromDir scans a directory for delegate preset .md files.
func loadPresetsFromDir(ctx context.Context, dir, source string) []DelegatePreset {
	info, err := statHostPath(ctx, dir)
	if err != nil || !info.Exists || !info.IsDir {
		return nil
	}

	entries, err := readHostDir(ctx, dir)
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

		filePath := filepath.Join(dir, entry.Name)
		if p, ok := loadPresetFromFile(ctx, filePath, source); ok {
			presets = append(presets, p)
		}
	}

	return presets
}

// loadPresetFromFile parses an delegate preset from a markdown file with YAML frontmatter.
func loadPresetFromFile(ctx context.Context, filePath, source string) (DelegatePreset, bool) {
	data, err := readHostFile(ctx, filePath)
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
		name = strings.TrimSuffix(filepath.Base(filePath), ".md")
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
		FilePath:    filePath,
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

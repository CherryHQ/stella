package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"gopkg.in/yaml.v3"
)

// LoadAgentPresetsConfig configures the agent preset discovery paths.
type LoadAgentPresetsConfig struct {
	AgentRoot        string // agent root dir (e.g. ~/.anna/workspaces/{agentID})
	UserRoot         string // user root dir (e.g. ~/.anna/workspaces/{agentID}/users/{userID}/data)
	Cwd              string // working directory
	BuiltinSkillsDir string // pre-extracted builtin skills directory (caller ensures extraction)
	Runtime          pkgplugins.ToolRuntime
}

// LoadAgentPresets discovers agent presets in increasing priority order:
// ANNA_HOME -> agent root -> user root -> cwd.
func LoadAgentPresets(cfg LoadAgentPresetsConfig) []AgentPreset {
	return loadAgentPresets(context.Background(), cfg.Runtime, cfg.AgentRoot, cfg.UserRoot, cfg.Cwd, cfg.BuiltinSkillsDir)
}

func loadAgentPresets(ctx context.Context, runtime pkgplugins.ToolRuntime, agentRoot, userRoot, cwd, builtinSkillsDir string) []AgentPreset {
	indexByName := map[string]int{}
	var presets []AgentPreset

	add := func(p AgentPreset) {
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
		for _, p := range loadPresetsFromDir(ctx, runtime, dir, source) {
			add(p)
		}
	}

	if builtinSkillsDir != "" {
		addDir(filepath.Join(builtinSkillsDir, "agents"), "anna")
	}
	if agentRoot != "" {
		addDir(filepath.Join(agentRoot, "agents"), "agent")
	}
	if userRoot != "" {
		addDir(filepath.Join(filepath.Dir(userRoot), ".agents", "agents"), "user")
	}
	if cwd != "" {
		addDir(filepath.Join(cwd, ".agents", "agents"), "project")
	}

	return presets
}

// loadPresetsFromDir scans a directory for agent preset .md files.
func loadPresetsFromDir(ctx context.Context, runtime pkgplugins.ToolRuntime, dir, source string) []AgentPreset {
	info, err := statHostPath(ctx, runtime, dir)
	if err != nil || !info.Exists || !info.IsDir {
		return nil
	}

	entries, err := readHostDir(ctx, runtime, dir)
	if err != nil {
		return nil
	}

	var presets []AgentPreset
	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		if strings.HasPrefix(entry.Name, ".") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name)
		if p, ok := loadPresetFromFile(ctx, runtime, filePath, source); ok {
			presets = append(presets, p)
		}
	}

	return presets
}

// loadPresetFromFile parses an agent preset from a markdown file with YAML frontmatter.
func loadPresetFromFile(ctx context.Context, runtime pkgplugins.ToolRuntime, filePath, source string) (AgentPreset, bool) {
	data, err := readHostFile(ctx, runtime, filePath)
	if err != nil {
		slog.Debug("failed to read agent preset file", "path", filePath, "error", err)
		return AgentPreset{}, false
	}

	content := string(data)
	fm, body, err := parseAgentFrontmatter(content)
	if err != nil {
		slog.Debug("failed to parse agent preset frontmatter", "path", filePath, "error", err)
		return AgentPreset{}, false
	}

	// Name is required — fall back to filename without extension.
	name := fm.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(filePath), ".md")
	}

	// Description is required.
	if strings.TrimSpace(fm.Description) == "" {
		slog.Debug("agent preset missing description", "path", filePath)
		return AgentPreset{}, false
	}

	var timeout time.Duration
	if fm.Timeout != "" {
		timeout, err = time.ParseDuration(fm.Timeout)
		if err != nil {
			slog.Debug("invalid timeout in agent preset", "path", filePath, "timeout", fm.Timeout, "error", err)
			return AgentPreset{}, false
		}
	}

	return AgentPreset{
		Name:        name,
		Description: fm.Description,
		System:      strings.TrimSpace(body),
		Tools:       fm.Tools,
		HasTools:    fm.HasTools,
		MaxTurns:    fm.MaxTurns,
		Timeout:     timeout,
		Model:       fm.Model,
		FilePath:    filePath,
		Source:      source,
	}, true
}

// parseAgentFrontmatter extracts YAML frontmatter and body from markdown content.
func parseAgentFrontmatter(content string) (agentFrontmatter, string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	if !strings.HasPrefix(content, "---") {
		return agentFrontmatter{}, "", fmt.Errorf("no frontmatter")
	}

	endIdx := strings.Index(content[3:], "\n---")
	if endIdx == -1 {
		return agentFrontmatter{}, "", fmt.Errorf("no closing frontmatter delimiter")
	}

	yamlStr := content[4 : 3+endIdx]
	body := content[3+endIdx+4:] // skip "\n---\n"

	var fm agentFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return agentFrontmatter{}, "", fmt.Errorf("invalid yaml: %w", err)
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

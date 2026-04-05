package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadAgentPresetsConfig configures the agent preset discovery paths.
type LoadAgentPresetsConfig struct {
	AnnaHome         string // anna home directory (e.g. ~/.anna)
	Workspace        string // agent workspace dir (e.g. ~/.anna/workspaces/{agentID})
	Cwd              string // working directory
	BuiltinSkillsDir string // pre-extracted builtin skills directory (caller ensures extraction)
}

// LoadAgentPresets discovers agent presets from multiple directories.
// Priority order: cwd/.agents/agents/ > workspace/agents/ > ~/.agents/agents/ > builtin
func LoadAgentPresets(cfg LoadAgentPresetsConfig) []AgentPreset {
	home, _ := os.UserHomeDir()
	return loadAgentPresets(home, cfg.AnnaHome, cfg.Workspace, cfg.Cwd, cfg.BuiltinSkillsDir)
}

func loadAgentPresets(homeDir, annaHome, workspace, cwd, builtinSkillsDir string) []AgentPreset {
	seen := map[string]bool{}
	var presets []AgentPreset

	add := func(p AgentPreset) {
		if seen[p.Name] {
			return
		}
		seen[p.Name] = true
		presets = append(presets, p)
	}

	dedupPaths := map[string]bool{}
	addDir := func(dir, source string) {
		abs, _ := filepath.Abs(dir)
		if dedupPaths[abs] {
			return
		}
		dedupPaths[abs] = true
		for _, p := range loadPresetsFromDir(dir, source) {
			add(p)
		}
	}

	// 1. Project-local agents: cwd/.agents/agents/ (highest priority)
	if cwd != "" {
		addDir(filepath.Join(cwd, ".agents", "agents"), "project")
	}

	// 2. Agent-level workspace agents: workspace/agents/
	if workspace != "" {
		addDir(filepath.Join(workspace, "agents"), "agent")
	}

	// 3. Common agents: ~/.agents/agents/
	if homeDir != "" {
		addDir(filepath.Join(homeDir, ".agents", "agents"), "common")
	}

	// 4. Builtin agents: pre-extracted by the runner (lowest priority).
	if builtinSkillsDir != "" {
		addDir(filepath.Join(builtinSkillsDir, "agents"), "builtin")
	}

	return presets
}

// loadPresetsFromDir scans a directory for agent preset .md files.
func loadPresetsFromDir(dir, source string) []AgentPreset {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var presets []AgentPreset
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())
		if p, ok := loadPresetFromFile(filePath, source); ok {
			presets = append(presets, p)
		}
	}

	return presets
}

// loadPresetFromFile parses an agent preset from a markdown file with YAML frontmatter.
func loadPresetFromFile(filePath, source string) (AgentPreset, bool) {
	data, err := os.ReadFile(filePath)
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

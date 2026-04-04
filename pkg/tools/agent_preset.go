package tools

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/agent/runner/builtin"
	"gopkg.in/yaml.v3"
)

// AgentPreset defines a reusable subagent configuration loaded from a markdown file.
type AgentPreset struct {
	Name        string        // unique identifier
	Description string        // human-readable description
	System      string        // markdown body = system prompt for the subagent
	Tools       []string      // nil = all tools; empty slice = no tools
	HasTools    bool          // true when tools is explicitly set (even if empty)
	MaxTurns    int           // 0 = use global default
	Timeout     time.Duration // 0 = use global default
	Model       string        // empty = inherit parent model
	FilePath    string        // absolute path to the source .md file
	Source      string        // "project", "agent", "common", or "builtin"
}

// agentFrontmatter is the YAML frontmatter parsed from an agent preset file.
type agentFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Model       string   `yaml:"model"`
	Tools       []string `yaml:"tools"`
	HasTools    bool     `yaml:"-"` // set during parsing, not from YAML
	MaxTurns    int      `yaml:"max_turns"`
	Timeout     string   `yaml:"timeout"` // parsed as time.Duration
}

// LoadAgentPresetsConfig configures the agent preset discovery paths.
type LoadAgentPresetsConfig struct {
	AnnaHome  string // anna home directory (e.g. ~/.anna)
	Workspace string // agent workspace dir (e.g. ~/.anna/workspaces/{agentID})
	Cwd       string // working directory
}

// LoadAgentPresets discovers agent presets from multiple directories.
// Priority order: cwd/.agents/agents/ > workspace/agents/ > ~/.agents/agents/ > builtin
func LoadAgentPresets(cfg LoadAgentPresetsConfig) []AgentPreset {
	home, _ := os.UserHomeDir()
	return loadAgentPresets(home, cfg.AnnaHome, cfg.Workspace, cfg.Cwd)
}

func loadAgentPresets(homeDir, annaHome, workspace, cwd string) []AgentPreset {
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

	// 4. Builtin agents: extracted from binary (lowest priority).
	// Ensure builtins are extracted to disk (idempotent).
	if annaHome != "" {
		builtinBaseDir := filepath.Join(annaHome, "cache", "builtin-skills")
		if err := builtin.Extract(builtinBaseDir); err != nil {
			slog.Warn("failed to extract builtin agents", "error", err)
		}
		addDir(filepath.Join(builtinBaseDir, "agents"), "builtin")
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

// PresetRegistry holds loaded agent presets with O(1) lookup.
type PresetRegistry struct {
	presets []AgentPreset
	byName  map[string]AgentPreset
}

// NewPresetRegistry creates a registry from a slice of presets.
func NewPresetRegistry(presets []AgentPreset) *PresetRegistry {
	m := make(map[string]AgentPreset, len(presets))
	for _, p := range presets {
		m[p.Name] = p
	}
	return &PresetRegistry{presets: presets, byName: m}
}

// Lookup returns a preset by name and whether it exists.
func (r *PresetRegistry) Lookup(name string) (AgentPreset, bool) {
	p, ok := r.byName[name]
	return p, ok
}

// Names returns all available preset names in load order.
func (r *PresetRegistry) Names() []string {
	names := make([]string, len(r.presets))
	for i, p := range r.presets {
		names[i] = p.Name
	}
	return names
}

// All returns all loaded presets.
func (r *PresetRegistry) All() []AgentPreset {
	return r.presets
}

package delegate

import "time"

// DelegatePreset defines a reusable delegate configuration loaded from a markdown file.
type DelegatePreset struct {
	Name        string        // unique identifier
	Description string        // human-readable description
	System      string        // markdown body = system prompt for the delegate
	Tools       []string      // nil = all tools; empty slice = no tools
	HasTools    bool          // true when tools is explicitly set (even if empty)
	Timeout     time.Duration // wall-clock timeout override; 0 = use global default
	Model       string        // empty = inherit parent model
	Source      string        // "project", "agent", "user", or "builtin"
}

// delegateFrontmatter is the YAML frontmatter parsed from a delegate preset file.
//
// Supported fields:
//   - name, description, model: identity and display
//   - tools: explicit allowlist (omit to inherit all parent tools)
//   - timeout: wall-clock limit per run, e.g. "30m" (omit to use global default)
//
// max_turns is intentionally not exposed here. It is a fixed internal safety
// rail (50 turns) that prevents runaway loops regardless of preset configuration.
// Use timeout to bound long-running delegates by wall-clock time instead.
type delegateFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Model       string   `yaml:"model"`
	Tools       []string `yaml:"tools"`
	HasTools    bool     `yaml:"-"`       // set during parsing, not from YAML
	Timeout     string   `yaml:"timeout"` // parsed as time.Duration, e.g. "30m"
}

// PresetRegistry holds loaded delegate presets with O(1) lookup.
type PresetRegistry struct {
	presets []DelegatePreset
	byName  map[string]DelegatePreset
}

// NewPresetRegistry creates a registry from a slice of presets.
func NewPresetRegistry(presets []DelegatePreset) *PresetRegistry {
	m := make(map[string]DelegatePreset, len(presets))
	for _, p := range presets {
		m[p.Name] = p
	}
	return &PresetRegistry{presets: presets, byName: m}
}

// Lookup returns a preset by name and whether it exists.
func (r *PresetRegistry) Lookup(name string) (DelegatePreset, bool) {
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
func (r *PresetRegistry) All() []DelegatePreset {
	return r.presets
}

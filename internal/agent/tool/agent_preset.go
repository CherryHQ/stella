package tool

import "time"

// AgentPreset defines a reusable subagent configuration.
// When a task specifies a preset, its values provide defaults
// that explicit task fields can override.
type AgentPreset struct {
	Name        string        // unique identifier used in the "preset" field
	Description string        // human-readable description for tool schema
	System      string        // appended to base system prompt
	Tools       []string      // nil = all tools; empty = no tools
	HasTools    bool          // true when Tools is explicitly set (even if empty)
	MaxTurns    int           // 0 = use global default
	Timeout     time.Duration // 0 = use global default
	Model       string        // empty = inherit parent model
}

// builtinPresets are the default presets shipped with anna.
var builtinPresets = []AgentPreset{
	{
		Name:        "researcher",
		Description: "Search and synthesize information with read-only access.",
		System:      "Focus on finding accurate information. Cite sources when available. Be thorough but concise in your synthesis.",
		Tools:       []string{"bash", "read"},
		HasTools:    true,
		MaxTurns:    15,
		Timeout:     3 * time.Minute,
	},
	{
		Name:        "reviewer",
		Description: "Code review with read-only access. No write or edit tools.",
		System:      "Review code for bugs, security issues, and maintainability. Be specific with file paths and line references. Summarize findings with severity levels.",
		Tools:       []string{"read", "bash"},
		HasTools:    true,
		MaxTurns:    10,
		Timeout:     2 * time.Minute,
	},
	{
		Name:        "coder",
		Description: "Implementation subtasks with full tool access.",
		System:      "Implement the requested changes. Write clean, idiomatic code. Verify changes compile when possible.",
		Tools:       nil,
		HasTools:    false,
		MaxTurns:    20,
		Timeout:     5 * time.Minute,
	},
	{
		Name:        "writer",
		Description: "Documentation and content drafting with no tool access.",
		System:      "Write clear, well-structured content. Match the tone and style of existing documentation when applicable.",
		Tools:       []string{},
		HasTools:    true,
		MaxTurns:    5,
		Timeout:     1 * time.Minute,
	},
}

// presetMap provides O(1) lookup by name.
var presetMap = func() map[string]AgentPreset {
	m := make(map[string]AgentPreset, len(builtinPresets))
	for _, p := range builtinPresets {
		m[p.Name] = p
	}
	return m
}()

// LookupPreset returns a preset by name and whether it exists.
func LookupPreset(name string) (AgentPreset, bool) {
	p, ok := presetMap[name]
	return p, ok
}

// PresetNames returns all available preset names in definition order.
func PresetNames() []string {
	names := make([]string, len(builtinPresets))
	for i, p := range builtinPresets {
		names[i] = p.Name
	}
	return names
}

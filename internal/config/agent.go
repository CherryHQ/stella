package config

import (
	"errors"
	"time"
)

// ErrAgentInUse prevents deleting an Agent while a durable resource still
// depends on it. Callers surface this normal lifecycle conflict as HTTP 409.
var (
	ErrAgentInUse = errors.New("agent is still in use")
	// ErrAgentVersionConflict means a conditional Agent mutation observed a newer
	// durable row. Callers must re-read before choosing their next write.
	ErrAgentVersionConflict = errors.New("agent version conflict")
)

// AgentScope constants define the access scope for an agent.
const (
	AgentScopeSystem     = "system"     // all users can access
	AgentScopeRestricted = "restricted" // only assigned users can access
)

// AgentSnapshot binds an Agent projection to the opaque version that must be
// supplied for a conditional Settings mutation. Both are derived from one
// durable row read, so an Agent tool cannot pair stale fields with a newer CAS
// token after a concurrent UI or admin write.
type AgentSnapshot struct {
	Agent   Agent
	Version string
}

// Agent represents an agent definition.
// Model fields use {provider}/{model} format (e.g. "anthropic/claude-sonnet-4-6").
// They are overrides: an empty field inherits the deployment-wide DefaultModels
// value rather than meaning "no model". There is deliberately no vision tier
// here — reading an image is infrastructure rather than personality, so it stays
// deployment-wide.
type Agent struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Model               string        `json:"model"`
	ModelThinking       string        `json:"model_thinking"`
	ModelStrong         string        `json:"model_strong"`
	ModelStrongThinking string        `json:"model_strong_thinking"`
	ModelFast           string        `json:"model_fast"`
	ModelFastThinking   string        `json:"model_fast_thinking"`
	SystemPrompt        string        `json:"system_prompt"`
	Soul                string        `json:"soul"`
	Workspace           string        `json:"workspace"`
	Sandbox             SandboxConfig `json:"sandbox"`
	Scope               string        `json:"scope"`
	CreatorID           string        `json:"creator_id"`
	Enabled             bool          `json:"enabled"`
	// SystemSettingsToolsEnabled controls discovery of the Settings tool family
	// for this Agent. It never grants domain or deployment authority.
	SystemSettingsToolsEnabled bool       `json:"system_settings_tools_enabled"`
	LastActive                 *time.Time `json:"last_active,omitempty"`
}

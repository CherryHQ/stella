package config

import (
	"errors"
	"time"
)

// ErrAgentInUse prevents deleting an Agent while a durable resource still
// depends on it. Callers surface this normal lifecycle conflict as HTTP 409.
var ErrAgentInUse = errors.New("agent is still in use")

// AgentScope constants define the access scope for an agent.
const (
	AgentScopeSystem     = "system"     // all users can access
	AgentScopeRestricted = "restricted" // only assigned users can access
)

// Agent represents an agent definition.
// Model fields use {provider}/{model} format (e.g. "anthropic/claude-sonnet-4-6").
type Agent struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Model               string `json:"model"`
	ModelThinking       string `json:"model_thinking"`
	ModelStrong         string `json:"model_strong"`
	ModelStrongThinking string `json:"model_strong_thinking"`
	ModelFast           string `json:"model_fast"`
	ModelFastThinking   string `json:"model_fast_thinking"`
	// ModelVision names an auxiliary multimodal model used to render images as
	// text for models that cannot see them. Unlike the strong and fast tiers it
	// has no fallback: empty means the agent has no image-understanding model.
	ModelVision  string        `json:"model_vision"`
	SystemPrompt string        `json:"system_prompt"`
	Soul         string        `json:"soul"`
	Workspace    string        `json:"workspace"`
	Sandbox      SandboxConfig `json:"sandbox"`
	Scope        string        `json:"scope"`
	CreatorID    string        `json:"creator_id"`
	Enabled      bool          `json:"enabled"`
	LastActive   *time.Time    `json:"last_active,omitempty"`
}

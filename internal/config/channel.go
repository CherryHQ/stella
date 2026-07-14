package config

import "fmt"

// ChannelBindingConflictError reports an attempted second bidirectional channel
// for one (agent_id, type) binding. Webhooks and unbound channels are exempt.
type ChannelBindingConflictError struct {
	AgentID   string
	Type      string
	ChannelID string
}

func (e *ChannelBindingConflictError) Error() string {
	return fmt.Sprintf("agent is already bound to %s channel %s", e.Type, e.ChannelID)
}

// Channel represents a platform channel configuration.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	AgentID string `json:"agent_id,omitempty"`
	Enabled bool   `json:"enabled"`
	Config  string `json:"config"`
}

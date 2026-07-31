package config

import (
	"errors"
	"fmt"
)

// ErrChannelEndpointActive reports an attempted change to a channel's binding,
// or a hard-delete of the channel, while its webhook capability endpoint is
// still active. The endpoint must be revoked first.
var ErrChannelEndpointActive = errors.New("channel webhook endpoint is active")

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
	// OwnerUserID is internal ownership metadata for personal webhook channels.
	// It is never request-bound or serialized by the channel API.
	OwnerUserID string `json:"-"`
}

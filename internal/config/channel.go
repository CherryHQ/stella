package config

import (
	"errors"
	"fmt"
)

// ErrChannelExists reports an insert-only channel create that collided with an existing ID.
var ErrChannelExists = errors.New("channel already exists")

// ErrChannelEndpointActive reports an attempted change to a channel binding or
// webhook provider while its endpoint remains active.
var ErrChannelEndpointActive = errors.New("channel endpoint is active")

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

// ChannelUpdate carries a desired existing-channel state plus the endpoint
// provider decoded by the owning channel plugin. Store implementations must
// never parse plugin configuration; callers leave EndpointProvider empty when
// the channel has no endpoint provider.
type ChannelUpdate struct {
	Channel          Channel
	EndpointProvider string
}

package channel

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
)

// ValidateGroupMembership checks that a reply_channel_id remains an enabled
// channel of the expected platform and belongs to the expected agent. This is
// deliberately re-read immediately before a queued group turn executes.
func ValidateGroupMembership(ctx context.Context, store config.Store, platform, agentID, replyChannelID string) error {
	ch, err := validatePlatformChannel(ctx, store, platform, replyChannelID)
	if err != nil {
		return fmt.Errorf("reply channel %q: %w", replyChannelID, err)
	}
	if ch.AgentID != agentID {
		return fmt.Errorf("reply channel %q: channel belongs to agent %q, not %q", replyChannelID, ch.AgentID, agentID)
	}
	return nil
}

func validatePlatformChannel(ctx context.Context, store config.Store, platform, channelID string) (config.Channel, error) {
	ch, err := store.GetChannel(ctx, channelID)
	if err != nil {
		return config.Channel{}, fmt.Errorf("lookup channel: %w", err)
	}
	if err := validateGroupChannel(ch, platform, ch.AgentID); err != nil {
		return config.Channel{}, err
	}
	ag, err := store.GetAgent(ctx, ch.AgentID)
	if err != nil {
		return config.Channel{}, fmt.Errorf("lookup bound agent %q: %w", ch.AgentID, err)
	}
	if !ag.Enabled {
		return config.Channel{}, fmt.Errorf("bound agent %q is disabled", ch.AgentID)
	}
	return ch, nil
}

func validateGroupChannel(ch config.Channel, platform, agentID string) error {
	if !ch.Enabled {
		return fmt.Errorf("channel is disabled")
	}
	if ch.Type != platform {
		return fmt.Errorf("channel type %q does not match platform %q", ch.Type, platform)
	}
	if ch.AgentID == "" {
		return fmt.Errorf("channel has no bound agent")
	}
	if ch.AgentID != agentID {
		return fmt.Errorf("channel belongs to agent %q, not %q", ch.AgentID, agentID)
	}
	return nil
}

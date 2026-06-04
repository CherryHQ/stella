package channel

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
)

// ValidateGroupMembership checks that a reply_channel_id belongs to the
// expected agent — prevents an agent from replying through another bot.
func ValidateGroupMembership(ctx context.Context, store config.Store, agentID, replyChannelID string) error {
	ch, err := store.GetChannel(ctx, replyChannelID)
	if err != nil {
		return fmt.Errorf("lookup reply channel %q: %w", replyChannelID, err)
	}
	if ch.AgentID != agentID {
		return fmt.Errorf("reply channel %q belongs to agent %q, not %q", replyChannelID, ch.AgentID, agentID)
	}
	return nil
}

package channel

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/config"
)

// ChatContext describes the chat environment for agent routing.
type ChatContext struct {
	Platform string // "telegram", "qq", "feishu", "cli"
	ChatID   string // group/channel ID (empty for DMs)
	IsGroup  bool
}

// ResolveUser upserts a user by external ID + platform, returning the user record.
func ResolveUser(ctx context.Context, store config.Store, externalID, platform, name string) (config.User, error) {
	user, err := store.UpsertUser(ctx, externalID, platform, name)
	if err != nil {
		return config.User{}, fmt.Errorf("resolve user: %w", err)
	}
	return user, nil
}

// ResolveAgent determines which agent to route to.
// DM: user's default_agent_id
// Group: chat_agents(platform, chat_id)
// Fallback: first enabled agent
func ResolveAgent(ctx context.Context, store config.Store, user config.User, chat ChatContext) (string, error) {
	// Group chat: look up per-group agent assignment.
	if chat.IsGroup && chat.ChatID != "" {
		agentID, err := store.GetChatAgent(ctx, chat.Platform, chat.ChatID)
		if err == nil && agentID != "" {
			return agentID, nil
		}
	}

	// DM: use user's default agent.
	if !chat.IsGroup && user.DefaultAgentID != "" {
		return user.DefaultAgentID, nil
	}

	// Fallback: first enabled agent.
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve agent: list enabled agents: %w", err)
	}
	if len(agents) == 0 {
		return "", fmt.Errorf("resolve agent: no enabled agents found")
	}
	return agents[0].ID, nil
}

package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agentaccess"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
)

// AgentCommander owns /agent selection. It never reads an Agent or persists a
// selection without an Agent PEP decision.
type AgentCommander struct {
	store  config.Store
	users  auth.UserStore
	access *agentaccess.Service
}

func NewAgentCommander(store config.Store, users auth.UserStore, access *agentaccess.Service) *AgentCommander {
	return &AgentCommander{store: store, users: users, access: access}
}

func (ac *AgentCommander) ListForChat(ctx context.Context, authority authz.Authority, chat ChatContext) ([]config.Agent, error) {
	access, err := ac.access.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	agents, err := access.ListReadable(ctx, false)
	if err != nil {
		return nil, err
	}
	usable := make([]config.Agent, 0, len(agents))
	for _, agent := range agents {
		ok, err := access.CanUse(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			usable = append(usable, agent)
		}
	}
	return filterDedicatedAgents(ctx, ac.store, usable, chat)
}

func (ac *AgentCommander) Switch(ctx context.Context, authority authz.Authority, user auth.User, chat ChatContext, agentSlug string) error {
	agentSlug = strings.TrimSpace(agentSlug)
	if agentSlug == "" {
		return fmt.Errorf("agent slug is required")
	}

	channelID := chat.ChannelID
	if channelID == "" {
		channelID = chat.Platform
	}
	if channelID != "" {
		ch, err := ac.store.GetChannel(ctx, channelID)
		if err == nil && ch.AgentID != "" {
			return fmt.Errorf("this channel has a dedicated agent")
		}
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("load channel binding: %w", err)
		}
	}

	// Begin and Use are deliberately adjacent to the state write below. A denied
	// target is never loaded directly and never persisted as a chat/default agent.
	access, err := ac.access.Begin(ctx, authority)
	if err != nil {
		return err
	}
	ag, err := access.Use(ctx, agentSlug)
	if err != nil {
		return err
	}
	if !ag.Enabled {
		return fmt.Errorf("agent %q is not enabled", agentSlug)
	}
	if _, dedicated, err := agentDedicatedToOtherChannel(ctx, ac.store, ag.ID, channelID); err != nil {
		return fmt.Errorf("load channel bindings: %w", err)
	} else if dedicated {
		return fmt.Errorf("agent is dedicated to another channel")
	}

	if chat.IsGroup && chat.ChatID != "" {
		return ac.store.SetChatAgent(ctx, channelID, chat.Platform, chat.ChatID, ag.ID)
	}
	if user.ID == "" {
		return fmt.Errorf("link your account first to set a default agent")
	}
	return ac.users.UpdateUserDefaultAgent(ctx, user.ID, ag.ID)
}

func filterDedicatedAgents(ctx context.Context, store config.Store, agents []config.Agent, chat ChatContext) ([]config.Agent, error) {
	channelID := chat.ChannelID
	if channelID == "" {
		channelID = chat.Platform
	}
	channels, err := store.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	dedicated := map[string]string{}
	currentDedicatedAgent := ""
	for _, ch := range channels {
		if ch.ID == channelID {
			currentDedicatedAgent = ch.AgentID
			continue
		}
		if ch.AgentID != "" {
			dedicated[ch.AgentID] = ch.ID
		}
	}
	out := make([]config.Agent, 0, len(agents))
	for _, ag := range agents {
		if currentDedicatedAgent != "" && ag.ID != currentDedicatedAgent {
			continue
		}
		if _, ok := dedicated[ag.ID]; !ok {
			out = append(out, ag)
		}
	}
	return out, nil
}

func agentDedicatedToOtherChannel(ctx context.Context, store config.Store, agentID, channelID string) (string, bool, error) {
	channels, err := store.ListChannels(ctx)
	if err != nil {
		return "", false, err
	}
	for _, ch := range channels {
		if ch.AgentID == agentID && ch.ID != channelID {
			return ch.ID, true, nil
		}
	}
	return "", false, nil
}

func FormatAgentList(agents []config.Agent, currentAgentID string) string {
	if len(agents) == 0 {
		return "No agents available."
	}
	var b strings.Builder
	b.WriteString("Available agents:\n")
	for _, ag := range agents {
		marker := "  "
		if ag.ID == currentAgentID {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%s (%s)\n", marker, ag.ID, ag.Name)
	}
	b.WriteString("\nUse /agent <name> to switch.")
	return b.String()
}

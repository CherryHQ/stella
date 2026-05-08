package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type AgentCommander struct {
	store     config.Store
	authStore auth.AuthStore
}

func NewAgentCommander(store config.Store, authStore auth.AuthStore) *AgentCommander {
	return &AgentCommander{store: store, authStore: authStore}
}

func (ac *AgentCommander) List(ctx context.Context) ([]config.Agent, error) {
	return ac.store.ListEnabledAgents(ctx)
}

func (ac *AgentCommander) ListForChat(ctx context.Context, chat ChatContext) ([]config.Agent, error) {
	agents, err := ac.store.ListEnabledAgents(ctx)
	if err != nil {
		return nil, err
	}
	return filterDedicatedAgents(ctx, ac.store, agents, chat)
}

func (ac *AgentCommander) Switch(ctx context.Context, user auth.AuthUser, chat ChatContext, agentSlug string) error {
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
			return fmt.Errorf("channel %q is dedicated to agent %q", channelID, ch.AgentID)
		}
	}

	ag, err := ac.store.GetAgent(ctx, agentSlug)
	if err != nil {
		return fmt.Errorf("agent %q not found", agentSlug)
	}
	if !ag.Enabled {
		return fmt.Errorf("agent %q is not enabled", agentSlug)
	}
	if dedicated, ok := agentDedicatedToOtherChannel(ctx, ac.store, ag.ID, channelID); ok {
		return fmt.Errorf("agent %q is dedicated to channel %q", ag.ID, dedicated)
	}

	if chat.IsGroup && chat.ChatID != "" {
		return ac.store.SetChatAgent(ctx, channelID, chat.Platform, chat.ChatID, ag.ID)
	}

	if user.ID == 0 {
		return fmt.Errorf("link your account first to set a default agent")
	}
	return ac.authStore.UpdateUserDefaultAgent(ctx, user.ID, ag.ID)
}

var ParseCommandArgs = pkgchannel.ParseCommandArgs

type IndexedAgent struct {
	config.Agent
	GlobalIdx int
}

func IndexAgents(agents []config.Agent) []IndexedAgent {
	out := make([]IndexedAgent, len(agents))
	for i, a := range agents {
		out[i] = IndexedAgent{Agent: a, GlobalIdx: i + 1}
	}
	return out
}

func HandleAgentCommand(ctx context.Context, ac *AgentCommander, rc *ResolvedChat, args string, reply func(string)) {
	slug := strings.TrimSpace(args)

	if slug != "" {
		if err := ac.Switch(ctx, rc.User, rc.ChatCtx, slug); err != nil {
			reply(fmt.Sprintf("Error switching agent: %v", err))
			return
		}
		reply(fmt.Sprintf("Switched to agent: %s", slug))
		return
	}

	agents, err := ac.List(ctx)
	if err != nil {
		reply(fmt.Sprintf("Error listing agents: %v", err))
		return
	}
	agents, err = filterDedicatedAgents(ctx, ac.store, agents, rc.ChatCtx)
	if err != nil {
		reply(fmt.Sprintf("Error listing agents: %v", err))
		return
	}
	reply(FormatAgentList(agents, rc.AgentID))
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
		if ch.AgentID == "" {
			continue
		}
		dedicated[ch.AgentID] = ch.ID
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

func agentDedicatedToOtherChannel(ctx context.Context, store config.Store, agentID, channelID string) (string, bool) {
	channels, err := store.ListChannels(ctx)
	if err != nil {
		return "", false
	}
	for _, ch := range channels {
		if ch.AgentID == agentID && ch.ID != channelID {
			return ch.ID, true
		}
	}
	return "", false
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

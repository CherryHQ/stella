package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
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

func (ac *AgentCommander) Switch(ctx context.Context, user auth.AuthUser, chat ChatContext, agentSlug string) error {
	agentSlug = strings.TrimSpace(agentSlug)
	if agentSlug == "" {
		return fmt.Errorf("agent slug is required")
	}

	ag, err := ac.store.GetAgent(ctx, agentSlug)
	if err != nil {
		return fmt.Errorf("agent %q not found", agentSlug)
	}
	if !ag.Enabled {
		return fmt.Errorf("agent %q is not enabled", agentSlug)
	}

	if chat.IsGroup && chat.ChatID != "" {
		return ac.store.SetChatAgent(ctx, chat.Platform, chat.ChatID, ag.ID)
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
	reply(FormatAgentList(agents, rc.AgentID))
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
